package service

// 腾讯 Marvis 的登录流程。
//
// 与 WorkBuddy 的差别：WorkBuddy 服务端拿 state 后轮询上游；Marvis 的授权在
// QQ / 微信的开放平台上完成，回调会把 code 送回来，我们要拿 code 自己再换一次
// 登录态。因此流程是：
//
//	1. 生成设备 guid 与授权 URL（本地拼，不调上游）
//	2. 用户在浏览器里扫码完成授权，浏览器跳到 Marvis 的回调地址
//	3. 把回调地址（或其中的 code）交回本服务
//	4. 本服务用 code 换登录态：GET {base}/marvis_client_login/marvis_oauth
//	   成功是 302 + Set-Cookie，从 Cookie 里读出 openid / access_token / refresh_token
//
// 第 3 步对应 magpie 的「粘贴回调地址」登录方式 —— 它本来就是为无浏览器的
// 服务器环境设计的，而 Auto2API 正是这种环境。

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MarvisOAuthService 负责 Marvis 的登录与续期。
type MarvisOAuthService struct {
	client *http.Client
	// timeout 是单次换取登录态的超时。
	timeout time.Duration
	// signIns 保存进行中的回调式登录会话。
	signIns *marvisSignInStore
}

// NewMarvisOAuthService 构造 Marvis 登录服务。
func NewMarvisOAuthService() *MarvisOAuthService {
	return &MarvisOAuthService{
		client:  &http.Client{Timeout: 30 * time.Second},
		timeout: 30 * time.Second,
		signIns: newMarvisSignInStore(),
	}
}

// MarvisAuthURLResult 是生成授权链接的结果。
type MarvisAuthURLResult struct {
	// AuthURL 是要用户在浏览器里打开的授权页地址。
	AuthURL string `json:"auth_url"`
	// GUID 是本次登录的设备标识。换取登录态时必须带同一个值。
	GUID string `json:"guid"`
	// State 是放进授权 URL 的 state（QQ 是随机小数，微信就是 guid）。
	State string `json:"state"`
	// LoginType 是 1（QQ）或 2（微信）。
	LoginType string `json:"login_type"`
	// RedirectURI 是回调地址，用户登录后会跳到它。
	RedirectURI string `json:"redirect_uri"`
	// ExpiresAt 提示这次授权窗口的有效期。
	ExpiresAt time.Time `json:"expires_at"`
}

// MarvisSignInResult 是完成登录后的账号凭据。
type MarvisSignInResult struct {
	Credentials MarvisCredentials `json:"credentials"`
	// AccountName 是建议的账号显示名（昵称，取不到时用 openid 尾号）。
	AccountName string `json:"account_name"`
}

// marvisOAuthRedirect 是回调地址，必须先带上 login_type 查询参数。
func marvisOAuthRedirect(loginType string) string {
	return MarvisBaseURL + MarvisOAuthPath + "?login_type=" + url.QueryEscape(loginType)
}

// GenerateAuthURL 生成 QQ / 微信授权链接。
//
// loginType 接受 "1"/"QQ"/"qc" 或 "2"/"WX"/"wechat"，空值默认 QQ。
func (s *MarvisOAuthService) GenerateAuthURL(loginType string) (*MarvisAuthURLResult, error) {
	code, err := normalizeMarvisLoginType(loginType)
	if err != nil {
		return nil, err
	}
	guid := marvisDeviceGUID()

	var authURL, state string
	if code == MarvisLoginTypeWX {
		// 微信：state 就是 guid，回调时服务端会原样带回。
		state = guid
		p := url.Values{}
		p.Set("appid", MarvisWXAppID)
		p.Set("redirect_uri", marvisOAuthRedirect(code))
		p.Set("response_type", "code")
		p.Set("scope", "snsapi_login")
		p.Set("state", state)
		authURL = "https://open.weixin.qq.com/connect/qrconnect?" + p.Encode()
	} else {
		// QQ：⚠ state 必须是**随机小数**的字符串形态（如 31.1177412）。
		// Marvis 渲染层就是这么写的，服务端按格式校验，整数会被判参数无效。
		state = marvisQQState()
		p := url.Values{}
		p.Set("which", "Login")
		p.Set("display", "pc")
		p.Set("response_type", "code")
		p.Set("client_id", MarvisQQAppID)
		p.Set("redirect_uri", marvisOAuthRedirect(code))
		p.Set("state", state)
		authURL = "https://graph.qq.com/oauth2.0/authorize?" + p.Encode()
	}

	return &MarvisAuthURLResult{
		AuthURL:     authURL,
		GUID:        guid,
		State:       state,
		LoginType:   code,
		RedirectURI: marvisOAuthRedirect(code),
		ExpiresAt:   time.Now().Add(10 * time.Minute),
	}, nil
}

// marvisQQState 生成 QQ 授权用的 state：0~100 之间的随机小数。
func marvisQQState() string {
	// 取 7 位小数，与渲染层 Math.random()*100 的形态一致。
	n := marvisNonceSeq.Add(1)
	frac := float64(n%10000000) / 100000.0
	state := float64(time.Now().UnixNano()%100) + frac
	return trimFloatString(state)
}

// trimFloatString 把浮点数格式化成不带多余尾零的字符串。
func trimFloatString(v float64) string {
	s := fmt.Sprintf("%.7f", v)
	s = strings.TrimRight(s, "0")
	s = strings.TrimRight(s, ".")
	return s
}

// marvisExchangeLoginType 把内部编码（1/2）转成换取接口要求的字符串。
//
// ⚠ 这两处取值不同，混用会直接 -109 RC_PARAMS_INVALID：
//
//	授权 URL 的 redirect_uri 里是 login_type=1 / 2（数字）
//	换取接口必须传 login_type=QC / WX（字符串）
//
// 实测：传数字回 -109，传 QC/WX 回 302 RC_SUCCESS。
func marvisExchangeLoginType(code string) string {
	if code == MarvisLoginTypeWX {
		return "WX"
	}
	return "QC"
}

// normalizeMarvisLoginType 把各种写法归一成 "1" / "2"。
func normalizeMarvisLoginType(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "qq", "qc":
		return MarvisLoginTypeQC, nil
	case "2", "wx", "wechat", "weixin":
		return MarvisLoginTypeWX, nil
	}
	return "", fmt.Errorf("不支持的登录方式: %s（用 qq 或 wechat）", raw)
}

// MarvisExchangeRequest 是完成登录的入参。
type MarvisExchangeRequest struct {
	// Input 是用户交回的内容：完整回调地址、纯 code、或整段 JSON。
	Input string `json:"input"`
	// GUID 是 GenerateAuthURL 返回的设备标识。
	GUID string `json:"guid"`
	// LoginType 是 1（QQ）或 2（微信）。
	LoginType string `json:"login_type"`
	// State 可选；回调地址里带了就用那个。
	State string `json:"state"`
}

// ExchangeCode 用回调里的 code 换取登录态。
func (s *MarvisOAuthService) ExchangeCode(ctx context.Context, req MarvisExchangeRequest) (*MarvisSignInResult, error) {
	loginType, err := normalizeMarvisLoginType(req.LoginType)
	if err != nil {
		return nil, err
	}
	code, state := marvisExtractCodeAndState(req.Input)
	if code == "" {
		return nil, fmt.Errorf("没有找到授权码：请粘贴登录完成后的完整地址（里面有 code=）")
	}
	guid := strings.TrimSpace(req.GUID)
	if guid == "" && loginType == MarvisLoginTypeWX {
		// 微信的 state 就是 guid，允许从回调里补。
		guid = state
	}
	if guid == "" {
		return nil, fmt.Errorf("缺少设备标识 guid：请先用同一次会话生成的授权链接登录")
	}
	if state == "" {
		state = strings.TrimSpace(req.State)
	}

	creds, err := s.marvisExchange(ctx, code, state, guid, loginType)
	if err != nil {
		return nil, err
	}

	// 补一次昵称，失败不影响登录。
	displayName := creds.Nickname
	if displayName == "" {
		if profile, perr := s.fetchProfile(ctx, creds); perr == nil && profile != nil {
			if nick := strings.TrimSpace(profile.Nickname); nick != "" {
				creds.Nickname = nick
				creds.Avatar = profile.Avatar
				displayName = nick
			}
		}
	}
	if displayName == "" {
		displayName = marvisAccountNameFromOpenID(creds.OpenID)
	}
	return &MarvisSignInResult{Credentials: creds, AccountName: displayName}, nil
}

// marvisExchange 走一次回调换取登录态。
//
// ⚠ 必须 redirect: manual —— 成功形态是 302 + Set-Cookie，跟随重定向会把
// 登录态丢掉。
func (s *MarvisOAuthService) marvisExchange(ctx context.Context, code, state, guid, loginType string) (MarvisCredentials, error) {
	// query 里只能有 login_type / code / state，多一个参数服务端就回 -109。
	// ⚠ login_type 这里是 **QC / WX**，不是 1 / 2。
	q := url.Values{}
	q.Set("login_type", marvisExchangeLoginType(loginType))
	q.Set("code", code)
	if state != "" {
		q.Set("state", state)
	}
	target := MarvisBaseURL + MarvisOAuthPath + "?" + q.Encode()

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return MarvisCredentials{}, err
	}
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/json;q=0.9,*/*;q=0.8")
	req.Header.Set("User-Agent", MarvisBrowserUA)
	// ⚠ Ual-Access-Guid 是**必需**的：缺了它上游回 -109 RC_PARAMS_INVALID，
	// 与 code 无效的表现一样，很容易误判成授权码过期。
	// ⚠ 但也不能带 Ual-Access-Businessid —— 带上会回 400。两者都不要试。
	req.Header.Set("Ual-Access-Guid", guid)

	client := &http.Client{
		Timeout: s.timeout,
		// 不跟随重定向：Set-Cookie 就在 302 上。
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return MarvisCredentials{}, fmt.Errorf("换取登录态失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	// ① 主路径：302 + Set-Cookie
	if cookies := resp.Cookies(); len(cookies) > 0 {
		if creds, ok := marvisCredentialsFromCookies(cookies, guid, loginType); ok {
			return creds, nil
		}
	}

	// ② 兼容形态：200 + JSON
	if creds, ok := marvisCredentialsFromJSON(body, guid, loginType); ok {
		return creds, nil
	}

	errText := strings.TrimSpace(string(body))
	if len(errText) > 300 {
		errText = errText[:300] + "..."
	}
	return MarvisCredentials{}, fmt.Errorf("换取登录态失败（HTTP %d）：%s",
		resp.StatusCode, marvisFriendlyError(errText))
}

// marvisCredentialsFromCookies 从 Set-Cookie 里解出登录态。
func marvisCredentialsFromCookies(cookies []*http.Cookie, guid, loginType string) (MarvisCredentials, bool) {
	jar := make(map[string]string, len(cookies))
	for _, c := range cookies {
		if c == nil {
			continue
		}
		jar[strings.ToLower(c.Name)] = c.Value
	}
	openid := jar["openid"]
	access := jar["accesstoken"]
	if openid == "" || access == "" {
		return MarvisCredentials{}, false
	}
	creds := MarvisCredentials{
		OpenID:       openid,
		AccessToken:  access,
		RefreshToken: jar["refreshtoken"],
		GUID:         guid,
		LoginType:    loginType,
		Nickname:     marvisSafeDecode(jar["nickname"]),
		Avatar:       marvisSafeDecode(jar["head_img_url"]),
	}
	if raw := jar["access_token_expire_time"]; raw != "" {
		if ts, ok := marvisParseUnixSeconds(raw); ok {
			creds.ExpiresAt = ts
		}
	}
	if creds.ExpiresAt == 0 {
		creds.ExpiresAt = time.Now().Add(marvisTokenTTL(loginType)).Unix()
	}
	// 登录方式以 Cookie 里的为准（QC / WX），映射回数字码。
	switch strings.ToLower(jar["logintype"]) {
	case "qc":
		creds.LoginType = MarvisLoginTypeQC
	case "wx", "wxapp":
		creds.LoginType = MarvisLoginTypeWX
	}
	return creds, true
}

// marvisCredentialsFromJSON 从 JSON 响应里解出登录态（兼容形态）。
func marvisCredentialsFromJSON(body []byte, guid, loginType string) (MarvisCredentials, bool) {
	trimmed := strings.TrimSpace(string(body))
	a := strings.Index(trimmed, "{")
	b := strings.LastIndex(trimmed, "}")
	if a < 0 || b <= a {
		return MarvisCredentials{}, false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed[a:b+1]), &payload); err != nil {
		return MarvisCredentials{}, false
	}
	if code, ok := payload["code"].(float64); ok && code != 0 {
		return MarvisCredentials{}, false
	}
	info, _ := payload["user_info"].(map[string]any)
	if info == nil {
		info, _ = payload["data"].(map[string]any)
	}
	if info == nil {
		info = payload
	}
	openid := marvisStringField(info, "openid", "openId", "open_id")
	access := marvisStringField(info, "access_token", "accessToken")
	if openid == "" || access == "" {
		return MarvisCredentials{}, false
	}
	creds := MarvisCredentials{
		OpenID:       openid,
		AccessToken:  access,
		RefreshToken: marvisStringField(info, "refresh_token", "refreshToken"),
		GUID:         guid,
		LoginType:    loginType,
		Nickname:     marvisSafeDecode(marvisStringField(info, "nick_name", "nickname")),
		Avatar:       marvisSafeDecode(marvisStringField(info, "head_img_url", "avatar")),
	}
	if exp, ok := info["expires_in"].(float64); ok && exp > 0 {
		creds.ExpiresAt = time.Now().Add(time.Duration(exp) * time.Second).Unix()
	}
	if creds.ExpiresAt == 0 {
		creds.ExpiresAt = time.Now().Add(marvisTokenTTL(loginType)).Unix()
	}
	return creds, true
}

// marvisTokenTTL 是 token 的兜底有效期。
//
// 微信的 token 只有 2 小时，QQ 的是 30 天。上游没给 expires_in 时按登录方式取。
func marvisTokenTTL(loginType string) time.Duration {
	if loginType == MarvisLoginTypeWX {
		return 2 * time.Hour
	}
	return 30 * 24 * time.Hour
}

// marvisExtractCodeAndState 从用户输入里抠出 code 与 state。
// 支持：纯 code、完整回调地址、整段 JSON。
func marvisExtractCodeAndState(input string) (string, string) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", ""
	}
	// 完整 URL 或带查询串的片段
	if strings.Contains(raw, "code=") {
		if u, err := url.Parse(raw); err == nil && u.RawQuery != "" {
			q := u.Query()
			return strings.TrimSpace(q.Get("code")), strings.TrimSpace(q.Get("state"))
		}
		// 不含 scheme 的片段：手工解析
		if idx := strings.Index(raw, "?"); idx >= 0 {
			if q, err := url.ParseQuery(raw[idx+1:]); err == nil {
				return strings.TrimSpace(q.Get("code")), strings.TrimSpace(q.Get("state"))
			}
		}
	}
	// JSON 片段
	if strings.HasPrefix(raw, "{") {
		var payload map[string]any
		if err := json.Unmarshal([]byte(raw), &payload); err == nil {
			return marvisStringField(payload, "code"), marvisStringField(payload, "state")
		}
	}
	// 纯 code：只允许字母数字与常见分隔符，避免把整段文本当 code 发出去
	if len(raw) <= 128 && !strings.ContainsAny(raw, " \n\t") {
		return raw, ""
	}
	return "", ""
}

// marvisStringField 依次尝试多个键取字符串。
func marvisStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			if s, isStr := v.(string); isStr && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

// marvisSafeDecode 做一次 URL 百分号解码；失败时原样返回。
func marvisSafeDecode(s string) string {
	if s == "" {
		return ""
	}
	if decoded, err := url.QueryUnescape(s); err == nil {
		return decoded
	}
	return s
}

// marvisParseUnixSeconds 解析 Unix 秒时间戳。
func marvisParseUnixSeconds(raw string) (int64, bool) {
	var v int64
	for _, ch := range raw {
		if ch < '0' || ch > '9' {
			return 0, false
		}
		v = v*10 + int64(ch-'0')
	}
	if v <= 0 {
		return 0, false
	}
	// 毫秒时间戳归一成秒
	if v > 1e12 {
		v /= 1000
	}
	return v, true
}

// marvisAccountNameFromOpenID 在拿不到昵称时给出一个可读的账号名。
func marvisAccountNameFromOpenID(openid string) string {
	openid = strings.TrimSpace(openid)
	if openid == "" {
		return "Marvis 账号"
	}
	if len(openid) <= 6 {
		return "Marvis " + openid
	}
	return "Marvis ..." + openid[len(openid)-6:]
}

// marvisFriendlyError 把上游错误码翻译成可读提示。
func marvisFriendlyError(text string) string {
	switch {
	case strings.Contains(text, "-109"):
		return "参数不合法（-109）：code 与 guid 必须来自同一次登录"
	case strings.Contains(text, "-107"):
		return "授权码无效或已过期（-107）：请重新生成授权链接再登录"
	case strings.Contains(text, "4100403"):
		return "登录态过期：请重新登录"
	default:
		return text
	}
}

// marvisProfile 是用户信息。
type marvisProfile struct {
	Nickname string
	Avatar   string
}

// fetchProfile 读取当前账号的昵称与头像。失败不影响登录主流程。
//
// ⚠ 这个接口的形状很反直觉，实测要点（magpie profile.mjs 逐条验证过）：
//
//	GET  /v3/marvis_get_user_info             → WAF 403
//	GET  + Ual-Access-Access-Token            → 仍 403
//	POST /marvis_client/marvis_get_user_info  → 200 {"ret":0,"nick_name":"..."}
//
// 也就是：路径在 /marvis_client/ 下而不是 /v3/，身份放在 **body 的 userInfo**
// 里，而**不能**带 Ual-Access-Access-Token / Openid / Guid 那几个头 ——
// 带了会直接 500。只带 businessid + 时间戳 + nonce + 签名。
func (s *MarvisOAuthService) fetchProfile(ctx context.Context, creds MarvisCredentials) (*marvisProfile, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	loginType := "QC"
	if creds.LoginType == MarvisLoginTypeWX {
		loginType = "WX"
	}
	bodyMap := map[string]any{
		"userInfo": map[string]any{
			"openId":       creds.OpenID,
			"accessToken":  creds.AccessToken,
			"refreshToken": creds.RefreshToken,
			"loginType":    loginType,
			"guid":         creds.GUID,
		},
	}
	raw, err := json.Marshal(bodyMap)
	if err != nil {
		return nil, err
	}
	bodyStr := string(raw)

	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	timestamp = timestamp[:len(timestamp)] // 毫秒
	nonce := fmt.Sprintf("%04d", 1000+marvisNonceSeq.Add(1)%9000)
	signature := marvisSign(bodyStr, timestamp, nonce)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		MarvisBaseURL+MarvisUserInfoPath, strings.NewReader(bodyStr))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Ual-Access-Businessid", MarvisBusinessID)
	req.Header.Set("Ual-Access-Timestamp", timestamp)
	req.Header.Set("Ual-Access-Nonce", nonce)
	req.Header.Set("Ual-Access-Signature", signature)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("用户信息返回不是 JSON（HTTP %d）", resp.StatusCode)
	}
	if ret, ok := payload["ret"].(float64); ok && ret != 0 {
		return nil, fmt.Errorf("用户信息返回 ret=%d", int64(ret))
	}
	nickname := marvisSafeDecode(marvisStringField(payload, "nick_name", "nickname"))
	if nickname == "" {
		return nil, fmt.Errorf("用户信息里没有昵称")
	}
	return &marvisProfile{
		Nickname: nickname,
		Avatar:   marvisSafeDecode(marvisStringField(payload, "head_img_url", "avatar")),
	}, nil
}

// MarvisRefreshRequest 是续期入参。
type MarvisRefreshRequest struct {
	OpenID       string
	AccessToken  string
	RefreshToken string
	GUID         string
	LoginType    string
}

// RefreshToken 用 refresh token 换一组新 token。
//
// ⚠ 上游**成对轮换** token：续期成功后旧的 access/refresh 立即失效。调用方
// 必须串行化（同账号同时只允许一次刷新），否则会把账号搞失效。
func (s *MarvisOAuthService) RefreshToken(ctx context.Context, in MarvisRefreshRequest) (MarvisCredentials, error) {
	if strings.TrimSpace(in.RefreshToken) == "" {
		return MarvisCredentials{}, fmt.Errorf("缺少 refresh_token，无法续期")
	}
	bodyMap := map[string]any{
		"userInfo": map[string]any{
			"openId":       in.OpenID,
			"refreshToken": in.RefreshToken,
			"accessToken":  in.AccessToken,
			// ⚠ 恒为字面量 "Marvis"，不是 Ual-Access-Login-Type 的 1/2
			"loginType": "Marvis",
		},
	}
	raw, err := json.Marshal(bodyMap)
	if err != nil {
		return MarvisCredentials{}, err
	}
	bodyStr := string(raw)
	headers := marvisSignedHeaders(MarvisCredentials{
		OpenID:      in.OpenID,
		AccessToken: in.AccessToken,
		GUID:        in.GUID,
		LoginType:   in.LoginType,
	}, bodyStr)

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, MarvisBaseURL+MarvisRefreshPath, strings.NewReader(bodyStr))
	if err != nil {
		return MarvisCredentials{}, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return MarvisCredentials{}, fmt.Errorf("续期请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return MarvisCredentials{}, fmt.Errorf("续期返回不是 JSON（HTTP %d）", resp.StatusCode)
	}
	info, _ := payload["user_info"].(map[string]any)
	if info == nil {
		info, _ = payload["data"].(map[string]any)
	}
	access := ""
	refresh := ""
	if info != nil {
		access = marvisStringField(info, "access_token", "accessToken")
		refresh = marvisStringField(info, "refresh_token", "refreshToken")
	}
	if bizCode, ok := payload["code"].(float64); ok && bizCode != 0 {
		msg := marvisStringField(payload, "msg")
		return MarvisCredentials{}, fmt.Errorf("续期失败（code=%d）：%s", int64(bizCode), msg)
	}
	if access == "" {
		return MarvisCredentials{}, fmt.Errorf("续期没有返回新的 access_token")
	}
	// ⚠ refresh_token 可能回空串（微信实测）。空串是「这次没给新值」，不是清空。
	if refresh == "" {
		refresh = in.RefreshToken
	}
	loginType := in.LoginType
	if loginType == "" {
		loginType = MarvisLoginTypeQC
	}
	return MarvisCredentials{
		OpenID:       in.OpenID,
		AccessToken:  access,
		RefreshToken: refresh,
		GUID:         in.GUID,
		LoginType:    loginType,
		ExpiresAt:    time.Now().Add(marvisTokenTTL(loginType)).Unix(),
	}, nil
}
