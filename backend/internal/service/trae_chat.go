package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// Trae 的对话转发。
//
// Trae 不用 OpenAI 协议：请求发到 IDE 的 /api/agent/v3/llm_utils_chat，回答是
// 它自己的 SSE 事件（output / token_usage / done / error）。这里做两件事：
//  1. 把 OpenAI 的 messages 组装成 Trae 的请求体（含 function 选择与回退）；
//  2. 把 Trae 的 SSE 翻回 OpenAI 的 delta 流，并在结束时给出 usage。
//
// function 回退：Trae 的每个 function 只服务一部分模型（chat_v3 是经典 IDE 的，
// solo_* 是 SOLO 的）。上游回 4001 / 4023 / 1005 时按顺序换下一个，直到有一个
// 接住；全都不行才把最后一次的错误交出去。

const traeChatPath = "/api/agent/v3/llm_utils_chat"

// traeChatFunctionFallbackCodes 是「换一个 function 再试」的业务码。
var traeChatFunctionFallbackCodes = map[int]bool{4001: true, 4023: true, 1005: true}

// buildTraeChatPayload 组装一次对话请求。
//
// session_id 以账号设备为盐、按对话首条用户消息派生：同一会话的后续轮次
// （工具结果回传、重试）保持同一个 id，不同账号之间不会撞。
func buildTraeChatPayload(account *Account, body []byte, model, function string) (map[string]any, error) {
	if account == nil {
		return nil, fmt.Errorf("trae: 账号为空")
	}
	messages := gjson.GetBytes(body, "messages").Value()
	if messages == nil {
		return nil, fmt.Errorf("trae: 请求缺少 messages")
	}
	payload := map[string]any{
		"messages":    convertTraeMessages(messages),
		"config_name": model,
		"model":       model,
		"function":    function,
		"request_id":  traeSessionID(account, string(body)+"|"+fmt.Sprintf("%d", time.Now().UnixNano())),
		"session_id":  traeSessionID(account, string(body)),
	}
	if effort := strings.TrimSpace(gjson.GetBytes(body, "reasoning_effort").String()); effort != "" {
		payload["reasoning_effort"] = traeReasoningEffort(effort)
	}
	if v := gjson.GetBytes(body, "max_tokens"); v.Exists() {
		payload["max_tokens"] = v.Int()
	}
	return payload, nil
}

// convertTraeMessages 把 OpenAI 的 content 转成 Trae 的 [{type:text,text}] 形状。
func convertTraeMessages(raw any) []map[string]any {
	items, ok := raw.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		msg, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role == "" {
			role = "user"
		}
		text := ""
		switch content := msg["content"].(type) {
		case string:
			text = content
		case []any:
			var b strings.Builder
			for _, part := range content {
				if pm, ok := part.(map[string]any); ok {
					if t, ok := pm["text"].(string); ok {
						b.WriteString(t)
					}
				}
			}
			text = b.String()
		}
		out = append(out, map[string]any{
			"role":    role,
			"content": []map[string]any{{"type": "text", "text": text}},
		})
	}
	return out
}

// traeReasoningEffort 把网关档位映射成 Trae 的档位名。
func traeReasoningEffort(effort string) string {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return "light"
	case "medium":
		return "medium"
	case "high":
		return "high"
	case "xhigh", "max":
		return "extra_high"
	default:
		return effort
	}
}

// traeSessionID 派生稳定的会话 id（账号盐 + 内容哈希）。
func traeSessionID(account *Account, seed string) string {
	salt := ""
	if account != nil {
		salt = traeStringValue(account.Credentials, "machine_id")
	}
	sum := sha256.Sum256([]byte(salt + "|" + seed))
	return hex.EncodeToString(sum[:16])
}

// traeChatUpstream 发一次对话请求，返回原始响应。
func traeChatUpstream(ctx context.Context, site *traeSite, token traeToken, payload map[string]any) (*http.Response, error) {
	hosts := site.traeHostsFor(token.Region)
	endpoint := strings.TrimRight(hosts.ModelHost, "/") + traeChatPath
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	applyTraeHeaders(req.Header, site, token)
	req.Header.Set("Accept", "text/event-stream")
	client := &http.Client{Timeout: 0}
	return client.Do(req)
}

// forwardTraeChatCompletions 是 Trae 的转发入口：组装、回退、翻译、回写客户端。
func (s *OpenAIGatewayService) forwardTraeChatCompletions(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	body []byte,
	originalModel, billingModel, upstreamModel string,
	clientStream bool,
	startTime time.Time,
) (*OpenAIForwardResult, error) {
	site := traeSiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("trae: 不支持的平台 %q", account.Platform)
	}
	token := traeTokenOf(account.Credentials)
	if token.Access == "" {
		return nil, fmt.Errorf("trae: 账号缺少登录态，请重新授权")
	}

	var (
		resp     *http.Response
		lastErr  error
		function string
	)
	for _, candidate := range traeChatFunctions {
		payload, err := buildTraeChatPayload(account, body, upstreamModel, candidate)
		if err != nil {
			return nil, err
		}
		upstream, err := traeChatUpstream(ctx, site, token, payload)
		if err != nil {
			return nil, err
		}
		if upstream.StatusCode == http.StatusOK {
			resp, function = upstream, candidate
			break
		}
		rawErr, _ := io.ReadAll(io.LimitReader(upstream.Body, 8<<10))
		_ = upstream.Body.Close()
		code := traeUpstreamCode(rawErr)
		if traeChatFunctionFallbackCodes[code] {
			lastErr = fmt.Errorf("trae: function %s 拒绝(%d): %s", candidate, code, traeTrimBody(rawErr))
			continue
		}
		if code == 1001 || code == 20101 || upstream.StatusCode == http.StatusUnauthorized {
			return nil, errTraeSignInLapsed
		}
		return nil, fmt.Errorf("trae: 上游 HTTP %d: %s", upstream.StatusCode, traeTrimBody(rawErr))
	}
	if resp == nil {
		if lastErr == nil {
			lastErr = fmt.Errorf("trae: 上游拒绝了这次请求")
		}
		return nil, lastErr
	}
	defer func() { _ = resp.Body.Close() }()

	usage := &OpenAIUsage{}
	finishReason := "stop"

	if clientStream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")
		c.Writer.WriteHeader(http.StatusOK)
	}

	var content strings.Builder
	var reasoning strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		// 只处理正文类事件，与 magpie 的 ANSWER 集合一致。
		// 不加这层过滤会把 error / usage / done 等事件里的同名字段当正文取出来。
		if !traeIsAnswerEvent(event) {
			// 仍要读取 usage 与结束原因，只是不产正文。
			if used := traeUsageFromEvent(event); used != nil {
				usage = used
			}
			if reason := traeFinishReason(event); reason != "" {
				finishReason = reason
			}
			if errMsg := traeEventError(event); errMsg != "" {
				if !clientStream {
					c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": errMsg, "type": "upstream_error"}})
				}
				return nil, fmt.Errorf("trae: 上游错误: %s", errMsg)
			}
			continue
		}
		if txt := traeDeltaText(event); txt != "" {
			content.WriteString(txt)
			if clientStream {
				writeTraeChunk(c, originalModel, map[string]any{"content": txt}, nil, "")
			}
		}
		if txt := traeDeltaReasoning(event); txt != "" {
			reasoning.WriteString(txt)
			if clientStream {
				writeTraeChunk(c, originalModel, map[string]any{"reasoning_content": txt}, nil, "")
			}
		}
		if used := traeUsageFromEvent(event); used != nil {
			usage = used
		}
		if reason := traeFinishReason(event); reason != "" {
			finishReason = reason
		}
		if errMsg := traeEventError(event); errMsg != "" {
			if !clientStream {
				c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": errMsg, "type": "upstream_error"}})
			}
			return nil, fmt.Errorf("trae: 上游错误: %s", errMsg)
		}
	}

	// 上游不返回 token 计量，这里用本地估算兜底。
	// 只有上游明确给出非零值时采信上游，否则一律估算，避免 usage 全 0
	// 导致 usage_logs 没有可聚合的量、仪表盘按平台拆分看不到 Trae。
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		usage.InputTokens = traeEstimatePromptTokens(body)
		usage.OutputTokens = traeEstimateTokens(content.String()) + traeEstimateTokens(reasoning.String())
	}

	if clientStream {
		writeTraeChunk(c, originalModel, map[string]any{}, nil, finishReason)
		final := gin.H{
			"id":      "chatcmpl-trae",
			"object":  "chat.completion.chunk",
			"created": time.Now().Unix(),
			"model":   originalModel,
			"choices": []any{},
			"usage": gin.H{
				"prompt_tokens":     usage.InputTokens,
				"completion_tokens": usage.OutputTokens,
				"total_tokens":      usage.InputTokens + usage.OutputTokens,
			},
		}
		if raw, err := json.Marshal(final); err == nil {
			fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
		}
		fmt.Fprint(c.Writer, "data: [DONE]\n\n")
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	} else {
		c.JSON(http.StatusOK, gin.H{
			"id":      "chatcmpl-trae",
			"object":  "chat.completion",
			"created": time.Now().Unix(),
			"model":   originalModel,
			"choices": []any{gin.H{
				"index":         0,
				"finish_reason": finishReason,
				"message": gin.H{
					"role":              "assistant",
					"content":           content.String(),
					"reasoning_content": strings.TrimSpace(reasoning.String()),
				},
			}},
			"usage": gin.H{
				"prompt_tokens":     usage.InputTokens,
				"completion_tokens": usage.OutputTokens,
				"total_tokens":      usage.InputTokens + usage.OutputTokens,
			},
		})
	}

	return &OpenAIForwardResult{
		RequestID:       "trae-" + function,
		ResponseID:      "chatcmpl-trae",
		UpstreamHeaders: resp.Header,
		Usage:           *usage,
		Model:           originalModel,
		BillingModel:    billingModel,
		UpstreamModel:   upstreamModel,
	}, nil
}

// writeTraeChunk 写一个 OpenAI SSE chunk。
func writeTraeChunk(c *gin.Context, model string, delta map[string]any, _ any, finishReason string) {
	choice := gin.H{"index": 0, "delta": delta, "finish_reason": nil}
	if finishReason != "" {
		choice["finish_reason"] = finishReason
	}
	chunk := gin.H{
		"id":      "chatcmpl-trae",
		"object":  "chat.completion.chunk",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []any{choice},
	}
	if raw, err := json.Marshal(chunk); err == nil {
		fmt.Fprintf(c.Writer, "data: %s\n\n", raw)
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// ---------- Trae SSE 事件解析 ----------

// traeAnswerEvents 是会产生正文的事件名集合，与 magpie 的 ANSWER 完全一致。
//
// 事件名为空也算正文事件：部分上游版本不发 type 字段，正文直接放在顶层。
var traeAnswerEvents = map[string]struct{}{
	"": {}, "output": {}, "message": {}, "delta": {}, "content": {},
	"text": {}, "answer": {}, "chunk": {}, "output_delta": {}, "message_delta": {},
}

// traeIsAnswerEvent 判断事件是否属于正文类。
func traeIsAnswerEvent(event map[string]any) bool {
	name := traeEventName(event)
	_, ok := traeAnswerEvents[name]
	return ok
}

// traeEventName 归一化事件名，对齐 magpie 的 eventName()：
// 驼峰转下划线、非字母数字转下划线、转小写、去首尾下划线。
func traeEventName(event map[string]any) string {
	raw, _ := event["type"].(string)
	if raw == "" {
		raw, _ = event["event"].(string)
	}
	if raw == "" {
		return ""
	}
	var b strings.Builder
	prevLowerOrDigit := false
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevLowerOrDigit = true
		case r >= 'A' && r <= 'Z':
			if prevLowerOrDigit {
				b.WriteRune('_')
			}
			b.WriteRune(r - 'A' + 'a')
			prevLowerOrDigit = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevLowerOrDigit = true
		default:
			b.WriteRune('_')
			prevLowerOrDigit = false
		}
	}
	return strings.Trim(b.String(), "_")
}

// traeDeltaText 取事件里的正文增量。
// traeDeltaText 取事件里的正文增量。
//
// 取值顺序严格对齐 magpie trae_index.mjs 的 parts()：
//
//	d.response (string)
//	> d.content (string)
//	> d.delta.content (string)
//	> d.delta (string 本身)
//
// 最后一条最容易漏：上游在某些事件里把 delta 直接发成字符串，
// 而结构体断言会把它挡掉，表现就是"回复为空"。
func traeDeltaText(event map[string]any) string {
	if v, ok := event["response"].(string); ok && v != "" {
		return v
	}
	if v, ok := event["content"].(string); ok && v != "" {
		return v
	}
	if delta, ok := event["delta"].(map[string]any); ok {
		if v, ok := delta["content"].(string); ok {
			return v
		}
	}
	// delta 本身是字符串的情况。
	if v, ok := event["delta"].(string); ok && v != "" {
		return v
	}
	// 兼容 message 包裹（magpie 没有，但部分上游版本会这样发）。
	if message, ok := event["message"].(map[string]any); ok {
		if v, ok := message["content"].(string); ok {
			return v
		}
	}
	return ""
}

// traeDeltaReasoning 取事件里的思考增量。
// traeDeltaReasoning 取事件里的思考增量。
//
// 对齐 magpie：按 reasoning_content > reasoning > delta.reasoning_content > delta.reasoning
// 的顺序取第一个非空值——空串不算答案，要继续往后找。
func traeDeltaReasoning(event map[string]any) string {
	candidates := []any{event["reasoning_content"], event["reasoning"]}
	if delta, ok := event["delta"].(map[string]any); ok {
		candidates = append(candidates, delta["reasoning_content"], delta["reasoning"])
	}
	for _, c := range candidates {
		if v, ok := c.(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// traeUsageFromEvent 把 token_usage 事件转成网关的 usage。
func traeUsageFromEvent(event map[string]any) *OpenAIUsage {
	kind := firstNonEmpty(
		traeStringValue(event, "type", "event"),
	)
	if kind != "token_usage" && event["token_usage"] == nil && event["usage"] == nil {
		return nil
	}
	source, ok := event["token_usage"].(map[string]any)
	if !ok {
		source, ok = event["usage"].(map[string]any)
	}
	if !ok {
		source = event
	}
	usage := &OpenAIUsage{
		InputTokens:              int(traeNumberValue(source, "input_tokens", "prompt_tokens")),
		OutputTokens:             int(traeNumberValue(source, "output_tokens", "completion_tokens")),
		CacheReadInputTokens:     int(traeNumberValue(source, "cache_read_input_tokens", "prompt_cache_hit_tokens", "cached_tokens")),
		CacheCreationInputTokens: int(traeNumberValue(source, "cache_creation_input_tokens")),
	}
	if usage.InputTokens == 0 && usage.OutputTokens == 0 {
		return nil
	}
	return usage
}

// traeFinishReason 取结束原因。
func traeFinishReason(event map[string]any) string {
	kind := traeStringValue(event, "type", "event")
	if kind != "done" && event["finish_reason"] == nil {
		return ""
	}
	reason := traeStringValue(event, "finish_reason")
	switch reason {
	case "", "stop":
		return "stop"
	case "length", "max_tokens":
		return "length"
	default:
		return reason
	}
}

// traeEventError 取错误事件。
func traeEventError(event map[string]any) string {
	kind := traeStringValue(event, "type", "event")
	if kind != "error" && event["error"] == nil {
		return ""
	}
	if msg, ok := event["error"].(string); ok {
		return msg
	}
	if m, ok := event["error"].(map[string]any); ok {
		if msg := traeStringValue(m, "message", "msg"); msg != "" {
			return msg
		}
	}
	return firstNonEmpty(traeStringValue(event, "message", "msg"), "未知错误")
}

// testTraeAccountConnection 是账号「连接测试」的 Trae 实现。
//
// 走真实对话链路（llm_utils_chat + function 回退），把上游回答作为测试内容回显；
// 不复用 OpenAI 兼容测试，那条路径会索要 API Key（Trae 是 OAuth 账号）。
func (s *AccountTestService) testTraeAccountConnection(c *gin.Context, account *Account, modelID, prompt string) error {
	site := traeSiteForPlatform(account.Platform)
	if site == nil {
		return s.sendErrorAndEnd(c, "Trae 平台配置缺失")
	}
	token := traeTokenOf(account.Credentials)
	if token.Access == "" {
		return s.sendErrorAndEnd(c, "账号缺少登录态，请重新授权")
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = "hi"
	}
	model := strings.TrimSpace(modelID)
	if model == "" {
		model = firstTraeModelID(account)
	}
	if model == "" {
		return s.sendErrorAndEnd(c, "没有可用模型，请先同步模型目录")
	}

	body, err := json.Marshal(map[string]any{
		"messages": []map[string]any{{"role": "user", "content": prompt}},
		"model":    model,
	})
	if err != nil {
		return s.sendErrorAndEnd(c, "构造测试请求失败: "+err.Error())
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: model})

	var lastErr error
	for _, function := range traeChatFunctions {
		payload, buildErr := buildTraeChatPayload(account, body, model, function)
		if buildErr != nil {
			return s.sendErrorAndEnd(c, buildErr.Error())
		}
		resp, callErr := traeChatUpstream(c.Request.Context(), site, token, payload)
		if callErr != nil {
			return s.sendErrorAndEnd(c, callErr.Error())
		}
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
			_ = resp.Body.Close()
			code := traeUpstreamCode(raw)
			lastErr = fmt.Errorf("上游 HTTP %d: %s", resp.StatusCode, traeTrimBody(raw))
			if traeChatFunctionFallbackCodes[code] {
				continue
			}
			return s.sendErrorAndEnd(c, lastErr.Error())
		}

		var content strings.Builder
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			payloadText := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payloadText == "" || payloadText == "[DONE]" {
				continue
			}
			var event map[string]any
			if err := json.Unmarshal([]byte(payloadText), &event); err != nil {
				continue
			}
			if msg := traeEventError(event); msg != "" {
				_ = resp.Body.Close()
				return s.sendErrorAndEnd(c, msg)
			}
			if text := traeDeltaText(event); text != "" {
				content.WriteString(text)
				s.sendEvent(c, TestEvent{Type: "content", Text: text})
			}
		}
		_ = resp.Body.Close()

		if strings.TrimSpace(content.String()) == "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: "（上游未返回文本内容）"})
		}
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("上游拒绝了这次请求")
	}
	return s.sendErrorAndEnd(c, lastErr.Error())
}

// firstTraeModelID 取账号映射里的第一个模型（无映射时返回空）。
func firstTraeModelID(account *Account) string {
	if account == nil {
		return ""
	}
	for id := range account.GetModelMapping() {
		if strings.TrimSpace(id) != "" {
			return id
		}
	}
	return ""
}

