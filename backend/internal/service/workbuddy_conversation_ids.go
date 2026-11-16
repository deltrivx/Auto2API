package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
)

// WorkBuddy 官方面板的每次对话都带三个会话头（magpie index.mjs 的 attendChat /
// setIds）。缺失时请求没有「会话」归属，渠道校验会把客户端当成非官方程序，
// 表现就是 400 code 11128。
//
// 移植自 magpie：
//
//	const SALT = randomBytes(16).toString("hex")   // 每进程随机
//	function idFor(kind, key) { sha256(SALT|kind| + key).slice(0,32) }
//	function attendChat(headers, chat, session, who) {
//	  ... headers.set("X-Conversation-Message-ID", msg)
//	      headers.set("X-Request-ID", msg)
//	      headers.set("X-Conversation-Request-ID", ...)
//	      headers.set("X-Conversation-ID", ...)
//	}
//
// SALT 每次进程启动随机，值本身对上游无意义，只要求同进程内稳定。

var workBuddyIDSeqSalt = newWorkBuddySalt()

func newWorkBuddySalt() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b)
}

// workBuddyIDFor 对应 magpie 的 idFor：sha256(SALT|kind|key) 前 32 位 hex。
func workBuddyIDFor(kind, key string) string {
	h := sha256.New()
	_, _ = h.Write([]byte(workBuddyIDSeqSalt + "|" + kind + "|"))
	_, _ = h.Write([]byte(key))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// workBuddyRandomID 对应 magpie 的 hex()：16 字节随机值的 hex。
func workBuddyRandomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return workBuddyIDFor("rand", "fallback")
	}
	return hex.EncodeToString(b)
}

// applyWorkBuddyConversationHeaders 写入三个会话头。
//
// 只在调用方还没设置时写，避免覆盖上游回传的会话标识。
func applyWorkBuddyConversationHeaders(h http.Header, payload map[string]any, account *Account) {
	// 会话键：用 conversationId（已有则沿用）+ 首条用户消息，让同一对话稳定、
	// 不同对话区分。magpie 用 conversationParts/turnParts 做的也是这件事。
	convKey := "wb"
	if v, ok := payload["conversationId"].(string); ok && v != "" {
		convKey = v
	}
	turnKey := convKey
	if msgs, ok := payload["messages"].([]any); ok {
		for _, raw := range msgs {
			if m, ok := raw.(map[string]any); ok {
				if role, _ := m["role"].(string); role == "user" {
					if b, err := json.Marshal(m["content"]); err == nil {
						turnKey = convKey + "|" + string(b)
					}
					break
				}
			}
		}
	}
	// 账号作用域：同一对话换账号重试时 id 需要不同。
	who := ""
	if account != nil {
		who = account.workBuddyUIDOf() + "|" + account.workBuddyEnterpriseIDOf()
	}

	msg := workBuddyRandomID()
	if h.Get("X-Conversation-Message-ID") == "" {
		h.Set("X-Conversation-Message-ID", msg)
	}
	if h.Get("X-Request-ID") == "" {
		h.Set("X-Request-ID", msg)
	}
	if h.Get("X-Conversation-Request-ID") == "" {
		h.Set("X-Conversation-Request-ID", workBuddyIDFor("turn", who+"|"+turnKey))
	}
	if h.Get("X-Conversation-ID") == "" {
		h.Set("X-Conversation-ID", workBuddyIDFor("conversation", who+"|"+convKey))
	}
}

// applyWorkBuddyHeadersWithBody 在 applyWorkBuddyHeaders 之上补会话头。
//
// applyWorkBuddyHeaders 只拿得到 header，而会话 id 需要请求体里的
// conversationId 与消息内容，故在出站点单独调用。
func applyWorkBuddyHeadersWithBody(account *Account, h http.Header, body []byte) bool {
	if !applyWorkBuddyHeaders(account, h) {
		return false
	}
	if len(body) > 0 {
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err == nil {
			applyWorkBuddyConversationHeaders(h, payload, account)
		}
	}
	return true
}
