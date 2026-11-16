package service

// Marvis 的可回调登录流程。
//
// 前一个实现照搬了 magpie 的「粘贴回调地址」——那是它作为桌面客户端、跑在
// 无浏览器环境下的妥协。Auto2API 是 Web 服务，可以做得更顺：
//
//	1. 服务端生成 state（一次性票据），带 TTL
//	2. 授权链接的 redirect_uri 指向本服务 /api/v1/marvis/oauth/callback
//	3. 用户点链接扫码，浏览器授权完成后由 upstream 302 到我们的回调
//	4. 回调里用 code 换登录态，把结果落进票据
//	5. 前端轮询 /oauth/poll 拿结果 —— 与 WorkBuddy 完全一致的交互
//
// 与 WorkBuddy 的差别只在「谁去上游查结果」：WorkBuddy 是拿 state 问上游，
// Marvis 是上游直接把 code 送到我们门口。对用户来说两者都是
// 「生成链接 → 扫码 → 自动完成」，不需要复制任何东西。
//
// ⚠ redirect_uri 有**严格白名单**，只能用它自己的回调地址：
//
//	QQ:   redirect uri is illegal [appid: ...] (100010)
//	微信:  redirect_uri 参数错误
//
// 早先一次探测返回 501 是 WAF 劫持页，误判成了「不校验」。因此流程只能是
// 「用官方回调 → 授权完成后浏览器停在 Marvis 域上 → 把那条地址交回来」。
// Auto2API 这边提供两种交回方式：粘贴，或者让前端把地址栏内容读出来。

import (
	"context"
	"fmt"
	"net/url"
	"sync"
	"time"
)

const (
	// marvisSignInTTL 是一次登录票据的有效期。用户扫码 + 手机确认通常
	// 在 2 分钟内完成，5 分钟留足余量。
	marvisSignInTTL = 5 * time.Minute
	// marvisSignInPending 表示用户尚未完成授权。
	marvisSignInPending = "pending"
)

// marvisCallbackPath 是上游回调落地的本服务路径。
const marvisCallbackPath = "/api/v1/marvis/oauth/callback"

// MarvisSignIn 是一次登录会话。
type MarvisSignIn struct {
	State     string    `json:"state"`
	GUID      string    `json:"guid"`
	LoginType string    `json:"login_type"`
	AuthURL   string    `json:"auth_url"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`

	mu     sync.Mutex
	status string
	creds  *MarvisSignInResult
	errMsg string
}

// snapshot 返回票据的当前状态，避免调用方直接读结构体字段。
func (s *MarvisSignIn) snapshot() (status string, result *MarvisSignInResult, errMsg string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status, s.creds, s.errMsg
}

// marvisSignInStore 保存进行中的登录会话。
//
// 进程内 map 足够：登录是一次性短流程，多实例部署时前端会固定打到同一实例
// （会话由负载均衡的粘性保证，或退化为重复扫码）。这里不做跨实例共享 ——
// 代价只是偶发重试，比引入 Redis 依赖轻。
type marvisSignInStore struct {
	mu   sync.Mutex
	byID map[string]*MarvisSignIn
}

func newMarvisSignInStore() *marvisSignInStore {
	return &marvisSignInStore{byID: make(map[string]*MarvisSignIn)}
}

func (s *marvisSignInStore) put(sign *MarvisSignIn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	// 顺手清掉过期票据，避免长期运行后 map 无限增长。
	for id, item := range s.byID {
		if now.After(item.ExpiresAt) {
			delete(s.byID, id)
		}
	}
	s.byID[sign.State] = sign
}

func (s *marvisSignInStore) get(state string) *MarvisSignIn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byID[state]
}

func (s *marvisSignInStore) delete(state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.byID, state)
}

// MarvisSignInStart 是一次登录的启动结果。
type MarvisSignInStart struct {
	State       string    `json:"state"`
	GUID        string    `json:"guid"`
	LoginType   string    `json:"login_type"`
	AuthURL     string    `json:"auth_url"`
	CallbackURL string    `json:"callback_url"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// StartSignIn 生成一次带回调的登录会话。
//
// publicBase 是本服务对外可达的根地址（如 https://auto2api.example.com）；
// 为空时退回到调用方传入的请求地址。
func (s *MarvisOAuthService) StartSignIn(loginType, publicBase string) (*MarvisSignInStart, error) {
	code, err := normalizeMarvisLoginType(loginType)
	if err != nil {
		return nil, err
	}
	guid := marvisDeviceGUID()
	// state 用 guid 派生：上游会原样带回，我们据此找回票据。
	state := guid

	// 必须用上游自己的回调地址（白名单内），不能指向本服务。
	callback := marvisOAuthRedirect(code)
	authURL, err := marvisAuthURLWithRedirect(code, guid, state)
	if err != nil {
		return nil, err
	}

	sign := &MarvisSignIn{
		State:     state,
		GUID:      guid,
		LoginType: code,
		AuthURL:   authURL,
		CreatedAt: time.Now(),
		ExpiresAt: time.Now().Add(marvisSignInTTL),
		status:    marvisSignInPending,
	}
	if s.signIns == nil {
		s.signIns = newMarvisSignInStore()
	}
	s.signIns.put(sign)

	return &MarvisSignInStart{
		State:       state,
		GUID:        guid,
		LoginType:   code,
		AuthURL:     authURL,
		CallbackURL: callback,
		ExpiresAt:   sign.ExpiresAt,
	}, nil
}

// marvisAuthURLWithRedirect 拼授权地址（redirect_uri 固定为上游回调）。
func marvisAuthURLWithRedirect(loginType, guid, state string) (string, error) {
	redirect := marvisOAuthRedirect(loginType)
	if loginType == MarvisLoginTypeWX {
		p := url.Values{}
		p.Set("appid", MarvisWXAppID)
		p.Set("redirect_uri", redirect)
		p.Set("response_type", "code")
		p.Set("scope", "snsapi_login")
		p.Set("state", state)
		return "https://open.weixin.qq.com/connect/qrconnect?" + p.Encode(), nil
	}
	p := url.Values{}
	p.Set("which", "Login")
	p.Set("display", "pc")
	p.Set("response_type", "code")
	p.Set("client_id", MarvisQQAppID)
	p.Set("redirect_uri", redirect)
	p.Set("state", state)
	return "https://graph.qq.com/oauth2.0/authorize?" + p.Encode(), nil
}

// CompleteSignIn 在校收回调里用 code 换登录态，并把结果写进票据。
//
// 返回的账号信息用于前端展示；真正的凭据留在票据里，由 Poll 取回。
func (s *MarvisOAuthService) CompleteSignIn(ctx context.Context, state, code, loginType, guid string) error {
	if s.signIns == nil {
		return fmt.Errorf("没有进行中的登录")
	}
	sign := s.signIns.get(state)
	if sign == nil {
		return fmt.Errorf("登录会话不存在或已过期，请重新生成授权链接")
	}
	if time.Now().After(sign.ExpiresAt) {
		s.signIns.delete(state)
		return fmt.Errorf("登录已超时，请重新生成授权链接")
	}

	lt := loginType
	if lt == "" {
		lt = sign.LoginType
	}
	g := guid
	if g == "" {
		g = sign.GUID
	}

	result, err := s.ExchangeCode(ctx, MarvisExchangeRequest{
		Input:     code,
		GUID:      g,
		LoginType: lt,
		State:     state,
	})

	sign.mu.Lock()
	defer sign.mu.Unlock()
	if err != nil {
		sign.status = "failed"
		sign.errMsg = err.Error()
		return err
	}
	sign.status = "completed"
	sign.creds = result
	return nil
}

// PollSignIn 返回票据当前状态。前端按 1 秒轮询，与 WorkBuddy 一致。
func (s *MarvisOAuthService) PollSignIn(state string) (*MarvisSignInResult, bool, string, error) {
	if s.signIns == nil {
		return nil, false, "", fmt.Errorf("没有进行中的登录")
	}
	sign := s.signIns.get(state)
	if sign == nil {
		return nil, false, "", fmt.Errorf("登录会话不存在或已过期，请重新生成授权链接")
	}
	if time.Now().After(sign.ExpiresAt) {
		s.signIns.delete(state)
		return nil, false, "", fmt.Errorf("登录已超时，请重新生成授权链接")
	}
	status, result, errMsg := sign.snapshot()
	switch status {
	case "completed":
		// 取走后即失效，避免同一凭据被重复建号。
		s.signIns.delete(state)
		return result, true, "", nil
	case "failed":
		return nil, false, errMsg, nil
	default:
		return nil, false, "", nil
	}
}

// MarvisCallbackResult 是回调页要展示给用户的信息。
type MarvisCallbackResult struct {
	OK      bool
	Message string
}

// marvisOAuthSingleton 是回调路径用的全局实例。
//
// 回调是公开端点，由网关 handler 直接接收，拿不到 admin 侧的依赖注入链。
// 登录会话本来就存在进程内，单例足够；而 MarvisOAuthService 无状态
// （只有 http.Client 与票据表），不存在并发安全问题。
var (
	marvisOAuthOnce sync.Once
	marvisOAuthInst *MarvisOAuthService
)

// MarvisOAuthServiceForCallback 返回回调路径使用的登录服务。
func MarvisOAuthServiceForCallback() *MarvisOAuthService {
	marvisOAuthOnce.Do(func() {
		marvisOAuthInst = NewMarvisOAuthService()
	})
	return marvisOAuthInst
}

// BindMarvisOAuthService 让启动时的实例接管回调路径。
//
// wire 里构造的那个实例带着后端配置，回调必须用同一个，否则查不到票据。
func BindMarvisOAuthService(svc *MarvisOAuthService) {
	if svc == nil {
		return
	}
	marvisOAuthOnce.Do(func() { marvisOAuthInst = svc })
	if marvisOAuthInst != svc {
		marvisOAuthInst = svc
	}
}
