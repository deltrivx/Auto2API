package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// WorkBuddy 浏览器登录（OAuth）。
//
// 移植自 magpie 的 browserSignIn / poll / enterpriseOf / accountName。
//
// 登录是一条「浏览器授权 + 轮询换票」的链路，与 WorkBuddy 桌面端一致：
//
//  1. POST /v2/plugin/auth/state?platform=<platform>
//     拿到 { state, authUrl }；请求需带 X-No-Authorization 等头，
//     表示这是一次尚未登录的调用。
//  2. 让用户在浏览器打开 authUrl，追加 version 与 loginSessionId 参数。
//     版本号影响登录页展示的登录方式（低于 5.8.0 不提供企业微信）。
//  3. 轮询 GET /v2/plugin/auth/token?state=<state>，直到拿到 token。
//     未完成时上游返回业务码 11217，需要继续等待。
//  4. 轮询 GET /v2/plugin/login/account?state=<state> 取 uid。
//     未完成时返回 12151。
//  5. 解析企业归属（个人 / 团队 / 教育版），企业账号还需带上企业 ID。
//
// 轮询是必需的：授权在浏览器里完成，服务端只能通过 state 得知结果。

// workBuddySignInPollRetry 是 token 轮询中「继续等待」的业务码。
const workBuddySignInTokenPending = 11217

// workBuddySignInAccountPending 是账号信息轮询中「继续等待」的业务码。
const workBuddySignInAccountPending = 12151

// workBuddySignIn 是一次进行中的登录。
type workBuddySignIn struct {
	Site      *workBuddySite
	State     string
	AuthURL   string
	ExpiresAt time.Time
}

// workBuddySignInResult 是登录成功后要落到账号上的信息。
type workBuddySignInResult struct {
	Token        workBuddyToken
	UID          string
	EnterpriseID string
	Name         string
}

// workBuddyLoginAccount 是 /v2/plugin/login/account 的返回。
type workBuddyLoginAccount struct {
	UID          string `json:"uid"`
	Domain       string `json:"domain"`
	Type         string `json:"type"`
	EnterpriseID string `json:"enterpriseId"`
	EnterpriseNm string `json:"enterpriseName"`
	NickName     string `json:"nickName"`
	Name         string `json:"name"`
}

// workBuddyAuthState 是 /v2/plugin/auth/state 的返回。
type workBuddyAuthState struct {
	State   string `json:"state"`
	AuthURL string `json:"authUrl"`
}

// workBuddyOAuthService 负责 WorkBuddy 的登录与账号解析。
type workBuddyOAuthService struct {
	client  *http.Client
	tokens  *workBuddyTokenManager
	pollInt time.Duration
	timeout time.Duration
}

func newWorkBuddyOAuthService() *workBuddyOAuthService {
	return &workBuddyOAuthService{
		client:  &http.Client{Timeout: 30 * time.Second},
		tokens:  newWorkBuddyTokenManager(),
		pollInt: workBuddyPollInterval,
		timeout: workBuddySignInTimeout,
	}
}

// workBuddyStartSignIn 申请一个登录会话并返回授权地址。
//
// 对应 magpie 的 browserSignIn 前半段：调用 auth/state 拿到 state 与
// authUrl，再补上桌面端打开时会带的 version 与 loginSessionId。
func (s *workBuddyOAuthService) workBuddyStartSignIn(
	ctx context.Context,
	site *workBuddySite,
) (*workBuddySignIn, error) {
	if site == nil {
		return nil, errors.New("workbuddy: site is required")
	}

	headers := map[string]string{
		// 这四个头告诉上游：这是一次尚未登录的调用，不要按缺少身份信息拒绝。
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	}

	path := "/plugin/auth/state?platform=" + url.QueryEscape(site.Platform)
	data, err := workBuddyCall(ctx, s.client, site, http.MethodPost, path, headers, map[string]any{})
	if err != nil {
		return nil, err
	}

	var state workBuddyAuthState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("%s: decode auth state: %w", site.Name, err)
	}
	if state.State == "" || state.AuthURL == "" {
		return nil, fmt.Errorf("%s: gave no sign-in page", site.Name)
	}

	authURL, err := url.Parse(state.AuthURL)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid auth url: %w", site.Name, err)
	}
	if authURL.Scheme != "https" {
		return nil, fmt.Errorf("%s: sign-in page is not https", site.Name)
	}

	// 桌面端打开授权页时会带上客户端版本；国内站的授权页对低于 5.8.0 的
	// 版本隐藏企业微信登录入口，因此这里必须传真实版本号。
	q := authURL.Query()
	q.Set("version", site.App)
	q.Set("loginSessionId", workBuddyRandomHex())
	authURL.RawQuery = q.Encode()

	return &workBuddySignIn{
		Site:      site,
		State:     state.State,
		AuthURL:   authURL.String(),
		ExpiresAt: time.Now().Add(s.timeout),
	}, nil
}

// workBuddyCompleteSignIn 轮询等待浏览器授权完成，并解析出账号信息。
//
// 对应 magpie 的 browserSignIn 的 callback 段。
func (s *workBuddyOAuthService) workBuddyCompleteSignIn(
	ctx context.Context,
	signIn *workBuddySignIn,
) (*workBuddySignInResult, error) {
	if signIn == nil || signIn.Site == nil {
		return nil, errors.New("workbuddy: sign-in is required")
	}
	site := signIn.Site
	deadline := signIn.ExpiresAt
	if deadline.IsZero() {
		deadline = time.Now().Add(s.timeout)
	}

	q := url.QueryEscape(signIn.State)

	// 1. 换取 token。
	var tokenResp workBuddyTokenResponse
	data, err := s.workBuddyPoll(ctx, site,
		"/plugin/auth/token?state="+q,
		map[string]string{},
		[]int{workBuddySignInTokenPending},
		deadline,
	)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &tokenResp); err != nil {
		return nil, fmt.Errorf("%s: decode token: %w", site.Name, err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("%s: sent back no token", site.Name)
	}

	now := time.Now()
	token := workBuddyToken{}.workBuddyApply(&tokenResp, now)

	// 2. 取账号信息。这一步要带上刚拿到的 token。
	who, err := s.workBuddyPollAccount(ctx, site, token, q, deadline)
	if err != nil {
		return nil, err
	}
	token.Domain = firstNonEmpty(token.Domain, who.Domain)

	uid := strings.TrimSpace(who.UID)

	// 3. 解析企业归属。
	entID, entName, err := s.workBuddyEnterprise(ctx, site, token, who)
	if err != nil {
		return nil, err
	}

	return &workBuddySignInResult{
		Token:        token,
		UID:          uid,
		EnterpriseID: entID,
		Name:         workBuddyAccountName(who, entID, entName),
	}, nil
}

// workBuddyPollAccount 轮询账号信息接口。
func (s *workBuddyOAuthService) workBuddyPollAccount(
	ctx context.Context,
	site *workBuddySite,
	token workBuddyToken,
	state string,
	deadline time.Time,
) (*workBuddyLoginAccount, error) {
	headers := map[string]string{
		"Authorization":      "Bearer " + token.Access,
		"X-Domain":           workBuddyDomainOf(site, token),
		"X-No-User-Id":       "true",
		"X-No-Enterprise-Id": "true",
	}
	data, err := s.workBuddyPoll(ctx, site,
		"/plugin/login/account?state="+state,
		headers,
		[]int{workBuddySignInAccountPending},
		deadline,
	)
	if err != nil {
		return nil, err
	}
	var who workBuddyLoginAccount
	if err := json.Unmarshal(data, &who); err != nil {
		return nil, fmt.Errorf("%s: decode account: %w", site.Name, err)
	}
	return &who, nil
}

// workBuddyPoll 轮询一个接口直到拿到非空数据。
//
// 对应 magpie 的 poll：业务码在 retry 列表内的错误视为「尚未就绪」，
// 继续等待；其它错误（含 408/429 以外的 HTTP 错误）立即上抛。
func (s *workBuddyOAuthService) workBuddyPoll(
	ctx context.Context,
	site *workBuddySite,
	path string,
	headers map[string]string,
	retryCodes []int,
	deadline time.Time,
) (json.RawMessage, error) {
	interval := s.pollInt
	if interval <= 0 {
		interval = workBuddyPollInterval
	}
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("%s: sign-in timed out", site.Name)
		}

		data, err := workBuddyCall(ctx, s.client, site, http.MethodGet, path, headers, nil)
		if err == nil {
			// 上游在未就绪时可能返回空对象，同样视为需要继续等待。
			if len(data) > 0 && string(data) != "{}" && string(data) != "null" {
				return data, nil
			}
		} else {
			var werr *workBuddyError
			if !errors.As(err, &werr) {
				return nil, err
			}
			retry := werr.StatusCode == http.StatusRequestTimeout || werr.StatusCode == http.StatusTooManyRequests
			if !retry {
				for _, c := range retryCodes {
					if werr.Code == c {
						retry = true
						break
					}
				}
			}
			if !retry {
				return nil, err
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// workBuddyEnterprise 解析登录账号的企业归属。
//
// 对应 magpie 的 enterpriseOf：
//   - /login/account 已给出 enterpriseId 时以它为准；
//   - 没有企业 ID 且类型是个人（或为空）时判为个人账号；
//   - 其余情况（团队、教育版等）查 /v2/plugin/accounts 列表匹配。
//
// 之所以要认企业：企业账号的 token 只能在其企业域名下使用，普通调用会
// 被判为无效登录。
func (s *workBuddyOAuthService) workBuddyEnterprise(
	ctx context.Context,
	site *workBuddySite,
	token workBuddyToken,
	who *workBuddyLoginAccount,
) (id, name string, err error) {
	if who == nil {
		return "", "", nil
	}
	own := strings.TrimSpace(who.EnterpriseID)
	typ := strings.ToLower(strings.TrimSpace(who.Type))
	if own == "" && (typ == "" || typ == "personal") {
		return "", "", nil
	}
	if own != "" && who.EnterpriseNm != "" {
		return own, strings.TrimSpace(who.EnterpriseNm), nil
	}

	headers := map[string]string{
		"Authorization": "Bearer " + token.Access,
		"X-Domain":      workBuddyDomainOf(site, token),
	}
	data, callErr := workBuddyCall(ctx, s.client, site, http.MethodGet, "/plugin/accounts", headers, nil)
	if callErr != nil {
		// 列表拉不到不影响登录：沿用 /login/account 给出的信息。
		return own, "", nil
	}

	var list struct {
		Accounts []workBuddyLoginAccount `json:"accounts"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return own, "", nil
	}

	mine := make([]workBuddyLoginAccount, 0, len(list.Accounts))
	for _, a := range list.Accounts {
		if strings.TrimSpace(a.UID) != strings.TrimSpace(who.UID) {
			continue
		}
		if strings.TrimSpace(a.EnterpriseID) == "" {
			continue
		}
		mine = append(mine, a)
	}

	if own != "" {
		for _, a := range mine {
			if strings.TrimSpace(a.EnterpriseID) == own {
				return own, strings.TrimSpace(a.EnterpriseNm), nil
			}
		}
		return own, "", nil
	}

	// 与桌面端一致的选择顺序：该类型的唯一账号 → 唯一最近登录过的 →
	// 列表里唯一的账号。
	same := make([]workBuddyLoginAccount, 0, len(mine))
	for _, a := range mine {
		if strings.ToLower(strings.TrimSpace(a.Type)) == typ {
			same = append(same, a)
		}
	}
	if len(same) == 1 {
		return strings.TrimSpace(same[0].EnterpriseID), strings.TrimSpace(same[0].EnterpriseNm), nil
	}
	if len(mine) == 1 {
		return strings.TrimSpace(mine[0].EnterpriseID), strings.TrimSpace(mine[0].EnterpriseNm), nil
	}
	return "", "", nil
}

// workBuddyAccountName 生成账号展示名。
//
// 对应 magpie 的 accountName：个人账号用昵称；企业账号在昵称后附企业名，
// 否则同一人的个人号与企业号会同名而互相覆盖。
func workBuddyAccountName(who *workBuddyLoginAccount, entID, entName string) string {
	if who == nil {
		return ""
	}
	base := strings.TrimSpace(who.NickName)
	if base == "" {
		base = strings.TrimSpace(who.Name)
	}
	if base == "" {
		base = strings.TrimSpace(who.UID)
	}
	if entID == "" || entName == "" {
		return base
	}
	if base == "" {
		return entName
	}
	return base + " @ " + entName
}

// workBuddyHeaderNoAuth 是未登录调用需要携带的一组头。
func workBuddyHeaderNoAuth() map[string]string {
	return map[string]string{
		"X-No-Authorization":   "true",
		"X-No-User-Id":         "true",
		"X-No-Enterprise-Id":   "true",
		"X-No-Department-Info": "true",
	}
}

// workBuddyRandomHex 生成 16 字节随机十六进制串，用作 loginSessionId。
func workBuddyRandomHex() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// 随机数失败时退化为时间戳，仅影响会话去重，不涉及安全。
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf[:])
}

// errWorkBuddySignInPending 表示浏览器授权尚未完成，需要继续轮询。
var errWorkBuddySignInPending = errors.New("workbuddy: sign-in pending")

// workBuddyPollOnce 查询一次登录结果，不阻塞等待。
//
// 与 workBuddyCompleteSignIn 的区别：这个函数只查一次，未完成时返回
// errWorkBuddySignInPending，由前端决定何时再查。这样 HTTP 请求不会被
// 长时间挂住，也便于前端展示「等待授权中」。
func (s *workBuddyOAuthService) workBuddyPollOnce(
	ctx context.Context,
	site *workBuddySite,
	state string,
) (*workBuddySignInResult, error) {
	q := url.QueryEscape(state)

	// 1. 取 token。
	data, err := workBuddyCall(ctx, s.client, site, http.MethodGet,
		"/plugin/auth/token?state="+q, map[string]string{}, nil)
	if err != nil {
		var werr *workBuddyError
		if errors.As(err, &werr) && werr.Code == workBuddySignInTokenPending {
			return nil, errWorkBuddySignInPending
		}
		return nil, err
	}

	var tokenResp workBuddyTokenResponse
	if err := json.Unmarshal(data, &tokenResp); err != nil {
		return nil, fmt.Errorf("%s: decode token: %w", site.Name, err)
	}
	if tokenResp.AccessToken == "" {
		return nil, errWorkBuddySignInPending
	}

	token := workBuddyToken{}.workBuddyApply(&tokenResp, time.Now())

	// 2. 取账号信息。
	accountData, err := workBuddyCall(ctx, s.client, site, http.MethodGet,
		"/plugin/login/account?state="+q,
		map[string]string{
			"Authorization":      "Bearer " + token.Access,
			"X-Domain":           workBuddyDomainOf(site, token),
			"X-No-User-Id":       "true",
			"X-No-Enterprise-Id": "true",
		}, nil)
	if err != nil {
		var werr *workBuddyError
		if errors.As(err, &werr) && werr.Code == workBuddySignInAccountPending {
			return nil, errWorkBuddySignInPending
		}
		return nil, err
	}

	var who workBuddyLoginAccount
	if err := json.Unmarshal(accountData, &who); err != nil {
		return nil, fmt.Errorf("%s: decode account: %w", site.Name, err)
	}
	token.Domain = firstNonEmpty(token.Domain, who.Domain)

	// 3. 企业归属。
	entID, entName, err := s.workBuddyEnterprise(ctx, site, token, &who)
	if err != nil {
		return nil, err
	}

	return &workBuddySignInResult{
		Token:        token,
		UID:          strings.TrimSpace(who.UID),
		EnterpriseID: entID,
		Name:         workBuddyAccountName(&who, entID, entName),
	}, nil
}
