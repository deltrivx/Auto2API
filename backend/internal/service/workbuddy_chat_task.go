package service

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// WorkBuddy 聊天类任务的完成路径。
//
// 聊天任务（chat_5 / RichMeow_Chat / Model_chat_*）需要一条**真实的会话**：
// 先建会话、真发一次对话拿到服务端返回的 conversationId 与回复，再按
// send → message → response 三段上报事件。上游校验 requestId / messageId
// 必须是真实会话的 id，凭空捏造不会计数。
//
// 这里的 ideType 是 web-Agents（与桌面/WEB 都不同），是上游对
// 「智能体会话」这一类事件的专用域。

// workBuddyChatDomainProfile 返回聊天事件使用的设备信封。
//
// 上游对聊天事件使用 web-Agents 域，但 machineId 仍取该账号的桌面
// machineId——与参考实现一致，换了反而会让同账号出现两台设备。
func workBuddyChatDomainProfile(account *Account, fp workBuddyFingerprint) workBuddyDomainProfile {
	return workBuddyDomainProfile{
		MachineID: fp.MachineID,
		IDE:       "web-Agents",
		IDEType:   "web-Agents",
		UA:        fp.UA,
		OS:        fp.OS,
		OSVersion: fp.OSVersion,
		Arch:      fp.Arch,
	}
}

// WorkBuddyChatOnce 完成一次真实对话，返回会话 id、提问与回复。
//
// 走与网关相同的上游端点，因此这次调用本身也会产生正常的用量记录。
func WorkBuddyChatOnce(ctx context.Context, account *Account, prompt, model string) (convID, reply string, err error) {
	if strings.TrimSpace(model) == "" {
		model = "glm-5.2"
	}
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return "", "", fmt.Errorf("unsupported platform: %s", account.Platform)
	}

	// 1. 建会话拿 conversationId。
	convID, err = workBuddyCreateConversation(ctx, &http.Client{Timeout: 30 * time.Second}, account, site.Endpoint)
	if err != nil {
		return "", "", fmt.Errorf("创建会话失败: %w", err)
	}

	// 2. 发一次对话，收集回复正文（用于计算 token 与事件字段）。
	reply, err = workBuddySendChatMessage(ctx, account, site, convID, prompt, model)
	if err != nil {
		return convID, "", fmt.Errorf("发送对话失败: %w", err)
	}
	return convID, reply, nil
}

// workBuddySendChatMessage 向会话发送一条消息并读取回复。
func workBuddySendChatMessage(ctx context.Context, account *Account, site *workBuddySite, convID, prompt, model string) (string, error) {
	token := account.workBuddyTokenOf()
	body := fmt.Sprintf(
		`{"conversationId":%q,"model":%q,"stream":true,"messages":[{"role":"user","content":%q}]}`,
		convID, model, prompt)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		workBuddyConsoleChatURL(site.Endpoint), strings.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", workBuddySiteDomain(site))
	req.Header.Set("User-Agent", workBuddyReportUserAgent)

	resp, err := (&http.Client{Timeout: 120 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	return readWorkBuddySSEContent(resp.Body)
}

// WorkBuddyReportChatEvents 上报一次完整对话的三段事件。
//
// 顺序固定为 send → message → response，时间戳逐个递增：上游按这个顺序
// 判定「用户发问 → 客户端收到消息 → 模型返回答案」。
func WorkBuddyReportChatEvents(ctx context.Context, account *Account, convID, prompt, reply, model string) error {
	if convID == "" {
		return fmt.Errorf("会话 id 为空")
	}
	if strings.TrimSpace(model) == "" {
		model = "glm-5.2"
	}
	reqID := "cmb-" + workBuddyUUIDv5(workBuddyFingerprintNamespace, fmt.Sprintf("req:%s:%d", convID, time.Now().UnixNano()))
	msgID := "cmb-" + workBuddyUUIDv5(workBuddyFingerprintNamespace, fmt.Sprintf("msg:%s:%d", convID, time.Now().UnixNano()))

	inTok := maxInt(1, len(prompt)/4)
	outTok := maxInt(1, len(reply)/4)

	common := map[string]any{
		"conversationId":   convID,
		"requestId":        reqID,
		"requestModelId":   model,
		"requestModelName": strings.ToUpper(model),
	}

	events := []workBuddyReportEvent{
		{
			Code:   "chat_request_send",
			Domain: workBuddyDomainChat,
			Fields: mergeFields(common, map[string]any{
				"mode":            "craft",
				"inputLength":     len(prompt),
				"customAgentName": "",
			}),
		},
		{
			Code:   "chat_message_send",
			Domain: workBuddyDomainChat,
			Fields: mergeFields(common, map[string]any{
				"messageId":            msgID,
				"historyCount":         1,
				"isContextTruncated":   false,
				"currentStepCount":     1,
				"traceId":              reqID,
				"rootRequestId":        reqID,
				"parentConversationId": convID,
				"agentName":            "cli",
				"agentType":            "main",
			}),
		},
		{
			Code:   "chat_request_response",
			Domain: workBuddyDomainChat,
			Fields: mergeFields(common, map[string]any{
				"toolCallCount": 0,
				"inputToken":    inTok,
				"outputToken":   outTok,
				"totalToken":    inTok + outTok,
			}),
		},
	}
	return WorkBuddyReportEvents(ctx, account, events)
}

// mergeFields 合并两组字段，后者覆盖前者。
func mergeFields(base, extra map[string]any) map[string]any {
	out := make(map[string]any, len(base)+len(extra))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// workBuddyChatPrompts 是聊天任务使用的提问池。
//
// 用真实的编程/办公问题：内容越自然，上报的 inputLength 与 token 数
// 越贴近真实使用。
var workBuddyChatPrompts = []string{
	"帮我把这段 Python 代码改成异步实现",
	"解释一下什么是闭包，给个例子",
	"如何优化 SQL 查询性能",
	"写一个正则匹配邮箱地址",
	"解释 TCP 三次握手的过程",
	"帮我设计一个简单的缓存方案",
}

// workBuddyChatPromptFor 按序号取一个提问（循环使用）。
func workBuddyChatPromptFor(index int) string {
	if len(workBuddyChatPrompts) == 0 {
		return "你好"
	}
	return workBuddyChatPrompts[index%len(workBuddyChatPrompts)]
}

// 保证包内引用（避免未使用告警）。
var _ = rand.Intn

// readWorkBuddySSEContent 从 SSE 流里抽取回复正文。
//
// 上游把增量放在 choices[0].delta.content；reasoning_content 不计入正文，
// 但同样说明「模型确实回复过」，因此只累加 content。
func readWorkBuddySSEContent(body io.Reader) (string, error) {
	var sb strings.Builder
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
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
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			sb.WriteString(c.Delta.Content)
		}
	}
	if err := scanner.Err(); err != nil {
		return sb.String(), err
	}
	return sb.String(), nil
}
