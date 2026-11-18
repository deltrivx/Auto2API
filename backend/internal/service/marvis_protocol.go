package service

// 腾讯 Marvis 模型网关协议。
//
// Marvis 是应用宝的 AI 助手，账号用 QQ / 微信扫码登录，没有 API Key。
// 它的模型网关（/v1/chat/completions）是标准 OpenAI 形状，但鉴权方式很特别：
//
//	Authorization: Bearer placeholder        ← 占位符，上游不校验
//	Ual-Access-Openid        openid
//	Ual-Access-Access-Token  access_token
//	Ual-Access-Login-Type    登录方式码
//	Ual-Access-Guid          设备标识
//	Ual-Access-MarvisExt     设备指纹（每次现签的紧凑 JSON）
//
// 两个硬约束（来自 magpie-marvis 的实测结论）：
//
//  1. User-Agent 必须是 `AsyncOpenAI/Python 2.32.0`。这是 Marvis agent 自己的
//     UA，换成浏览器 UA 或别的值会被风控拦（4100404）。
//
//  2. uskey 一个只能用两次。上游按 uskey 计配额，第 3 次起 403（等 75 秒仍是 403，
//     是配额不是频率）。所以每次请求都要现签一个新的 —— 本地 randomBytes(376)
//     再 base64，毫秒级，不联网、不依赖任何二进制。
//
// 模型名：上游只认**槽位名**，不认真实模型名。传真实名会回 400 无效模型 id。

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// MarvisBaseURL 是 Marvis 上游基址。
	//   推理 /v1/chat/completions（不需要 UAL 签名）
	//   业务 /v3/*（钱包、用户信息；需要 md5 签名）
	MarvisBaseURL = "https://yybadaccess.3g.qq.com"

	// MarvisChatPath 是模型网关端点。
	MarvisChatPath = "/v1/chat/completions"
	// MarvisOAuthPath 是登录回调端点，用 code 换登录态。
	MarvisOAuthPath = "/marvis_client_login/marvis_oauth"
	// MarvisRefreshPath 是续期端点。⚠ 必须 POST，body 带 userInfo。
	MarvisRefreshPath = "/marvis_client_login/marvis_refresh_token"
	// MarvisWalletPath 是额度端点（/v3/*，需要签名）。
	MarvisWalletPath = "/v3/marvis_get_wallet"
	// MarvisUserInfoPath 是昵称/头像端点。
	//
	// ⚠ 它在 /marvis_client/ 下，不在 /v3/ 下；而且身份要放 body，
	// 带上 Ual-Access-Access-Token 那几个头反而 500。这是实测结论。
	MarvisUserInfoPath = "/marvis_client/marvis_get_user_info"

	// MarvisBusinessID 是请求头 Ual-Access-Businessid 的取值。
	MarvisBusinessID = "marvis_client"
	// MarvisAccessKey 是 /v3/* 签名用的 accessKey。
	// 它明文硬编码在 Marvis 渲染层里，不是密钥。
	MarvisAccessKey = "fpzkeTlcGLVP37qiOWd29MyaOqaJYxMi"

	// MarvisClientUA 是模型网关要求的 User-Agent，照抄 Marvis agent 自己的。
	// 换成别的会被风控拦（4100404）—— 这不是伪装，是这个端点的准入条件。
	MarvisClientUA = "AsyncOpenAI/Python 2.32.0"
	// MarvisBrowserUA 是 /v3/* 业务接口用的 UA，同样照抄客户端。
	MarvisBrowserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Marvis/1.60.3300.408 Chrome/152.0.7977.130 Electron/44.7.0 Safari/537.36"

	// MarvisClientVersion 是 MarvisExt 里声明的客户端版本。
	MarvisClientVersion = "1.60.3300.765"

	// MarvisQQAppID / MarvisWXAppID 是登录用的开放平台 appid。
	MarvisQQAppID = "1903794758"
	MarvisWXAppID = "wxb650708317d56f88"

	// MarvisUSKeyBytes 是 uskey 的字节长度（上游要求 376）。
	MarvisUSKeyBytes = 376

	// marvisDefaultOSVersion 是画像缺失时 MarvisExt 用的系统版本。
	marvisDefaultOSVersion = "Windows-10-10.0.26200"
)

// Marvis 登录方式码。
//
// 注意两套码不能混：
//   - Ual-Access-Login-Type 用 MarvisLoginTypeQC/WX
//   - 续期接口 body 里的 loginType 恒为 "Marvis"（字面量）
const (
	MarvisLoginTypeQC = "1"
	MarvisLoginTypeWX = "2"
)

// marvisNonceSeq 让同一毫秒内的多次调用也能拿到不同的 nonce。
var marvisNonceSeq atomic.Uint64

// marvisRandomHex 生成 n 个字节的十六进制串。
func marvisRandomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败极罕见；退化成时间派生的值，保证请求仍能发出。
		seed := time.Now().UnixNano() + int64(marvisNonceSeq.Add(1))
		for i := range buf {
			buf[i] = byte(seed >> (uint(i%8) * 8))
		}
	}
	return fmt.Sprintf("%x", buf)
}

// marvisDeviceGUID 生成设备标识：32 位十六进制。
//
// 它与账号绑定，后续所有请求都要带同一个值（上游拿它做设备一致性校验）。
func marvisDeviceGUID() string {
	return marvisRandomHex(16)
}

// marvisUSKey 现签一个设备指纹。
//
// 上游对 uskey 计配额：同一个值第 3 次请求起 403。因此**每次请求都要新签**，
// 不能缓存。这里是本地随机生成（376 字节 → base64），与官方 SDK 的输出格式一致。
func marvisUSKey() string {
	buf := make([]byte, MarvisUSKeyBytes)
	if _, err := rand.Read(buf); err != nil {
		seed := time.Now().UnixNano() + int64(marvisNonceSeq.Add(1))
		for i := range buf {
			buf[i] = byte(seed >> (uint(i%8) * 8))
		}
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// marvisUSKeyFor 返回该账号固定的 uskey。
//
// 优先用凭据里已保存的值；没有则现生成一个，由调用方负责落库
// （见 ApplyMarvisFingerprint）。固定 uskey 才能让上游把多次请求认成
// 同一台设备，否则每次随机都会被按「新设备」风控。
func marvisUSKeyFor(creds MarvisCredentials) string {
	if v := strings.TrimSpace(creds.USKey); v != "" {
		return v
	}
	return marvisUSKey()
}

// marvisNonce 生成一个 UUIDv4 形态的 nonce。
func marvisNonce() string {
	h := marvisRandomHex(16)
	return fmt.Sprintf("%s-%s-%s-%s-%s", h[0:8], h[8:12], h[12:16], h[16:20], h[20:32])
}

// marvisExtJSON 构造 Ual-Access-MarvisExt 的值。
//
// 必须是**紧凑 JSON**（不能有空格）：服务端按原文字符串校验。
//
// qimei36 只在能拿到真值时才有意义 —— 它要求与设备一致。我们拿不到真值，
// 因此整个字段不发，而不是用占位值冒充（官方实现同样如此）。
func marvisExtJSON(creds MarvisCredentials) string {
	// osVersion 是常量，不跟随账号画像。
	//
	// magpie-marvis 的 marvisExt() 把它写死成 "Windows-10-10.0.26200"
	// （照抄 Marvis agent 的 build_llm_auth_headers）。画像里的 os_version
	// 是给管理端弹窗看的展示值，放进 MarvisExt 会和上游期望的格式对不上。
	osVersion := marvisDefaultOSVersion
	ext := map[string]any{
		"osVersion":     osVersion,
		"guid":          creds.GUID,
		"uskey":         marvisUSKeyFor(creds),
		"nonce":         marvisNonce(),
		"reqScene":      1,
		"taskType":      "user",
		"clientVersion": MarvisClientVersion,
		"isIoa":         false,
	}
	raw, err := json.Marshal(ext)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// MarvisCredentials 是 Marvis 账号的登录态。
type MarvisCredentials struct {
	OpenID       string `json:"openid"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	GUID         string `json:"guid"`
	// LoginType 是 Ual-Access-Login-Type：1=QQ，2=微信。
	LoginType string `json:"login_type,omitempty"`
	// OSVersion 来自账号硬件画像，写进 MarvisExt 的 osVersion。
	OSVersion string `json:"os_version,omitempty"`
	// USKey 是该账号固定的设备指纹，首次使用时生成并持久化。
	//
	// 上游按 uskey 识别设备：每次请求随机生成一个新的，等于每次都自称
	// 一台新设备，会持续触发 4100404 风控。固定成账号级之后就表现为
	// 「同一台机器一直用」。
	USKey string `json:"uskey,omitempty"`
	// ExpiresAt 是 access token 的绝对过期时间（Unix 秒）。
	ExpiresAt int64  `json:"expires_at,omitempty"`
	Nickname  string `json:"nickname,omitempty"`
	Avatar    string `json:"avatar,omitempty"`
}

// marvisCredentialsOf 从账号凭据里解出 Marvis 登录态。
func marvisCredentialsOf(account *Account) MarvisCredentials {
	var out MarvisCredentials
	if account == nil || account.Credentials == nil {
		return out
	}
	get := func(key string) string {
		if v, ok := account.Credentials[key]; ok {
			if s, isStr := v.(string); isStr {
				return strings.TrimSpace(s)
			}
		}
		return ""
	}
	out.OpenID = get("openid")
	out.AccessToken = get("access_token")
	out.RefreshToken = get("refresh_token")
	out.GUID = get("guid")
	out.LoginType = get("login_type")
	out.OSVersion = get("os_version")
	out.Nickname = get("nickname")
	out.Avatar = get("avatar")
	if out.LoginType == "" {
		out.LoginType = MarvisLoginTypeQC
	}
	switch v := account.Credentials["expires_at"].(type) {
	case float64:
		out.ExpiresAt = int64(v)
	case int64:
		out.ExpiresAt = v
	}
	return out
}

// marvisLLMHeaders 构造模型网关的完整请求头。
//
// 注意：**不要加 UAL 签名**。签名只用于 /v3/* 业务接口；加到模型网关上会把
// 403 风控变成 401 登录态过期（官方实测）。
func marvisLLMHeaders(creds MarvisCredentials) map[string]string {
	loginType := creds.LoginType
	if loginType == "" {
		loginType = MarvisLoginTypeQC
	}
	return map[string]string{
		"Accept":                      "application/json",
		"Content-Type":                "application/json",
		"User-Agent":                  MarvisClientUA,
		"X-Stainless-Lang":            "python",
		"X-Stainless-Package-Version": "2.32.0",
		"X-Stainless-OS":              "Windows",
		"X-Stainless-Arch":            "other:amd64",
		"X-Stainless-Runtime":         "CPython",
		"X-Stainless-Runtime-Version": "3.11.9",
		"X-Stainless-Async":           "async:asyncio",
		"X-Stainless-Raw-Response":    "true",
		// 上游不校验这个值，但 SDK 形态的请求要带上。
		"Authorization":           "Bearer placeholder",
		"Ual-Access-Openid":       creds.OpenID,
		"Ual-Access-Access-Token": creds.AccessToken,
		"Ual-Access-Login-Type":   loginType,
		"Ual-Access-Guid":         creds.GUID,
		"Marvis-AgentTag":         "auto_discovery",
		"Marvis-Conversation-ID":  "",
		"Marvis-Response-ID":      "",
		"Marvis-Scene":            "preload_summary",
		"Marvis-Protocol-Version": "1000",
		"X-Device-GUID":           creds.GUID,
		"Ual-Access-MarvisExt":    marvisExtJSON(creds),
	}
}

// marvisSignedHeaders 构造 /v3/* 业务接口的请求头（含 md5 签名）。
//
// 签名原文：query + timestamp + accessKey + nonce
// GET 的 query 是查询串（不含 ?），POST 是 JSON body。
func marvisSignedHeaders(creds MarvisCredentials, query string) map[string]string {
	timestamp := fmt.Sprintf("%d", time.Now().UnixMilli())
	nonce := fmt.Sprintf("%04d", 1000+marvisNonceSeq.Add(1)%9000)
	return map[string]string{
		"Accept":                  "*/*",
		"Content-Type":            "application/json; charset=utf-8",
		"User-Agent":              MarvisBrowserUA,
		"Ual-Access-Businessid":   MarvisBusinessID,
		"Ual-Access-Timestamp":    timestamp,
		"Ual-Access-Nonce":        nonce,
		"Ual-Access-Signature":    marvisSign(query, timestamp, nonce),
		"Ual-Access-Guid":         creds.GUID,
		"Ual-Access-Access-Token": creds.AccessToken,
		"Ual-Access-Login-Type":   creds.LoginType,
		"Ual-Access-Openid":       creds.OpenID,
	}
}

// marvisSign 计算 /v3/* 的签名：md5(query + timestamp + accessKey + nonce)。
func marvisSign(query, timestamp, nonce string) string {
	sum := md5.Sum([]byte(query + timestamp + MarvisAccessKey + nonce))
	return hex.EncodeToString(sum[:])
}
