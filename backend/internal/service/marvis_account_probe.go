package service

// 腾讯 Marvis 账号连接测试。
//
// 与 WorkBuddy 同样的动机：Marvis 是 OAuth 账号，没有 API Key，落进通用
// 测试分支会先被要求提供 API Key（"No API key available"）或走到别的协议
// 分支上，最终只会得到一个与真实转发无关的错误。所以这里给 Marvis 一条
// 自己的路径，用与网关转发**完全相同**的请求头与请求体去打分。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	// marvisTestDefaultPrompt 是连接测试的默认提示词。
	marvisTestDefaultPrompt = "你好"
	// marvisTestPreviewLimit 是回显给前端的文本上限。
	marvisTestPreviewLimit = 200
	// marvisTestTimeout 覆盖上游首字节较慢的情况。
	marvisTestTimeout = 90 * time.Second
)

// defaultMarvisTestModel 返回账号可用的测试模型。
//
// 优先用账号 model_mapping 里的第一个真实模型名；没有映射时退回表头那条，
// 它对应 main-auto，是上游最稳的槽位。
func defaultMarvisTestModel(account *Account) string {
	mapping := account.GetModelMapping()
	for display := range mapping {
		if strings.TrimSpace(display) != "" {
			return display
		}
	}
	if len(marvisModels) > 0 {
		return marvisModels[0].Display
	}
	return ""
}

// testMarvisAccountConnection 跑一次真实的流式对话并回显结果。
func (s *AccountTestService) testMarvisAccountConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	ctx := c.Request.Context()

	if account.Type != AccountTypeOAuth {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported account type: %s", account.Type))
	}
	creds := marvisCredentialsOf(account)
	if creds.AccessToken == "" || creds.OpenID == "" {
		return s.sendErrorAndEnd(c, "账号缺少 access_token 或 openid，请重新登录")
	}

	testModel := strings.TrimSpace(modelID)
	if testModel == "" {
		testModel = defaultMarvisTestModel(account)
	}
	if testModel == "" {
		return s.sendErrorAndEnd(c, "No model available for this Marvis account")
	}
	// 已下线的槽位直接拒绝，别让测试去撞一个必然 400 的请求。
	if IsMarvisUnavailableModel(testModel) {
		return s.sendErrorAndEnd(c, fmt.Sprintf("模型 %s 已不可用，请选择其他模型", testModel))
	}
	// 与转发路径一致：把显示名映射成上游槽位名。
	slot := marvisResolveSlot(testModel)
	if mapped := account.GetMappedModel(testModel); mapped != "" {
		slot = marvisResolveSlot(mapped)
	}

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModel})

	userPrompt := strings.TrimSpace(prompt)
	if userPrompt == "" {
		userPrompt = marvisTestDefaultPrompt
	}

	body := map[string]any{
		"model":  slot,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": userPrompt},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}

	reqCtx, cancel := context.WithTimeout(ctx, marvisTestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, MarvisBaseURLForAccount(account)+MarvisChatPath, bytes.NewReader(payload))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	// 复用转发路径的请求头：Ual-Access-* 四件套与每次现签的 MarvisExt。
	applyMarvisHeaders(account, req.Header)

	client := &http.Client{Timeout: marvisTestTimeout}
	resp, err := client.Do(req)
	if err != nil {
		msg := fmt.Sprintf("Upstream request failed: %s", err.Error())
		logAccountTestError(c, msg)
		return s.sendErrorAndEnd(c, msg)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(raw))
		logAccountTestError(c, msg)
		s.sendEvent(c, TestEvent{Type: "error", Error: msg})
		return fmt.Errorf("%s", msg)
	}

	// 上游是 SSE：每行 "data: {json}"。
	var collected strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if chunk == "" || chunk == "[DONE]" {
			continue
		}
		var parsed struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(chunk), &parsed); err != nil {
			continue
		}
		for _, ch := range parsed.Choices {
			if ch.Delta.Content != "" {
				collected.WriteString(ch.Delta.Content)
			}
		}
		if collected.Len() >= marvisTestPreviewLimit {
			break
		}
	}

	preview := collected.String()
	if r := []rune(preview); len(r) > marvisTestPreviewLimit {
		preview = string(r[:marvisTestPreviewLimit])
	}
	if preview == "" {
		preview = "（上游返回成功，但未包含文本内容）"
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: preview})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}
