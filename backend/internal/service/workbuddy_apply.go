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

// workBuddyFingerprint 返回账号的指纹。
//
// 优先读取凭据里持久化的值（保证跨进程重启稳定），缺失时按 uid 派生。
func (a *Account) workBuddyFingerprint() workBuddyFingerprint {
	if a == nil {
		return workBuddyFingerprint{}
	}
	uid := a.workBuddyUIDOf()

	// 1. 已持久化的指纹。
	if a.Credentials != nil {
		if raw, ok := a.Credentials["fingerprint"]; ok {
			if m, ok := raw.(map[string]any); ok {
				fp := workBuddyFingerprint{
					UID:          workBuddyStringField(m, "uid"),
					MachineID:    workBuddyStringField(m, "machine_id"),
					SessionID:    workBuddyStringField(m, "session_id"),
					MPMachineID:  workBuddyStringField(m, "mp_machine_id"),
					WebMachineID: workBuddyStringField(m, "web_machine_id"),
					MAC:          workBuddyStringField(m, "mac"),
					OS:           workBuddyStringField(m, "os"),
					Arch:         workBuddyStringField(m, "arch"),
					OSVersion:    workBuddyStringField(m, "os_version"),
					CPUCores:     int(workBuddyIntField(m, "cpu_cores")),
					MemorySize:   int(workBuddyIntField(m, "memory_size")),
					UA:           workBuddyStringField(m, "ua"),
					IDEVersion:   workBuddyStringField(m, "ide_version"),
				}
				if fp.MachineID != "" {
					return fp
				}
			}
		}
	}

	// 2. 按 uid 派生。
	return workBuddyFingerprintForAccount(uid)
}
