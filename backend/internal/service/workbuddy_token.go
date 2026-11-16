package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// WorkBuddy 账号凭据与 token 生命周期。
//
// 移植自 magpie 的 merge / stale / refreshable / renew / merged。
//
// 上游的 token 语义：
//   - accessToken 有效期较短，即将过期时用 refreshToken 换新的。
//   - refreshToken 约一年有效，过期后只能重新登录。
//   - 一次刷新同时轮换 access 与 refresh，旧 refresh 立即失效；因此刷新
//     必须串行化，否则并发请求会拿着已作废的 refresh 互相失败。

// workBuddyToken 是一个账号当前的登录态。
type workBuddyToken struct {
	// Access 是访问令牌。
	Access string
	// Refresh 是刷新令牌，可为空（部分企业账号只给 access）。
	Refresh string
	// Expires 是 Access 的过期时间（epoch 秒），0 表示未知。
	Expires int64
	// RefreshExpires 是 Refresh 的过期时间（epoch 秒），0 表示未知。
	RefreshExpires int64
	// Domain 是登录态所属域名，云端企业账号为其专属域名。
	Domain string
	// TokenType 是令牌类型，通常为 Bearer。
	TokenType string
}

// workBuddyTokenResponse 是 token 接口与刷新接口的响应体。
//
// 上游同时给出绝对时间（expiresAt）与相对秒数（expiresIn）两种写法，
// 二者择一使用，绝对时间优先。
type workBuddyTokenResponse struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	ExpiresIn        int64  `json:"expiresIn"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
	RefreshExpiresIn int64  `json:"refreshExpiresIn"`
	Domain           string `json:"domain"`
	TokenType        string `json:"tokenType"`
}

// workBuddyApply 把接口返回的 token 合并进现有登录态。
//
// 对应 magpie 的 merge：只有当返回里带了某个字段时才覆盖，
// 这样刷新接口只轮换 access 时不会把 refresh 清空。
func (t workBuddyToken) workBuddyApply(got *workBuddyTokenResponse, now time.Time) workBuddyToken {
	out := t
	if got == nil {
		return out
	}
	out.Access = got.AccessToken
	if got.RefreshToken != "" {
		out.Refresh = got.RefreshToken
	}
	if got.Domain != "" {
		out.Domain = got.Domain
	}
	if got.TokenType != "" {
		out.TokenType = got.TokenType
	}
	switch {
	case got.ExpiresAt > 0:
		out.Expires = got.ExpiresAt
	case got.ExpiresIn > 0:
		out.Expires = now.Unix() + got.ExpiresIn
	}
	switch {
	case got.RefreshExpiresAt > 0:
		out.RefreshExpires = got.RefreshExpiresAt
	case got.RefreshExpiresIn > 0:
		out.RefreshExpires = now.Unix() + got.RefreshExpiresIn
	}
	return out
}

// workBuddyStale 判断 access token 是否已到需要提前刷新的时间点。
func (t workBuddyToken) workBuddyStale(now time.Time) bool {
	if t.Expires == 0 {
		return false
	}
	return now.Unix() >= t.Expires-int64(workBuddyEarlyRefresh/time.Second)
}

// workBuddyRefreshable 判断是否仍可用 refresh token 换新。
//
// refresh token 自身也有有效期；一旦过期，任何刷新都会失败，
// 此时应直接标记账号需要重新登录，而不是反复打上游。
func (t workBuddyToken) workBuddyRefreshable(now time.Time) bool {
	if t.Refresh == "" {
		return false
	}
	if t.RefreshExpires > 0 && now.Unix() >= t.RefreshExpires {
		return false
	}
	return true
}

// workBuddyRenewResult 是一次刷新的结果，缓存一小段时间供并发请求复用。
type workBuddyRenewResult struct {
	at   time.Time
	next workBuddyToken
}

// workBuddyTokenManager 负责串行化同一 refresh token 的刷新。
//
// 上游在刷新时轮换 refresh token，旧值立即作废。多 goroutine 并发刷新
// 会让除第一个之外的请求全部失败（invalid_grant），进而在账号上留下连续
// 失败记录。这里按 site + refresh 做单飞：同一时刻只有一个刷新在途，
// 其余等待它的结果。
type workBuddyTokenManager struct {
	mu       sync.Mutex
	inflight map[string]*workBuddyRenewCall
	renewed  map[string]workBuddyRenewResult
}

type workBuddyRenewCall struct {
	done chan struct{}
	next workBuddyToken
	err  error
}

func newWorkBuddyTokenManager() *workBuddyTokenManager {
	return &workBuddyTokenManager{
		inflight: make(map[string]*workBuddyRenewCall),
		renewed:  make(map[string]workBuddyRenewResult),
	}
}

func (m *workBuddyTokenManager) workBuddyKey(site *workBuddySite, t workBuddyToken) string {
	return site.ID + "\x00" + t.Refresh
}

// workBuddyFresh 确保返回的 token 尚未过期。
//
// 对应 magpie 的 fresh：access 接近过期时触发刷新；refresh 已失效或刷新
// 失败时保留现有 access（可能仍可用），由调用方在 401 时再处理。
func (m *workBuddyTokenManager) workBuddyFresh(
	ctx context.Context,
	site *workBuddySite,
	t workBuddyToken,
	renewFn func(context.Context, workBuddyToken) (workBuddyToken, error),
) (workBuddyToken, error) {
	if !t.workBuddyStale(time.Now()) {
		return t, nil
	}
	if !t.workBuddyRefreshable(time.Now()) {
		return t, nil
	}
	next, err := m.workBuddyRenew(ctx, site, t, renewFn)
	if err != nil {
		// 刷新失败不阻断本次请求：access 可能仍在有效期内，
		// 由上游 401 与 invalid_token 判定是否需要重新登录。
		return t, nil
	}
	return next, nil
}

// workBuddyRenew 执行一次单飞刷新。
func (m *workBuddyTokenManager) workBuddyRenew(
	ctx context.Context,
	site *workBuddySite,
	t workBuddyToken,
	renewFn func(context.Context, workBuddyToken) (workBuddyToken, error),
) (workBuddyToken, error) {
	key := m.workBuddyKey(site, t)

	m.mu.Lock()
	// 刚刷新过的结果直接复用，避免读到已作废的旧 refresh token 时再次消费。
	if done, ok := m.renewed[key]; ok && time.Since(done.at) < workBuddyKeepRenewed {
		m.mu.Unlock()
		return t.workBuddyMerge(done.next), nil
	}
	if call, ok := m.inflight[key]; ok {
		m.mu.Unlock()
		select {
		case <-call.done:
			if call.err != nil {
				return t, call.err
			}
			return t.workBuddyMerge(call.next), nil
		case <-ctx.Done():
			return t, ctx.Err()
		}
	}
	call := &workBuddyRenewCall{done: make(chan struct{})}
	m.inflight[key] = call
	m.mu.Unlock()

	next, err := renewFn(ctx, t)

	m.mu.Lock()
	delete(m.inflight, key)
	if err == nil {
		m.renewed[key] = workBuddyRenewResult{at: time.Now(), next: next}
		if len(m.renewed) > 512 {
			// 简单容量控制：超限时清掉最旧的一半，避免长期运行后无限增长。
			cut := len(m.renewed) / 2
			for k := range m.renewed {
				delete(m.renewed, k)
				if cut--; cut <= 0 {
					break
				}
			}
		}
	}
	m.mu.Unlock()

	call.err = err
	call.next = next
	close(call.done)

	if err != nil {
		return t, err
	}
	return t.workBuddyMerge(next), nil
}

// workBuddyMerge 把 next 中的 token 字段覆盖到当前登录态上。
//
// 对应 magpie 的 merged：只搬运 token 相关字段，保留 uid、enterprise_id
// 等其它绑定在账号上的身份信息。
func (t workBuddyToken) workBuddyMerge(next workBuddyToken) workBuddyToken {
	out := t
	if next.Access != "" {
		out.Access = next.Access
	}
	if next.Refresh != "" {
		out.Refresh = next.Refresh
	}
	if next.Expires != 0 {
		out.Expires = next.Expires
	}
	if next.RefreshExpires != 0 {
		out.RefreshExpires = next.RefreshExpires
	}
	if next.Domain != "" {
		out.Domain = next.Domain
	}
	if next.TokenType != "" {
		out.TokenType = next.TokenType
	}
	return out
}

// workBuddyTokenFromCredentials 从账号凭据中解析登录态。
func workBuddyTokenFromCredentials(creds map[string]any) workBuddyToken {
	if creds == nil {
		return workBuddyToken{}
	}
	return workBuddyToken{
		Access:         workBuddyStringField(creds, workBuddyFieldAccess),
		Refresh:        workBuddyStringField(creds, workBuddyFieldRefresh),
		Expires:        workBuddyIntField(creds, workBuddyFieldExpires),
		RefreshExpires: workBuddyIntField(creds, workBuddyFieldRefreshExpires),
		Domain:         workBuddyStringField(creds, workBuddyFieldDomain),
		TokenType:      workBuddyStringField(creds, workBuddyFieldTokenType),
	}
}

// workBuddyCredentials 把登录态写回凭据结构，保留其它字段。
func (t workBuddyToken) workBuddyCredentials(base map[string]any) map[string]any {
	out := make(map[string]any, len(base)+6)
	for k, v := range base {
		out[k] = v
	}
	out[workBuddyFieldAccess] = t.Access
	if t.Refresh != "" {
		out[workBuddyFieldRefresh] = t.Refresh
	}
	if t.Expires > 0 {
		out[workBuddyFieldExpires] = strconv.FormatInt(t.Expires, 10)
	}
	if t.RefreshExpires > 0 {
		out[workBuddyFieldRefreshExpires] = strconv.FormatInt(t.RefreshExpires, 10)
	}
	if t.Domain != "" {
		out[workBuddyFieldDomain] = t.Domain
	}
	if t.TokenType != "" {
		out[workBuddyFieldTokenType] = t.TokenType
	}
	return out
}

// workBuddyStringField 读取字符串字段，兼容数字被存成 float64 的情况。
func workBuddyStringField(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case json.Number:
		return v.String()
	default:
		return ""
	}
}

// workBuddyIntField 读取整数字段，兼容字符串与浮点存储。
func workBuddyIntField(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

// ---- 上游调用 ------------------------------------------------------------

// workBuddyError 是上游返回的业务错误。
//
// 上游统一以 {code, msg} 表达失败，HTTP 状态码不一定反映业务结果：
// 部分场景（例如登录轮询尚未完成）用 200 + 业务 code 表示「继续等待」。
type workBuddyError struct {
	Site       string
	Message    string
	Code       int
	StatusCode int
}

func (e *workBuddyError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != 0 {
		return fmt.Sprintf("%s: %s (HTTP %d, code %d)", e.Site, e.Message, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%s: %s (HTTP %d)", e.Site, e.Message, e.StatusCode)
}

// workBuddyEnvelope 是上游所有接口的统一响应外壳。
type workBuddyEnvelope struct {
	Code any             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

// workBuddyCall 向上游发起一次 JSON 调用，返回 data 段。
//
// 对应 magpie 的 call。headers 由调用方给出（登录阶段与业务阶段的要求不同），
// 这里只补齐公共的 Content-Type / Accept / User-Agent。
func workBuddyCall(
	ctx context.Context,
	client *http.Client,
	site *workBuddySite,
	method, path string,
	headers map[string]string,
	body any,
) (json.RawMessage, error) {
	if client == nil {
		client = http.DefaultClient
	}

	var payload io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal workbuddy request: %w", err)
		}
		payload = strings.NewReader(string(buf))
	}

	req, err := http.NewRequestWithContext(ctx, method, site.workBuddyAPIBase()+path, payload)
	if err != nil {
		return nil, fmt.Errorf("build workbuddy request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", site.workBuddyCLIUserAgent())
	for k, v := range headers {
		if v == "" {
			continue
		}
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", site.Name, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// 上游可能返回很大的错误页，限制读取量避免无谓占用内存。
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, fmt.Errorf("%s: read response: %w", site.Name, err)
	}

	var env workBuddyEnvelope
	_ = json.Unmarshal(raw, &env)

	code := workBuddyEnvelopeCode(env.Code)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := env.Msg
		if msg == "" {
			msg = strings.TrimSpace(string(raw))
			if len(msg) > 200 {
				msg = msg[:200]
			}
		}
		if msg == "" {
			msg = resp.Status
		}
		return nil, &workBuddyError{Site: site.Name, Message: msg, Code: code, StatusCode: resp.StatusCode}
	}
	if code != 0 {
		return nil, &workBuddyError{Site: site.Name, Message: env.Msg, Code: code, StatusCode: resp.StatusCode}
	}
	return env.Data, nil
}

// workBuddyEnvelopeCode 把 code 字段归一化为整数。
//
// 上游有的接口返回数字、有的返回字符串，需要同时兼容。
func workBuddyEnvelopeCode(v any) int {
	switch c := v.(type) {
	case float64:
		return int(c)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(c))
		return n
	case json.Number:
		n, _ := c.Int64()
		return int(n)
	default:
		return 0
	}
}

// workBuddyDomainOf 返回账号请求应使用的域名。
//
// 对应 magpie 的 domainOf：登录态里带了 domain（云端企业账号）就用它，
// 否则用站点自身的域名。
func workBuddyDomainOf(site *workBuddySite, t workBuddyToken) string {
	if d := strings.TrimSpace(t.Domain); d != "" {
		return d
	}
	u, err := url.Parse(site.Endpoint)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Host
}

// workBuddyRefresh 用 refresh token 换一组新 token。
//
// 对应 magpie 的 renew 中的请求部分：POST /v2/plugin/auth/token/refresh，
// 携带 X-Refresh-Token 与 X-Auth-Refresh-Source: plugin。
func workBuddyRefresh(
	ctx context.Context,
	client *http.Client,
	site *workBuddySite,
	t workBuddyToken,
	enterpriseHeaders map[string]string,
) (workBuddyToken, error) {
	if t.Refresh == "" {
		return t, errors.New("workbuddy: no refresh token")
	}
	headers := map[string]string{
		"X-Refresh-Token":       t.Refresh,
		"X-Auth-Refresh-Source": "plugin",
		"X-Domain":              workBuddyDomainOf(site, t),
	}
	for k, v := range enterpriseHeaders {
		headers[k] = v
	}

	data, err := workBuddyCall(ctx, client, site, http.MethodPost, "/plugin/auth/token/refresh", headers, map[string]any{})
	if err != nil {
		return t, err
	}
	var got workBuddyTokenResponse
	if err := json.Unmarshal(data, &got); err != nil {
		return t, fmt.Errorf("%s: decode refreshed token: %w", site.Name, err)
	}
	if got.AccessToken == "" {
		return t, &workBuddyError{Site: site.Name, Message: "gave no refreshed token"}
	}
	return t.workBuddyApply(&got, time.Now()), nil
}

// workBuddyAccessToken 返回账号可用的 access token，必要时先刷新。
//
// OpenAI 的 TokenProvider 只接受 OpenAI 平台账号，WorkBuddy 因此需要自己的
// 取 token 路径：优先用凭据里的 access，临近过期时用 refresh token 换新。
// 刷新失败不阻断请求——access 可能仍在有效期内，由上游 401 决定是否要重新登录。
func (s *OpenAIGatewayService) workBuddyAccessToken(ctx context.Context, account *Account) (string, error) {
	if account == nil {
		return "", errors.New("workbuddy: account is nil")
	}
	t := account.workBuddyTokenOf()
	if strings.TrimSpace(t.Access) == "" {
		return "", errors.New("workbuddy: access_token not found in credentials")
	}
	now := time.Now()
	if !t.workBuddyStale(now) || !t.workBuddyRefreshable(now) {
		return t.Access, nil
	}

	site := account.workBuddySiteOf()
	if site == nil || s == nil || s.httpUpstream == nil {
		return t.Access, nil
	}
	next, err := workBuddyRefresh(ctx, &http.Client{Timeout: 60 * time.Second}, site, t, nil)
	if err != nil {
		// 刷新失败沿用旧 token，让上游 401 决定是否要求重新登录。
		return t.Access, nil
	}
	if err := s.persistWorkBuddyToken(ctx, account, next); err != nil {
		slog.Warn("workbuddy_token_persist_failed", "account_id", account.ID, "error", err)
	}
	return next.Access, nil
}

// persistWorkBuddyToken 把刷新后的 token 写回账号凭据。
//
// 上游刷新会轮换 refresh token，旧值立即作废，因此必须落库，
// 否则进程重启后账号会因持有已作废的 refresh token 而无法续期。
func (s *OpenAIGatewayService) persistWorkBuddyToken(ctx context.Context, account *Account, t workBuddyToken) error {
	if s == nil || s.accountRepo == nil || account == nil || account.ID <= 0 {
		return nil
	}
	next := make(map[string]any, len(account.Credentials)+4)
	for k, v := range account.Credentials {
		next[k] = v
	}
	next["access_token"] = t.Access
	if t.Refresh != "" {
		next["refresh_token"] = t.Refresh
	}
	if t.Expires > 0 {
		exp := time.Unix(t.Expires, 0)
		next["expires_at"] = exp.Format(time.RFC3339)
		next["expiresAt"] = exp.UnixMilli()
	}
	return persistAccountCredentials(ctx, s.accountRepo, account, next)
}
