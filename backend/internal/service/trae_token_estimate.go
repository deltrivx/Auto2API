package service

import "github.com/tidwall/gjson"

// Trae 的 token 用量估算。
//
// 上游 Trae 的对话接口（IDE 的 llm_utils_chat）不返回 token 计量事件：
// SSE 里只有内容增量，结束时也没有 usage 汇总。实测真实调用返回的
// usage 恒为 {0,0,0}，而它自己的额度钱包确实在扣费。
//
// 因此本地按字符数估算 token。误差约 20~30%，只用于展示与统计，
// 不能作为真金白银的计费依据（对账以钱包为准）。

// traeEstimateTokens 按字符构成估算 token 数。
//
// CJK（中日韩）字符基本 1 字 ≈ 1 token；拉丁字母与常见 ASCII 约 4 字符 ≈ 1 token；
// 其余字符（emoji、符号）按 1 字符 ≈ 1 token 处理，宁可略高不要低估。
func traeEstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	var cjk, ascii, other int
	for _, r := range text {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // CJK 统一表意文字
			r >= 0x3400 && r <= 0x4DBF, // CJK 扩展 A
			r >= 0x3040 && r <= 0x30FF, // 日文假名
			r >= 0xAC00 && r <= 0xD7AF: // 韩文音节
			cjk++
		case r < 128:
			ascii++
		default:
			other++
		}
	}
	tokens := cjk + other + (ascii+3)/4
	if tokens == 0 {
		return 1
	}
	return tokens
}

// traeEstimatePromptTokens 估算整段 messages 的输入 token。
//
// 每轮对话内容与 OpenAI 的聊天模板结构完全一致（同样的 messages 数组），
// 顶层消息对象是模型 api 文档里标明的固定结构，这里直接按该结构读取。
// 除正文外，每条消息的 role 与分隔符也占少量 token，按每条约 4 token 计。
func traeEstimatePromptTokens(body []byte) int {
	messages := gjson.GetBytes(body, "messages")
	if !messages.Exists() {
		return 0
	}
	total := 0
	count := 0
	messages.ForEach(func(_, msg gjson.Result) bool {
		count++
		content := msg.Get("content")
		if content.Type == gjson.String {
			total += traeEstimateTokens(content.String())
		} else if content.IsArray() {
			// 多模态内容：把可读文本拼起来，图片按固定开销估计。
			content.ForEach(func(_, part gjson.Result) bool {
				if t := part.Get("text"); t.Exists() {
					total += traeEstimateTokens(t.String())
				} else if part.Get("type").String() == "image_url" {
					total += 850 // 单张图片的经验估值
				}
				return true
			})
		}
		return true
	})
	if count > 0 {
		total += count * 4
	}
	return total
}
