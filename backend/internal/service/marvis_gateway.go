package service

// Marvis 在网关侧的接入点。
//
// 与 WorkBuddy 的差别：
//   - 上游是**标准** OpenAI Chat Completions，不像 WorkBuddy 只支持流式；
//     非流式请求可以原样透传，不需要 SSE 聚合。
//   - 鉴权不是 Bearer，而是四个 Ual-Access-* 头 + 一个每次现签的设备指纹。
//   - model 必须换成槽位名。

import (
	"net/http"
	"strings"
)

// IsMarvisPlatform 报告平台是否为腾讯 Marvis。
func IsMarvisPlatform(platform string) bool {
	return platform == PlatformMarvis
}

// isMarvisAccount 报告账号是否属于 Marvis。
func isMarvisAccount(account *Account) bool {
	return account != nil && IsMarvisPlatform(account.Platform)
}

// MarvisBaseURLForAccount 返回账号对应的上游基址。
//
// 目前只有一个站点，保留函数是为了和 WorkBuddy 的 workBuddySiteForPlatform 对齐，
// 将来接入国际版时只改这里。
func MarvisBaseURLForAccount(_ *Account) string {
	return MarvisBaseURL
}

// marvisChatURL 返回模型网关的完整地址。
func marvisChatURL(account *Account) string {
	return MarvisBaseURLForAccount(account) + MarvisChatPath
}

// applyMarvisHeaders 写入 Marvis 模型网关要求的请求头。
//
// ⚠ 必须在透传白名单与 UA 收敛之后调用：Marvis 的 UA 与鉴权头都是硬要求，
// 被客户端透传值覆盖就会 401 / 4100404。
func applyMarvisHeaders(account *Account, header http.Header) {
	if !isMarvisAccount(account) || header == nil {
		return
	}
	creds := marvisCredentialsOf(account)
	if creds.OpenID == "" || creds.AccessToken == "" {
		return
	}
	for key, value := range marvisLLMHeaders(creds) {
		header.Set(key, value)
	}
}

// marvisAccessToken 返回可用作 bearer 的占位值。
//
// Marvis 的 Authorization 头不参与校验（上游只认 Ual-Access-*），但 SDK 形态
// 的请求要求它存在，因此统一填 placeholder。放在这里是为了让调用方不必
// 为「没有 bearer 的账号」特殊分支。
func marvisRequestPlaceholderToken(account *Account) string {
	if isMarvisAccount(account) {
		return "placeholder"
	}
	return ""
}

// marvisNormalizeChatBodyForUpstream 在出站前改写请求体。
//
// 只做 model → 槽位名这一件事。Marvis 上游是标准 OpenAI 形状，
// 其余字段原样透传即可。
func marvisNormalizeChatBodyForUpstream(account *Account, body []byte) []byte {
	if !isMarvisAccount(account) {
		return body
	}
	return marvisPrepareUpstreamBody(body)
}

// marvisUsesStandardChatProtocol 报告该账号是否走标准 Chat Completions。
//
// WorkBuddy 需要「强制流式 + 非流式聚合」，Marvis 不需要 —— 它接受
// stream=false。这个判定用来在网关里把两条路径分开。
func marvisUsesStandardChatProtocol(account *Account) bool {
	return isMarvisAccount(account)
}

// marvisModelDisplayName 把上游回的槽位名转成对外显示的真实模型名。
//
// 响应体里的 model 字段是真实模型名（上游自己填的），一般不需要改写；
// 这个函数用在日志与用量记录里，保证同一个模型在统计里只有一种写法。
func marvisModelDisplayName(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if spec, ok := marvisModelSlotToSpec[key]; ok {
		return spec.Display
	}
	return model
}

// isSupportedGatewayPlatform 报告平台是否属于本分支服务的接入平台。
//
// 本分支只服务 WorkBuddy 两个站点与腾讯 Marvis —— 它们都经 OpenAI 网关
// 转发。这个判定用于替代散落各处的「PlatformOpenAI || PlatformGrok」
// 之类的枚举，避免每删一个平台就要改一遍。
func isSupportedGatewayPlatform(platform string) bool {
	switch platform {
	case PlatformWorkBuddyCN, PlatformWorkBuddyIntl, PlatformMarvis:
		return true
	default:
		return false
	}
}
