package service

import (
	"context"
	"fmt"
	"time"
)

// WorkBuddy 桌面端任务的事件链。
//
// 这些任务（桌面端对话、发现应用、召唤专家等）在官方客户端里由真人操作
// 触发一连串埋点。事件序列本身是稳定的协议，只要按正确顺序上报、并带上
// 真实会话 id，就能推进任务进度——不需要真的在 Windows 上操作 GUI。
//
// 来源：L0NE-6/WorkBuddy-Daily 的 desktop_chat_sequence 等函数。

// WorkBuddyDesktopChatSequence 返回「桌面端成功对话」的 6 连事件。
//
// 顺序固定：agent_task_created → chat_message_send → chat_request_send →
// chat_message_response → chat_message_status → chat_request_response。
// 上游按这个顺序判定一次完整对话，缺项或乱序都不计数。
func WorkBuddyDesktopChatSequence(convID, reqID, msgID string) []workBuddyReportEvent {
	now := time.Now().UnixMilli()
	modelID := "fast-model"

	base := map[string]any{
		"traceId":              reqID,
		"rootRequestId":        reqID,
		"parentConversationId": convID,
		"agentName":            "cli",
		"agentType":            "main",
	}
	mk := func(code string, extra map[string]any) workBuddyReportEvent {
		return workBuddyReportEvent{
			Code:   code,
			Domain: workBuddyDomainDesktop,
			Fields: mergeFields(base, extra),
		}
	}

	return []workBuddyReportEvent{
		mk("agent_task_created", map[string]any{
			"source": "LOCAL", "name": "working", "task_target": "local", "mode": "craft",
			"requestModelId": modelID, "requestModelName": modelID,
			"has_repo": false, "repo_type": "none", "workspace_type": "empty",
			"has_connector": false, "connector_types": []string{},
			"has_mention": false, "mention_types": []string{},
			"has_template": false, "action": "", "template_name": "",
			"has_expert": false, "expert_id": "", "expert_name": "", "expert_industry_id": "",
			"has_skill": false, "skill_names": []string{},
			"conversationId": convID, "messageId": msgID,
			"buddyId": "", "buddyName": "",
		}),
		mk("chat_message_send", map[string]any{
			"messageId": msgID + "-assistant", "historyCount": 0,
			"isContextTruncated": false, "currentStepCount": 1,
		}),
		mk("chat_request_send", map[string]any{
			"inputLength": 24, "isPlan": false, "isAutoExecuteTerminal": false,
			"isAutoModify": false, "codebaseEnable": false, "maxToken": 0,
			"maxSteps": 500, "temperature": 0, "maxRetries": 0,
			"mentionContexts": []string{}, "knowledgeId": []string{}, "knowledgeName": []string{},
			"codebaseId": "", "mentionContextCount": 0, "command": "",
			"recommendId": "", "skillId": "", "skillCount": 0, "totalCount": 0,
		}),
		mk("chat_message_response", map[string]any{
			"messageId": msgID + "-assistant", "responseModelId": modelID,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
			"firstTokenAt": now, "conversationId": convID,
		}),
		mk("chat_message_status", map[string]any{
			"messageId": msgID + "-assistant", "messageErrorCode": "0",
		}),
		mk("chat_request_response", map[string]any{
			"mode": "craft", "toolCallCount": 0,
			"inputToken": 120, "outputToken": 80, "totalToken": 200,
			"cachedTokens": 0, "cachedWriteTokens": 0, "cachedMissTokens": 0,
			"isSuccessful": true, "messageErrorCode": "", "finishReason": "stop",
		}),
	}
}

// WorkBuddyDesktopChatTask 执行「桌面端对话」类任务。
//
// 先建一个真实会话拿到 conversationId（上游校验它必须真实存在），
// 再上报 6 连事件。
func WorkBuddyDesktopChatTask(ctx context.Context, account *Account) error {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	convID, err := workBuddyCreateConversation(ctx, workBuddyActivityClient(), account, site.Endpoint)
	if err != nil {
		return fmt.Errorf("创建会话失败: %w", err)
	}
	reqID := workBuddyNewMessageID()
	msgID := workBuddyNewMessageID()
	return WorkBuddyReportEvents(ctx, account, WorkBuddyDesktopChatSequence(convID, reqID, msgID))
}

// workBuddyNewMessageID 生成一个上游形态的消息 id（cmb- 前缀）。
func workBuddyNewMessageID() string {
	return "cmb-" + workBuddyUUIDv5(workBuddyFingerprintNamespace,
		fmt.Sprintf("msg:%d", time.Now().UnixNano()))
}
