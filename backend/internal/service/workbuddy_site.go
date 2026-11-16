package service

import (
	"regexp"
	"sort"
	"time"
)

// WorkBuddy 平台接入。
//
// 移植自 magpie-community/plugins 的 packages/workbuddy（MIT）。
// 该插件同时服务 WorkBuddy 的两个站点：
//
//   - 国内站 workbuddy    https://copilot.tencent.com
//   - 国际站 workbuddy-ai https://www.workbuddy.ai
//
// 两个站点的协议相同：浏览器登录换取 access/refresh token，聊天走
// <endpoint>/v2 下的 Chat Completions（仅流式），请求必须带 WorkBuddy
// 桌面客户端的专属请求头——User-Agent 不是 WorkBuddy/<version> 的请求
// 会被以 error 10085 拒绝。
//
// 本文件先落地「站点常量与静态配置」这一层：端点、客户端版本号、
// 模型目录。后续文件在此之上实现登录、续期、请求签名与转发。

// WorkBuddy 各站点使用的固定常量。
const (
	// workBuddyPollInterval 是登录轮询的间隔。
	workBuddyPollInterval = 1 * time.Second
	// workBuddySignInTimeout 是登录轮询的整体超时。
	workBuddySignInTimeout = 5 * time.Minute
	// workBuddyEarlyRefresh 是 access token 距过期小于该值时提前刷新。
	workBuddyEarlyRefresh = 60 * time.Second
	// workBuddyRefreshLead 是上游建议提前续期的窗口。
	workBuddyRefreshLead = 5 * time.Minute
	// workBuddyKeepRenewed 是一次刷新结果对旧 refresh token 的复用窗口。
	workBuddyKeepRenewed = 60 * time.Second
	// workBuddyEdgeReads 是上游边缘（APISIX）在答复被拒登录前读取的请求体上限。
	// 超过该长度的被拒请求会直接重置连接，而不是返回 401。
	workBuddyEdgeReads = 2 * 1024 * 1024
	// workBuddySessionHeader 是透传会话标识的请求头名。
	workBuddySessionHeader = "x-auto2api-workbuddy-session"
	// workBuddyPriceTTL 是模型/价格列表的缓存时长。
	workBuddyPriceTTL = 60 * time.Second
)

// DefaultWorkBuddyTestModel 是连接测试使用的默认模型。
//
// 两个站点都有 glm-5.3-flash（国内站在售、国际站免费额度覆盖），
// 用它做连通性测试代价最低。
const DefaultWorkBuddyTestModel = "glm-5.3-flash"

// WorkBuddy 的 token 字段名，与凭据表（credentials）中的键保持一致。
const (
	workBuddyFieldAccess         = "access_token"
	workBuddyFieldRefresh        = "refresh_token"
	workBuddyFieldExpires        = "expires_at"
	workBuddyFieldRefreshExpires = "refresh_expires_at"
	workBuddyFieldDomain         = "domain"
	workBuddyFieldTokenType      = "token_type"
	workBuddyFieldUID            = "uid"
	workBuddyFieldEnterpriseID   = "enterprise_id"
)

// workBuddySite 描述一个 WorkBuddy 站点。
type workBuddySite struct {
	// ID 是站点标识，与平台常量对应。
	ID string
	// Name 是展示名。
	Name string
	// Endpoint 是该站点的 API 根地址。
	Endpoint string
	// AuthID 是桌面端保存登录态时使用的文件名前缀。
	AuthID string
	// Platform 是登录接口 platform 参数的取值。
	Platform string
	// AI 为 true 表示国际站。
	AI bool
	// App 是桌面客户端版本号，用于 User-Agent 与 X-IDE-Version。
	App string
	// CLI 是随客户端分发的 CLI 版本号，用于 User-Agent 的 CLI/ 段。
	CLI string
	// AppName 是 User-Agent 中客户端自身的名字。
	AppName string
	// Runtime 是 X-Agent-Runtime 的取值；空表示不带该头。
	Runtime string
	// KeysURL 是该站点生成 API Key 的页面。
	KeysURL string
	// Messages 表示该站点同时提供 Anthropic Messages 协议。
	Messages bool
	// NoImages 匹配该站点不接受的图片模型（国际站丢弃 glm-* 的图片）。
	NoImages string
}

// workBuddyCN 是国内站配置，字段取值对照 magpie 的 SITES.workbuddy。
//
// 版本号取自 magpie 的 SITES.workbuddy：app=5.9.3、cli=2.165.0，
// 对应 WorkBuddy 5.9.3.42398134 官方桌面端。magpie 是这套客户端形态的
// 权威参考实现，AutoBuddy 的 5.5.6 是当时的旧构建。
var workBuddyCN = workBuddySite{
	ID:       PlatformWorkBuddyCN,
	Name:     "WorkBuddy",
	Endpoint: "https://copilot.tencent.com",
	AuthID:   "workbuddy-desktop",
	Platform: "workbuddy",
	AI:       false,
	App:      "5.9.3",
	CLI:      "2.165.0",
	AppName:  "WorkBuddy",
	Runtime:  "local-agent",
	KeysURL:  "https://www.workbuddy.cn/profile/keys",
	Messages: true,
}

// workBuddyIntl 是国际站配置，字段取值对照 magpie 的 SITES["workbuddy-ai"]。
var workBuddyIntl = workBuddySite{
	ID:       PlatformWorkBuddyIntl,
	Name:     "WorkBuddy AI",
	Endpoint: "https://www.workbuddy.ai",
	AuthID:   "workbuddy-desktop-ai",
	Platform: "workbuddy-ai",
	AI:       true,
	App:      "5.9.3",
	CLI:      "2.165.0",
	AppName:  "workbuddy-ai",
	Runtime:  "local-agent",
	KeysURL:  "https://www.workbuddy.ai/profile/keys",
	// 国际站不为 glm-* 接受图片，即使其配置声明支持。
	NoImages: `^glm-`,
}

// workBuddySiteForPlatform 按平台常量返回站点配置。
func workBuddySiteForPlatform(platform string) *workBuddySite {
	switch platform {
	case PlatformWorkBuddyCN:
		return &workBuddyCN
	case PlatformWorkBuddyIntl:
		return &workBuddyIntl
	default:
		return nil
	}
}

// workBuddyOtherPlatform 返回同一厂商的另一个站点平台名。
func workBuddyOtherPlatform(platform string) string {
	switch platform {
	case PlatformWorkBuddyCN:
		return PlatformWorkBuddyIntl
	case PlatformWorkBuddyIntl:
		return PlatformWorkBuddyCN
	default:
		return ""
	}
}

// workBuddyAPIBase 返回站点的 API 根地址（/v2 之前的部分）。
func (s *workBuddySite) workBuddyAPIBase() string {
	return s.Endpoint + "/v2"
}

// workBuddyMessagesBase 返回站点 Messages 协议的根地址。
func (s *workBuddySite) workBuddyMessagesBase() string {
	return s.Endpoint + "/v1"
}

// workBuddyCLIUserAgent 返回该站点 CLI 发送 API 调用时使用的 User-Agent。
//
// 形如 `WorkBuddy/<app> <appName>/<app> CLI/<cli>`。上游会校验其中的
// WorkBuddy/<version> 前缀，缺失时以 error 10085 拒绝请求。
func (s *workBuddySite) workBuddyCLIUserAgent() string {
	return "WorkBuddy/" + s.App + " " + s.AppName + "/" + s.App + " CLI/" + s.CLI
}

// workBuddyTextOnly 判断该站点的这个模型是否会丢弃图片。
func (s *workBuddySite) workBuddyTextOnly(model string) bool {
	if s == nil || s.NoImages == "" {
		return false
	}
	re, err := regexp.Compile(s.NoImages)
	if err != nil {
		return false
	}
	return re.MatchString(model)
}

// IsWorkBuddyAccount 判断账号是否属于 WorkBuddy 的两个站点之一。
func IsWorkBuddyAccount(account *Account) bool {
	return account != nil && isWorkBuddyPlatform(account.Platform)
}

// workBuddyAccountModel 是账号可用模型的一项。
//
// 字段名对齐 OpenAI 的 /models 返回，前端模型选择器可直接复用。
type workBuddyAccountModel struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// WorkBuddyAccountModels 返回账号当前可用模型。
//
// 只认两个来源：/v3/config 的落库缓存（权威），以及凭据里的 model_mapping
// （导入时的快照）。两者都为空时返回空列表，由调用方触发一次实时同步；
// 不再回落到站点的内置目录——那份硬编码清单会和上游脱节（它至今还留着
// gpt-5.5 / gemini-3.1-pro 这些已下线的名字）。
func WorkBuddyAccountModels(account *Account) []workBuddyAccountModel {
	if account == nil {
		return nil
	}
	// 优先返回 /v3/config 缓存：那才是账号当前真正可用的模型。
	// credentials.model_mapping 是导入时的历史快照，可能包含已下线模型，
	// 只能作为缓存缺失时的兜底。
	if cached := WorkBuddyCachedModels(account); len(cached) > 0 {
		// 直接返回 /v3/config 的 agents[cli] 清单（与 magpie 同源）。
		//
		// 早期这里按一张手写白名单过滤「可对话模型」，白名单只留了
		// balanced-model / default-model / fast-model / auto / hy3 —— 上游换成
		// gpt-6-* / gemini-4-* / kimi-k3.1 之后，整张清单被过滤到只剩一个，
		// 测试连接里自然只看得到一个模型。CLI 清单本身就是能对话的集合。
		return cached
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		// 没有缓存也没有映射：交给调用方去上游拉一次（并写回缓存）。
		return nil
	}

	out := make([]workBuddyAccountModel, 0, len(mapping))
	for id := range mapping {
		out = append(out, workBuddyAccountModel{
			ID: id, Object: "model", Type: "model", DisplayName: id,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// WorkBuddyCachedModels 从账号 extra 读取 /v3/config 的落库缓存。
//
// 缓存写在 accounts.extra 的 workbuddy_models_cache 字段，随账号持久化，
// 进程重启后仍然有效——不能只放内存，否则重启即丢失、编辑页又回落到
// 过期的 model_mapping。
func WorkBuddyCachedModels(account *Account) []workBuddyAccountModel {
	if account == nil || account.Extra == nil {
		return nil
	}
	raw, ok := account.Extra["workbuddy_models_cache"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m["models"].([]any)
	if !ok || len(list) == 0 {
		return nil
	}
	out := make([]workBuddyAccountModel, 0, len(list))
	for _, item := range list {
		s, ok := item.(string)
		if !ok || s == "" {
			continue
		}
		out = append(out, workBuddyAccountModel{
			ID: s, Object: "model", Type: "model", DisplayName: s,
		})
	}
	return out
}

// WorkBuddyModelsCacheExtra 构造写回 accounts.extra 的缓存字段。
func WorkBuddyModelsCacheExtra(models []string, fetchedAt time.Time) map[string]any {
	list := make([]any, 0, len(models))
	for _, m := range models {
		list = append(list, m)
	}
	return map[string]any{
		"models":     list,
		"updated_at": fetchedAt.Format(time.RFC3339),
		"updated_ms": fetchedAt.UnixMilli(),
		"source":     "v3_config",
	}
}

// WorkBuddyModelInstalled 判断模型是否在账号的可用集合内。
func WorkBuddyModelInstalled(account *Account, model string) bool {
	if account == nil {
		return false
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		return true // 空映射 = 不限制
	}
	_, ok := mapping[model]
	return ok
}
