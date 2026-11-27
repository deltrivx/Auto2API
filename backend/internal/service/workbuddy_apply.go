package service

import (
	"net/http"
	"strings"
)

// WorkBuddy 出站请求的接入层。
//
// 这里把 workbuddy_site / workbuddy_token / workbuddy_sign 三块能力接到
// 现有的 OpenAI 网关出站链路上：网关在 cc_pipeline 中构造完 upstreamReq
// 后调用本文件的函数补上 WorkBuddy 的身份头，其余（协议、流式解析、
// 计费、重试）完全复用既有实现。

// isWorkBuddyPlatform 判断平台是否属于 WorkBuddy 的两个站点。
func isWorkBuddyPlatform(platform string) bool {
	return platform == PlatformWorkBuddyCN || platform == PlatformWorkBuddyIntl
}

// workBuddyTokenOf 从账号凭据中解析登录态。
func (a *Account) workBuddyTokenOf() workBuddyToken {
	if a == nil {
		return workBuddyToken{}
	}
	return workBuddyTokenFromCredentials(a.Credentials)
}

// workBuddyUIDOf 返回账号绑定的 WorkBuddy uid。
//
// uid 参与 X-User-Id，缺失时上游会把请求归到匿名客户端，用量无法归属。
func (a *Account) workBuddyUIDOf() string {
	if a == nil {
		return ""
	}
	return workBuddyStringField(a.Credentials, workBuddyFieldUID)
}

// workBuddyEnterpriseIDOf 返回账号绑定的企业 ID。
//
// 云端企业账号必须带上它，否则上游按个人账号处理，模型与配额都不对。
func (a *Account) workBuddyEnterpriseIDOf() string {
	if a == nil {
		return ""
	}
	return workBuddyStringField(a.Credentials, workBuddyFieldEnterpriseID)
}

// workBuddyAPIKeyOf 返回账号配置的 WorkBuddy API Key。
//
// WorkBuddy 也提供 API Key 方式（网页端生成），其签名规则与登录态不同：
// 同时带 X-API-Key 且不带 X-Domain。这里读取凭据里的 api_key 字段。
func (a *Account) workBuddyAPIKeyOf() string {
	if a == nil {
		return ""
	}
	return strings.TrimSpace(workBuddyStringField(a.Credentials, "api_key"))
}

// workBuddySiteOf 返回账号对应站点的配置。
func (a *Account) workBuddySiteOf() *workBuddySite {
	if a == nil {
		return nil
	}
	return workBuddySiteForPlatform(a.Platform)
}

// applyWorkBuddyHeaders 为 WorkBuddy 账号的模型请求写入身份头。
//
// 这是出站链路的唯一入口：网关构造完请求后调用一次即可。函数内部按账号
// 凭据形态选择「登录态」或「API Key」两套签名规则。
//
// 返回 true 表示请求已被 WorkBuddy 规则处理（调用方可据此跳过其它默认头）。
func applyWorkBuddyHeaders(account *Account, h http.Header) bool {
	if account == nil || !isWorkBuddyPlatform(account.Platform) {
		return false
	}
	site := account.workBuddySiteOf()
	if site == nil {
		return false
	}

	if key := account.workBuddyAPIKeyOf(); key != "" {
		// API Key 形态：key 同时用于 Authorization 与 X-API-Key。
		if acct := workBuddyKeyAccountOf(key); acct != nil {
			workBuddySignKey(h, site, acct)
			workBuddyChatHeaders(h, site)
			applyWorkBuddyFingerprintHeaders(account, h)
			return true
		}
	}

	token := account.workBuddyTokenOf()
	if token.Access == "" {
		// 既没有 API Key 也没有 access token，无法签名；交由调用方按
		// 未授权处理，而不是发出一个必然被拒的请求。
		return false
	}
	workBuddySign(h, site, token, account.workBuddyUIDOf(), account.workBuddyEnterpriseIDOf())
	workBuddyChatHeaders(h, site)
	applyWorkBuddyFingerprintHeaders(account, h)
	return true
}

// applyWorkBuddyFingerprintHeaders 为请求写入设备指纹头。
//
// 指纹按账号 uid 稳定派生：同一账号每次请求的 machineId / MAC / UA 完全一致，
// 不同账号之间互不冲突。上游据此判断「同一设备」还是「多个账号挤在一台机器」。
//
// 若账号凭据里已存有此前分配的指纹，直接复用——从 AutoBuddy 迁移过来的账号
// 必须保留原有 machineId，否则会被判定为设备变更。
func applyWorkBuddyFingerprintHeaders(account *Account, h http.Header) {
	fp := account.workBuddyFingerprint()
	for k, v := range fp.workBuddyFingerprintHeaders() {
		// User-Agent 由站点统一决定，这里跳过：
		// 指纹画像是**浏览器** UA（Mozilla/5.0 … Chrome/…），而上游按 UA
		// 判定客户端形态。把浏览器 UA 发到 /v2/chat/completions 会被判为
		// 非官方客户端，稳定返回 11128「Illegal API invocation from an
		// unapproved channel」。设备身份靠 machineId 等头表达即可。
		if strings.EqualFold(k, "User-Agent") {
			continue
		}
		if v != "" {
			h.Set(k, v)
		}
	}
}

// workBuddyFingerprint 返回账号的身份。
//
// 以「运行时现算」为准，但会读回凭据里持久化的值来**校验一致性**：
//
//   - 旧版本在凭据里写过模拟硬件（Win32 机型、假 MAC、假内存、假 GPU），
//     那些字段一律丢弃，只用当前版本认识的字段。
//   - 旧版本的 machine_id 是主机哈希（所有账号撞成一个值），与现在的
//     按账号派生不一致；这种情况下以现算值为准，凭据里那份视为过期。
//   - machine_id 一致时沿用凭据里的 session_id，避免每次重启换会话号。
//
// 换句话说：持久化值不再是「权威来源」，也不再被静默忽略——两者不一致时
// 以真实身份为准，并由 workBuddyFingerprintDrift 暴露给管理端。
func (a *Account) workBuddyFingerprint() workBuddyFingerprint {
	if a == nil {
		return workBuddyFingerprint{}
	}
	uid := a.workBuddyUIDOf()
	fp := workBuddyFingerprintForAccount(uid)

	// 凭据里若存过同一 machine_id，沿用它的 session_id 保持不变。
	if a.Credentials != nil {
		if raw, ok := a.Credentials["fingerprint"].(map[string]any); ok {
			if workBuddyStringField(raw, "machine_id") == fp.MachineID {
				if sid := workBuddyStringField(raw, "session_id"); sid != "" {
					fp.SessionID = sid
				}
			}
		}
	}
	return fp
}

// workBuddyFingerprintDrift 报告凭据里持久化的指纹与当前真实身份是否一致。
//
// 返回空字符串表示一致（或压根没存过）；否则返回一句可直接展示给运维的说明。
// 只做描述，不改数据——管理端据此提示「这条指纹是历史遗留，可重新生成」。
func (a *Account) workBuddyFingerprintDrift() string {
	if a == nil || a.Credentials == nil {
		return ""
	}
	raw, ok := a.Credentials["fingerprint"].(map[string]any)
	if !ok {
		return ""
	}
	persisted := workBuddyStringField(raw, "machine_id")
	if persisted == "" {
		return ""
	}
	current := workBuddyFingerprintForAccount(a.workBuddyUIDOf()).MachineID
	if persisted == current {
		return ""
	}
	return "stored fingerprint does not match the current per-account identity; " +
		"the stored value is from an older release and is no longer used"
}

