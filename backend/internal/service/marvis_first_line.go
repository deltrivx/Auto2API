package service

import (
	"encoding/json"
	"strings"
)

// Marvis 的内容过滤拦的是「system 首行里的身份词对」。
//
// 与 magpie 插件（MYCF711/magpie-marvis，issue #22 / #26）实测一致：
//
//	"You are an AI agent powered by DeepSeek Harness."  → 403
//	同一句放第 2 行 / 加前缀 / 换 user 角色              → 200
//
// 判据是「首行里的词对」，不是整句模板。因此这里不去猜哪句能过，而是照插件
// 的做法：被拦之后把首行换成一句不含任何 agent 产品名的中性句，重试一次。
const marvisFallbackFirstLine = "You are Marvis, Tencent's AI assistant."

// marvisIsContentFiltered 判断上游响应是否是内容过滤（而不是登录或密钥问题）。
//
// 418 与 4100404 是上游对内容拦截的两种回法（插件 AUTH_EXPIRED 与
// CONTENT_FILTERED 两个正则分别覆盖）。
func marvisIsContentFiltered(status int, body []byte) bool {
	if status == 403 || status == 418 {
		text := string(body)
		if strings.Contains(text, "4100404") || strings.Contains(text, "风控") {
			return true
		}
	}
	return false
}

// marvisWithFirstLine 把第一条 system 消息的**首行**整行替换。
//
// 只动第一条 system 消息：再往后的 system 不是身份句的常见位置。
// 没有 system 消息时返回 changed=false，调用方据此决定要不要重试。
func marvisWithFirstLine(body []byte, text string) ([]byte, bool) {
	if len(body) == 0 || strings.TrimSpace(text) == "" {
		return body, false
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, false
	}
	messages, ok := payload["messages"].([]any)
	if !ok {
		return body, false
	}
	changed := false
	for i := range messages {
		msg, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		if role, _ := msg["role"].(string); role != "system" {
			continue
		}
		content, ok := msg["content"].(string)
		if !ok {
			continue
		}
		// 整行替换第一行，第二行起原样保留。
		rest := content
		if idx := strings.IndexByte(content, '\n'); idx >= 0 {
			rest = content[idx:]
		} else {
			rest = ""
		}
		msg["content"] = text + rest
		messages[i] = msg
		changed = true
		break
	}
	if !changed {
		return body, false
	}
	payload["messages"] = messages
	out, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return out, true
}
