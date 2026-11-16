package handler

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DeltrivX/Auto2API/internal/config"
	"github.com/DeltrivX/Auto2API/internal/domain"
	"github.com/DeltrivX/Auto2API/internal/pkg/claude"
	pkgerrors "github.com/DeltrivX/Auto2API/internal/pkg/errors"
	"github.com/DeltrivX/Auto2API/internal/pkg/ip"
	"github.com/DeltrivX/Auto2API/internal/pkg/logger"
	"github.com/DeltrivX/Auto2API/internal/pkg/openai"
	"github.com/DeltrivX/Auto2API/internal/pkg/timezone"
	"github.com/DeltrivX/Auto2API/internal/securityaudit"
	middleware2 "github.com/DeltrivX/Auto2API/internal/server/middleware"
	"github.com/DeltrivX/Auto2API/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

const gatewayCompatibilityMetricsLogInterval = 1024

var gatewayCompatibilityMetricsLogCounter atomic.Uint64

// GatewayHandler handles API gateway requests
type GatewayHandler struct {
	gatewayService           *service.GatewayService
	openAIGatewayService     *service.OpenAIGatewayService
	userService              *service.UserService
	billingCacheService      *service.BillingCacheService
	usageService             *service.UsageService
	apiKeyService            *service.APIKeyService
	usageRecordWorkerPool    *service.UsageRecordWorkerPool
	errorPassthroughService  *service.ErrorPassthroughService
	contentModerationService *service.ContentModerationService
	securityAuditCoordinator *securityaudit.Coordinator
	// geminiCachedContentService 管理 Gemini 显式缓存；为 nil 时相关接口不可用。
	concurrencyHelper        *ConcurrencyHelper
	userMsgQueueHelper       *UserMsgQueueHelper
	maxAccountSwitches       int
	maxAccountSwitchesGemini int
	cfg                      *config.Config
	settingService           *service.SettingService
}

// NewGatewayHandler creates a new GatewayHandler
func NewGatewayHandler(
	gatewayService *service.GatewayService,
	openAIGatewayService *service.OpenAIGatewayService,

	userService *service.UserService,
	concurrencyService *service.ConcurrencyService,
	billingCacheService *service.BillingCacheService,
	usageService *service.UsageService,
	apiKeyService *service.APIKeyService,
	usageRecordWorkerPool *service.UsageRecordWorkerPool,
	errorPassthroughService *service.ErrorPassthroughService,
	contentModerationService *service.ContentModerationService,
	userMsgQueueService *service.UserMessageQueueService,
	cfg *config.Config,
	settingService *service.SettingService,
) *GatewayHandler {
	pingInterval := time.Duration(0)
	maxAccountSwitches := 10
	maxAccountSwitchesGemini := 3
	if cfg != nil {
		pingInterval = time.Duration(cfg.Concurrency.PingInterval) * time.Second
		if cfg.Gateway.MaxAccountSwitches > 0 {
			maxAccountSwitches = cfg.Gateway.MaxAccountSwitches
		}
		if cfg.Gateway.MaxAccountSwitchesGemini > 0 {
			maxAccountSwitchesGemini = cfg.Gateway.MaxAccountSwitchesGemini
		}
	}

	// 初始化用户消息串行队列 helper
	var umqHelper *UserMsgQueueHelper
	if userMsgQueueService != nil && cfg != nil {
		umqHelper = NewUserMsgQueueHelper(userMsgQueueService, SSEPingFormatClaude, pingInterval)
	}

	return &GatewayHandler{
		gatewayService:           gatewayService,
		openAIGatewayService:     openAIGatewayService,
		userService:              userService,
		billingCacheService:      billingCacheService,
		usageService:             usageService,
		apiKeyService:            apiKeyService,
		usageRecordWorkerPool:    usageRecordWorkerPool,
		errorPassthroughService:  errorPassthroughService,
		contentModerationService: contentModerationService,
		concurrencyHelper:        NewConcurrencyHelper(concurrencyService, SSEPingFormatClaude, pingInterval),
		userMsgQueueHelper:       umqHelper,
		maxAccountSwitches:       maxAccountSwitches,
		maxAccountSwitchesGemini: maxAccountSwitchesGemini,
		cfg:                      cfg,
		settingService:           settingService,
	}
}

// Models lists visible models, or retrieves the exact list entry for a model path parameter.
// GET /v1/models and /v1/models/:model (also exposed through root aliases)
// Returns models based on account configurations (model_mapping whitelist)
// Falls back to default models if no whitelist is configured
func (h *GatewayHandler) Models(c *gin.Context) {
	apiKey, _ := middleware2.GetAPIKeyFromContext(c)

	var groupID *int64
	var platform string

	if apiKey != nil && apiKey.Group != nil {
		groupID = &apiKey.Group.ID
		platform = apiKey.Group.Platform
	}
	if forcedPlatform, ok := middleware2.GetForcePlatformFromContext(c); ok && strings.TrimSpace(forcedPlatform) != "" {
		platform = forcedPlatform
	}

	if platform == service.PlatformOpenAI && apiKey != nil && apiKey.Group != nil &&
		apiKey.Group.Platform == service.PlatformOpenAI && apiKey.Group.CodexModelsManifestConfig.Enabled {
		h.pinnedOpenAIModels(c, apiKey.Group)
		return
	}

	if platform == service.PlatformComposite {
		availableModels := h.compositeAvailableModels(c.Request.Context(), groupID, "", true)
		if apiKey != nil && apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
			source := availableModels
			if len(source) == 0 {
				source = defaultModelIDsForPlatform(service.PlatformComposite)
			}
			writeAllowlistedModelsList(c, service.PlatformComposite, apiKey.Group.ModelAllowlist.FilterForListing(source))
			return
		}
		if len(availableModels) > 0 {
			writeModelsList(c, service.PlatformComposite, availableModels)
			return
		}
		writeModelsList(c, service.PlatformComposite, defaultModelIDsForPlatform(service.PlatformComposite))
		return
	}

	// Get available models from account configurations for the selected group platform.
	availableModels := h.gatewayService.GetAvailableModels(c.Request.Context(), groupID, platform)
	if apiKey != nil && apiKey.Group != nil && apiKey.Group.ModelAllowlistEnabled() {
		source := modelListingSource(platform, availableModels, defaultModelIDsForPlatform(platform))
		writeAllowlistedModelsList(c, platform, apiKey.Group.ModelAllowlist.FilterForListing(source))
		return
	}

	if len(availableModels) > 0 || (platform == service.PlatformOpenAI && availableModels != nil) {
		writeModelsList(c, platform, availableModels)
		return
	}

	// Fallback to default models
	if platform == service.PlatformOpenAI {
		writeModelsListResponse(c, openai.DefaultModels)
		return
	}

	writeModelsListResponse(c, claude.DefaultModels)
}

// CodexModels returns the effective group model list using the manifest shape
// expected by Codex custom providers. Official OpenAI groups continue to use
// OpenAIGatewayHandler.CodexModels so their live upstream metadata is preserved.
func (h *GatewayHandler) CodexModels(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok || apiKey == nil || apiKey.Group == nil {
		h.errorResponse(c, http.StatusUnauthorized, "invalid_request_error", "API key group is required")
		return
	}

	forcedPlatform := ""
	if value, exists := middleware2.GetForcePlatformFromContext(c); exists {
		forcedPlatform = strings.TrimSpace(value)
	}
	modelIDs := h.codexModelIDsForGroup(c.Request.Context(), apiKey.Group, forcedPlatform)
	modelIDs = service.FilterCodexModelIDsForGroup(modelIDs, apiKey.Group)
	body, err := h.gatewayService.BuildCodexModelsManifestForGroup(
		c.Request.Context(),
		apiKey.Group,
		forcedPlatform,
		modelIDs,
	)
	if err != nil {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "Failed to build Codex models manifest")
		return
	}
	etag := service.CodexModelsManifestETag(body)
	c.Header("ETag", etag)
	if service.CodexModelsManifestETagMatches(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		c.Writer.WriteHeaderNow()
		return
	}
	c.Data(http.StatusOK, "application/json", body)
}

func (h *GatewayHandler) codexModelIDsForGroup(ctx context.Context, group *service.Group, platformOverride string) []string {
	if h == nil || h.gatewayService == nil || group == nil {
		return nil
	}

	groupID := &group.ID
	platform := strings.TrimSpace(platformOverride)
	if platform == "" {
		platform = group.Platform
	}
	if platform == service.PlatformComposite {
		availableModels := h.compositeAvailableModels(ctx, groupID, service.CompositeRouteEndpointResponses, false)
		fallbackModels := defaultCodexModelIDsForPlatform(service.PlatformComposite)
		models := availableModels
		if len(models) == 0 {
			models = fallbackModels
		}
		if group.ModelAllowlistEnabled() {
			models = group.ModelAllowlist.FilterForListing(models)
		}
		if filtered, err := h.gatewayService.FilterCompositeCodexModels(ctx, group.ID, models); err == nil {
			return filtered
		}
		return models
	}

	availableModels := h.gatewayService.GetAvailableModels(ctx, groupID, platform)
	fallbackModels := defaultCodexModelIDsForPlatform(platform)
	if group.ModelAllowlistEnabled() {
		return group.ModelAllowlist.FilterForListing(modelListingSource(platform, availableModels, fallbackModels))
	}
	if len(availableModels) > 0 {
		return availableModels
	}
	return fallbackModels
}

// compositeAvailableModels lists the models the composite group can serve.
// includeSystemOne adds TypeSafe models, which only work through /v1/systemone;
// LLM client catalogs (Codex) must exclude them.
func (h *GatewayHandler) compositeAvailableModels(ctx context.Context, groupID *int64, endpoint string, includeSystemOne bool) []string {
	if h == nil || h.gatewayService == nil {
		return nil
	}
	seen := make(map[string]struct{})
	models := make([]string, 0)
	schedulablePlatforms := h.gatewayService.GetSchedulablePlatforms(ctx, groupID)
	for _, platform := range domain.CompositePrecedencePlatformIDs() {
		platformModels := h.gatewayService.GetAvailableModels(ctx, groupID, platform)
		if len(platformModels) == 0 && (platform != service.PlatformOpenAI || platformModels == nil) {
			// CN 供应商没有静态默认模型列表（defaultModelIDsForPlatform 的
			// default 分支是 Claude 列表），composite 下只暴露账号映射键。
			if _, ok := schedulablePlatforms[platform]; ok && !service.IsMultiProtocolAPIKeyProvider(platform) {
				platformModels = defaultModelIDsForPlatform(platform)
			}
		}
		for _, model := range platformModels {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			models = append(models, model)
		}
	}
	// A route can expose a public ID that no account model mapping contains.
	// On lookup failure, retain the existing account-derived catalog only.
	if routeModels, err := h.gatewayService.GetCompositeRouteModels(ctx, groupID, endpoint, includeSystemOne); err == nil {
		for _, model := range routeModels {
			if _, ok := seen[model]; !ok {
				seen[model] = struct{}{}
				models = append(models, model)
			}
		}
	}
	return models
}

func writeModelsList(c *gin.Context, platform string, modelIDs []string) {
	if platform == service.PlatformOpenAI {
		writeOpenAIModelsList(c, modelIDs)
		return
	}
	models := make([]claude.Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		models = append(models, claude.Model{
			ID:          modelID,
			Type:        "model",
			DisplayName: modelID,
			CreatedAt:   "2024-01-01T00:00:00Z",
		})
	}
	writeModelsListResponse(c, models)
}

func writeAllowlistedModelsList(c *gin.Context, platform string, modelIDs []string) {
	if platform == service.PlatformOpenAI {
		writeOpenAIModelsList(c, modelIDs)
		return
	}
	writeModelsList(c, platform, modelIDs)
}

type grokReasoningEffortOption struct {
	Value   string `json:"value"`
	Label   string `json:"label"`
	Default bool   `json:"default,omitempty"`
}

func grokModelSupportsConfigurableReasoning(modelID string) bool {
	switch strings.ToLower(strings.TrimSpace(modelID)) {
	case "grok-4.7", "grok-4.7-latest", "grok-4.6", "grok-4.6-latest", "grok-4.5", "grok-4.5-latest", "grok", "grok-latest", "grok-build", "grok-build-latest", "grok-build-0.1":
		return true
	default:
		return false
	}
}

func writeOpenAIModelsList(c *gin.Context, modelIDs []string) {
	defaultsByID := make(map[string]openai.Model, len(openai.DefaultModels))
	for _, model := range openai.DefaultModels {
		defaultsByID[model.ID] = model
	}

	models := make([]openai.Model, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		if model, ok := defaultsByID[modelID]; ok {
			models = append(models, model)
			continue
		}
		models = append(models, openai.Model{
			ID:          modelID,
			Object:      "model",
			Created:     1704067200,
			OwnedBy:     "openai",
			Type:        "model",
			DisplayName: modelID,
		})
	}
	writeModelsListResponse(c, models)
}

// modelListingSource 汇总模型列表过滤的候选来源：账号映射键（availableModels）
// 与平台默认列表（fallbackModels）。账号映射为空时回落默认列表；Anthropic
// 平台两者取并集，其余平台以账号映射键为准。
func modelListingSource(platform string, availableModels, fallbackModels []string) []string {
	if platform == service.PlatformOpenAI && availableModels != nil {
		return availableModels
	}
	if len(availableModels) == 0 {
		return fallbackModels
	}
	if platform == service.PlatformAnthropic {
		return mergeModelIDs(availableModels, fallbackModels)
	}
	return availableModels
}

// defaultCodexModelIDsForPlatform 返回 Codex 兼容端点下的默认模型。
//
// 不再按平台硬编码模型名：本分支接入的两个平台都以各自上游下发的
// 模型目录为准，没有「内置默认表」这种东西。
func defaultCodexModelIDsForPlatform(platform string) []string {
	return defaultModelIDsForPlatform(platform)
}

func defaultModelIDsForPlatform(platform string) []string {
	switch platform {
	case service.PlatformOpenAI:
		return openai.DefaultModelIDs()
	case service.PlatformAnthropic:
		return claude.DefaultModelIDs()
	case service.PlatformComposite:
		ids := make([]string, 0)
		seen := make(map[string]struct{})
		for _, concretePlatform := range domain.CompositePrecedencePlatformIDs() {
			for _, id := range defaultModelIDsForPlatform(concretePlatform) {
				if _, ok := seen[id]; ok {
					continue
				}
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
		return ids
	default:
		// ⚠ 不要在这里回退到 Claude 模型表。
		//
		// 早先 default 返回 claude.DefaultModels，于是每接一个新平台
		// （WorkBuddy、Marvis）都会先在模型列表里冒出一堆 claude-*——
		// 上游根本不认这些 id，连接测试必然失败。没有内置目录的平台
		// 就该返回空，让调用方去读账号自己的 model_mapping。
		return nil
	}
}

func mergeModelIDs(primary, secondary []string) []string {
	seen := make(map[string]struct{}, len(primary)+len(secondary))
	merged := make([]string, 0, len(primary)+len(secondary))
	for _, models := range [][]string{primary, secondary} {
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			if _, ok := seen[model]; ok {
				continue
			}
			seen[model] = struct{}{}
			merged = append(merged, model)
		}
	}
	return merged
}

func cloneAPIKeyWithGroup(apiKey *service.APIKey, group *service.Group) *service.APIKey {
	if apiKey == nil || group == nil {
		return apiKey
	}
	cloned := *apiKey
	groupID := group.ID
	cloned.GroupID = &groupID
	cloned.Group = group
	return &cloned
}

// Usage 返回 API Key 的配额与用量，供下游客户端（CC Switch 等）查询。
// GET /v1/usage
//
// Two modes:
//   - quota_limited: API Key 有配额或速率限制。返回 key 级限制与用量。
//   - unrestricted:  无 key 级限制。只返回可用性，**不包含余额**——
//     余额属于账号主权信息，不应随每个请求下发给第三方终端。
func (h *GatewayHandler) Usage(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	ctx := c.Request.Context()

	// 解析可选的日期范围参数（用于 model_stats 查询）
	startTime, endTime := h.parseUsageDateRange(c)
	days, ok := parseAPIKeyDailyUsageDays(c.DefaultQuery("days", ""))
	if !ok {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Invalid days, allowed range is 1-90")
		return
	}

	// Best-effort: 获取用量统计（按当前 API Key 过滤），失败不影响基础响应
	usageData := h.buildUsageData(ctx, apiKey.ID)
	dailyUsage := h.buildAPIKeyDailyUsage(c, subject.UserID, apiKey.ID, days)

	// Best-effort: 获取模型统计
	var modelStats any
	if h.usageService != nil {
		if stats, err := h.usageService.GetAPIKeyModelStats(ctx, apiKey.ID, startTime, endTime); err == nil && len(stats) > 0 {
			modelStats = stats
		}
	}

	// 判断模式: key 有总额度或速率限制 → quota_limited，否则 → unrestricted
	isQuotaLimited := apiKey.Quota > 0 || apiKey.HasRateLimits()

	if isQuotaLimited {
		h.usageQuotaLimited(c, ctx, apiKey, usageData, dailyUsage, modelStats)
		return
	}

	h.usageUnrestricted(c, ctx, apiKey, subject, usageData, dailyUsage, modelStats)
}

// parseUsageDateRange 解析 start_date / end_date query params，默认返回近 30 天范围
func (h *GatewayHandler) parseUsageDateRange(c *gin.Context) (time.Time, time.Time) {
	now := timezone.Now()
	endTime := now
	startTime := now.AddDate(0, 0, -30)

	if s := c.Query("start_date"); s != "" {
		if t, err := timezone.ParseInLocation("2006-01-02", s); err == nil {
			startTime = t
		}
	}
	if s := c.Query("end_date"); s != "" {
		if t, err := timezone.ParseInLocation("2006-01-02", s); err == nil {
			endTime = t.AddDate(0, 0, 1) // half-open range upper bound
		}
	}
	return startTime, endTime
}

// buildUsageData 构建 today/total 用量摘要
func (h *GatewayHandler) buildUsageData(ctx context.Context, apiKeyID int64) gin.H {
	if h.usageService == nil {
		return nil
	}
	dashStats, err := h.usageService.GetAPIKeyDashboardStats(ctx, apiKeyID)
	if err != nil || dashStats == nil {
		return nil
	}
	return gin.H{
		"today": gin.H{
			"requests":              dashStats.TodayRequests,
			"input_tokens":          dashStats.TodayInputTokens,
			"output_tokens":         dashStats.TodayOutputTokens,
			"cache_creation_tokens": dashStats.TodayCacheCreationTokens,
			"cache_read_tokens":     dashStats.TodayCacheReadTokens,
			"total_tokens":          dashStats.TodayTokens,
			"cost":                  dashStats.TodayCost,
			"actual_cost":           dashStats.TodayActualCost,
		},
		"total": gin.H{
			"requests":              dashStats.TotalRequests,
			"input_tokens":          dashStats.TotalInputTokens,
			"output_tokens":         dashStats.TotalOutputTokens,
			"cache_creation_tokens": dashStats.TotalCacheCreationTokens,
			"cache_read_tokens":     dashStats.TotalCacheReadTokens,
			"total_tokens":          dashStats.TotalTokens,
			"cost":                  dashStats.TotalCost,
			"actual_cost":           dashStats.TotalActualCost,
		},
		"average_duration_ms": dashStats.AverageDurationMs,
		"rpm":                 dashStats.Rpm,
		"tpm":                 dashStats.Tpm,
	}
}

func (h *GatewayHandler) buildAPIKeyDailyUsage(c *gin.Context, userID, apiKeyID int64, days int) any {
	if h.usageService == nil {
		return nil
	}
	startTime, endTime := apiKeyDailyUsageRange(days, c.Query("timezone"))
	stats, err := h.usageService.GetAPIKeyDailyUsage(c.Request.Context(), userID, apiKeyID, startTime, endTime)
	if err != nil {
		return nil
	}
	return stats
}

// usageQuotaLimited 处理 quota_limited 模式的响应
func (h *GatewayHandler) usageQuotaLimited(c *gin.Context, ctx context.Context, apiKey *service.APIKey, usageData gin.H, dailyUsage any, modelStats any) {
	resp := gin.H{
		"mode":    "quota_limited",
		"isValid": apiKey.Status == service.StatusAPIKeyActive || apiKey.Status == service.StatusAPIKeyQuotaExhausted || apiKey.Status == service.StatusAPIKeyExpired,
		"status":  apiKey.Status,
	}

	// 总额度信息
	if apiKey.Quota > 0 {
		remaining := apiKey.GetQuotaRemaining()
		resp["quota"] = gin.H{
			"limit":     apiKey.Quota,
			"used":      apiKey.QuotaUsed,
			"remaining": remaining,
			"unit":      "USD",
		}
		resp["remaining"] = remaining
		resp["unit"] = "USD"
	}

	// 速率限制信息（从 DB 获取实时用量）
	if apiKey.HasRateLimits() && h.apiKeyService != nil {
		rateLimitData, err := h.apiKeyService.GetRateLimitData(ctx, apiKey.ID)
		if err == nil && rateLimitData != nil {
			var rateLimits []gin.H
			if apiKey.RateLimit5h > 0 {
				used := rateLimitData.EffectiveUsage5h()
				entry := gin.H{
					"window":       "5h",
					"limit":        apiKey.RateLimit5h,
					"used":         used,
					"remaining":    max(0, apiKey.RateLimit5h-used),
					"window_start": rateLimitData.Window5hStart,
				}
				if rateLimitData.Window5hStart != nil && !service.IsWindowExpired(rateLimitData.Window5hStart, service.RateLimitWindow5h) {
					entry["reset_at"] = rateLimitData.Window5hStart.Add(service.RateLimitWindow5h)
				}
				rateLimits = append(rateLimits, entry)
			}
			if apiKey.RateLimit1d > 0 {
				used := rateLimitData.EffectiveUsage1d()
				entry := gin.H{
					"window":       "1d",
					"limit":        apiKey.RateLimit1d,
					"used":         used,
					"remaining":    max(0, apiKey.RateLimit1d-used),
					"window_start": rateLimitData.Window1dStart,
				}
				if rateLimitData.Window1dStart != nil && !service.IsWindowExpired(rateLimitData.Window1dStart, service.RateLimitWindow1d) {
					entry["reset_at"] = rateLimitData.Window1dStart.Add(service.RateLimitWindow1d)
				}
				rateLimits = append(rateLimits, entry)
			}
			if apiKey.RateLimit7d > 0 {
				used := rateLimitData.EffectiveUsage7d()
				entry := gin.H{
					"window":       "7d",
					"limit":        apiKey.RateLimit7d,
					"used":         used,
					"remaining":    max(0, apiKey.RateLimit7d-used),
					"window_start": rateLimitData.Window7dStart,
				}
				if rateLimitData.Window7dStart != nil && !service.IsWindowExpired(rateLimitData.Window7dStart, service.RateLimitWindow7d) {
					entry["reset_at"] = rateLimitData.Window7dStart.Add(service.RateLimitWindow7d)
				}
				rateLimits = append(rateLimits, entry)
			}
			if len(rateLimits) > 0 {
				resp["rate_limits"] = rateLimits
			}
		}
	}

	// 过期时间
	if apiKey.ExpiresAt != nil {
		resp["expires_at"] = apiKey.ExpiresAt
		resp["days_until_expiry"] = apiKey.GetDaysUntilExpiry()
	}

	if usageData != nil {
		resp["usage"] = usageData
	}
	if dailyUsage != nil {
		resp["daily_usage"] = dailyUsage
	}
	if modelStats != nil {
		resp["model_stats"] = modelStats
	}

	c.JSON(http.StatusOK, resp)
}

// usageUnrestricted 处理 unrestricted 模式的响应（向后兼容）
func (h *GatewayHandler) usageUnrestricted(c *gin.Context, ctx context.Context, apiKey *service.APIKey, subject middleware2.AuthSubject, usageData gin.H, dailyUsage any, modelStats any) {
	// 订阅模式
	if apiKey.Group != nil && apiKey.Group.IsSubscriptionType() {
		resp := gin.H{
			"mode":     "unrestricted",
			"isValid":  true,
			"planName": apiKey.Group.Name,
			"unit":     "USD",
		}

		// 订阅信息可能不在 context 中（/v1/usage 路径跳过了中间件的计费检查）
		subscription, ok := middleware2.GetSubscriptionFromContext(c)
		if ok {
			remaining := h.calculateSubscriptionRemaining(apiKey.Group, subscription)
			resp["remaining"] = remaining
			resp["subscription"] = gin.H{
				"daily_usage_credits":   subscription.DailyUsageCredits,
				"weekly_usage_credits":  subscription.WeeklyUsageCredits,
				"monthly_usage_credits": subscription.MonthlyUsageCredits,
				"daily_limit_credits":   apiKey.Group.DailyLimitCredits,
				"weekly_limit_credits":  apiKey.Group.WeeklyLimitCredits,
				"monthly_limit_credits": apiKey.Group.MonthlyLimitCredits,
				"weekly_window_start":   subscription.WeeklyWindowStart,
				"expires_at":            subscription.ExpiresAt,
			}
		}

		if usageData != nil {
			resp["usage"] = usageData
		}
		if dailyUsage != nil {
			resp["daily_usage"] = dailyUsage
		}
		if modelStats != nil {
			resp["model_stats"] = modelStats
		}
		c.JSON(http.StatusOK, resp)
		return
	}

	// 不向调用方（各类智能体客户端）暴露余额。
	//
	// /v1/usage 的消费者是 Codex / Claude Code 这类下游工具，它们只需要
	// 知道「这个 key 还能不能用」；把余额、套餐名回给客户端既无必要，
	// 也让额度信息随每个请求扩散到第三方终端。
	// 这里只保留可用性与配额窗口，余额字段一律不下发。
	resp := gin.H{
		"mode":    "unrestricted",
		"isValid": true,
		"unit":    "credits",
	}
	if usageData != nil {
		resp["usage"] = usageData
	}
	if dailyUsage != nil {
		resp["daily_usage"] = dailyUsage
	}
	if modelStats != nil {
		resp["model_stats"] = modelStats
	}
	c.JSON(http.StatusOK, resp)
}

// calculateSubscriptionRemaining 计算订阅剩余可用额度
// 逻辑：
// 1. 如果日/周/月任一限额达到100%，返回0
// 2. 否则返回所有已配置周期中剩余额度的最小值
func (h *GatewayHandler) calculateSubscriptionRemaining(group *service.Group, sub *service.UserSubscription) float64 {
	var remainingValues []float64

	// 检查日限额
	if group.HasDailyLimit() {
		remaining := *group.DailyLimitCredits - sub.DailyUsageCredits
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 检查周限额
	if group.HasWeeklyLimit() {
		remaining := *group.WeeklyLimitCredits - sub.WeeklyUsageCredits
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 检查月限额
	if group.HasMonthlyLimit() {
		remaining := *group.MonthlyLimitCredits - sub.MonthlyUsageCredits
		if remaining <= 0 {
			return 0
		}
		remainingValues = append(remainingValues, remaining)
	}

	// 如果没有配置任何限额，返回-1表示无限制
	if len(remainingValues) == 0 {
		return -1
	}

	// 返回最小值
	min := remainingValues[0]
	for _, v := range remainingValues[1:] {
		if v < min {
			min = v
		}
	}
	return min
}

// handleConcurrencyError handles concurrency-related acquire errors.
func (h *GatewayHandler) handleConcurrencyError(c *gin.Context, err error, slotType string, streamStarted bool) {
	status, errType, code, message := concurrencyErrorResponse(err, slotType)
	h.handleStreamingAwareErrorWithCode(c, status, errType, code, message, streamStarted)
}

func (h *GatewayHandler) handleFailoverExhausted(c *gin.Context, failoverErr *service.UpstreamFailoverError, platform string, streamStarted bool) {
	statusCode := failoverErr.StatusCode
	responseBody := failoverErr.ResponseBody
	if service.IsOpenAISilentRefusalErrorBody(responseBody) {
		service.SetOpsUpstreamError(c, statusCode, service.OpenAISilentRefusalClientMessage(), "")
		h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", service.OpenAISilentRefusalClientMessage(), streamStarted)
		return
	}

	// 先检查透传规则
	if h.errorPassthroughService != nil && len(responseBody) > 0 {
		if rule := h.errorPassthroughService.MatchRule(platform, statusCode, responseBody); rule != nil {
			// 确定响应状态码
			respCode := statusCode
			if !rule.PassthroughCode && rule.ResponseCode != nil {
				respCode = *rule.ResponseCode
			}

			// 确定响应消息
			msg := service.ExtractUpstreamErrorMessage(responseBody)
			if !rule.PassthroughBody && rule.CustomMessage != nil {
				msg = *rule.CustomMessage
			}

			if rule.SkipMonitoring {
				c.Set(service.OpsSkipPassthroughKey, true)
			}

			h.handleStreamingAwareError(c, respCode, "upstream_error", msg, streamStarted)
			return
		}
	}

	// 记录原始上游状态码，以便 ops 错误日志捕获真实的上游错误
	upstreamMsg := service.ExtractUpstreamErrorMessage(responseBody)
	service.SetOpsUpstreamError(c, statusCode, upstreamMsg, "")

	// 使用默认的错误映射
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

// handleFailoverExhaustedSimple 简化版本，用于没有响应体的情况
func (h *GatewayHandler) handleFailoverExhaustedSimple(c *gin.Context, statusCode int, streamStarted bool) {
	status, errType, errMsg := h.mapUpstreamError(statusCode)
	service.SetOpsUpstreamError(c, statusCode, errMsg, "")
	h.handleStreamingAwareError(c, status, errType, errMsg, streamStarted)
}

func (h *GatewayHandler) mapUpstreamError(statusCode int) (int, string, string) {
	switch statusCode {
	case 401:
		return http.StatusBadGateway, "upstream_error", "Upstream authentication failed, please contact administrator"
	case 403:
		return http.StatusBadGateway, "upstream_error", "Upstream access forbidden, please contact administrator"
	case 429:
		return http.StatusTooManyRequests, "rate_limit_error", "Upstream rate limit exceeded, please retry later"
	case 529:
		return http.StatusServiceUnavailable, "overloaded_error", "Upstream service overloaded, please retry later"
	case 500, 502, 503, 504:
		return http.StatusBadGateway, "upstream_error", "Upstream service temporarily unavailable"
	default:
		return http.StatusBadGateway, "upstream_error", "Upstream request failed"
	}
}

// handleStreamingAwareError handles errors that may occur after streaming has started
func (h *GatewayHandler) handleStreamingAwareError(c *gin.Context, status int, errType, message string, streamStarted bool) {
	h.handleStreamingAwareErrorWithCode(c, status, errType, "", message, streamStarted)
}

func (h *GatewayHandler) handleStreamingAwareErrorWithCode(c *gin.Context, status int, errType, code, message string, streamStarted bool) {
	if streamStarted {
		// 响应状态码已固化为 200（ping/部分数据已 flush），错误只能就地以 SSE 帧回传。
		// 标记本次流内错误，供 ops_error_logger 补记——否则该中间件按 status>=400 采集，
		// 这类挂在 200 流上的失败（如并发限流回退）不会进错误看板。
		service.MarkOpsStreamError(c, errType, message, status)

		// /v1/responses 的严格 SDK（Codex CLI）要求终止事件必须属于
		// response.completed/failed/incomplete/cancelled 集合。
		// Anthropic-backed Responses 路径同样会因为通用 error 帧被拒。
		if inboundIsResponses(c) {
			if writeResponsesFailedSSE(c, errType, code, message) {
				return
			}
		}
		// Stream already started, send error as SSE event then close
		flusher, ok := c.Writer.(http.Flusher)
		if ok {
			// SSE 错误事件固定 schema，使用 Quote 直拼可避免额外 Marshal 分配。
			errorCode := ""
			if code != "" {
				errorCode = `,"code":` + strconv.Quote(code)
			}
			errorEvent := `data: {"type":"error","error":{"type":` + strconv.Quote(errType) + errorCode + `,"message":` + strconv.Quote(message) + `}}` + "\n\n"
			if _, err := fmt.Fprint(c.Writer, errorEvent); err != nil {
				_ = c.Error(err)
			}
			flusher.Flush()
		}
		return
	}

	// Normal case: return JSON response with proper status code
	h.errorResponseWithCode(c, status, errType, code, message)
}

// ensureForwardErrorResponse 在 Forward 返回错误但尚未写响应时补写统一错误响应。
// Writer 已被写过时（ping 已 flush）走 streamStarted 分支，
// 让 handleStreamingAwareError 通过 SSE 发协议合规的终止事件，
// 否则下游收到的就是 silent EOF。
// 客户端已断开时不补写，响应未提交则标记 499（见 failoverClientGone）。
func (h *GatewayHandler) ensureForwardErrorResponse(c *gin.Context, streamStarted bool) bool {
	if c == nil || c.Writer == nil {
		return false
	}
	if c.Request != nil && errors.Is(c.Request.Context().Err(), context.Canceled) {
		failoverClientGone(c)
		return false
	}
	if service.IsResponseCommitted(c) {
		return false
	}
	if c.Writer.Written() {
		streamStarted = true
	}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "Upstream request failed", streamStarted)
	return true
}

// gatewayForwardErrorAlreadyCommunicated reports whether a Forward implementation
// has already written a complete error response to the client before returning
// an error to the handler.
//
// This is intentionally narrower than "writer size changed": a stream may have
// only emitted keepalive pings or partial data, in which case the handler still
// needs to append a protocol-level terminal error. Non-SSE output from Forward
// is different: service-level helpers such as handleErrorResponse/writeClaudeError
// already wrote the client-visible JSON body, so adding the generic streaming
// fallback would corrupt the response by appending a second `data: ...` frame.
func gatewayForwardErrorAlreadyCommunicated(c *gin.Context, writerSizeBeforeForward int, err error) bool {
	if err == nil || c == nil || c.Writer == nil {
		return false
	}
	if c.Writer.Size() == writerSizeBeforeForward {
		return false
	}

	contentType := strings.ToLower(strings.TrimSpace(c.Writer.Header().Get("Content-Type")))
	if contentType == "" {
		return false
	}
	return !strings.Contains(contentType, "text/event-stream")
}

// checkClaudeCodeVersion 检查 Claude Code 客户端版本是否满足版本要求
// 仅对已识别的 Claude Code 客户端执行，count_tokens 路径除外
func (h *GatewayHandler) checkClaudeCodeVersion(c *gin.Context) bool {
	ctx := c.Request.Context()
	if !service.IsClaudeCodeClient(ctx) {
		return true
	}

	// 排除 count_tokens 子路径
	if strings.HasSuffix(c.Request.URL.Path, "/count_tokens") {
		return true
	}

	minVersion, maxVersion := h.settingService.GetClaudeCodeVersionBounds(ctx)
	if minVersion == "" && maxVersion == "" {
		return true // 未设置，不检查
	}

	clientVersion := service.GetClaudeCodeVersion(ctx)
	if clientVersion == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			"Unable to determine Claude Code version. Please update Claude Code: npm update -g @anthropic-ai/claude-code")
		return false
	}

	if minVersion != "" && service.CompareVersions(clientVersion, minVersion) < 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Your Claude Code version (%s) is below the minimum required version (%s). Please update: npm update -g @anthropic-ai/claude-code",
				clientVersion, minVersion))
		return false
	}

	if maxVersion != "" && service.CompareVersions(clientVersion, maxVersion) > 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error",
			fmt.Sprintf("Your Claude Code version (%s) exceeds the maximum allowed version (%s). "+
				"Please downgrade: npm install -g @anthropic-ai/claude-code@%s && "+
				"set CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 to prevent auto-upgrade",
				clientVersion, maxVersion, maxVersion))
		return false
	}

	return true
}

// errorResponse 返回Claude API格式的错误响应
func (h *GatewayHandler) errorResponse(c *gin.Context, status int, errType, message string) {
	h.errorResponseWithCode(c, status, errType, "", message)
}

func (h *GatewayHandler) errorResponseWithCode(c *gin.Context, status int, errType, code, message string) {
	errorObject := gin.H{"type": errType, "message": message}
	if code != "" {
		errorObject["code"] = code
	}
	c.JSON(status, gin.H{
		"type":  "error",
		"error": errorObject,
	})
}

// CountTokens handles token counting endpoint
// POST /v1/messages/count_tokens
// 特点：校验订阅/余额，但不计算并发、不记录使用量
func (h *GatewayHandler) CountTokens(c *gin.Context) {
	// 从context获取apiKey和user（ApiKeyAuth中间件已设置）
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}

	_, ok = middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.gateway.count_tokens",
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)
	defer h.maybeLogCompatibilityFallbackMetrics(reqLog)

	// 读取请求体
	body, err := readLenientJSONRequestBodyWithPrealloc(c.Request, h.cfg)
	if err != nil {
		if handleRequestBodyReadError(c, reqLog, err) {
			return
		}
		status, message := requestBodyReadFailure(reqLog, c.Request, err)
		h.errorResponse(c, status, "invalid_request_error", message)
		return
	}

	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Request body is empty")
		return
	}

	setOpsRequestContext(c, "", false)

	bodyRef := service.NewRequestBodyRef(body)
	parsedReq, err := service.ParseGatewayRequest(bodyRef, domain.PlatformAnthropic)
	if err != nil {
		logRequestBodyParseFailure(reqLog, body, err)
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Failed to parse request body")
		return
	}
	body = parsedReq.Body.Bytes()
	// count_tokens 走 messages 严格校验时，复用已解析请求，避免二次反序列化。
	SetClaudeCodeClientContext(c, body, parsedReq)
	ensureCompositeTargetPlatform(c, apiKey, parsedReq.Model)
	reqLog = reqLog.With(zap.String("model", parsedReq.Model), zap.Bool("stream", parsedReq.Stream))
	// 在请求上下文中记录 thinking 状态，供 Antigravity 最终模型 key 推导/模型维度限流使用
	c.Request = c.Request.WithContext(service.WithThinkingEnabled(c.Request.Context(), parsedReq.ThinkingEnabled, h.metadataBridgeEnabled()))

	// 验证 model 必填
	if parsedReq.Model == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if !compositeTargetPlatformResolved(c, apiKey, parsedReq.Model) {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "Model is not supported by composite groups")
		return
	}

	setOpsRequestContext(c, parsedReq.Model, parsedReq.Stream)
	setOpsEndpointContext(c, "", int16(service.RequestTypeFromLegacy(parsedReq.Stream, false)))

	// 获取订阅信息（可能为nil）
	subscription, _ := middleware2.GetSubscriptionFromContext(c)

	// 校验 billing eligibility（订阅/余额）
	// 【注意】不计算并发，但需要校验订阅/余额
	if err := h.billingCacheService.CheckBillingEligibility(c.Request.Context(), apiKey.User, apiKey, apiKey.Group, subscription, service.QuotaPlatform(c.Request.Context(), apiKey)); err != nil {
		status, code, message, retryAfter := billingErrorDetails(err)
		if retryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(retryAfter))
		}
		h.errorResponse(c, status, code, message)
		return
	}

	// 计算粘性会话 hash
	parsedReq.SessionContext = &service.SessionContext{
		ClientIP:  ip.GetClientIP(c),
		UserAgent: c.GetHeader("User-Agent"),
		APIKeyID:  apiKey.ID,
	}
	sessionHash := h.gatewayService.GenerateSessionHash(parsedReq)

	// 选择支持该模型的账号
	account, err := h.gatewayService.SelectAccountForModel(c.Request.Context(), apiKey.GroupID, sessionHash, parsedReq.Model)
	if err != nil {
		reqLog.Warn("gateway.count_tokens_select_account_failed", zap.Error(err))
		cls := classifyNoAccountErrorFromGin(c, h.gatewayService, apiKey, parsedReq.Model, parsedReq.Model, service.PlatformAnthropic)
		if !cls.ModelNotFound {
			markOpsRoutingCapacityLimitedIfNoAvailable(c, err)
		}
		h.errorResponse(c, cls.Status, cls.ErrType, cls.Message)
		return
	}
	setOpsSelectedAccount(c, account.ID, account.Platform)

	// 转发请求（不记录使用量）
	if err := h.gatewayService.ForwardCountTokens(c.Request.Context(), c, account, parsedReq); err != nil {
		reqLog.Error("gateway.count_tokens_forward_failed", zap.Int64("account_id", account.ID), zap.Error(err))
		// 错误响应已在 ForwardCountTokens 中处理
		// 上游未服务该会话，立即释放选号时注册的会话槽（客户端可能已断开，用独立 ctx）
		h.gatewayService.ReleaseAccountSession(context.Background(), account, sessionHash)
		return
	}
}

// InterceptType 表示请求拦截类型
type InterceptType int

const (
	InterceptTypeNone              InterceptType = iota
	InterceptTypeWarmup                          // 预热请求（返回 "New Conversation"）
	InterceptTypeSuggestionMode                  // SUGGESTION MODE（返回空字符串）
	InterceptTypeMaxTokensOneHaiku               // max_tokens=1 + haiku 探测请求（返回 "#"）
)

// isHaikuModel 检查模型名称是否包含 "haiku"（大小写不敏感）
func isHaikuModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "haiku")
}

// isMaxTokensOneHaikuRequest 检查是否为 max_tokens=1 + haiku 模型的探测请求
// 这类请求用于 Claude Code 验证 API 连通性（流式/非流式均会出现，如 cc-switch v3.9.0 起的健康检查探测为流式）
// 条件：max_tokens == 1 且 model 包含 "haiku"
func isMaxTokensOneHaikuRequest(model string, maxTokens int) bool {
	return maxTokens == 1 && isHaikuModel(model)
}

// detectInterceptType 检测请求是否需要拦截，返回拦截类型
// 参数说明：
//   - body: 请求体字节
//   - model: 请求的模型名称
//   - maxTokens: max_tokens 值
//   - isClaudeCodeClient: 是否已通过 Claude Code 客户端校验
func detectInterceptType(body []byte, model string, maxTokens int, isClaudeCodeClient bool) InterceptType {
	// 优先检查 max_tokens=1 + haiku 探测请求（流式/非流式均适用）
	if isClaudeCodeClient && isMaxTokensOneHaikuRequest(model, maxTokens) {
		return InterceptTypeMaxTokensOneHaiku
	}

	// 快速检查：如果不包含任何关键字，直接返回
	bodyStr := string(body)
	hasSuggestionMode := strings.Contains(bodyStr, "[SUGGESTION MODE:")
	hasWarmupKeyword := strings.Contains(bodyStr, "title") || strings.Contains(bodyStr, "Warmup")

	if !hasSuggestionMode && !hasWarmupKeyword {
		return InterceptTypeNone
	}

	// 解析请求（只解析一次）
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"messages"`
		System []struct {
			Text string `json:"text"`
		} `json:"system"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return InterceptTypeNone
	}

	// 检查 SUGGESTION MODE（最后一条 user 消息）
	if hasSuggestionMode && len(req.Messages) > 0 {
		lastMsg := req.Messages[len(req.Messages)-1]
		if lastMsg.Role == "user" && len(lastMsg.Content) > 0 &&
			lastMsg.Content[0].Type == "text" &&
			strings.HasPrefix(lastMsg.Content[0].Text, "[SUGGESTION MODE:") {
			return InterceptTypeSuggestionMode
		}
	}

	// 检查 Warmup 请求
	if hasWarmupKeyword {
		// 检查 messages 中的标题提示模式
		for _, msg := range req.Messages {
			for _, content := range msg.Content {
				if content.Type == "text" {
					if strings.Contains(content.Text, "Please write a 5-10 word title for the following conversation:") ||
						content.Text == "Warmup" {
						return InterceptTypeWarmup
					}
				}
			}
		}
		// 检查 system 中的标题提取模式
		for _, sys := range req.System {
			if strings.Contains(sys.Text, "nalyze if this message indicates a new conversation topic. If it does, extract a 2-3 word title") {
				return InterceptTypeWarmup
			}
		}
	}

	return InterceptTypeNone
}

// sendMockInterceptStream 发送流式 mock 响应（用于请求拦截）
func sendMockInterceptStream(c *gin.Context, model string, interceptType InterceptType) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// 根据拦截类型决定响应内容
	var msgID string
	var outputTokens int
	var textDeltas []string

	switch interceptType {
	case InterceptTypeSuggestionMode:
		msgID = generateRealisticMsgID()
		outputTokens = 1
		textDeltas = []string{""} // 空内容
	default: // InterceptTypeWarmup
		msgID = generateRealisticMsgID()
		outputTokens = 2
		textDeltas = []string{"New", " Conversation"}
	}

	// Build message_start event — field order matches real Anthropic API response.
	messageStartJSON := `{"type":"message_start","message":{"model":` + strconv.Quote(model) + `,"id":` + strconv.Quote(msgID) + `,"type":"message","role":"assistant","content":[],"stop_reason":null,"stop_sequence":null,"stop_details":null,"usage":{"input_tokens":10,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":0}}}`

	// Build events
	events := []string{
		`event: message_start` + "\n" + `data: ` + string(messageStartJSON),
		`event: content_block_start` + "\n" + `data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
	}

	// Add text deltas
	for _, text := range textDeltas {
		deltaJSON := `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":` + strconv.Quote(text) + `}}`
		events = append(events, `event: content_block_delta`+"\n"+`data: `+string(deltaJSON))
	}

	// Add final events
	messageDeltaJSON := `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null,"stop_details":null},"usage":{"output_tokens":` + strconv.Itoa(outputTokens) + `}}`

	events = append(events,
		`event: content_block_stop`+"\n"+`data: {"index":0,"type":"content_block_stop"}`,
		`event: message_delta`+"\n"+`data: `+string(messageDeltaJSON),
		`event: message_stop`+"\n"+`data: {"type":"message_stop"}`,
	)

	for _, event := range events {
		_, _ = c.Writer.WriteString(event + "\n\n")
		c.Writer.Flush()
		time.Sleep(20 * time.Millisecond)
	}
}

// generateRealisticMsgID 生成仿真的消息 ID（msg_01XXXXXXX 格式）
// 格式与 Anthropic API 官方响应一致：msg_01 + 22 位 Base62 随机字符
func generateRealisticMsgID() string {
	const charset = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	const idLen = 22
	randomBytes := make([]byte, idLen)
	if _, err := rand.Read(randomBytes); err != nil {
		return fmt.Sprintf("msg_01%d", time.Now().UnixNano())
	}
	b := make([]byte, idLen)
	for i := range b {
		b[i] = charset[int(randomBytes[i])%len(charset)]
	}
	return "msg_01" + string(b)
}

// sendMockInterceptResponse 发送非流式 mock 响应（用于请求拦截）
func sendMockInterceptResponse(c *gin.Context, model string, interceptType InterceptType) {
	var msgID, text, stopReason string
	var outputTokens int

	switch interceptType {
	case InterceptTypeSuggestionMode:
		msgID = generateRealisticMsgID()
		text = ""
		outputTokens = 1
		stopReason = "end_turn"
	case InterceptTypeMaxTokensOneHaiku:
		msgID = generateRealisticMsgID()
		text = "#"
		outputTokens = 1
		stopReason = "max_tokens" // max_tokens=1 探测请求的 stop_reason 应为 max_tokens
	default: // InterceptTypeWarmup
		msgID = generateRealisticMsgID()
		text = "New Conversation"
		outputTokens = 2
		stopReason = "end_turn"
	}

	// 构建完整的响应格式（与 Anthropic API 官方响应格式一致）
	response := gin.H{
		"model":         model,
		"id":            msgID,
		"type":          "message",
		"role":          "assistant",
		"content":       []gin.H{{"type": "text", "text": text}},
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"stop_details":  nil,
		"usage": gin.H{
			"input_tokens":                10,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
			"cache_creation": gin.H{
				"ephemeral_5m_input_tokens": 0,
				"ephemeral_1h_input_tokens": 0,
			},
			"output_tokens": outputTokens,
		},
	}

	c.JSON(http.StatusOK, response)
}

// extractQuotaResetSeconds 从 quota 错误的 metadata 中提取 window_resets_at 并计算
// 距重置剩余秒数。fallback 路径必须返回 ≥1 秒，避免客户端立即重试无限循环。
func extractQuotaResetSeconds(err error) int {
	const fallback = 60
	appErr := pkgerrors.FromError(err)
	if appErr == nil {
		return fallback
	}
	raw, ok := appErr.Metadata["window_resets_at"]
	if !ok || raw == "" {
		return fallback
	}
	resetAt, parseErr := time.Parse(time.RFC3339, raw)
	if parseErr != nil {
		logger.L().With(
			zap.String("component", "handler.gateway.billing"),
			zap.String("raw", raw),
			zap.Error(parseErr),
		).Warn("quota.invalid_window_resets_at_format")
		return fallback
	}
	secs := time.Until(resetAt).Seconds()
	if secs <= 0 {
		// reset 时间已过：cache 与 DB 应该正在自愈，返回 fallback 让客户端按常规节奏退避，
		// 避免返回 1 秒导致客户端立即重试仍触发限额的退避循环。
		return fallback
	}
	return int(math.Ceil(secs))
}

func billingErrorDetails(err error) (status int, code, message string, retryAfter int) {
	if errors.Is(err, service.ErrBillingServiceUnavailable) {
		msg := pkgerrors.Message(err)
		if msg == "" {
			msg = "Billing service temporarily unavailable. Please retry later."
		}
		return http.StatusServiceUnavailable, "billing_service_error", msg, 0
	}
	if errors.Is(err, service.ErrAPIKeyRateLimit5hExceeded) {
		msg := pkgerrors.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	if errors.Is(err, service.ErrAPIKeyRateLimit1dExceeded) {
		msg := pkgerrors.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	if errors.Is(err, service.ErrAPIKeyRateLimit7dExceeded) {
		msg := pkgerrors.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, 0
	}
	// 用户/分组 RPM 超限统一映射为 HTTP 429；保留与其它 rate_limit 一致的错误码便于客户端分类。
	// 返回 Retry-After 秒数（当前分钟剩余秒数），让 SDK 自动退避。
	if errors.Is(err, service.ErrGroupRPMExceeded) || errors.Is(err, service.ErrUserRPMExceeded) {
		msg := pkgerrors.Message(err)
		retrySeconds := 60 - int(time.Now().Unix()%60)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, retrySeconds
	}
	if errors.Is(err, service.ErrUserPlatformDailyQuotaExhausted) ||
		errors.Is(err, service.ErrUserPlatformWeeklyQuotaExhausted) ||
		errors.Is(err, service.ErrUserPlatformMonthlyQuotaExhausted) {
		// 与 RPM 超限一致映射 429 + Retry-After，让 SDK 自动退避（而非 403 直接失败）。
		// 错误码用 rate_limit_exceeded 与 OpenAI 兼容客户端一致；细分类型由 ErrCode + window_resets_at metadata 区分。
		msg := pkgerrors.Message(err)
		return http.StatusTooManyRequests, "rate_limit_exceeded", msg, extractQuotaResetSeconds(err)
	}
	msg := pkgerrors.Message(err)
	if msg == "" {
		logger.L().With(
			zap.String("component", "handler.gateway.billing"),
			zap.Error(err),
		).Warn("gateway.billing_error_missing_message")
		msg = "Billing error"
	}
	return http.StatusForbidden, "billing_error", msg, 0
}

func (h *GatewayHandler) metadataBridgeEnabled() bool {
	if h == nil || h.cfg == nil {
		return true
	}
	return h.cfg.Gateway.OpenAIWS.MetadataBridgeEnabled
}

func (h *GatewayHandler) maybeLogCompatibilityFallbackMetrics(reqLog *zap.Logger) {
	if reqLog == nil {
		return
	}
	if gatewayCompatibilityMetricsLogCounter.Add(1)%gatewayCompatibilityMetricsLogInterval != 0 {
		return
	}
	metrics := service.SnapshotOpenAICompatibilityFallbackMetrics()
	reqLog.Info("gateway.compatibility_fallback_metrics",
		zap.Int64("session_hash_legacy_read_fallback_total", metrics.SessionHashLegacyReadFallbackTotal),
		zap.Int64("session_hash_legacy_read_fallback_hit", metrics.SessionHashLegacyReadFallbackHit),
		zap.Int64("session_hash_legacy_dual_write_total", metrics.SessionHashLegacyDualWriteTotal),
		zap.Float64("session_hash_legacy_read_hit_rate", metrics.SessionHashLegacyReadHitRate),
		zap.Int64("metadata_legacy_fallback_total", metrics.MetadataLegacyFallbackTotal),
	)
}

func (h *GatewayHandler) submitUsageRecordTask(parent context.Context, task service.UsageRecordTask) {
	if task == nil {
		return
	}
	task, abandon := wrapUsageRecordTaskContext(parent, task)
	if h.usageRecordWorkerPool != nil {
		if mode := h.usageRecordWorkerPool.Submit(task); mode != service.UsageRecordSubmitModeDroppedStopped {
			if mode.Dropped() {
				abandon()
			}
			return
		}
		// 池已停止（进程关停窗口）：计费任务不能静默丢失，降级为内联同步执行。
		// 显式配置的 drop/sample 溢出丢弃仍按配置语义保留。
		logger.L().With(
			zap.String("component", "handler.gateway.messages"),
		).Warn("gateway.usage_record_task_stopped_sync_fallback")
	}
	// 回退路径：worker 池未注入或已停止时同步执行，避免退回到无界 goroutine 模式。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", "handler.gateway.messages"),
				zap.Any("panic", recovered),
			).Error("gateway.usage_record_task_panic_recovered")
		}
	}()
	task(ctx)
}

// submitMandatoryUsageRecordTask never silently drops billing work on pool overflow.
func (h *GatewayHandler) submitMandatoryUsageRecordTask(parent context.Context, task service.UsageRecordTask) {
	if task == nil {
		return
	}
	task, _ = wrapUsageRecordTaskContext(parent, task)
	if h.usageRecordWorkerPool != nil {
		if mode := h.usageRecordWorkerPool.Submit(task); !mode.Dropped() {
			return
		}
		logger.L().With(
			zap.String("component", "handler.gateway.usage"),
		).Warn("gateway.usage_record_task_mandatory_sync_fallback")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", "handler.gateway.usage"),
				zap.Any("panic", recovered),
			).Error("gateway.usage_record_task_panic_recovered")
		}
	}()
	task(ctx)
}

// getUserMsgQueueMode 获取当前请求的 UMQ 模式
// 返回 "serialize" | "throttle" | ""
func (h *GatewayHandler) getUserMsgQueueMode(account *service.Account, parsed *service.ParsedRequest) string {
	if h.userMsgQueueHelper == nil {
		return ""
	}
	// 仅适用于 Anthropic OAuth/SetupToken 账号
	if !account.IsAnthropicOAuthOrSetupToken() {
		return ""
	}
	if !service.IsRealUserMessage(parsed) {
		return ""
	}
	// 账号级模式优先，fallback 到全局配置
	mode := account.GetUserMsgQueueMode()
	if mode == "" {
		mode = h.cfg.Gateway.UserMessageQueue.GetEffectiveMode()
	}
	return mode
}
