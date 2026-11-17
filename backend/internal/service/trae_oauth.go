package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Trae 的登录：浏览器授权 + 粘贴回调地址。
//
// Trae 的授权页只接受 http://127.0.0.1:<port>/authorize 作为回调，因此部署在
// 服务器上的 Auto2API 收不到回调（回调会落到用户自己的机器）。与 Marvis 同一
// 条路：生成链接 → 用户浏览器完成授权 → 把地址栏里的回调 URL 粘回来。
//
// 回调参数（对照 magpie 的 Trae 提供方）：
//
//	userJwt     JSON：{Token, RefreshToken, TokenExpireAt, ClientID}
//	userInfo    JSON：{UserID, ScreenName, Host, AIRegion}
//	refreshToken / clientId / userId  明文参数（userJwt 缺失时的兜底）
//	error / error_description         失败时才出现
//
// 设备身份随登录一起生成并交给授权页；授权完成后设备必须随账号保存，后续每次
// 请求与续期都用同一套设备。

const (
	// traeAuthCallbackAddr 是授权页要求的回调地址（固定端口，便于用户识别）。
	traeAuthCallbackAddr = "http://127.0.0.1:47823/authorize"
	// traeAuthSessionTTL 是一次授权会话的有效期。
	traeAuthSessionTTL = 15 * time.Minute
	// traeRefreshPathCN 是国内站基于设备的续期路径。
	traeRefreshPathCN = "/trae/api/v3/oauth/ExchangeToken"
	// traeRefreshPathCloudIDE 是国际站（以及国内站兜底）的续期路径。
	traeRefreshPathCloudIDE = "/cloudide/api/v3/trae/oauth/ExchangeToken"
	// traeRefreshLead 是提前续期的窗口。
	traeRefreshLead = 10 * time.Minute
)

// traeAuthSession 是一次待完成的授权。
type traeAuthSession struct {
	Platform  string
	Device    *traeDevice
	TraceID   string
	ExpiresAt time.Time
}

// traeAuthSessions 保存待完成的授权会话（进程内，带 TTL）。
//
// 设备私钥不发给浏览器：前端只拿到 state，回调时由服务端取出设备并一起写进
// 账号凭据。
type traeAuthSessions struct {
	mu sync.Mutex
	m  map[string]*traeAuthSession
}

var traeAuthStore = &traeAuthSessions{m: map[string]*traeAuthSession{}}

func (s *traeAuthSessions) put(session *traeAuthSession) (string, error) {
	state, err := traeRandomHex(16)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for key, item := range s.m {
		if now.After(item.ExpiresAt) {
			delete(s.m, key)
		}
	}
	session.ExpiresAt = now.Add(traeAuthSessionTTL)
	s.m[state] = session
	return state, nil
}

func (s *traeAuthSessions) take(state string) (*traeAuthSession, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.m[state]
	if !ok {
		return nil, false
	}
	delete(s.m, state)
	if time.Now().After(session.ExpiresAt) {
		return nil, false
	}
	return session, true
}

func traeRandomHex(size int) (string, error) {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// traeAuthInstruction 是生成授权链接的返回。
type traeAuthInstruction struct {
	// AuthURL 需要在浏览器打开。
	AuthURL string `json:"auth_url"`
	// State 回传给回调接口。
	State string `json:"state"`
	// Platform 回显站点。
	Platform string `json:"platform"`
	// CallbackHint 是回调会落到的地址（打不开属正常，用户复制地址栏即可）。
	CallbackHint string `json:"callback_hint"`
}

// startTraeAuthorization 生成一次授权链接，并暂存该账号的设备身份。
func startTraeAuthorization(platform string) (*traeAuthInstruction, error) {
	site := traeSiteForPlatform(platform)
	if site == nil {
		return nil, fmt.Errorf("trae: 不支持的平台 %q", platform)
	}
	device, err := newTraeDevice(site)
	if err != nil {
		return nil, err
	}
	trace, err := traeRandomHex(16)
	if err != nil {
		return nil, err
	}
	state, err := traeAuthStore.put(&traeAuthSession{Platform: platform, Device: device, TraceID: trace})
	if err != nil {
		return nil, err
	}

	// 参数与 Trae IDE 自己发起登录时一致（见 magpie 的 Trae 提供方）。
	q := url.Values{}
	q.Set("login_version", "1")
	q.Set("auth_from", "solo")
	q.Set("login_channel", "native_ide")
	q.Set("plugin_version", site.PluginVersion)
	q.Set("auth_type", "local")
	q.Set("client_id", traeOAuthClientID)
	q.Set("redirect", "0")
	q.Set("login_trace_id", trace)
	q.Set("auth_callback_url", traeAuthCallbackAddr)
	q.Set("machine_id", device.MachineID)
	q.Set("device_id", device.DeviceID)
	q.Set("x_device_id", device.DeviceID)
	q.Set("x_machine_id", device.MachineID)
	q.Set("x_device_brand", device.DeviceBrand)
	q.Set("x_device_type", device.DeviceType)
	q.Set("x_os_version", device.OSVersion)
	q.Set("x_env", "")
	q.Set("x_app_version", site.IDEVersion)
	q.Set("x_app_type", "stable")
	// 企业（SaaS）登录与企业账号并列提供，个人号仍可用。
	q.Set("hide_saas_login", "false")

	return &traeAuthInstruction{
		AuthURL:      site.AuthURL + "?" + q.Encode(),
		State:        state,
		Platform:     site.ID,
		CallbackHint: traeAuthCallbackAddr,
	}, nil
}

// traeCallback 是从粘贴的回调地址里解析出来的登录结果。
type traeCallback struct {
	Token        string
	RefreshToken string
	ClientID     string
	UID          string
	Name         string
	Region       string
	Host         string
	AuthHost     string
	ExpiresAt    time.Time
}

// parseTraeCallback 解析用户粘贴的回调地址。
//
// 接受三种输入：整条回调 URL（推荐）、只有 query 的片段、以及页面里直接复制的
// JSON。Trae 的回调是 GET + query（userJwt / userInfo / refreshToken），
// 这里同时兼容参数在 #fragment 里、以及被二次 URL 编码的情况。
func parseTraeCallback(raw string) (*traeCallback, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("trae: 回调地址为空")
	}

	// 直接粘贴 JSON（userJwt/userInfo 内容）时按字段解析。
	if strings.HasPrefix(raw, "{") {
		values := url.Values{}
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, errors.New("trae: 粘贴的内容既不是地址也不是 JSON")
		}
		for key, value := range obj {
			if text, ok := value.(string); ok {
				values.Set(key, text)
			} else if value != nil {
				if encoded, err := json.Marshal(value); err == nil {
					values.Set(key, string(encoded))
				}
			}
		}
		return traeCallbackFromValues(values)
	}

	if strings.Contains(raw, "/authorization") && !strings.Contains(raw, "userJwt") &&
		!strings.Contains(raw, "refreshToken") {
		return nil, errors.New("trae: 这是授权页地址，不是回调地址——请在浏览器里完成登录，等页面跳到 127.0.0.1:47823 后复制地址栏里的那一条")
	}

	query := raw
	if idx := strings.Index(raw, "?"); idx >= 0 {
		query = raw[idx+1:]
	}
	// 参数也可能落在 #fragment 里。
	values, err := url.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("trae: 回调地址解析失败: %w", err)
	}
	if frag := traeFragmentValues(raw); len(frag) > 0 {
		for key, items := range frag {
			if values.Get(key) == "" && len(items) > 0 {
				values.Set(key, items[0])
			}
		}
	}
	if len(values) == 0 {
		return nil, errors.New("trae: 粘贴的地址里没有任何参数，请复制浏览器跳转到 127.0.0.1:47823 后的完整地址")
	}
	return traeCallbackFromValues(values)
}

// traeFragmentValues 取 # 之后的参数（有些客户端把令牌放在 fragment 里）。
func traeFragmentValues(raw string) url.Values {
	idx := strings.Index(raw, "#")
	if idx < 0 || idx == len(raw)-1 {
		return nil
	}
	fragment := raw[idx+1:]
	if !strings.Contains(fragment, "=") {
		return nil
	}
	values, err := url.ParseQuery(fragment)
	if err != nil {
		return nil
	}
	return values
}

// traeDecoded 在值看起来是二次编码时再解一次。
func traeDecoded(value string) string {
	trimmed := strings.TrimSpace(value)
	if !strings.Contains(trimmed, "%") {
		return trimmed
	}
	if decoded, err := url.QueryUnescape(trimmed); err == nil {
		return decoded
	}
	return trimmed
}

// traeCallbackFromValues 把参数集合解析成回调结果。
func traeCallbackFromValues(values url.Values) (*traeCallback, error) {
	if msg := values.Get("error_description"); msg != "" {
		return nil, fmt.Errorf("trae: 授权被拒绝: %s", msg)
	}
	if msg := values.Get("error"); msg != "" {
		return nil, fmt.Errorf("trae: 授权被拒绝: %s", msg)
	}

	out := &traeCallback{
		RefreshToken: traeDecoded(values.Get("refreshToken")),
		ClientID:     traeDecoded(values.Get("clientId")),
		UID:          traeDecoded(values.Get("userId")),
		Host:         traeDecoded(values.Get("host")),
		AuthHost:     firstNonEmpty(traeDecoded(values.Get("authHost")), traeDecoded(values.Get("auth_host"))),
	}

	if jwtRaw := traeDecoded(values.Get("userJwt")); jwtRaw != "" {
		var jwt map[string]any
		if err := json.Unmarshal([]byte(jwtRaw), &jwt); err != nil {
			return nil, fmt.Errorf("trae: userJwt 解析失败: %w", err)
		}
		out.Token = firstNonEmpty(traeStringValue(jwt, "Token", "token"))
		out.RefreshToken = firstNonEmpty(out.RefreshToken, traeStringValue(jwt, "RefreshToken", "refreshToken"))
		out.ClientID = firstNonEmpty(out.ClientID, traeStringValue(jwt, "ClientID", "clientId"))
		out.ExpiresAt = traeExpiryFromUnix(int64(traeNumberValue(jwt, "TokenExpireAt", "tokenExpireAt")))
	}

	if infoRaw := traeDecoded(values.Get("userInfo")); infoRaw != "" {
		if info, ok := traeAnyToMap(infoRaw); ok {
			out.UID = firstNonEmpty(out.UID, traeStringValue(info, "UserID", "userId"))
			out.Name = traeStringValue(info, "ScreenName", "screenName")
			out.Region = traeStringValue(info, "AIRegion", "aiRegion", "Region", "region")
			out.Host = firstNonEmpty(out.Host, traeStringValue(info, "Host", "host"))
			out.AuthHost = firstNonEmpty(out.AuthHost, traeStringValue(info, "AuthHost", "authHost"))
		}
	}

	if out.Token == "" && out.RefreshToken == "" {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sortStrings(keys)
		return nil, fmt.Errorf("trae: 回调里没有登录态（userJwt / refreshToken 都为空）。收到的参数：%s", strings.Join(keys, ", "))
	}
	if out.ClientID == "" {
		out.ClientID = traeOAuthClientID
	}
	if out.ExpiresAt.IsZero() && out.Token != "" {
		out.ExpiresAt = traeJWTExpiry(out.Token)
	}
	return out, nil
}

// credentials 把回调结果组装成账号凭据。
func (c *traeCallback) credentials(platform string, device *traeDevice) map[string]any {
	creds := map[string]any{
		"access_token":  c.Token,
		"refresh_token": c.RefreshToken,
		"client_id":     c.ClientID,
		"uid":           c.UID,
		"token_type":    "Cloud-IDE-JWT",
		"expires_at":    c.ExpiresAt.Format(time.RFC3339),
		"platform":      platform,
	}
	if !c.ExpiresAt.IsZero() {
		creds["expiresAt"] = c.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if c.Region != "" {
		creds["region"] = c.Region
	}
	if c.Host != "" {
		creds["host"] = c.Host
	}
	if c.AuthHost != "" {
		creds["auth_host"] = c.AuthHost
	}
	if device != nil {
		creds["device_id"] = device.DeviceID
		creds["machine_id"] = device.MachineID
		creds["device_brand"] = device.DeviceBrand
		creds["device_type"] = device.DeviceType
		creds["os_version"] = device.OSVersion
		creds["device_private_key"] = device.PrivateKeyPEM
		creds["device_public_key"] = device.PublicKeyPEM
	}
	return creds
}

// traeToken 是凭据里与登录态相关的部分。
type traeToken struct {
	Access    string
	Refresh   string
	UID       string
	ClientID  string
	Region    string
	AuthHost  string
	Device    *traeDevice
	ExpiresAt time.Time
}

// traeTokenOf 从账号凭据里取出登录态与设备。
func traeTokenOf(creds map[string]any) traeToken {
	if creds == nil {
		return traeToken{}
	}
	out := traeToken{
		Access:   traeStringValue(creds, "access_token"),
		Refresh:  traeStringValue(creds, "refresh_token"),
		UID:      traeStringValue(creds, "uid"),
		ClientID: traeStringValue(creds, "client_id"),
		Region:   traeStringValue(creds, "region"),
		AuthHost: traeStringValue(creds, "auth_host"),
	}
	if out.ClientID == "" {
		out.ClientID = traeOAuthClientID
	}
	if raw := traeStringValue(creds, "expires_at", "expiresAt"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			out.ExpiresAt = t
		}
	}
	deviceID := traeStringValue(creds, "device_id")
	machineID := traeStringValue(creds, "machine_id")
	priv := traeStringValue(creds, "device_private_key")
	if deviceID != "" && machineID != "" && priv != "" {
		out.Device = &traeDevice{
			DeviceID:      deviceID,
			MachineID:     machineID,
			PrivateKeyPEM: priv,
			PublicKeyPEM:  traeStringValue(creds, "device_public_key"),
			DeviceBrand:   traeStringValue(creds, "device_brand"),
			DeviceType:    traeStringValue(creds, "device_type"),
			OSVersion:     traeStringValue(creds, "os_version"),
		}
	}
	return out
}

// traeRefreshResult 是续期结果。
type traeRefreshResult struct {
	Access    string
	Refresh   string
	ExpiresAt time.Time
	UID       string
	Name      string
	Region    string
	Host      string
	AuthHost  string
}

// traeExchangeToken 用 refresh token 换新 JWT。
//
// 国内站先走基于设备的 /trae/api/v3/oauth/ExchangeToken（带 DeviceProof），
// 被拒时回落 /cloudide/v3，与国际站同一路径。
func traeExchangeToken(ctx context.Context, client *http.Client, site *traeSite, token traeToken) (*traeRefreshResult, error) {
	if site == nil {
		return nil, errors.New("trae: 站点为空")
	}
	if token.Refresh == "" {
		return nil, errors.New("trae: 缺少 refresh token")
	}
	device := token.Device
	if device == nil {
		return nil, errors.New("trae: 账号缺少设备身份，需重新授权")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	hosts := site.traeHostsFor(token.Region)
	authHost := hosts.AuthHost
	if token.AuthHost != "" {
		authHost = strings.TrimRight(token.AuthHost, "/")
	}

	var lastErr error
	// 国内站：设备绑定续期（SOLO 客户端优先，IDE 兜底）。
	if site.ID == PlatformTraeCN {
		for _, clientID := range []string{traeChatClientID, traeOAuthClientID} {
			result, err := traeExchangeDeviceBound(ctx, client, authHost, token, clientID)
			if err == nil {
				return result, nil
			}
			lastErr = err
		}
	}
	result, err := traeExchangeCloudIDE(ctx, client, authHost, token)
	if err == nil {
		return result, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, err
}

// traeExchangeDeviceBound 走国内站的设备绑定续期。
func traeExchangeDeviceBound(ctx context.Context, client *http.Client, authHost string, token traeToken, clientID string) (*traeRefreshResult, error) {
	timestamp := time.Now().Unix()
	nonce, err := traeRandomHex(16)
	if err != nil {
		return nil, err
	}
	proof, err := token.Device.signTraeDeviceProof(http.MethodPost, traeRefreshPathCN, clientID, token.Refresh, timestamp, nonce)
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"ClientID":     clientID,
		"RefreshToken": token.Refresh,
		"DeviceInfo": map[string]any{
			"DeviceID":  token.Device.DeviceID,
			"MachineID": token.Device.MachineID,
			"PublicKey": token.Device.PublicKeyPEM,
		},
		"DeviceProof":  proof,
		"Timestamp":    timestamp,
		"Nonce":        nonce,
		"ClientSecret": "-",
	}
	return traePostExchange(ctx, client, strings.TrimRight(authHost, "/")+traeRefreshPathCN, body, token.Refresh)
}

// traeExchangeCloudIDE 走 cloudide 续期（国际站与国内站兜底）。
func traeExchangeCloudIDE(ctx context.Context, client *http.Client, authHost string, token traeToken) (*traeRefreshResult, error) {
	body := map[string]any{
		"ClientID":     token.ClientID,
		"RefreshToken": token.Refresh,
		"ClientSecret": "-",
	}
	if token.Device != nil {
		body["DeviceInfo"] = map[string]any{
			"DeviceID":  token.Device.DeviceID,
			"MachineID": token.Device.MachineID,
			"PublicKey": token.Device.PublicKeyPEM,
		}
	}
	return traePostExchange(ctx, client, strings.TrimRight(authHost, "/")+traeRefreshPathCloudIDE, body, token.Refresh)
}

// traePostExchange 发一次续期请求并解析结果。
func traePostExchange(ctx context.Context, client *http.Client, endpoint string, payload map[string]any, fallbackRefresh string) (*traeRefreshResult, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Trae/"+traeCN.IDEVersion)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("trae: 续期请求失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("trae: 读取续期响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("trae: 续期被拒绝 (HTTP %d): %s", resp.StatusCode, traeTrimBody(body))
	}

	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("trae: 续期响应不是 JSON: %s", traeTrimBody(body))
	}
	root, _ := parsed["Result"].(map[string]any)
	if root == nil {
		root, _ = parsed["result"].(map[string]any)
	}
	if root == nil {
		root = parsed
	}
	access := traeStringValue(root, "Token", "token")
	if access == "" {
		return nil, fmt.Errorf("trae: 续期响应缺少 Token: %s", traeTrimBody(body))
	}
	out := &traeRefreshResult{
		Access:    access,
		Refresh:   firstNonEmpty(traeStringValue(root, "RefreshToken", "refreshToken"), fallbackRefresh),
		ExpiresAt: traeExpiryFromUnix(int64(traeNumberValue(root, "TokenExpireAt", "tokenExpireAt"))),
		AuthHost:  "",
		Host:      "",
	}
	if out.ExpiresAt.IsZero() {
		out.ExpiresAt = traeJWTExpiry(access)
	}
	if infoRaw, ok := root["UserInfo"]; ok {
		if info, ok := traeAnyToMap(infoRaw); ok {
			out.UID = traeStringValue(info, "UserID", "userId")
			out.Name = traeStringValue(info, "ScreenName", "screenName")
			out.Region = traeStringValue(info, "AIRegion", "aiRegion", "Region", "region")
		}
	}
	return out, nil
}

// ---------- 小工具 ----------

func traeStringValue(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch typed := v.(type) {
			case string:
				if strings.TrimSpace(typed) != "" {
					return strings.TrimSpace(typed)
				}
			case json.Number:
				return typed.String()
			case float64:
				return fmt.Sprintf("%.0f", typed)
			}
		}
	}
	return ""
}

func traeNumberValue(m map[string]any, keys ...string) float64 {
	for _, key := range keys {
		switch typed := m[key].(type) {
		case float64:
			return typed
		case json.Number:
			if f, err := typed.Float64(); err == nil {
				return f
			}
		case string:
			var f float64
			if _, err := fmt.Sscanf(strings.TrimSpace(typed), "%f", &f); err == nil {
				return f
			}
		}
	}
	return 0
}

func traeAnyToMap(v any) (map[string]any, bool) {
	switch typed := v.(type) {
	case map[string]any:
		return typed, true
	case string:
		var out map[string]any
		if err := json.Unmarshal([]byte(typed), &out); err == nil {
			return out, true
		}
	}
	return nil, false
}

func traeExpiryFromUnix(ts int64) time.Time {
	if ts <= 0 {
		return time.Time{}
	}
	// 兼容毫秒时间戳。
	if ts > 1_000_000_000_000 {
		ts /= 1000
	}
	return time.Unix(ts, 0)
}

// traeJWTExpiry 从 JWT 的 exp 声明里取过期时间（不可解析时返回零值）。
func traeJWTExpiry(token string) time.Time {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}
	}
	return traeExpiryFromUnix(claims.Exp)
}

func traeTrimBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		return text[:300] + "…"
	}
	return text
}

func firstNonEmptyInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}
