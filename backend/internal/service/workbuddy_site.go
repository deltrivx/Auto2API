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

// workBuddyModelSpec 是站点配置里的一条模型定义。
//
// 字段对应 magpie 的 [id, name, context, efforts, output]：
//   - Context   是 maxInputTokens
//   - Efforts   是该模型支持的 reasoning effort 取值；nil 表示不支持
//   - MaxOutput 是 maxOutputTokens；0 表示交给外部目录（models.dev）决定
type workBuddyModelSpec struct {
	ID        string
	Name      string
	Context   int
	Efforts   []string
	MaxOutput int
}

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
	// Models 是站点内置的模型目录。
	Models []workBuddyModelSpec
	// Messages 表示该站点同时提供 Anthropic Messages 协议。
	Messages bool
	// NoImages 匹配该站点不接受的图片模型（国际站丢弃 glm-* 的图片）。
	NoImages string
}

// workBuddyCN 是国内站配置，字段取值对照 magpie 的 SITES.workbuddy。
//
// 版本号对齐 AutoBuddy 的 DESKTOP_IDE_VERSION=5.5.6 / DESKTOP_CLI_VERSION=2.137.1。
//
// 原先用 5.9.3 / 2.165.0（取自当时的更新源构建），但实测同账号、同模型、
// 同提示词下 AutoBuddy 能过、本网关返回 400 code 11128
// 「Illegal API invocation from an unapproved channel」。
// 上游的渠道校验按 UA 与 X-Client-Version 识别客户端形态，版本号不在其
// 认可的桌面端集合内即判为非官方客户端；这两个值参与签名，改动需谨慎。
var workBuddyCN = workBuddySite{
	ID:       PlatformWorkBuddyCN,
	Name:     "WorkBuddy",
	Endpoint: "https://copilot.tencent.com",
	AuthID:   "workbuddy-desktop",
	Platform: "workbuddy",
	AI:       false,
	App:      "5.5.6",
	CLI:      "2.137.1",
	AppName:  "WorkBuddy",
	Runtime:  "local-agent",
	KeysURL:  "https://www.workbuddy.cn/profile/keys",
	Models:   workBuddyCNModels,
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
	App:      "5.5.6",
	CLI:      "2.137.1",
	AppName:  "workbuddy-ai",
	Runtime:  "local-agent",
	KeysURL:  "https://www.workbuddy.ai/profile/keys",
	Models:   workBuddyAIModels,
	// 国际站不为 glm-* 接受图片，即使其配置声明支持。
	NoImages: `^glm-`,
}

// workBuddyCNModels 是国内站的模型目录，取自上游 config 接口。
var workBuddyCNModels = []workBuddyModelSpec{
	{ID: "auto", Name: "Auto", Context: 168000},
	{ID: "hy4-preview-f", Name: "Hy4 preview", Context: 1000000, Efforts: []string{"high"}},
	{ID: "hy3", Name: "Hy3", Context: 192000, Efforts: []string{"low", "high"}},
	{ID: "hy3-x", Name: "Hy3-X", Context: 192000, Efforts: []string{"low", "high"}},
	{ID: "deepseek-v4.1-flash", Name: "Deepseek-V4.1-Flash", Context: 1000000, Efforts: []string{"none", "low", "high", "max"}, MaxOutput: 128000},
	{ID: "glm-5.3", Name: "GLM-5.3", Context: 1000000, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.3-flash", Name: "GLM-5.3-Flash", Context: 1000000, Efforts: []string{"low", "high", "max"}, MaxOutput: 131072},
	{ID: "glm-5.2", Name: "GLM-5.2", Context: 1000000, Efforts: []string{"high", "xhigh"}},
	{ID: "glm-5.1", Name: "GLM-5.1", Context: 200000},
	{ID: "glm-5v-turbo", Name: "GLM-5v-Turbo", Context: 200000},
	{ID: "minimax-m3", Name: "MiniMax-M3", Context: 512000},
	{ID: "kimi-k3-1", Name: "Kimi-K3", Context: 1000000, Efforts: []string{"low", "high", "xhigh"}},
	{ID: "kimi-k2.7", Name: "Kimi-K2.7-Code", Context: 256000},
	{ID: "kimi-k2.6", Name: "Kimi-K2.6", Context: 256000},
	{ID: "deepseek-v4-pro", Name: "Deepseek-V4-Pro", Context: 1000000, Efforts: []string{"none", "high", "xhigh"}, MaxOutput: 128000},
}

// workBuddyAIModels 是国际站的模型目录。
var workBuddyAIModels = []workBuddyModelSpec{
	{ID: "default-model", Name: "Default", Context: 176000, MaxOutput: 24000},
	{ID: "fast-model", Name: "Fast", Context: 200000, MaxOutput: 32000},
	{ID: "balanced-model", Name: "Balanced", Context: 256000},
	{ID: "primary-model", Name: "Primary", Context: 272000, MaxOutput: 72000},
	{ID: "deep-model", Name: "Deep", Context: 176000},
	{ID: "gpt-5.5", Name: "GPT-5.5", Context: 1000000},
	{ID: "gpt-5.4", Name: "GPT-5.4", Context: 272000},
	{ID: "gpt-5.3-codex", Name: "GPT-5.3-Codex", Context: 272000},
	{ID: "gemini-3.1-pro", Name: "Gemini-3.1-Pro", Context: 400000},
	{ID: "gemini-3.5-flash", Name: "Gemini-3.5-Flash", Context: 1000000, MaxOutput: 65536},
	{ID: "glm-5.3", Name: "GLM-5.3", Context: 1000000, Efforts: []string{"low", "high", "max"}},
	{ID: "glm-5.2", Name: "GLM-5.2", Context: 1000000, Efforts: []string{"high", "xhigh"}, MaxOutput: 48000},
	{ID: "hy3", Name: "Hy3", Context: 192000, Efforts: []string{"low", "high"}, MaxOutput: 64000},
	{ID: "kimi-k3", Name: "Kimi-K3", Context: 1000000, MaxOutput: 32000},
	{ID: "kimi-k2.6", Name: "Kimi-K2.6", Context: 256000},
	{ID: "minimax-m3", Name: "MiniMax-M3", Context: 512000},
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

// workBuddyModelByID 在站点的静态模型目录中查找模型。
func (s *workBuddySite) workBuddyModelByID(id string) (workBuddyModelSpec, bool) {
	for _, m := range s.Models {
		if m.ID == id {
			return m, true
		}
	}
	return workBuddyModelSpec{}, false
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
// 数据源是凭据里的 model_mapping：上游会按套餐与灰度下发不同的模型集合，
// 所以不能用固定目录。映射为空时回落到站点的内置清单，
// 保证编辑页面在账号尚未同步过的情况下仍有可选项。
func WorkBuddyAccountModels(account *Account) []workBuddyAccountModel {
	if account == nil {
		return nil
	}
	// 优先返回 /v3/config 缓存：那才是账号当前真正可用的模型。
	// credentials.model_mapping 是导入时的历史快照，可能包含已下线模型，
	// 只能作为缓存缺失时的兜底。
	if cached := WorkBuddyCachedModels(account); len(cached) > 0 {
		// 国际站的缓存里混着补全/NES/视频模型，它们不能对话。
		// 只对客户端暴露能跑 chat 的那几个，避免用户选到必然失败的模型。
		if isWorkBuddyAIVariant(account.Platform) {
			filtered := make([]workBuddyAccountModel, 0, len(cached))
			for _, m := range cached {
				if workBuddyIntlChatCapableModels[m.ID] {
					filtered = append(filtered, m)
				}
			}
			if len(filtered) > 0 {
				return filtered
			}
		}
		return cached
	}
	mapping := account.GetModelMapping()
	if len(mapping) == 0 {
		site := workBuddySiteForPlatform(account.Platform)
		if site == nil {
			return nil
		}
		out := make([]workBuddyAccountModel, 0, len(site.Models))
		for _, m := range site.Models {
			out = append(out, workBuddyAccountModel{
				ID: m.ID, Object: "model", Type: "model", DisplayName: m.Name,
			})
		}
		return out
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
