package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/DeltrivX/Auto2API/internal/domain"

	"github.com/gin-gonic/gin"
)

// Gemini / Antigravity 平台的桩实现。
//
// 这两个平台已从产品中移除，其专属实现文件已删除。但共享链路（限流、用量
// 统计、token 刷新、wire 注入）里仍保留按平台分派的调用点与类型字段。
//
// 这里提供最小可用定义：判定类函数返回「不适用」，构造器返回空壳，
// 常量保留原值以免影响既有判定逻辑。

var errGeminiRemoved = errors.New("gemini platform has been removed")
var errAntigravityRemoved = errors.New("antigravity platform has been removed")

// ---- 共享常量（原定义在已删除的平台文件中）----

const (
	forbiddenTypeValidation = "validation"
	forbiddenTypeViolation  = "violation"

	errorCodeForbidden       = "forbidden"
	errorCodeNetworkError    = "network_error"
	errorCodeRateLimited     = "rate_limited"
	errorCodeUnauthenticated = "unauthenticated"

	geminiThinkingBudgetLowMax    = 1024
	geminiThinkingBudgetMediumMax = 8192

	creditsExhaustedKey = "AICredits"

	GeminiCachedContentUsageRequestIDPrefix = "gemini-cache"
)

// ---- Gemini / Antigravity 判定与辅助函数 ----

// isImageGenerationModel 判断模型是否为图片生成模型；Gemini 移除后恒为 false。
func isImageGenerationModel(_ string) bool { return false }

// isOfficialGeminiAPIBaseURL 判断目标是否为 Gemini 官方主机；平台已移除。
func isOfficialGeminiAPIBaseURL(_ string) bool { return false }

// geminiModelFlash 是 Flash 系列标识；平台已移除后无模型命中。
var geminiModelFlash = ""

// geminiModelClass 是模型分类枚举值；平台已移除后仅保留零值。
type geminiModelClass = string

// buildGeminiAIStudioModelActionURL 构造 AI Studio 端点；平台已移除。

// applyThinkingModelSuffix 追加思考档位后缀；平台已移除后原样返回。
func applyThinkingModelSuffix(model string, _ bool) string { return model }

// classifyForbiddenType 归类 403 原因；平台已移除后返回空。
func classifyForbiddenType(_ string) string { return "" }

// extractValidationURL 从错误体里提取验证链接；平台已移除后返回空。
func extractValidationURL(_ string) string { return "" }

// mapAntigravityModel 映射模型名；平台已移除后原样返回。

// normalizeAntigravityModelName 归一化模型名；平台已移除后原样返回。
func normalizeAntigravityModelName(model string) string { return model }

// accountNeedsAntigravityForceTokenRefresh 判断是否需要强制刷新 token；平台已移除。
func accountNeedsAntigravityForceTokenRefresh(_ *Account) bool { return false }

// clearAntigravityForceTokenRefreshExtra 清除强制刷新标记；平台已移除后为空操作。
func clearAntigravityForceTokenRefreshExtra() map[string]any { return map[string]any{} }

// applyAntigravityPrivacyMode 应用隐私模式；平台已移除后为空操作。
func applyAntigravityPrivacyMode(_ *Account, _ string) {}

// setAntigravityPrivacy 设置隐私开关；平台已移除后为空操作。
func setAntigravityPrivacy(_ context.Context, _ string, _ string, _ string) string { return "" }

// tempUnscheduleGoogleConfigError 因 Google 配置错误临时不可调度；平台已移除后为空操作。
func tempUnscheduleGoogleConfigError(_ ...any) {}

// tempUnscheduleEmptyResponse 因空响应临时不可调度；平台已移除后为空操作。
func tempUnscheduleEmptyResponse(_ ...any) {}

// antigravityForceTokenRefreshExtra 返回强制刷新标记的 extra 键；平台已移除后恒为空。
func antigravityForceTokenRefreshExtra(_ string) map[string]any { return nil }

// builtinGeminiCacheStoragePrice 返回内置缓存存储价格；平台已移除后返回 0。
func builtinGeminiCacheStoragePrice(_ string) float64 { return 0 }

// mapAntigravityModel 映射模型名；平台已移除后原样返回。参数顺序为 (account, model)。
func mapAntigravityModel(_ *Account, model string) string { return model }

// geminiModelClassFromName 返回模型分类；平台已移除后仅返回零值。
func (s *RateLimitService) geminiModelClassFromName(_ string) geminiModelClass { return "" }

// antigravityTokenInfo 是 token 刷新结果占位。
type antigravityTokenInfo struct {
	ProjectIDMissing bool
}

// grokBillingExtraKey 是 Grok 计费的 extra 键。
const grokBillingExtraKey = "grok_billing"

// grokQuotaSnapshotExtraKey 是 Grok 配额快照的 extra 键。
const grokQuotaSnapshotExtraKey = "grok_quota_snapshot"

// maxInt 返回两个整数中的较大值。
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// grokObservedModelsExtraKey 是 Grok 观测模型的 extra 键。
const grokObservedModelsExtraKey = "grok_observed_models"

// ---- Grok 平台桩实现 ----
//
// Grok 的 30 个实现文件已删除。共享链路（账号测试、上游模型同步、计费探针）
// 仍引用其类型与辅助函数，这里统一补为「平台已移除」语义。

var errGrokRemoved = errors.New("grok platform has been removed")

// Grok 相关类型。
type (
	GrokMediaEndpoint       string
	GrokModelDiagnostics    struct{}
	GrokOAuthReconciler     interface{}
	GrokPasswordLoginResult struct{}
	GrokTokenInfo           struct{}
)

// Grok 相关常量。
const (
	DefaultGrokRealtimeDialTimeout = 10 * time.Second

	GrokMediaEndpointImagesEdits       GrokMediaEndpoint = "images/edits"
	GrokMediaEndpointImagesGenerations GrokMediaEndpoint = "images/generations"
	GrokMediaEndpointVideoContent      GrokMediaEndpoint = "video/content"
	GrokMediaEndpointVideoStatus       GrokMediaEndpoint = "video/status"
	GrokMediaEndpointVideosGenerations GrokMediaEndpoint = "videos/generations"
	GrokMediaEndpointVideosEdits       GrokMediaEndpoint = "videos/edits"
	GrokMediaEndpointVideosExtensions  GrokMediaEndpoint = "videos/extensions"
)

// Grok 相关变量。
var (
	errGrokCatalogEmpty             = errors.New("grok catalog empty")
	errGrokOAuthRefreshTokenMissing = errors.New("grok oauth refresh token missing")
	openaiGrokFreeQuotaGateCache    any
	grokDefaultResponsesModel       = ""
	grokConversationIDHeader        = "X-Grok-Conversation-Id"
	grokFreeUsageProbeCooldown      = time.Duration(0)
	grokStreamIdleCooldown          = time.Duration(0)

	grokResponsesClientToolMappingContextKey = "grok_responses_client_tool_mapping"
)

// isSuccessfulGrokRateLimitRecovery 判断是否为成功的限流恢复；平台已移除。
func isSuccessfulGrokRateLimitRecovery(_ *Account, _ any) bool { return false }

// grokCatalogStatusConfiguredFallback 返回目录状态；平台已移除后返回空。
const grokCatalogStatusConfiguredFallback = "configured_fallback"

// grokChatRawEndpoint 返回 Chat 端点；平台已移除后返回空。
const grokChatRawEndpoint = ""

// grokChatResponsesBridgeEligibility 判断 Responses 桥接是否可用；平台已移除。
func grokChatResponsesBridgeEligibility(_ ...any) (bool, string) { return false, "" }

// grokContentPolicyClientMessage 是内容策略拒绝的用户文案。
func grokContentPolicyClientMessage(_ ...any) string { return "grok platform has been removed" }

// grokOAuthRequestAccountEligibilityError 返回账号可用性错误；平台已移除。
func grokOAuthRequestAccountEligibilityError(_ *Account) error { return errGrokRemoved }

// isGrokContentPolicyRejection 判断是否为内容策略拒绝；平台已移除。
func isGrokContentPolicyRejection(_ int, _ []byte) bool { return false }

// isGrokInvalidEncryptedContentResponse 判断是否为无效加密内容响应；平台已移除。
func isGrokInvalidEncryptedContentResponse(_ ...any) bool { return false }

// isGrokModelQuotaBlocked 判断模型是否被配额阻塞；平台已移除。
func isGrokModelQuotaBlocked(_ ...any) bool { return false }

// isGrokRequestContext 判断上下文是否来自 Grok 请求；平台已移除。
func isGrokRequestContext(_ context.Context) bool { return false }

// isGrokSpendingLimitError 判断是否为消费限额错误；平台已移除。
func isGrokSpendingLimitError(_ []byte) bool { return false }

// isGrokTeamModelRateLimited 判断团队模型是否限流；平台已移除。
func isGrokTeamModelRateLimited(_ ...any) bool { return false }

// markGrokEncryptedContentStripRetried 标记已重试；平台已移除后为空操作。
func markGrokEncryptedContentStripRetried(ctx context.Context) context.Context { return ctx }

// normalizeGrokChatReasoningEffort 归一化推理档位；平台已移除后原样返回。
func normalizeGrokChatReasoningEffort(body []byte, _ ...any) ([]byte, error) { return body, nil }

// normalizeGrokExhaustedWindowResets 归一化重置时刻；平台已移除后为空操作。
func normalizeGrokExhaustedWindowResets(_ any, _ time.Time, _ time.Time) {}

// normalizeGrokMediaEligibilityExtra 归一化媒体资格 extra；平台已移除后原样返回。
func normalizeGrokMediaEligibilityExtra(_ string, extra map[string]any) (map[string]any, error) {
	return extra, nil
}

// normalizeGrokMediaEligibilityUpdateExtra 归一化媒体资格更新 extra。
func normalizeGrokMediaEligibilityUpdateExtra(_ ...any) (map[string]any, error) {
	return nil, nil
}

// patchGrokResponsesBody 改写 Responses 请求体；平台已移除后原样返回。
func patchGrokResponsesBody(body []byte, _ ...any) ([]byte, error) { return body, nil }

// persistGrokRateLimit 持久化限流状态；平台已移除后为空操作。
func persistGrokRateLimit(_ context.Context, _ any, _ *Account, _ time.Time) {}

// requestHasGrokEncryptedReasoning 判断请求是否含加密推理；平台已移除。
func requestHasGrokEncryptedReasoning(_ []byte) bool { return false }

// resolveGrokCacheIdentity 解析缓存标识；平台已移除后返回空。
func resolveGrokCacheIdentity(_ ...any) string { return "" }

// resolveGrokStreamIdleTimeout 解析流空闲超时；平台已移除后返回 0。
func resolveGrokStreamIdleTimeout(_ ...any) time.Duration { return 0 }

// restoreGrokResponsesClientToolPayload 恢复客户端工具负载；平台已移除后原样返回。
func restoreGrokResponsesClientToolPayload(_ *gin.Context, body []byte) ([]byte, error) {
	return body, nil
}

// sanitizeGrokResponsesInput 清洗 Responses 输入；平台已移除后原样返回。
func sanitizeGrokResponsesInput(body []byte) ([]byte, error) { return body, nil }

// sanitizeGrokUnsupportedFields 清洗不支持字段；平台已移除后原样返回。
func sanitizeGrokUnsupportedFields(body []byte) ([]byte, error) { return body, nil }

// stripGrokChatPromptCacheKey 移除 prompt cache key；平台已移除后原样返回。
func stripGrokChatPromptCacheKey(body []byte) ([]byte, error) { return body, nil }

// trimGrokInvalidEncryptedContentRetryBody 裁剪重试请求体；平台已移除后原样返回。
func trimGrokInvalidEncryptedContentRetryBody(body []byte, _ ...any) ([]byte, bool, error) {
	return body, false, nil
}

// withGrokTeamRateLimitModel 附加团队限流模型；平台已移除后原样返回。
func withGrokTeamRateLimitModel(ctx context.Context, _ string) context.Context { return ctx }

// convertGrokResponseToOpenAICompact 转换为紧凑格式；平台已移除后原样返回。
func convertGrokResponseToOpenAICompact(body []byte, _ ...any) ([]byte, error) { return body, nil }

// countGrokNativeSearchCallsFromJSONBytes 统计原生搜索调用；平台已移除后返回 0。
func countGrokNativeSearchCallsFromJSONBytes(_ []byte) int { return 0 }

// countGrokNativeSearchCallsFromSSEBody 统计 SSE 搜索调用；平台已移除后返回 0。
func countGrokNativeSearchCallsFromSSEBody(_ string) int { return 0 }

// countGrokNativeSearchCallsInSSEDataDedup 统计去重后的搜索调用；平台已移除后返回 0。
func countGrokNativeSearchCallsInSSEDataDedup(_ ...any) int { return 0 }

// defaultGrokUpstreamUserAgent 返回默认 UA；平台已移除后返回空。
func defaultGrokUpstreamUserAgent() string { return "" }

// extractClaudeCodeSessionIDFromPayload 提取会话 ID；平台已移除后返回空。
func extractClaudeCodeSessionIDFromPayload(_ []byte) string { return "" }

// filterGrokFreeQuotaAccountsCore 过滤免费配额账号；平台已移除后返回空。
func filterGrokFreeQuotaAccountsCore(_ ...any) []Account { return nil }

// filterGrokModelQuotaBlockedAccounts 过滤被配额阻塞的账号；平台已移除后返回原列表。
func filterGrokModelQuotaBlockedAccounts(accounts []Account, _ ...any) []Account { return accounts }

// filterGrokTeamModelRateLimitedAccounts 过滤团队限流账号；平台已移除后返回原列表。
func filterGrokTeamModelRateLimitedAccounts(accounts []Account, _ ...any) []Account { return accounts }

// hasGrokResponsesToolIntent 判断是否有工具意图；平台已移除。
func hasGrokResponsesToolIntent(_ []byte) bool { return false }

// newGrokResponsesClientToolStreamBody 构造流式响应体；平台已移除后返回 nil。
func newGrokResponsesClientToolStreamBody(_ ...any) io.ReadCloser { return nil }

// newResponsesClientToolStreamBody 构造流式响应体；平台已移除后返回 nil。
func newResponsesClientToolStreamBody(_ ...any) io.ReadCloser { return nil }

// applyGrokFreeMessagesFunctionToolCacheRoute 应用免费工具缓存路由；平台已移除后返回 false。
func applyGrokFreeMessagesFunctionToolCacheRoute(body []byte, _ ...any) ([]byte, error) {
	return body, nil
}

// applyGrokFreeRequestToolCacheRoute 应用免费请求工具缓存路由；平台已移除后返回 false。
func applyGrokFreeRequestToolCacheRoute(body any, _ ...any) ([]byte, error) {
	return nil, nil
}

// applyGrokResponsesCacheIdentity 应用缓存标识；平台已移除后原样返回。
func applyGrokResponsesCacheIdentity(body []byte, _ ...any) ([]byte, error) { return body, nil }

// buildGrokResponsesURL 构造 Responses 端点；平台已移除后返回空。
func buildGrokResponsesURL(_ ...any) (string, error) { return "", errGrokRemoved }

// buildGrokResponsesRequest 构造 Responses 请求；平台已移除后返回「平台已移除」。
func buildGrokResponsesRequest(_ ...any) (*http.Request, error) { return nil, errGrokRemoved }

// claudeCodeSessionHeader 是 Claude Code 会话头名称。
const claudeCodeSessionHeader = "X-Claude-Code-Session-Id"

// GrokFailureFreeUsage / GrokFailureBilling 是失败分类枚举值。
const (
	GrokFailureFreeUsage = "free_usage"
	GrokFailureBilling   = "billing"
)

// grokFailureDecision 是上游失败分类结果。
type grokFailureDecision struct {
	Class  string
	Reason string
}

// filterGrokFreeQuotaAccountsForGateway 过滤免费配额账号；平台已移除后返回原列表。
func (s *GatewayService) filterGrokFreeQuotaAccountsForGateway(_ context.Context, accounts []Account) []Account {
	return accounts
}

// filterGrokFreeQuotaAccounts 过滤免费配额账号；平台已移除后返回原列表。
func (s *defaultOpenAIAccountScheduler) filterGrokFreeQuotaAccounts(_ context.Context, accounts []Account) []Account {
	return accounts
}

// shouldFailoverGrokUpstreamError 判断是否应该切换账号；平台已移除后恒为 false。
func (s *OpenAIGatewayService) shouldFailoverGrokUpstreamError(_ ...any) bool { return false }

// handleGrokAccountUpstreamError 处理上游错误；平台已移除后为空操作。
func (s *OpenAIGatewayService) handleGrokAccountUpstreamError(_ ...any) {}

// forwardGrokChatCompletionsViaResponses 经 Responses 转发 Chat 请求；平台已移除。
func (s *OpenAIGatewayService) forwardGrokChatCompletionsViaResponses(_ ...any) (*OpenAIForwardResult, error) {
	return nil, errGrokRemoved
}

// grokSameAccountRetryMetadata 返回同账号重试元数据；平台已移除后返回空。
func grokSameAccountRetryMetadata(_ ...any) (bool, time.Duration, time.Time, int) {
	return false, 0, time.Time{}, 0
}

// updateGrokUsageFromResponse 更新用量；平台已移除后为空操作。
func (s *OpenAIGatewayService) updateGrokUsageFromResponse(_ ...any) {}

// forwardGrokResponses 转发 Responses 请求；平台已移除后返回「平台已移除」。
func (s *OpenAIGatewayService) forwardGrokResponses(_ ...any) (*OpenAIForwardResult, error) {
	return nil, errGrokRemoved
}

// grokEncryptedContentStripRetried 判断是否已重试剥离；平台已移除后恒为 false。
func grokEncryptedContentStripRetried(_ context.Context) bool { return false }

// grokStreamIdleFailoverError 返回流空闲切换错误；平台已移除后返回「平台已移除」。
func grokStreamIdleFailoverError(_ ...any) error { return errGrokRemoved }

// tempUnscheduleGrok 临时不可调度；平台已移除后为空操作。
func (s *OpenAIGatewayService) tempUnscheduleGrok(_ ...any) {}

// deriveQuotaCheckResult 从配额快照推导检测结果。
func deriveQuotaCheckResult(snapshot *domain.MonitorQuotaSnapshot, model string, now time.Time) *CheckResult {
	return &CheckResult{Model: model}
}

// quotaErrorSnapshot 构造带错误的配额快照。
func quotaErrorSnapshot(kind, message string, now time.Time) *domain.MonitorQuotaSnapshot {
	return &domain.MonitorQuotaSnapshot{}
}

// upstreamRoutingModel 返回上游路由模型；平台已移除后原样返回请求模型。
func upstreamRoutingModel(account *Account, body []byte, _ ...any) string { return "" }

// resolveModelRoutedProtocol 解析按模型分流的协议；平台已移除后返回空。
func (a *Account) resolveModelRoutedProtocol(_ string) string { return "" }

// resolveUpstreamProtocol 解析账号的上游协议。
//
// 移除其它平台后只剩 WorkBuddy 两个站点：它们统一使用流式
// Chat Completions（/v2/chat/completions），没有 Anthropic / Responses
// 端点。返回空串会让调用方落入默认分支并走到 Anthropic 直通路径，
// 请求被打到不存在的 /v1/messages 上，表现为上游 502。
//
// 保留可变参数签名：调用方仍按 (account, inbound, model, catalog) 传入，
// 这里只关心平台。
func resolveUpstreamProtocol(args ...any) string {
	for _, a := range args {
		if acc, ok := a.(*Account); ok && acc != nil {
			if isWorkBuddyPlatform(acc.Platform) {
				return APIProtocolChatCompletions
			}
			return APIProtocolChatCompletions
		}
	}
	return APIProtocolChatCompletions
}

// convertResponsesShapedChatBody 转换 Responses 形状的请求体。
func (s *OpenAIGatewayService) convertResponsesShapedChatBody(body []byte, _ ...any) ([]byte, error) {
	return body, nil
}

// normalizeOpenCodeGoModelID 归一化模型 ID；平台已移除后原样返回。
func normalizeOpenCodeGoModelID(model string) string { return model }

// scheduleOpenCodeGoUsageActivity 记录用量活动；平台已移除后为空操作。
func scheduleOpenCodeGoUsageActivity(_ ...any) {}

// ProtocolRule 是协议分流规则；多协议平台移除后仅保留类型定义供配置结构引用。
type ProtocolRule struct {
	MatchField string
	MatchOp    string
	MatchValue string
	Protocol   string
	Protocols  []string
}

// applyGrokCLIHeaders 注入 Grok CLI 请求头；平台已移除后为空操作。
func applyGrokCLIHeaders(_ http.Header) {}
