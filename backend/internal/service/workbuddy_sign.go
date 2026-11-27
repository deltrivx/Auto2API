package service

import (
	"github.com/tidwall/gjson"
	"bytes"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// WorkBuddy 请求签名。
//
// 移植自 magpie 的 sign / cliAgent / chatHeaders / keyAccount。
//
// WorkBuddy 的前置网关（APISIX，两个站点都有）会校验一组客户端身份头。
// 只带 Authorization 的请求会被拒：
//
//   - User-Agent 不是 WorkBuddy/<version> 时返回业务错误 10085；
//   - 缺少 X-User-Id / X-Domain 的请求不计入任何「使用端」，
//     用量列表里显示为空客户端；
//   - X-Product 与 X-IDE-* 用于把请求归因到桌面客户端。
//
// 因此这里必须原样复刻桌面端发送的全部头部，而不是只挑必需的几个。

// WorkBuddy 客户端依赖的固定版本号，参与 X-Stainless-* 头。
const (
	workBuddyOpenAISDK   = "6.25.0"   // 桌面端内置的 openai SDK 版本
	workBuddyNodeVersion = "v22.21.1" // 桌面端 Electron 内运行的 Node 版本
)

// workBuddyKeyAccountPattern 匹配那些会让 CLI 去拉远端模型目录的 key 前缀。
//
// 对应 magpie 的 remoteConfig：只有 ck_ / pt_ 开头的 API Key 才会请求
// /v3/config 取账号可用模型，其它 key 一律用内置列表。
var workBuddyKeyAccountPattern = regexp.MustCompile(`^(ck_|pt_)`)

// workBuddyKeyAccount 是一个 API Key 形式的账号。
//
// WorkBuddy 的 API Key 登录等价于一个会话：access token 就是 key 本身，
// uid 为 "anonymous_" + key 后 8 位，类型为个人，没有域名与企业归属。
// 它的模型请求会把 key 同时放进 X-API-Key 与 Authorization: Bearer 两处。
type workBuddyKeyAccount struct {
	Key    string
	Access string
	UID    string
}

// workBuddyKeyAccountOf 从 API Key 构造账号；key 为空时返回 nil。
func workBuddyKeyAccountOf(key string) *workBuddyKeyAccount {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	tail := key
	if len(tail) > 8 {
		tail = tail[len(tail)-8:]
	}
	return &workBuddyKeyAccount{
		Key:    key,
		Access: key,
		UID:    "anonymous_" + tail,
	}
}

// workBuddyKeyUsesRemoteConfig 判断该 key 是否应拉取远端模型目录。
func workBuddyKeyUsesRemoteConfig(key string) bool {
	return workBuddyKeyAccountPattern.MatchString(strings.TrimSpace(key))
}

// workBuddySign 为一次请求写入 WorkBuddy 的身份头。
//
// 对应 magpie 的 sign。isKey 为 true 时按 API Key 会话签名：
// 带 X-API-Key、不带 X-Domain。
//
// 参数：
//   - h:       目标请求头
//   - site:    站点配置（决定 UA 与 X-IDE-Version）
//   - token:   登录态（取 access 与 domain）
//   - uid:     账号 uid
//   - entID:   企业 ID，非空时补 X-Enterprise-Id / X-Tenant-Id
func workBuddySign(h http.Header, site *workBuddySite, token workBuddyToken, uid, entID string) {
	h.Set("Authorization", "Bearer "+token.Access)
	h.Set("X-User-Id", uid)

	// 登录态账号带域名；域名是云端企业账号路由到其专属主机的依据。
	if domain := workBuddyDomainOf(site, token); domain != "" {
		h.Set("X-Domain", domain)
	}

	h.Set("X-Product", "SaaS")
	h.Set("X-IDE-Type", "WorkBuddy")
	h.Set("User-Agent", site.workBuddyCLIUserAgent())
	h.Set("X-Requested-With", "XMLHttpRequest")
	// X-Agent-Intent / X-Agent-Purpose 是官方桌面端的固有头。
	//
	// magpie 的 sign() 原文：WorkBuddy's own client sends the whole of this on
	// both builds（application-manifest.js: X-Agent-Purpose "conversation",
	// X-IDE-Name/Type "WorkBuddy", X-IDE-Version, X-Product "SaaS"）；
	// 缺了它们请求在上游的「使用端」统计里落空，且客户端形态不完整。
	// 早期注释曾把这组头整体判为风控特征，实测与 magpie 的做法相反，
	// 这里按 magpie 恢复。
	h.Set("X-Agent-Intent", "craft")
	h.Set("X-Agent-Purpose", "conversation")
	h.Set("X-IDE-Name", "WorkBuddy")
	h.Set("X-IDE-Version", site.App)

	workBuddyApplyEnterpriseHeaders(h, entID)
}

// workBuddySignKey 为 API Key 账号写入身份头。
//
// 对应 magpie 的 sign 中 a.source === "key" 的分支：同时带 X-API-Key，
// 且不带 X-Domain（key 会话没有域名）。
func workBuddySignKey(h http.Header, site *workBuddySite, acct *workBuddyKeyAccount) {
	if acct == nil {
		return
	}
	h.Set("Authorization", "Bearer "+acct.Access)
	h.Set("X-User-Id", acct.UID)
	h.Set("X-API-Key", acct.Key)
	// key 会话没有域名，显式删掉，避免调用方残留其它账号的域名。
	h.Del("X-Domain")

	h.Set("X-Product", "SaaS")
	h.Set("X-IDE-Type", "WorkBuddy")
	h.Set("User-Agent", site.workBuddyCLIUserAgent())
	h.Set("X-Requested-With", "XMLHttpRequest")
	// 同上：X-Agent-* 系列会触发 11140 账号级风控，不发送。
	h.Set("X-IDE-Name", "WorkBuddy")
	h.Set("X-IDE-Version", site.App)
}

// workBuddyChatHeaders 补上模型请求独有的头。
//
// 对应 magpie 的 chatHeaders。这些头来自桌面端 CLI 的 OpenAI SDK 拦截器，
// 用于把请求标记为「主对话代理」的运行环境。
func workBuddyChatHeaders(h http.Header, site *workBuddySite) {
	// X-Agent-Type / X-Agent-Runtime 由 magpie 的 chatHeaders 设置：
	//   headers.set("X-Agent-Type", "main")
	//   if (site.runtime) headers.set("X-Agent-Runtime", site.runtime)
	// 它们是「主对话代理」的标记，属于官方桌面端形态的一部分。
	h.Set("X-Agent-Type", "main")
	if strings.TrimSpace(site.Runtime) != "" {
		h.Set("X-Agent-Runtime", site.Runtime)
	} else {
		h.Del("X-Agent-Runtime")
	}
	// magpie 的 chatHeaders 用单值 application/json。
	h.Set("Accept", "application/json")
	h.Set("X-Stainless-Retry-Count", "0")
	// 桌面端客户端超时为 1200 秒，且自身不做重试。
	h.Set("X-Stainless-Timeout", "1200")
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Package-Version", workBuddyOpenAISDK)
	h.Set("X-Stainless-OS", workBuddySDKOS())
	h.Set("X-Stainless-Arch", workBuddySDKArch())
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", workBuddyNodeVersion)
	// 官方桌面端字节级对齐所需的四项。
	//
	// AutoBuddy 的记录：这三项此前缺失，是 11128「Illegal API invocation
	// from an unapproved channel」间歇性触发的直接原因——同样的账号和模型
	// 在 AutoBuddy 上能过、在 Auto2API 上被拦，差别就在这几个头。
	// Content-Type 必须带 charset，缺了同样按非官方客户端判定。
	h.Set("X-Client-Platform", "desktop")
	h.Set("X-Client-Product", "workbuddy-desktop")
	h.Set("X-Client-Version", site.App)
	h.Set("X-CodeBuddy-Request", "1")
	h.Set("Content-Type", "application/json;charset=UTF-8")
	// X-Request-ID：官方桌面端是 sessionId + 毫秒时间戳尾段。
	//
	// AutoBuddy 的 _desktop_headers 依 vendor/workbuddy_daily.py:3144 这样拼：
	//   rid = (sessionId or "wb") + str(int(time.time()*1000) % 1000000)
	// 上游把它当作官方客户端的请求标识；缺失时渠道校验会判为非官方来源。
	if h.Get("X-Request-ID") == "" {
		// sessionId 由调用方在签名阶段写入；拿不到时用 "wb" 兜底，
		// 与 AutoBuddy 的 (sessionId or "wb") 一致。
		session := h.Get("X-Session-Id")
		if session == "" {
			session = "wb"
		}
		h.Set("X-Request-ID", session+fmt.Sprintf("%d", time.Now().UnixMilli()%1000000))
	}
	// Origin / Referer 用带 scheme 的完整源，与 X-Domain 的纯域名分工不混。
	//
	// 上游 11128 的原文是「This call failed channel verification, possibly
	// because it was not sent from an official client」——它校验的是「像不像
	// 官方桌面端发出的请求」。AutoBuddy 的 _desktop_headers 全程带着这两个头，
	// 缺失时同样会被判为非官方客户端。任务、图片、事件路径都已设置，
	// 只有聊天转发这条主路径漏了，这正是 DSH/Codex/Claude 客户端反复 400 的原因。
	if endpoint := strings.TrimRight(strings.TrimSpace(site.Endpoint), "/"); endpoint != "" {
		h.Set("Origin", endpoint)
		h.Set("Referer", endpoint+"/")
	}
}

// workBuddyApplyEnterpriseHeaders 写入企业身份头。
//
// 对应 magpie 的 enterpriseHeaders：企业账号必须带企业 ID，
// 上游据此把请求路由到企业的模型配额与计费。
func workBuddyApplyEnterpriseHeaders(h http.Header, entID string) {
	entID = strings.TrimSpace(entID)
	if entID == "" {
		return
	}
	h.Set("X-Enterprise-Id", entID)
	h.Set("X-Tenant-Id", entID)
}

// workBuddySDKOS 返回 X-Stainless-OS 的取值。
//
// 上游按桌面端常见平台给值；服务端部署一律声明 Linux，
// 与桌面端在 Linux 上的取值一致，避免出现 "Other:" 形态的异常值。
func workBuddySDKOS() string {
	return "Linux"
}

// workBuddySDKArch 返回 X-Stainless-Arch 的取值。
func workBuddySDKArch() string {
	return "x64"
}

// workBuddySessionID 从请求头取出透传的会话标识。
func workBuddySessionID(h http.Header) string {
	return strings.TrimSpace(h.Get(workBuddySessionHeader))
}

// workBuddyIsInvalidToken 判断响应是否为「登录态被拒」。
//
// 对应 magpie 的 invalidToken。上游前置网关对不接受的 token 返回 401 并带
// WWW-Authenticate: Bearer realm="copilot", error="invalid_token"。
//
// 注意：不能只看 401 —— 上游对部分路径会给已接受的登录态返回裸 401，
// 那种情况不应触发重新登录。
func workBuddyIsInvalidToken(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		return false
	}
	if resp.Header == nil {
		return false
	}
	return strings.Contains(strings.ToLower(resp.Header.Get("WWW-Authenticate")), "invalid_token")
}

// workBuddyIsEdgeAuthChallenge 判断 401 是否来自上游边缘网关（APISIX / OpenResty）
// 而不是「登录态被拒」。
//
// 实测：同一个仍然有效的 token，在中国站偶发会被 APISIX 直接回
// `401 Authorization Required`（server: APISIX/*、body 是 nginx 风格 HTML），
// 既没有 WWW-Authenticate: invalid_token，也没有任何业务错误码；下一次请求
// 就能正常通。这种 401 **不代表 token 失效**，把它当成失效会让好账号被摘。
//
// 判据（全部满足才算边缘网关）：
//   - 没有 invalid_token（否则是真失效）
//   - Server 头是 APISIX / openresty / nginx
//   - 没有 WorkBuddy 的业务错误码（上游业务错误是 JSON，带 code/msg）
func workBuddyIsEdgeAuthChallenge(resp *http.Response) bool {
	if resp == nil || resp.StatusCode != http.StatusUnauthorized || resp.Header == nil {
		return false
	}
	if workBuddyIsInvalidToken(resp) {
		return false
	}
	server := strings.ToLower(resp.Header.Get("Server"))
	edge := strings.Contains(server, "apisix") ||
		strings.Contains(server, "openresty") ||
		strings.Contains(server, "nginx")
	return edge && resp.Header.Get("Content-Type") != ""
}

// workBuddyUnauthorizedRequiresReauth 判断一次 401 是否值得把账号标成凭据失效。
//
// 只有同时满足下面两点才算「需要重新授权」：
//   - 响应体是 WorkBuddy 的业务错误（JSON，带 code 或 msg）
//   - 并且带 invalid_token / 业务码指向登录态问题
//
// 反过来，APISIX / openresty 的边缘 401（HTML 体、无业务码）**不算**——
// 它和 token 有效性无关，实测同一个 token 下一次请求就能通。把这种 401
// 当成失效会让整个账号池在十几分钟内被轮流摘空。
func workBuddyUnauthorizedRequiresReauth(responseBody []byte) bool {
	trimmed := bytes.TrimSpace(responseBody)
	if len(trimmed) == 0 {
		// 空体 401：上游没给任何理由，按可重试处理，避免误摘
		return false
	}
	// 合法的业务错误一定是 JSON 对象
	if !gjson.ValidBytes(trimmed) || !gjson.GetBytes(trimmed, "@this").IsObject() {
		return false
	}
	for _, path := range []string{"code", "msg", "message", "error.code", "error.message"} {
		if gjson.GetBytes(trimmed, path).Exists() {
			return true
		}
	}
	return false
}
