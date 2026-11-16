package service

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"
)

// Codex 平台已从底层移除。
//
// Codex 是 OpenAI 官方 CLI 客户端，需要一整套配套能力：设备指纹收敛、
// turn 状态、身份头、模型清单构建、OAuth 协议转换。这些只服务于
// 「用 Codex 客户端登录 OpenAI 账号」这一种场景。
//
// Auto2API 只服务 WorkBuddy，网关走自己完整的链路
// （buildUpstreamRequest → /v2/chat/completions → SSE 聚合 → 协议回写），
// 不需要也不该借用 Codex 的任何一层。相关实现文件已整体删除。
//
// 本文件只保留共享代码路径仍在引用的最小符号：它们全部表达「该能力不存在」，
// 不会触发任何上游行为。

// setCodexToolNameReverse 记录工具名反向映射。Codex 移除后为空操作。
func setCodexToolNameReverse(_ any, _ map[string]string) {}

// isOpenAIResponsesLiteHeader 判定 Responses Lite 头。Codex 移除后恒为 false。
func isOpenAIResponsesLiteHeader(_ string) bool { return false }

// responsesLiteHeader 是 Responses Lite 的头名。
const responsesLiteHeader = "x-openai-responses-lite"

// OpenAICodexVersionSyncService 原同步 Codex CLI 版本号。Codex 移除后为空结构。
type OpenAICodexVersionSyncService struct{}

// Stop 空操作。
func (s *OpenAICodexVersionSyncService) Stop() {}

// prepareCodexAccountIdentitySource 准备 Codex 身份来源。Codex 移除后返回 nil。
func (s *OpenAIGatewayService) prepareCodexAccountIdentitySource(_ context.Context, _ any, _ *Account) (any, error) {
	return nil, nil
}

// codexFingerprintMode 原控制设备指纹收敛强度。Codex 移除后不再使用。
type codexFingerprintMode string

const (
	codexFingerprintOff     codexFingerprintMode = "off"
	codexFingerprintDevice  codexFingerprintMode = "device"
	codexFingerprintSession codexFingerprintMode = "session"
	codexFingerprintFull    codexFingerprintMode = "full"
)

// codexImageGenerationExplicitToolPolicy 是 Codex 图像工具策略。
type codexImageGenerationExplicitToolPolicy string

const (
	codexImageGenerationExplicitToolPolicyAllow codexImageGenerationExplicitToolPolicy = "allow"
	codexImageGenerationExplicitToolPolicyStrip codexImageGenerationExplicitToolPolicy = "strip"
)

// 图像生成相关：Codex 移除后一律不做改动。
func stripOpenAIImageGenerationToolsFromRawPayload(body []byte) ([]byte, bool, error) {
	return body, false, nil
}
func stripOpenAIImageGenerationTools(_ map[string]any) bool                    { return false }
func ensureOpenAIResponsesImageGenerationTool(_ map[string]any) bool           { return false }
func ensureOpenAIResponsesImageGenerationToolChoiceAuto(_ map[string]any) bool { return false }
func normalizeOpenAIResponsesImageGenerationTools(_ map[string]any) bool       { return false }
func normalizeOpenAIResponsesImageOnlyModel(_ map[string]any) bool             { return false }
func validateOpenAIResponsesImageModel(_ map[string]any, _ string) error       { return nil }
func openAIRequestBodyImageGenerationToolNeedsNormalization(_ []byte) bool     { return false }

// normalizeOpenAIResponseFormatSchemas 归一化响应格式 schema。Codex 移除后不改动。
func normalizeOpenAIResponseFormatSchemas(_ map[string]any) bool { return false }

// defaultCodexSynthInstructions 返回 Codex 合成指令。Codex 移除后为空串。
func defaultCodexSynthInstructions(_ string) string { return "" }

// IsImageGenerationIntent 判定图像生成意图。WorkBuddy 不支持该端点。
func IsImageGenerationIntent(_ string, _ string, _ []byte) bool { return false }

// openAIResponsesEndpoint 是 Responses 端点路径。
const openAIResponsesEndpoint = "/v1/responses"

// ImageGenerationPermissionMessage 返回图像生成被禁用的提示。
func ImageGenerationPermissionMessage() string { return "image generation is not available" }

// getAPIKeyFromContext 从 gin.Context 取当前请求的 API Key。
//
// 这不是 Codex 专属能力：图像生成要按分组开关决定是否计费/放行，
// 需要当前 Key 的 Group。之前这里恒返回 nil，导致
// GroupAllowsImageGeneration 永远拿到 nil → 图像生成对所有账号恒禁用。
func getAPIKeyFromContext(c any) *APIKey {
	gc, ok := c.(*gin.Context)
	if !ok || gc == nil {
		return nil
	}
	v, exists := gc.Get("api_key")
	if !exists {
		return nil
	}
	key, ok := v.(*APIKey)
	if !ok {
		return nil
	}
	return key
}

// GroupAllowsImageGeneration 判定分组是否允许图像生成。
//
// 读的是 Group.AllowImageGeneration —— 分组上的真实开关。之前恒返回
// false，把「分组允许图像生成」这个配置整个架空了。
func GroupAllowsImageGeneration(g any) bool {
	group, ok := g.(*Group)
	if !ok || group == nil {
		return false
	}
	return group.AllowImageGeneration
}

// normalizeOpenAIModelForUpstream 把模型 ID 归一化为上游写法。
// WorkBuddy 按模型名直传，原样返回。
func normalizeOpenAIModelForUpstream(_ *Account, model string) string { return model }

// applyCodexOAuthTransformWithOptions 应用 Codex 请求转换。移除后原样返回。
func applyCodexOAuthTransformWithOptions(body map[string]any, _ any) codexTransformOut {
	return codexTransformOut{Body: body}
}

// codexTransformOut 是转换结果。
type codexTransformOut struct {
	Body            map[string]any
	Error           error
	ToolNameReverse map[string]string
	NormalizedModel string
	PromptCacheKey  string
	Modified        bool
	SessionID       string
	TurnState       string
}

// codexOAuthTransformOptions 是转换选项。
type codexOAuthTransformOptions struct {
	SkipDefaultInstructions             bool
	OmitPromotedSystemMessagesFromInput bool
	IsCodexCLI                          bool
	IsCompact                           bool
	PreserveToolCallIDs                 bool
	ResponsesLite                       bool
}

// codexTransformResult 是 codexTransformOut 的别名。
type codexTransformResult = codexTransformOut

// restoreCodexToolNamesFromContext 还原工具名。Codex 移除后原样返回。
func restoreCodexToolNamesFromContext(_ any, payload []byte) []byte { return payload }

// isolateOpenAIUpstreamSessionID 生成上游会话隔离 ID。原样返回。
func isolateOpenAIUpstreamSessionID(_ int64, _ any, sessionID string) string { return sessionID }

// codexAccountIdentitySource 是 Codex 账号身份来源。
type codexAccountIdentitySource struct{}

// applyCodexAccountIdentityClientMetadataMap 注入客户端元数据。空操作。
func applyCodexAccountIdentityClientMetadataMap(_ map[string]any, _ any, _ int64) bool { return false }

// httpHeaderOf 仅为保持 http 导入被使用。
var _ = http.MethodGet

// IsImageGenerationIntentMap 判定 map 形态的图像意图。WorkBuddy 不支持。
func IsImageGenerationIntentMap(_ string, _ string, _ map[string]any) bool { return false }

// hasOpenAIImageGenerationTool 判定请求是否带图像工具。恒为 false。
func hasOpenAIImageGenerationTool(_ map[string]any) bool { return false }

// applyCodexImageGenerationBridgeInstructions 注入桥接指令。返回 false。
func applyCodexImageGenerationBridgeInstructions(_ map[string]any) bool { return false }

// openAIRequestBodyHasImageGenerationDeclaration 判定请求是否声明图像生成。
func openAIRequestBodyHasImageGenerationDeclaration(_ []byte) bool { return false }

// isCodexSparkModel 判定 Codex Spark 模型。恒为 false。
func isCodexSparkModel(_ string) bool { return false }

// validateCodexSparkInput 校验 Spark 输入。直接放行。
func validateCodexSparkInput(_ map[string]any, _ string) error { return nil }

// stripCodexSparkImageGenerationTools 剥离 Spark 图像工具。返回 false。
func stripCodexSparkImageGenerationTools(_ map[string]any) bool { return false }

// applyCodexClientMetadata 注入 Codex 客户端元数据。返回 false。
func applyCodexClientMetadata(_ map[string]any, _ *Account) bool { return false }

// stageCodexFingerprintIDs 暂存指纹标识。空操作。
func stageCodexFingerprintIDs(_ any, _ map[string]string) {}

// applyCodexFingerprintClientMetadata 注入指纹元数据。返回 false。
func applyCodexFingerprintClientMetadata(_ map[string]any, _ map[string]string) bool { return false }

// applyStagedCodexFingerprintHeaders 应用暂存指纹头。空操作。
func applyStagedCodexFingerprintHeaders(_ any, _ *Account, _ http.Header) {}

// resolveCodexFingerprintIDsFromRequest 提取指纹标识。返回 nil。
func resolveCodexFingerprintIDsFromRequest(_ ...any) map[string]string { return nil }

// SupportsVerbosity 报告是否支持 verbosity 参数。恒为 false。
func SupportsVerbosity(_ string) bool { return false }

// OpenAIResponsesImageBillingConfig 是图像计费配置。WorkBuddy 不支持。
type OpenAIResponsesImageBillingConfig struct {
	Model     string
	SizeTier  string
	InputSize string
}

// resolveOpenAIResponsesImageBillingConfigDetailed 解析图像计费配置。
func resolveOpenAIResponsesImageBillingConfigDetailed(_ map[string]any, _ string) (OpenAIResponsesImageBillingConfig, error) {
	return OpenAIResponsesImageBillingConfig{}, nil
}

// resolveOpenAIResponsesImageBillingConfigDetailedFromBody 从原始 body 解析。
func resolveOpenAIResponsesImageBillingConfigDetailedFromBody(_ []byte, _ string) (OpenAIResponsesImageBillingConfig, error) {
	return OpenAIResponsesImageBillingConfig{}, nil
}

// CodexCanonicalClientVersion 返回 Codex 客户端版本。Codex 移除后为占位值。
func CodexCanonicalClientVersion() string { return "0.0.0" }

// CodexCanonicalUserAgent 返回 Codex 客户端 UA。Codex 移除后为占位值。
func CodexCanonicalUserAgent() string { return "codex-cli/0.0.0" }

// applyCodexAccountIdentityHeaders 注入身份头。空操作。
func applyCodexAccountIdentityHeaders(_ http.Header, _ codexAccountIdentitySource, _ int64) {}

// enforceCodexIdentityHeadersWithUA 收敛身份头。空操作。
func enforceCodexIdentityHeadersWithUA(_ http.Header, _ string) {}

// guardOpenAICodexTurnStateEcho 防止 turn state 回显。空操作。
func (s *OpenAIGatewayService) guardOpenAICodexTurnStateEcho(_ ...any) {}

// guardOpenAICodexTurnStateEcho 需要类型定义配合。

// extractPromptLikeInstructionsFromInput 提取指令文本。返回空串。
func extractPromptLikeInstructionsFromInput(_ map[string]any) string { return "" }

// forcedCodexInstructionsTemplateData 是强制指令模板数据。
type forcedCodexInstructionsTemplateData struct {
	ExistingInstructions string
	OriginalModel        string
	NormalizedModel      string
	BillingModel         string
	UpstreamModel        string
}

// applyForcedCodexInstructionsTemplate 应用强制模板。原样返回。
func applyForcedCodexInstructionsTemplate(body map[string]any, _ string, _ forcedCodexInstructionsTemplateData) (map[string]any, error) {
	return body, nil
}

// ensureCodexIdentityHeaders 注入身份头。空操作。
func ensureCodexIdentityHeaders(_ http.Header) {}

// enforceCodexIdentityHeaders 收敛身份头。空操作。
func enforceCodexIdentityHeaders(_ http.Header) {}

// applyCodexAccountIdentityClientMetadataRaw 注入客户端元数据（原始 body）。
func applyCodexAccountIdentityClientMetadataRaw(body []byte, _ codexAccountIdentitySource, _ int64) ([]byte, bool, error) {
	return body, false, nil
}

// applyCodexFingerprintClientMetadataRaw 注入指纹元数据（原始 body）。
func applyCodexFingerprintClientMetadataRaw(body []byte, _ map[string]string) ([]byte, bool, error) {
	return body, false, nil
}

// isOpenAIResponsesLiteWebSocketPayload 判定 Lite WS 负载。恒为 false。
func isOpenAIResponsesLiteWebSocketPayload(_ []byte) bool { return false }

// aliasOpenAIOAuthReservedToolNamesBody 别名化保留工具名。原样返回。
func aliasOpenAIOAuthReservedToolNamesBody(body []byte) ([]byte, map[string]string, bool, error) {
	return body, nil, false, nil
}

// mergeCodexToolNameReverse 合并工具名映射。空操作。
func mergeCodexToolNameReverse(_ any, _ map[string]string) {}

// apiKeyGroup 返回 API Key 所属分组。Codex 移除后返回 nil。
func apiKeyGroup(_ any) any { return nil }

// extractOpenAICodexTurnState 提取 turn state。返回空串。
func extractOpenAICodexTurnState(_ http.Header) string { return "" }

// codexOutboundIdentity 是 Codex 出站身份头组合。
type codexOutboundIdentity struct {
	originator string
	userAgent  string
	version    string
}

// resolveCodexOutboundIdentity 解析出站身份。返回空身份。
func resolveCodexOutboundIdentity(_ string) codexOutboundIdentity {
	return codexOutboundIdentity{}
}

// restoreCodexToolNamesFromSSEContext 还原 SSE 工具名。原样返回。
func restoreCodexToolNamesFromSSEContext(_ any, data []byte, _ string) []byte { return data }

// openAICodexTurnStateHeader 是 turn state 头名。
const openAICodexTurnStateHeader = "x-codex-turn-state"

// noteOpenAICodexTurnStateProvenance 记录 turn state 来源。空操作。
func (s *OpenAIGatewayService) noteOpenAICodexTurnStateProvenance(_ ...any) {}

// stageOpenAICodexTurnState 暂存 turn state。空操作。
func stageOpenAICodexTurnState(_ *http.Header, _ http.Header) {}

// relayOpenAICodexTurnState 转发 turn state。空操作。
func (s *OpenAIGatewayService) relayOpenAICodexTurnState(_ ...any) {}

// noteStagedOpenAICodexTurnStateCommitted 记录已提交。空操作。
func (s *OpenAIGatewayService) noteStagedOpenAICodexTurnStateCommitted(_ ...any) {}

// isOfficialOpenAIModelsBaseURL 判定官方模型地址。恒为 false。
func isOfficialOpenAIModelsBaseURL(_ string) bool { return false }

// openAIChatGPTInternalUnsupportedFields 是 ChatGPT 内部字段黑名单。
var openAIChatGPTInternalUnsupportedFields []string

// SetCodexIdentityEnforcementEnabled 开关身份收敛。空操作。
func SetCodexIdentityEnforcementEnabled(_ bool) {}

// CodexImageGenerationBridgeOverride 报告图像桥接覆盖。返回 nil（未覆盖）。
func (a *Account) CodexImageGenerationBridgeOverride() *bool { return nil }

// CodexImageGenerationBridgeOverride 报告渠道图像桥接覆盖。返回 nil。
func (c *Channel) CodexImageGenerationBridgeOverride(_ string) *bool { return nil }

// responsesLiteWSMetadataKey 是 Lite WS 元数据键。
const responsesLiteWSMetadataKey = "openai_responses_lite_ws"

// getNormalizedCodexModel 返回归一化模型名。原样返回。
func getNormalizedCodexModel(model string) string { return model }

// isKnownCodexModelSuffix 判定 Codex 模型后缀。恒为 false。
func isKnownCodexModelSuffix(_ string) bool { return false }

// openAIModelsResponseForClient 按 If-None-Match 返回响应。
func openAIModelsResponseForClient(resp *OpenAIModelsResponse, ifNoneMatch string) *OpenAIModelsResponse {
	if resp == nil {
		return nil
	}
	if ifNoneMatch != "" && resp.ETag != "" && resp.ETag == ifNoneMatch {
		return &OpenAIModelsResponse{NotModified: true, ETag: resp.ETag}
	}
	return resp
}

// FetchCodexModelsManifest 拉取 Codex 模型清单。Codex 移除后返回空。
func (s *OpenAIGatewayService) FetchCodexModelsManifest(_ context.Context, _ *Account, _ string, _ string) (*OpenAIModelsResponse, error) {
	return &OpenAIModelsResponse{}, nil
}

// codexModelsManifestBodyETag 计算清单 ETag。
func codexModelsManifestBodyETag(_ []byte) string { return "" }

// codexModelsManifestUpstreamError 是清单错误类型。
type codexModelsManifestUpstreamError struct {
	err       error
	retryable bool
}

func (e *codexModelsManifestUpstreamError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "codex models manifest unavailable"
}
func (e *codexModelsManifestUpstreamError) Unwrap() error { return e.err }

// isCodexDedicatedMediaModel 判定 Codex 专用媒体模型。恒为 false。
func isCodexDedicatedMediaModel(_ string) bool { return false }

// codexAutoModelPrefix 是自动模型前缀。
const codexAutoModelPrefix = "codex-"

// FilterCodexModelIDsForGroup 过滤分组可见模型。原样返回。
func FilterCodexModelIDsForGroup(_ []string, _ any) []string { return nil }

// fetchPinnedOpenAIModels 拉取指定账号的模型清单。Codex 移除后返回空。
func (s *OpenAIGatewayService) fetchPinnedOpenAIModels(_ context.Context, _ *Group, _ func(ctx context.Context, account *Account) (*OpenAIModelsResponse, error)) ([]pinnedOpenAIModelsResult, error) {
	return nil, nil
}

// ErrNoPinnedCodexModelsAccounts 表示没有可用的固定账号。Codex 移除后为常规错误。
var ErrNoPinnedCodexModelsAccounts = fmt.Errorf("no pinned codex models accounts")

// IsRetryableCodexModelsManifestError 判定是否可重试。恒为 false。
func IsRetryableCodexModelsManifestError(_ error) bool { return false }

// isCodexToolCallInputType 判定工具调用输入类型。恒为 false。
func isCodexToolCallInputType(_ string) bool { return false }

// ValidateCodexPersonalAccessToken 校验 Codex PAT。Codex 移除后总是失败。
func (s *OpenAIOAuthService) ValidateCodexPersonalAccessToken(_ context.Context, _ string, _ string) (*OpenAITokenInfo, error) {
	return nil, fmt.Errorf("codex support has been removed")
}

// NormalizeOpenAIPersonalAccessTokenCredentials 归一化 PAT 凭证。原样返回。
func NormalizeOpenAIPersonalAccessTokenCredentials(_ *Account, _ *OpenAITokenInfo, creds map[string]any) map[string]any {
	return creds
}

// isCodexToolCallItemType 判定工具调用项类型。恒为 false。
func isCodexToolCallItemType(_ string) bool { return false }

// updateCodexToolNameReverseForWSFrame 更新 WS 工具名映射。空操作。
func updateCodexToolNameReverseForWSFrame(_ ...any) {}

// CodexImageGenerationExplicitToolPolicy 返回图像工具策略。恒为 strip。
func (a *Account) CodexImageGenerationExplicitToolPolicy() codexImageGenerationExplicitToolPolicy {
	return codexImageGenerationExplicitToolPolicyStrip
}

// IsImageGenerationIntentForPlatform 判定平台图像意图。恒为 false。
func IsImageGenerationIntentForPlatform(_ string, _ string, _ []byte, _ string) bool { return false }

// applyStagedCodexFingerprintClientMetadata 应用暂存指纹元数据。返回 false。
func applyStagedCodexFingerprintClientMetadata(_ *gin.Context, _ *Account, _ map[string]any) bool {
	return false
}

// activeCodexFingerprintMode 返回指纹模式。恒为 off。
func activeCodexFingerprintMode(_ *Account) codexFingerprintMode { return codexFingerprintOff }

// normalizeCodexModel 归一化模型名。原样返回。
func normalizeCodexModel(model string) string { return model }

// NormalizeCodexClientVersion 归一化客户端版本。原样返回。
func NormalizeCodexClientVersion(v string) string { return v }

// buildCodexCLIUserAgent 构造 Codex UA。返回空串。
func buildCodexCLIUserAgent(_ string) string { return "" }

// codexModelMap 是 Codex 模型映射表。Codex 移除后为空。
var codexModelMap = map[string]string{}

// applyCodexToolCapabilities 应用工具能力。空操作。
func applyCodexToolCapabilities(_ ...any) {}

// buildCodexModelsManifestURL 构造清单地址。返回空串。
func buildCodexModelsManifestURL(_ ...any) (*url.URL, error) {
	return url.Parse("https://api.openai.com/v1/models")
}

// chatgptCodexModelsURL 是 ChatGPT 模型列表地址。
const chatgptCodexModelsURL = ""

// NewOpenAICodexVersionSyncService 构造版本同步服务。Codex 移除后返回空服务。
func NewOpenAICodexVersionSyncService(_ ...any) *OpenAICodexVersionSyncService {
	return &OpenAICodexVersionSyncService{}
}

// Start 启动版本同步循环。Codex 移除后不做任何事。
func (s *OpenAICodexVersionSyncService) Start(_ ...any) {}

// openAICodexVersionSyncInterval 是版本同步周期。
const openAICodexVersionSyncInterval = time.Hour

// SetCodexCanonicalUserAgentResolver 设置 UA 解析器。空操作。
func SetCodexCanonicalUserAgentResolver(_ any) {}

// ScopedCodexIdentity 描述限定范围内的 Codex 身份。Codex 移除后为空结构。
type ScopedCodexIdentity struct {
	AccountID       string
	UserID          string
	Email           string
	AccessToken     string
	AccessTokenOnly bool
	AgentIdentity   bool
}

// ShouldEnsureCodexFingerprintSeedForExtraUpdates 报告是否需要预置指纹种子。
// Codex 移除后恒为 false。
func ShouldEnsureCodexFingerprintSeedForExtraUpdates(_ ...any) bool { return false }

// CodexCanonicalAuthIdentity 是 Codex 认证身份标识。
func CodexCanonicalAuthIdentity() (userAgent string, originator string) {
	return CodexCanonicalUserAgent(), "codex_cli_rs"
}
