package service

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/DeltrivX/Auto2API/internal/pkg/logger"
	"github.com/DeltrivX/Auto2API/internal/pkg/openai"
	"github.com/DeltrivX/Auto2API/internal/util/responseheaders"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// openaiCCRawAllowedHeaders 是 CC 直转路径专用的客户端 header 透传白名单。
//
// **关键**：不能复用 openaiAllowedHeaders——后者含 Codex 客户端专属 header
// （originator / session_id / x-codex-turn-state / x-codex-turn-metadata / conversation_id），
// 这些在 ChatGPT OAuth 上游是必需的，但透传给 DeepSeek/Kimi/GLM 等第三方
// OpenAI 兼容上游会造成：
//   - 完全忽略（多数友好厂商）——隐性污染上游统计
//   - 400 "unknown parameter"（严格上游）——可见错误
//
// 这里仅放行通用 HTTP header；content-type / authorization / accept 由上下文
// 显式设置，不依赖透传。
//
// 参见决策记录：
// pensieve/short-term/maxims/dont-reuse-shared-headers-whitelist-across-different-upstream-trust-domains
var openaiCCRawAllowedHeaders = map[string]bool{
	"accept-language": true,
	"user-agent":      true,
}

// forwardAsRawChatCompletions 直转客户端的 Chat Completions 请求到上游
// `{base_url}/v1/chat/completions`，**不**做 CC↔Responses 协议转换。
//
// 适用场景：account.platform=openai && account.type=apikey && 上游已被探测确认
// 不支持 /v1/responses 端点（如 GLM/Qwen 等第三方 OpenAI 兼容上游）；CN 供应商
// 固定 chat_completions 协议也走此路径。
//
// 与 ForwardAsChatCompletions 的关键差异：
//
//   - 不调用 apicompat.ChatCompletionsToResponses，body 仅做模型 ID 改写
//   - 上游 URL 拼到 /v1/chat/completions 而非 /v1/responses
//   - 流式响应 SSE 直接透传给客户端（上游 chunk 已是 CC 格式）
//   - 非流式响应 JSON 直接透传，仅按需提取 usage
//   - 不应用 codex OAuth transform（APIKey 路径无 OAuth）
//   - 不注入 prompt_cache_key（OAuth 专属机制）
//
// 调用入口：openai_gateway_chat_completions.go::ForwardAsChatCompletions
// 在函数顶部按 openai_compat.ShouldUseResponsesAPI 分流。
func (s *OpenAIGatewayService) forwardAsRawChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
) (*OpenAIForwardResult, error) {
	return s.forwardAsRawChatCompletionsWithStartTime(ctx, c, account, body, defaultMappedModel, time.Now())
}

func (s *OpenAIGatewayService) forwardAsRawChatCompletionsWithStartTime(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	defaultMappedModel string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {

	// 1. Parse minimal fields needed for routing/billing
	originalModel := gjson.GetBytes(body, "model").String()
	if originalModel == "" {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", "model is required")
		return nil, fmt.Errorf("missing model in request")
	}
	clientStream := gjson.GetBytes(body, "stream").Bool()

	// 2. Resolve model mapping (same as ForwardAsChatCompletions)
	billingModel := resolveOpenAIForwardModel(account, originalModel, defaultMappedModel)
	upstreamModel := billingModel
	if err := validateGPT61CompatRequest(body, upstreamModel); err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	SetOpsUpstreamModel(c, upstreamModel)
	grokCacheIdentity := ""
	if false {
		// Resolve before image bridging or other body rewrites so the fallback is
		// anchored to the client's stable conversation prefix.
		grokCacheIdentity = resolveGrokCacheIdentity(c, body, "", upstreamModel)
	}
	if openai.RequiresResponsesForToolCalls(upstreamModel) && (len(gjson.GetBytes(body, "tools").Array()) > 0 || len(gjson.GetBytes(body, "functions").Array()) > 0) {
		err := fmt.Errorf("%s requires Responses for tool calls; this account only supports Chat Completions", normalizeKnownOpenAICodexModel(upstreamModel))
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	if openai.IsGPT6SolOrLunaModelSpelling(upstreamModel) && (len(gjson.GetBytes(body, "tools").Array()) > 0 || len(gjson.GetBytes(body, "functions").Array()) > 0) && gjson.GetBytes(body, "reasoning_effort").String() != "none" {
		err := fmt.Errorf("%s requires Responses for tool calls with reasoning; this account only supports Chat Completions. Use reasoning_effort=none or a Responses-capable account", upstreamModel)
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	// 3. Rewrite model in body (no protocol conversion)
	upstreamBody := body
	if upstreamModel != originalModel {
		upstreamBody = ReplaceModelInBody(body, upstreamModel)
	}
	if normalizedBody, normalized := NormalizeGLMOpenAIReasoningEffort(upstreamBody, upstreamModel); normalized {
		upstreamBody = normalizedBody
	}

	// 4. Apply OpenAI fast policy on the CC body
	updatedBody, policyErr := s.applyOpenAIFastPolicyToBody(ctx, account, upstreamModel, upstreamBody)
	if policyErr != nil {
		var blocked *OpenAIFastBlockedError
		if errors.As(policyErr, &blocked) {
			MarkOpsClientBusinessLimited(c, OpsClientBusinessLimitedReasonLocalPolicyDenied)
			writeChatCompletionsError(c, http.StatusForbidden, "permission_error", blocked.Message)
		}
		return nil, policyErr
	}
	upstreamBody = updatedBody
	// Keep the final outbound tier separate from the observed response tier so
	// usage recording can apply the selected credential's response contract.
	serviceTier := extractOpenAIServiceTierFromBody(upstreamBody)

	// Grok Composer does not accept image_url parts directly, but Grok Build
	// can describe the images first. Bridge only this exact failure mode.
	token, tokenKind, err := s.getRequestCredential(ctx, c, account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("account %d missing %s credential", account.ID, tokenKind)
	}
	// Marvis 有一批槽位已被上游下线（claude-* / gpt-5.4），转发只会拿到
	// "无效模型id"。⚠ 必须按平台判断：同名模型在 WorkBuddy 国际站是
	// 正常可用的，早先只按模型名拦，误伤了国际版。
	if IsUnavailableModelForPlatform(account.Platform, upstreamModel) {
		return nil, fmt.Errorf("模型 %s 在 %s 平台已不可用", upstreamModel, account.Platform)
	}

	var bridgeUsage OpenAIUsage

	if clientStream {
		var usageErr error
		upstreamBody, usageErr = ensureOpenAIChatStreamUsage(upstreamBody)
		if usageErr != nil {
			return nil, fmt.Errorf("enable stream usage: %w", usageErr)
		}
	} else if isWorkBuddyPlatform(account.Platform) {
		// WorkBuddy 上游被强制为流式（见步骤 5），因此即使客户端要非流式，
		// 上游走的也是 SSE；不打开 include_usage 就拿不到任何 token 用量，
		// 计费会全部记成 0。
		var usageErr error
		upstreamBody, usageErr = ensureOpenAIChatStreamUsage(upstreamBody)
		if usageErr != nil {
			return nil, fmt.Errorf("enable stream usage: %w", usageErr)
		}
	}
	if false {
		upstreamBody, err = stripGrokChatPromptCacheKey(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("remove Responses-only Grok prompt cache key: %w", err)
		}
		upstreamBody, err = normalizeGrokChatReasoningEffort(upstreamBody, upstreamModel)
		if err != nil {
			return nil, fmt.Errorf("normalize Grok chat reasoning effort: %w", err)
		}
		upstreamBody, err = sanitizeGrokUnsupportedFields(upstreamBody)
		if err != nil {
			return nil, fmt.Errorf("sanitize Grok unsupported fields: %w", err)
		}
	}
	upstreamBody = applyOllamaCloudRawChatCompletionsRequest(account, upstreamBody)
	upstreamBody = clampOllamaCloudUpstreamMaxTokens(account, upstreamBody)

	logger.L().Debug("openai chat_completions raw: forwarding without protocol conversion",
		zap.Int64("account_id", account.ID),
		zap.String("original_model", originalModel),
		zap.String("billing_model", billingModel),
		zap.String("upstream_model", upstreamModel),
		zap.Bool("stream", clientStream),
	)

	// Trae 不走 OpenAI 协议：请求发到 IDE 的 llm_utils_chat，回答是它自己的
	// SSE 事件。这里整体交给专用适配器（含 function 回退与事件翻译）。
	if IsTraePlatform(account.Platform) {
		return s.forwardTraeChatCompletions(ctx, c, account, body, originalModel, billingModel, upstreamModel, clientStream, startTime)
	}

	// 5. Build and send upstream request via the shared CC pipeline
	targetURL, err := s.rawChatCompletionsURL(account)
	if err != nil {
		return nil, err
	}
	// 国际站必须走 console 两段式：先建会话拿 conversationId，再带着它
	// 打 /console/chat/completions。直接打 /v2/chat/completions 会被渠道
	// 校验拦下（11128 / 11140）。国内站仍是 /v2/chat/completions。
	if isWorkBuddyPlatform(account.Platform) && isWorkBuddyAIVariant(account.Platform) {
		site := workBuddySiteForPlatform(account.Platform)
		if site != nil {
			convClient := &http.Client{Timeout: 30 * time.Second}
			convID, convErr := workBuddyCreateConversation(ctx, convClient, account, site.Endpoint)
			if convErr != nil {
				return nil, convErr
			}
			var withConv map[string]any
			if err := json.Unmarshal(upstreamBody, &withConv); err == nil {
				withConv["conversationId"] = convID
				// 国际站模型列表里混着补全/NES/视频模型，直接转发会 11102。
				// 出站前收敛到真正能对话的 id。
				if rawModel, ok := withConv["model"].(string); ok {
					withConv["model"] = workBuddyNormalizeIntlModel(rawModel)
				}
				if merged, mErr := json.Marshal(withConv); mErr == nil {
					upstreamBody = merged
				}
			}
			targetURL = workBuddyConsoleChatURL(site.Endpoint)
		}
	}
	// 国内版：补一个稳定派生的 conversationId。
	//
	// 官方桌面端每次真实对话都带这个字段，缺它时上游会按非官方客户端判定，
	// 间歇性返回 400 code 11128 "Illegal API invocation from an unapproved
	// channel"——同一模型时好时坏。AutoBuddy 用 machineId + messages 的
	// md5 派生 "wb-<hash>" 补齐，这里照搬。
	if isWorkBuddyPlatform(account.Platform) && !isWorkBuddyAIVariant(account.Platform) {
		var reqBody map[string]any
		if err := json.Unmarshal(upstreamBody, &reqBody); err == nil {
			if _, ok := reqBody["conversationId"]; !ok {
				reqBody["conversationId"] = workBuddyDeriveConversationID(account, reqBody["messages"])
				if merged, mErr := json.Marshal(reqBody); mErr == nil {
					upstreamBody = merged
				}
			}
		}
	}
	// WorkBuddy 上游只接受流式请求：非流式会被直接拒绝（code 11101
	// "Non-stream chat request is currently not supported" / HTTP 400）。
	// 这里对上游强制开启流式，客户端仍按自己的 stream 标志拿聚合结果
	// （非流式走 bufferRawChatCompletions 聚合）。
	if isWorkBuddyPlatform(account.Platform) {
		var reqBody map[string]any
		if err := json.Unmarshal(upstreamBody, &reqBody); err == nil {
			if streamVal, ok := reqBody["stream"].(bool); !ok || !streamVal {
				reqBody["stream"] = true
				if merged, mErr := json.Marshal(reqBody); mErr == nil {
					upstreamBody = merged
				}
			}
		}
	}
	upstreamBody, err = normalizeStrictChatDeveloperRoles(account, targetURL, upstreamBody)
	if err != nil {
		writeChatCompletionsError(c, http.StatusBadRequest, "invalid_request_error", err.Error())
		return nil, err
	}
	SetActualOpenAIUpstreamEndpoint(c, grokChatRawEndpoint)
	customUA := account.GetOpenAIUserAgent()
	if customUA == "" && account.IsGrokOAuth() {
		customUA = defaultGrokUpstreamUserAgent()
	}
	// Record the final provider-normalized effort, so usage and pricing match
	// the outbound request (for example GLM xhigh is forwarded as max).
	reasoningEffort := extractOpenAIReasoningEffortFromBody(upstreamBody, upstreamModel, billingModel, originalModel)
	reasoningEffort = ApplyThinkingEnabledFallback(reasoningEffort, upstreamBody, upstreamModel)
	resp, err := s.sendCCUpstreamRequest(ctx, c, account, targetURL, upstreamBody, clientStream, token, customUA, grokCacheIdentity)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 7. Handle error response with failover
	if resp.StatusCode >= 400 {
		respBody, upstreamMsg := s.readOpenAIUpstreamError(resp)
		if false {
			kind := "http_error"
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				kind = "failover"
			}
			appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
				ProxyID:            opsUpstreamProxyID(account),
				ProxyName:          opsUpstreamProxyName(account),
				Platform:           account.Platform,
				AccountID:          account.ID,
				AccountName:        account.Name,
				UpstreamStatusCode: resp.StatusCode,
				UpstreamRequestID:  firstNonEmpty(resp.Header.Get("x-request-id"), resp.Header.Get("xai-request-id")),
				Kind:               kind,
				Message:            upstreamMsg,
			})
			s.handleGrokAccountUpstreamError(withGrokTeamRateLimitModel(ctx, upstreamModel), account, resp.StatusCode, resp.Header, respBody)
			if s.shouldFailoverGrokUpstreamError(resp.StatusCode, respBody) {
				retryable, retryDelay, retryDeadline, retryMax := grokSameAccountRetryMetadata(account, resp.StatusCode, respBody)
				return nil, &UpstreamFailoverError{
					StatusCode:               resp.StatusCode,
					ResponseBody:             respBody,
					ResponseHeaders:          resp.Header.Clone(),
					RetryableOnSameAccount:   retryable,
					RequestScopedTransient:   retryable && resp.StatusCode == http.StatusTooManyRequests,
					SameAccountRetryDelay:    retryDelay,
					SameAccountRetryDeadline: retryDeadline,
					SameAccountRetryMax:      retryMax,
				}
			}
			return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
		}
		if foErr := s.failoverOpenAIUpstreamHTTPError(ctx, c, account, resp, respBody, upstreamMsg, upstreamModel); foErr != nil {
			return nil, foErr
		}
		return s.handleChatCompletionsErrorResponse(resp, c, account, billingModel)
	}

	if false {
		s.updateGrokUsageFromResponse(withGrokTeamRateLimitModel(ctx, upstreamModel), account, resp.Header, resp.StatusCode)
	}

	// 8. Forward response
	var result *OpenAIForwardResult
	var forwardErr error
	// WorkBuddy 上游被强制为流式（见步骤 5），客户端要非流式时需要把 SSE
	// 聚合成标准 chat.completion 响应，否则会把流直接吐给非流式客户端。
	if isWorkBuddyPlatform(account.Platform) && !clientStream {
		result, forwardErr = s.bufferWorkBuddyStreamAsChatCompletion(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
		if result != nil {
			addOpenAIUsage(&result.Usage, bridgeUsage)
		}
		return result, forwardErr
	}
	if clientStream {
		result, forwardErr = s.streamRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime, len(body))
	} else {
		result, forwardErr = s.bufferRawChatCompletions(c, resp, account, originalModel, billingModel, upstreamModel, reasoningEffort, serviceTier, startTime)
	}
	if result != nil {
		addOpenAIUsage(&result.Usage, bridgeUsage)
		result.UpstreamEndpoint = grokChatRawEndpoint
	}
	return result, forwardErr
}

func (s *OpenAIGatewayService) rawChatCompletionsURL(account *Account) (string, error) {
	return s.openAIChatCompletionsTargetURL(account)
}

// streamRawChatCompletions 透传上游 CC SSE 流到客户端，并提取 usage（包括
// 末尾 [DONE] 之前的 chunk 中的 usage 字段，按 OpenAI CC 协议）。
//
// usage 字段仅在客户端请求 stream_options.include_usage=true 时出现于上游响应中。
// 网关会对上游强制打开 include_usage 以保证计费完整，并原样向下游透传 usage，
// 让级联代理或下游计费系统也能拿到完整用量。
func (s *OpenAIGatewayService) streamRawChatCompletions(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
	requestBodyLen int,
) (*OpenAIForwardResult, error) {
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	requestID := resp.Header.Get("x-request-id")
	writeStreamHeaders := s.newStreamHeaderWriter(c, resp.Header)
	scanner := s.newUpstreamSSEScanner(resp.Body)

	var usage OpenAIUsage
	var firstTokenMs *int
	clientDisconnected := false
	clientOutputStarted := false
	pendingLines := make([]string, 0, 8)
	refusalDetector := newOpenAIChatSilentRefusalDetector(requestBodyLen)
	var terminal openAIRawStreamTerminalState

	writeLine := func(line string) {
		if clientDisconnected {
			return
		}
		if !clientOutputStarted && !refusalDetector.ShouldReleaseClientOutput() {
			pendingLines = append(pendingLines, line)
			return
		}
		if !clientOutputStarted {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := c.Writer.WriteString(pending + "\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					return
				}
			}
			pendingLines = pendingLines[:0]
			clientOutputStarted = true
		}
		if _, werr := c.Writer.WriteString(line + "\n"); werr != nil {
			clientDisconnected = true
			logger.L().Debug("openai chat_completions raw: client disconnected, continuing to drain upstream for billing",
				zap.Error(werr),
				zap.String("request_id", requestID),
			)
		}
	}

	for scanner.Scan() {
		line := scanner.Text()
		refusalDetector.ObserveSSELine(line)
		if payload, ok := extractOpenAISSEDataLine(line); ok {
			trimmedPayload := strings.TrimSpace(payload)
			terminal.ObserveDataLine(trimmedPayload)
			if trimmedPayload != "[DONE]" {
				observer.ObserveOpenAI([]byte(payload), strings.TrimSpace(gjson.Get(payload, "type").String()))
				usageOnlyChunk := isOpenAIChatUsageOnlyStreamChunk(payload)
				if u := extractCCStreamUsage(payload); u != nil {
					usage = *u
				}
				if firstTokenMs == nil && !usageOnlyChunk {
					elapsed := int(time.Since(startTime).Milliseconds())
					firstTokenMs = &elapsed
				}
			}
		}
		line = applyOllamaCloudRawChatCompletionsSSELine(account, line)
		line = stripEmptyChatToolCallIdentityFromSSELine(line)

		line = s.replaceModelInSSELine(line, upstreamModel, originalModel)
		writeLine(line)
		if line == "" {
			if !clientDisconnected && clientOutputStarted {
				c.Writer.Flush()
			}
			continue
		}
		if !clientDisconnected && clientOutputStarted {
			c.Writer.Flush()
		}
	}

	resultWithUsage := func() *OpenAIForwardResult {
		return &OpenAIForwardResult{
			RequestID:                     requestID,
			UpstreamHeaders:               resp.Header,
			Usage:                         usage,
			Model:                         originalModel,
			BillingModel:                  billingModel,
			UpstreamModel:                 upstreamModel,
			UpstreamResponseModel:         observedUpstreamResponseModel(c),
			UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
			UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
			ReasoningEffort:               reasoningEffort,
			ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
			Stream:                        true,
			Duration:                      time.Since(startTime),
			FirstTokenMs:                  firstTokenMs,
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		logger.L().Warn("openai chat_completions raw: stream read error",
			zap.Error(scanErr),
			zap.String("request_id", requestID),
		)
	}

	// 客户端取消/断开后上游读失败与上游截断不可区分（取消会连带取消上游请求），
	// 沿用既有语义：按已收到的用量正常收尾计费，不判为上游故障。
	clientAborted := clientDisconnected ||
		errors.Is(scanErr, context.Canceled) ||
		errors.Is(scanErr, context.DeadlineExceeded)

	// 上游在任何终止信号之前结束：连接被 reset（scanErr != nil）或干净 EOF。
	// 两者都不能再记成功——此前统一返回 nil error，把上游截断伪装成
	// `HTTP 200 + usage 0/0`，客户端收到半截回答且 Ops 侧完全无感。
	if !clientAborted && terminal.IsTruncated(clientOutputStarted) {
		cause := scanErr
		if cause == nil {
			cause = ErrOpenAIUpstreamStreamTruncated
		}
		logger.L().Warn("openai chat_completions raw: upstream stream truncated before terminal chunk",
			zap.Error(cause),
			zap.String("request_id", requestID),
			zap.Int64("account_id", account.ID),
			zap.String("upstream_model", upstreamModel),
			zap.Bool("saw_sse_data", terminal.sawDataLine),
			zap.Bool("client_output_started", clientOutputStarted),
		)
		if !clientOutputStarted {
			// 响应头尚未提交：可以透明换号重试，客户端不会看到半截流。
			return nil, newOpenAIRawStreamTruncatedFailoverError(c, account, requestID, cause)
		}
		// 已写出语义字节：无法再 failover，改为带类型的上游错误。handler 会据此
		// 补发 SSE error 帧并把本次请求计入 SLA 失败。
		recordOpenAIRawStreamTruncation(c, account, requestID, cause, "http_error")
		return resultWithUsage(), newOpenAIUpstreamStreamReadError(cause)
	}

	if scanErr == nil && !clientDisconnected && !clientOutputStarted {
		if refusalDetector.IsSilentRefusal() {
			return nil, newOpenAISilentRefusalFailoverError(c, account, requestID)
		}
		if len(pendingLines) > 0 {
			writeStreamHeaders()
			for _, pending := range pendingLines {
				if _, werr := c.Writer.WriteString(pending + "\n"); werr != nil {
					clientDisconnected = true
					logger.L().Debug("openai chat_completions raw: client disconnected during final flush",
						zap.Error(werr),
						zap.String("request_id", requestID),
					)
					break
				}
			}
			if !clientDisconnected {
				c.Writer.Flush()
				clientOutputStarted = true
			}
		}
	}

	return resultWithUsage(), nil
}

// ensureOpenAIChatStreamUsage 确保 raw Chat Completions 流式请求会让上游返回 usage。
// usage 也会继续向下游透传，支持级联代理和下游计费系统。
func ensureOpenAIChatStreamUsage(body []byte) ([]byte, error) {
	updated, err := sjson.SetBytes(body, "stream_options.include_usage", true)
	if err != nil {
		return body, err
	}
	return updated, nil
}

func isOpenAIChatUsageOnlyStreamChunk(payload string) bool {
	if strings.TrimSpace(payload) == "" {
		return false
	}
	if !gjson.Get(payload, "usage").Exists() {
		return false
	}
	choices := gjson.Get(payload, "choices")
	return choices.Exists() && choices.IsArray() && len(choices.Array()) == 0
}

// extractCCStreamUsage 从单个 CC 流式 chunk 的 payload 中提取 usage 字段。
// CC 协议中 usage 仅出现在末尾 chunk（且仅当 include_usage 生效时），
// 但上游可能在多个 chunk 中重复——总是用最新值。
func extractCCStreamUsage(payload string) *OpenAIUsage {
	usageResult := gjson.Get(payload, "usage")
	if !usageResult.Exists() || !usageResult.IsObject() {
		return nil
	}
	u, ok := openAIUsageFromGJSON(usageResult)
	if !ok {
		return nil
	}
	return &u
}

// bufferRawChatCompletions 透传上游 CC 非流式 JSON 响应。
func (s *OpenAIGatewayService) bufferRawChatCompletions(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	respBody, err := ReadUpstreamResponseBody(resp.Body, s.cfg, c, openAITooLargeError)
	if err != nil {
		if !errors.Is(err, ErrUpstreamResponseBodyTooLarge) {
			writeChatCompletionsError(c, http.StatusBadGateway, "api_error", "Failed to read upstream response")
		}
		return nil, fmt.Errorf("read upstream body: %w", err)
	}
	observer := upstreamResponseModelObserverFromContext(c)
	if observer == nil {
		observer = beginUpstreamResponseModelObservation(c)
	}
	observer.ObserveOpenAI(respBody, strings.TrimSpace(gjson.GetBytes(respBody, "type").String()))

	var usage OpenAIUsage
	if parsedUsage, ok := extractOpenAIUsageFromJSONBytes(respBody); ok {
		usage = parsedUsage
	}
	respBody = applyOllamaCloudRawChatCompletionsResponse(account, respBody)
	respBody = s.replaceModelInResponseBody(respBody, upstreamModel, originalModel)

	if s.responseHeaderFilter != nil {
		responseheaders.WriteFilteredHeaders(c.Writer.Header(), resp.Header, s.responseHeaderFilter)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.Writer.Header().Set("Content-Type", ct)
	} else {
		c.Writer.Header().Set("Content-Type", "application/json")
	}
	c.Writer.WriteHeader(http.StatusOK)
	_, _ = c.Writer.Write(respBody)

	return &OpenAIForwardResult{
		RequestID:                     requestID,
		UpstreamHeaders:               resp.Header,
		Usage:                         usage,
		Model:                         originalModel,
		BillingModel:                  billingModel,
		UpstreamModel:                 upstreamModel,
		UpstreamResponseModel:         observedUpstreamResponseModel(c),
		UpstreamResponseModelConflict: observedUpstreamResponseModelConflict(c),
		UpstreamResponseServiceTier:   observedUpstreamResponseServiceTier(c),
		ReasoningEffort:               reasoningEffort,
		ServiceTier:                   resolvedOpenAIUpstreamServiceTier(c, serviceTier),
		Stream:                        false,
		Duration:                      time.Since(startTime),
	}, nil
}

// buildOpenAIChatCompletionsURL 拼接上游 Chat Completions 端点 URL。
//
//   - base 已是 /chat/completions：原样返回
//   - base 以 /v1 结尾：追加 /chat/completions
//   - base 以其他版本段结尾（如 /v4）：追加 /chat/completions
//   - 其他情况：追加 /v1/chat/completions
//
// 与 buildOpenAIResponsesURL 是姐妹函数。
func buildOpenAIChatCompletionsURL(base string) string {
	return buildOpenAIEndpointURL(base, "/v1/chat/completions")
}

// bufferWorkBuddyStreamAsChatCompletion 把 WorkBuddy 的 SSE 流聚合为
// 标准非流式 chat.completion 响应。
//
// WorkBuddy 上游只接受流式请求，网关对非流式客户端强制上游 stream=true；
// 客户端的到站协议仍是 stream=false，因此必须在这里把增量拼成完整响应，
// 否则客户端拿到的是原始 SSE 文本而不是 JSON。
func (s *OpenAIGatewayService) bufferWorkBuddyStreamAsChatCompletion(
	c *gin.Context,
	resp *http.Response,
	account *Account,
	originalModel string,
	billingModel string,
	upstreamModel string,
	reasoningEffort *string,
	serviceTier *string,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	requestID := resp.Header.Get("x-request-id")

	var (
		content   strings.Builder
		reasoning strings.Builder
		usage     OpenAIUsage
		respID    string
		created   int64
		finishRsn string
		// 工具调用按 index 归并：上游把一次 tool_call 拆成多帧增量
		// （首帧带 name，后续帧续 arguments），必须按 index 拼接，
		// 否则非流式客户端拿到的 tool_calls 为空 —— 智能体据此判断
		// 是否需要执行工具，丢了它整个工具链路就断了。
		toolCalls = map[int]*workBuddyAggToolCall{}
		toolOrder []int
	)

	scanner := s.newUpstreamSSEScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Created int64  `json:"created"`
			Choices []struct {
				Delta struct {
					Content          string `json:"content"`
					ReasoningContent string `json:"reasoning_content"`
					ToolCalls        []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Type     string `json:"type"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			// 上游遵循 OpenAI CC 协议：usage 用 prompt_tokens /
			// completion_tokens 命名，与内部 OpenAIUsage（input/output）
			// 不同，必须单独解出再转换。
			Usage *struct {
				PromptTokens             int `json:"prompt_tokens"`
				CompletionTokens         int `json:"completion_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if respID == "" && chunk.ID != "" {
			respID = chunk.ID
		}
		if created == 0 && chunk.Created > 0 {
			created = chunk.Created
		}
		for _, ch := range chunk.Choices {
			content.WriteString(ch.Delta.Content)
			reasoning.WriteString(ch.Delta.ReasoningContent)
			if ch.FinishReason != "" {
				finishRsn = ch.FinishReason
			}
			for _, tc := range ch.Delta.ToolCalls {
				agg, ok := toolCalls[tc.Index]
				if !ok {
					agg = &workBuddyAggToolCall{}
					toolCalls[tc.Index] = agg
					toolOrder = append(toolOrder, tc.Index)
				}
				if tc.ID != "" && agg.ID == "" {
					agg.ID = tc.ID
				}
				if tc.Type != "" && agg.Type == "" {
					agg.Type = tc.Type
				}
				if tc.Function.Name != "" && agg.Name == "" {
					agg.Name = tc.Function.Name
				}
				agg.Arguments.WriteString(tc.Function.Arguments)
			}
		}
		if chunk.Usage != nil && chunk.Usage.PromptTokens+chunk.Usage.CompletionTokens > 0 {
			usage = OpenAIUsage{
				InputTokens:              chunk.Usage.PromptTokens,
				OutputTokens:             chunk.Usage.CompletionTokens,
				CacheReadInputTokens:     chunk.Usage.CacheReadInputTokens,
				CacheCreationInputTokens: chunk.Usage.CacheCreationInputTokens,
			}
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read upstream stream: %w", err)
	}

	message := gin.H{"role": "assistant", "content": content.String()}
	if r := reasoning.String(); r != "" {
		message["reasoning_content"] = r
	}
	if len(toolOrder) > 0 {
		calls := make([]gin.H, 0, len(toolOrder))
		for _, idx := range toolOrder {
			agg := toolCalls[idx]
			typ := agg.Type
			if typ == "" {
				typ = "function"
			}
			calls = append(calls, gin.H{
				"id":   agg.ID,
				"type": typ,
				"function": gin.H{
					"name":      agg.Name,
					"arguments": agg.Arguments.String(),
				},
			})
		}
		message["tool_calls"] = calls
	}

	out := gin.H{
		"id":      firstNonEmpty(respID, "chatcmpl-"+strconv.FormatInt(time.Now().UnixNano(), 10)),
		"object":  "chat.completion",
		"created": firstNonZeroInt64(created, time.Now().Unix()),
		"model":   originalModel,
		"choices": []gin.H{{
			"index":         0,
			"message":       message,
			"finish_reason": firstNonEmpty(finishRsn, "stop"),
		}},
	}
	if usage.InputTokens > 0 || usage.OutputTokens > 0 {
		out["usage"] = usage
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(c.Writer).Encode(out); err != nil {
		return nil, err
	}

	return &OpenAIForwardResult{
		RequestID:       requestID,
		UpstreamHeaders: resp.Header,
		Usage:           usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
		Duration:        time.Since(startTime),
	}, nil
}

// firstNonZeroInt64 返回第一个非零值。
func firstNonZeroInt64(values ...int64) int64 {
	for _, v := range values {
		if v != 0 {
			return v
		}
	}
	return 0
}

// workBuddyAggToolCall 是流式聚合期间累积的一个工具调用。
type workBuddyAggToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments strings.Builder
}

// workBuddyDeriveConversationID 为国内版请求派生稳定的 conversationId。
//
// 移植自 AutoBuddy relay.go 的 deriveConversationID：seed 取账号指纹的
// machineId（无则 uid），拼上 messages 的前 512 字节做 md5，得到
// "wb-<hash>"。同一账号对同一消息序列产生稳定值，不同请求互不冲突。
func workBuddyDeriveConversationID(account *Account, messages any) string {
	seed := ""
	if fp := account.workBuddyFingerprint(); fp.MachineID != "" {
		seed = strings.TrimSpace(fp.MachineID)
	}
	if seed == "" {
		seed = strings.TrimSpace(account.workBuddyUIDOf())
	}
	if seed == "" {
		seed = "wb"
	}
	msgJSON, err := json.Marshal(messages)
	if err != nil {
		msgJSON = []byte("[]")
	}
	if len(msgJSON) > 512 {
		msgJSON = msgJSON[:512]
	}
	sum := md5.Sum([]byte(seed + "|" + string(msgJSON)))
	return "wb-" + hex.EncodeToString(sum[:])
}
