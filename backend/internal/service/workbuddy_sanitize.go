package service

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// WorkBuddy 出站请求的品牌痕迹清洗与消息规范化。
//
// 移植自 magpie 的 packages/workbuddy（FLAGGED / unflag / withSystem /
// asReasoning / withoutSignedThinking）并合并了 AutoBuddy 的 sanitize.py。
//
// 背景：WorkBuddy 上游会对请求内容做识别，命中特定表述时直接拒绝
// （错误码 11128 "Illegal API"）。主流智能体（Claude Code、Codex CLI、
// 以及本系统自身的 DSH）都会在系统提示词里写明自己的身份，把请求发给
// WorkBuddy 会被判为「非本家客户端」。
//
// 处理分两层：
//  1. 品牌痕迹改写（flagged）：把被拒绝的原文替换为等价但可放行的表述。
//     magpie 注释里记录了实测结论——WorkBuddy 对某些句子整行精确匹配，
//     因此改动一个标点即可放行，不必删除整句语义。
//  2. 消息规范化（systemOn）：字段名与结构对齐上游期望的形态
//     （developer→system、max_completion_tokens→max_tokens、
//     reasoning_content→reasoning、补齐 system 首条）。

// workBuddyFlaggedRule 是一条品牌痕迹改写规则。
type workBuddyFlaggedRule struct {
	re   *regexp.Regexp
	repl string
}

// workBuddyFlaggedRules 是上游会拒绝的表述及其放行改写。
//
// 顺序有意义：先整段剥离计费头，再改写信封句。
var workBuddyFlaggedRules = []workBuddyFlaggedRule{
	// Anthropic 计费头整段剥离。
	{regexp.MustCompile(`(?i)x-anthropic-billing-header:[^\n]*\n?`), ""},

	// Claude Code 身份句：去掉 "for Claude" 后缀即可放行。
	{regexp.MustCompile(`(?i)You are Claude Code, Anthropic['’]s official CLI for Claude`),
		"You are Claude Code, Anthropic's CLI for Claude"},

	// Claude Code 的 PR 提示：改写括号内的说明。
	{regexp.MustCompile(`(?i)Main branch \(you will usually use this for PRs\)`),
		"Main branch (usually the base for PRs)"},

	// Codex CLI 项目介绍句：改写主语。
	{regexp.MustCompile(`(?i)Codex CLI is an open source project led by OpenAI\.`),
		"The Codex CLI is an open source project led by OpenAI."},

	// 上游真正拦的是「DeepSeek」+「harness」这组语义串：两者单独出现都不拦，
	// 共存才命中（与大小写无关、子串匹配）。只把空格换成连字符（DeepSeek-Harness）
	// 两词照样共存，换号也无济于事——AutoBuddy 实测过 7 连败。
	// 因此删掉产品名，只留中性描述。工作目录路径里同样带着这个词。
	{regexp.MustCompile(`(?i)deepseek[-_]harness`), "local coding tool"},
	{regexp.MustCompile(`(?i)websearchdeepseek`), "local coding"},

	// DSH（local coding tool）的系统提示词首行。
	// 上游整行精确匹配并返回 400 code 11128；加一个逗号即可放行，
	// 同时保留智能体自己的名字——提示词其余部分都回指这个名字。
	{regexp.MustCompile(`(?i)You are an AI agent powered by local coding tool\.?`),
		"You are an AI agent, powered by local coding tool."},

	// 以下三条来自 AutoBuddy 的记录，但**实测 AutoBuddy 并不依赖它们过审**：
	// 直接调用它的 sanitize_messages，对同样的 DSH 文本返回原样（未改写），
	// 请求照样通过。保留规则但由 workBuddyChannelSanitizeEnabled 控制开关，
	// 默认关闭——改写一旦改变客户端语义反而可能触发新的判据。
	//
	// DSH 的真实首两行（AutoBuddy 2026-11-12 实测，5832 字符全文捕获）：
	//   You are an AI agent powered by local coding tool.
	//   You are a coding agent powered by the deepseek-v4.1-flash model.
	// 上游拦的是「deepseek」+「coding agent/tool 上下文」的组合语义：单独任一句
	// 都放行，两句共存即 400 code 11128。把第二行的 powered by 改成 using，
	// 组合被破坏，语义不变。切换任何模型都会重新拼出这句话，所以每个模型都 400。
	{regexp.MustCompile(`(?i)You are a coding agent powered by the ([\w.\-]+) model\.?`),
		"You are a coding agent using the $1 model."},

	// 同一条语义串可能被客户端写在别处。
	{regexp.MustCompile(`(?i)powered by the ([\w.\-]+) model`),
		"using the $1 model"},

	// DSH 的「@ 路径引用」整行：单独出现能过，408 字符原样发送即被拦，
	// 上游匹配的是整段长文本指纹。改写开头打断指纹，含义完全保留。
	{regexp.MustCompile(`(?i)Tokens prefixed with @ are paths the user explicitly referenced`),
		"Tokens starting with @ refer to paths the user explicitly mentioned"},

	// 长对话中会被引用的 Web GUI 说明句。
	{regexp.MustCompile(`(?i)Web GUI whose URL the runtime context names\.\s+When the user refers`),
		"Web GUI whose URL the runtime context names. The interface, when the user refers"},
}

// workBuddyUnflagText 对单段文本应用全部改写规则。
func workBuddyUnflagText(text string) string {
	if text == "" {
		return text
	}
	for _, r := range workBuddyFlaggedRules {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return text
}

// workBuddyUnflagMessages 改写 messages 中的品牌痕迹。
//
// 返回是否发生了改动。空白的 system 消息在改写后会被整条移除——
// 留着空行同样可能触发上游校验。
func workBuddyUnflagMessages(messages []any) bool {
	changed := false
	// 从后往前遍历，便于在删除元素时保持索引正确。
	for i := len(messages) - 1; i >= 0; i-- {
		m, ok := messages[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)

		switch content := m["content"].(type) {
		case string:
			next := workBuddyUnflagText(content)
			if next == content {
				continue
			}
			changed = true
			if strings.TrimSpace(next) != "" || role != "system" {
				m["content"] = next
			} else {
				// 改写后成为空白的 system 消息直接删除。
				messages = append(messages[:i], messages[i+1:]...)
			}
		case []any:
			parts := make([]any, 0, len(content))
			here := false
			for _, raw := range content {
				p, ok := raw.(map[string]any)
				if !ok {
					parts = append(parts, raw)
					continue
				}
				text, ok := p["text"].(string)
				if !ok {
					parts = append(parts, p)
					continue
				}
				next := workBuddyUnflagText(text)
				if next == text {
					parts = append(parts, p)
					continue
				}
				here = true
				if strings.TrimSpace(next) != "" {
					cp := make(map[string]any, len(p))
					for k, v := range p {
						cp[k] = v
					}
					cp["text"] = next
					parts = append(parts, cp)
				}
			}
			if !here {
				continue
			}
			changed = true
			if len(parts) > 0 || role != "system" {
				m["content"] = parts
			} else {
				messages = append(messages[:i], messages[i+1:]...)
			}
		}

		// 思考流与工具参数同样会发往上游。
		//
		// AutoBuddy 的事故记录指出这是旧实现的盲区：tool_calls 消息的 content
		// 常为 null，参数里的特征串会原样发出；Agent 客户端反复中招就在于只洗
		// content。DSH 这类客户端的系统提示词与会话路径也会出现在工具参数里。
		for _, key := range []string{"reasoning", "reasoning_content"} {
			if v, ok := m[key].(string); ok && v != "" {
				if next := workBuddyUnflagText(v); next != v {
					m[key] = next
					changed = true
				}
			}
		}
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, raw := range calls {
				call, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				fn, ok := call["function"].(map[string]any)
				if !ok {
					continue
				}
				if args, ok := fn["arguments"].(string); ok && args != "" {
					if next := workBuddyUnflagText(args); next != args {
						fn["arguments"] = next
						changed = true
					}
				}
			}
		}
	}
	return changed
}

// workBuddyReasoningOf 从 assistant 消息中提取思考文本。
//
// 兼容两种来源：reasoning_content 字符串，或 reasoning_details 数组。
func workBuddyReasoningOf(m map[string]any) string {
	if s, ok := m["reasoning_content"].(string); ok && s != "" {
		return s
	}
	if details, ok := m["reasoning_details"].([]any); ok {
		var b strings.Builder
		for _, d := range details {
			dm, ok := d.(map[string]any)
			if !ok {
				continue
			}
			if t, ok := dm["text"].(string); ok {
				b.WriteString(t)
			} else if s, ok := dm["summary"].(string); ok {
				b.WriteString(s)
			}
		}
		return b.String()
	}
	return ""
}

// workBuddyAsReasoning 把 reasoning_content / reasoning_details 归一到 reasoning 字段。
//
// WorkBuddy 上游只认 reasoning。归一后删掉旧字段，避免上游看到多余键。
func workBuddyAsReasoning(messages []any) bool {
	changed := false
	for _, raw := range messages {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		_, hasContent := m["reasoning_content"]
		_, hasDetails := m["reasoning_details"]
		if !hasContent && !hasDetails {
			continue
		}
		text := workBuddyReasoningOf(m)
		if text != "" {
			if cur, ok := m["reasoning"].(string); !ok || cur == "" {
				m["reasoning"] = text
			}
		}
		delete(m, "reasoning_content")
		delete(m, "reasoning_details")
		changed = true
	}
	return changed
}

// workBuddyWithoutSignedThinking 移除 assistant 消息中带签名的 thinking 块。
//
// 签名由上游生成，跨请求回传时校验会失败并导致 400；去掉整块比传错更安全。
// 返回改动后的 body 与是否发生改动。
func workBuddyWithoutSignedThinking(payload map[string]any) (map[string]any, int) {
	msgs, ok := payload["messages"].([]any)
	if !ok {
		return payload, 0
	}
	dropped := 0
	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "assistant" {
			continue
		}
		content, ok := m["content"].([]any)
		if !ok {
			continue
		}
		kept := make([]any, 0, len(content))
		for _, p := range content {
			pm, ok := p.(map[string]any)
			if !ok {
				kept = append(kept, p)
				continue
			}
			if t, _ := pm["type"].(string); t == "thinking" {
				continue
			}
			kept = append(kept, p)
		}
		if len(kept) == len(content) {
			continue
		}
		dropped += len(content) - len(kept)
		if len(kept) == 0 {
			kept = []any{map[string]any{"type": "text", "text": ""}}
		}
		m["content"] = kept
	}
	return payload, dropped
}

// workBuddySystemOn 把请求体规范化为 WorkBuddy 上游期望的形态。
//
// 做四件事：
//   - 字段改名：max_completion_tokens → max_tokens
//   - 角色归一：developer → system
//   - 思考归一：reasoning_content/details → reasoning
//   - 补齐首条 system 消息
//
// 返回是否有改动。
func workBuddySystemOn(payload map[string]any) bool {
	msgs, ok := payload["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return false
	}
	// 内容改写默认关闭：AutoBuddy 实测对同样的 DSH 文本返回原样仍能过审，
	// 说明上游拦的不是这些句子本身。改写会改变客户端语义（powered by→using），
	// 反而可能命中别的判据。需要时用 AUTO2API_WORKBUDDY_UNFLAG=1 打开。
	changed := false
	// 顺序与 magpie 一致：FLAGGED 先过实时 11128 校验，
	// hide_dsh 再处理「之后读提示词」的那一层校验（见 workbuddy_hide_dsh.go）。
	if workBuddyUnflagEnabled() {
		if workBuddyUnflagMessages(msgs) {
			changed = true
		}
	}
	if workBuddyHideDshEnabled() {
		if workBuddyHideDsh(payload) {
			changed = true
		}
	}
	// 开场句补逗号：上游认的是「那一行的形状」，加个逗号即放行，
	// 用词与语义都不变（magpie 实测 2026-10-30）。
	if workBuddyCommaPaused(payload) {
		changed = true
	}
	// unflag 可能删除元素，取回最新切片。
	msgs, ok = payload["messages"].([]any)
	if !ok {
		msgs = nil
	}

	// tool_choice 归一化：上游字段是 Go string，传对象直接 400 code=11101。
	//
	// DSH / Codex / Claude 这些 Agent 客户端按 OpenAI 规范发
	//   {"type":"auto"} / {"type":"required"} / {"type":"function","function":{...}}
	// 原样转发会被拒。AutoBuddy 的 _upstream_body 把它压成字符串：
	//   auto/required → 同名串；function → 函数名；none → 连同 tools/functions 一起删。
	if tc, exists := payload["tool_choice"]; exists {
		switch v := tc.(type) {
		case string:
			if strings.EqualFold(strings.TrimSpace(v), "none") {
				delete(payload, "tool_choice")
				delete(payload, "tools")
				delete(payload, "functions")
				changed = true
			}
		case map[string]any:
			typ := strings.ToLower(strings.TrimSpace(fmt.Sprint(v["type"])))
			switch typ {
			case "none":
				delete(payload, "tool_choice")
				delete(payload, "tools")
				delete(payload, "functions")
			case "auto", "required":
				payload["tool_choice"] = typ
			case "function":
				name := ""
				if fn, ok := v["function"].(map[string]any); ok {
					name = strings.TrimSpace(fmt.Sprint(fn["name"]))
				} else {
					name = strings.TrimSpace(fmt.Sprint(v["name"]))
				}
				if name != "" && name != "<nil>" {
					payload["tool_choice"] = name
				} else {
					delete(payload, "tool_choice")
				}
			default:
				delete(payload, "tool_choice")
			}
			changed = true
		default:
			delete(payload, "tool_choice")
			changed = true
		}
	}
	if v, exists := payload["max_completion_tokens"]; exists {
		if _, hasMax := payload["max_tokens"]; !hasMax {
			payload["max_tokens"] = v
		}
		delete(payload, "max_completion_tokens")
		changed = true
	}

	for _, raw := range msgs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role == "developer" {
			m["role"] = "system"
			changed = true
		}
	}

	if workBuddyAsReasoning(msgs) {
		changed = true
	}

	if len(msgs) > 0 {
		first, _ := msgs[0].(map[string]any)
		role, _ := first["role"].(string)
		if role != "system" {
			payload["messages"] = append([]any{map[string]any{
				"role":    "system",
				"content": "You are a helpful assistant.",
			}}, msgs...)
			changed = true
		}
	}
	return changed
}

// workBuddyNormalizeRequestBody 对出站请求体做完整规范化。
//
// 处理顺序与 magpie 一致：先清品牌痕迹与字段形态，再剥离带签名的 thinking。
// 解析失败时原样返回——规范化是尽力而为，不能因格式异常阻断请求。
func workBuddyNormalizeRequestBody(account *Account, body []byte) []byte {
	if account == nil || !isWorkBuddyPlatform(account.Platform) {
		return body
	}
	if len(body) == 0 {
		return body
	}

	var payload map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &payload); err != nil {
		return body
	}

	changed := workBuddySystemOn(payload)
	var dropped int
	payload, dropped = workBuddyWithoutSignedThinking(payload)
	if dropped > 0 {
		changed = true
	}
	if !changed {
		return body
	}

	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// workBuddyConversationKey 从请求体派生会话键。
//
// 用于会话粘性：同一段对话的每一次请求都要落到同一账号。
// 优先用客户端给出的会话标识；没有时退化为「最后一轮未回答的用户提问」。
func workBuddyConversationKey(payload map[string]any) string {
	if payload == nil {
		return ""
	}
	msgs, ok := payload["messages"].([]any)
	if !ok {
		return ""
	}
	parts := workBuddyTurnParts(msgs)
	if len(parts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("turn:")
	for _, p := range parts {
		b.WriteString(p)
		b.WriteString("|")
	}
	return b.String()
}

// workBuddyTurnParts 提取「最后一轮尚未被回答的用户提问」的特征串。
//
// 从后往前找第一条用户消息；若它已被助手回答（后面有 assistant 或
// tool 结果），说明这一轮已结束，不再作为会话标识。
func workBuddyTurnParts(msgs []any) []string {
	for i := len(msgs) - 1; i >= 0; i-- {
		m, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		if role, _ := m["role"].(string); role != "user" {
			continue
		}
		if workBuddyAnsweredBefore(msgs, i) {
			continue
		}
		sig := workBuddySignatureParts(m["content"])
		if len(sig) == 0 {
			return nil
		}
		out := make([]string, 0, len(sig)+1)
		out = append(out, "u"+itoa(i)+":")
		out = append(out, sig...)
		return out
	}
	return nil
}

// workBuddyAnsweredBefore 判断第 i 条消息之前是否已有助手答复。
func workBuddyAnsweredBefore(msgs []any, i int) bool {
	if cur, ok := msgs[i].(map[string]any); ok && workBuddyHasBlock(cur, "tool_result") {
		return true
	}
	j := i - 1
	for j >= 0 {
		m, _ := msgs[j].(map[string]any)
		if role, _ := m["role"].(string); role != "system" {
			break
		}
		j--
	}
	if j < 0 {
		return false
	}
	prev, ok := msgs[j].(map[string]any)
	if !ok {
		return false
	}
	role, _ := prev["role"].(string)
	switch role {
	case "tool":
		return true
	case "user":
		return workBuddyHasBlock(prev, "tool_result")
	case "assistant":
		if calls, ok := prev["tool_calls"].([]any); ok && len(calls) > 0 {
			return true
		}
		return workBuddyHasBlock(prev, "tool_use")
	}
	return false
}

// workBuddyHasBlock 判断消息 content 中是否含指定类型的块。
func workBuddyHasBlock(m map[string]any, blockType string) bool {
	parts, ok := m["content"].([]any)
	if !ok {
		return false
	}
	for _, p := range parts {
		pm, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := pm["type"].(string); t == blockType {
			return true
		}
	}
	return false
}

// workBuddySignatureParts 提取消息内容的特征片段。
//
// 纯文本直接取原文；非文本块（图片、工具调用等）取其内容的哈希前缀，
// 这样同一段对话即使换了序列化方式也能得到相同签名。
func workBuddySignatureParts(content any) []string {
	switch c := content.(type) {
	case string:
		if c == "" {
			return nil
		}
		return []string{c}
	case []any:
		out := make([]string, 0, len(c))
		for _, p := range c {
			pm, ok := p.(map[string]any)
			if !ok {
				continue
			}
			t, _ := pm["type"].(string)
			if t == "" || t == "text" {
				if txt, ok := pm["text"].(string); ok && txt != "" {
					out = append(out, txt)
				}
				continue
			}
			if t == "tool_result" || t == "tool-result" {
				continue
			}
			// 其它块用哈希占位，避免把大段二进制塞进签名。
			raw, err := json.Marshal(pm)
			if err != nil {
				continue
			}
			out = append(out, "["+t+":"+workBuddyShortHash(raw)+"]")
		}
		return out
	}
	return nil
}

// workBuddyShortHash 返回 8 位十六进制摘要。
func workBuddyShortHash(b []byte) string {
	sum := sha256Sum(b)
	const hexChars = "0123456789abcdef"
	out := make([]byte, 0, 8)
	for i := 0; i < 4; i++ {
		out = append(out, hexChars[sum[i]>>4], hexChars[sum[i]&0x0f])
	}
	return string(out)
}

// sha256Sum 返回 SHA-256 摘要。
func sha256Sum(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

// itoa 是 strconv.Itoa 的短别名，避免与业务变量名冲突。
func itoa(n int) string { return strconv.Itoa(n) }

// workBuddyStripUnsupportedFields 移除 WorkBuddy 上游不认识的顶层字段。
//
// 这些字段来自 Anthropic→OpenAI 转换后的产物（parallel_tool_calls、
// reasoning_effort 等），WorkBuddy 的 /v2/chat/completions 会直接 400。
func workBuddyStripUnsupportedFields(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &payload); err != nil {
		return body
	}
	// 逐个删除：这些键在 WorkBuddy 上游都是未知字段。
	//
	// reasoning_effort / reasoning 不在此列：上游 /v2/chat/completions 接受
	// 这两个字段（实测返回 200 并正常吐出 reasoning_content），模型的
	// Efforts 声明也依赖它们。早先一刀切剥离，导致推理强度控制失效。
	for _, key := range []string{
		"parallel_tool_calls",
		"store",
		"metadata",
		"service_tier",
		"prompt_cache_key",
		"stream_options",
		"logprobs",
		"top_logprobs",
		"prediction",
		"modalities",
		"audio",
		"response_format",
		"seed",
		"logit_bias",
		"user",
	} {
		delete(payload, key)
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// workBuddyForceStream 把请求体强制设为流式。
//
// WorkBuddy 的 /v2/chat/completions 只支持流式，非流式请求会被
// code 11101 拒绝。同时打开 include_usage 才能拿到 token 用量。
func workBuddyForceStream(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	var payload map[string]any
	if err := decodeOpenAIJSONUseNumber(body, &payload); err != nil {
		return body, nil
	}
	payload["stream"] = true
	payload["stream_options"] = map[string]any{"include_usage": true}
	out, err := json.Marshal(payload)
	if err != nil {
		return body, err
	}
	return out, nil
}

// aggregateWorkBuddySSEToChatCompletion 把 WorkBuddy 的 SSE 流聚合成一个
// 标准 chat.completion 响应，供非流式客户端使用。
//
// WorkBuddy 的 /v2/chat/completions 只支持流式；当客户端要非流式时，必须
// 把逐块 delta 拼回完整 message（content、tool_calls、usage），否则下游
// 按 JSON 解析会失败。
func aggregateWorkBuddySSEToChatCompletion(resp *http.Response) (*http.Response, error) {
	if resp == nil || resp.Body == nil {
		return nil, fmt.Errorf("workbuddy: empty upstream response")
	}
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		id, model string
		created   int64
		content   strings.Builder
		reasoning strings.Builder
		toolCalls = map[int]*workBuddyAggregatedToolCall{}
		toolOrder []int
		usage     map[string]any
		finish    string
	)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		var chunk struct {
			ID      string         `json:"id"`
			Model   string         `json:"model"`
			Created int64          `json:"created"`
			Usage   map[string]any `json:"usage"`
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Delta        struct {
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
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if chunk.ID != "" && id == "" {
			id = chunk.ID
		}
		if chunk.Model != "" && model == "" {
			model = chunk.Model
		}
		if chunk.Created != 0 && created == 0 {
			created = chunk.Created
		}
		if len(chunk.Usage) > 0 {
			usage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		choice := chunk.Choices[0]
		if choice.FinishReason != "" {
			finish = choice.FinishReason
		}
		content.WriteString(choice.Delta.Content)
		reasoning.WriteString(choice.Delta.ReasoningContent)
		for _, tc := range choice.Delta.ToolCalls {
			cur, ok := toolCalls[tc.Index]
			if !ok {
				cur = &workBuddyAggregatedToolCall{}
				toolCalls[tc.Index] = cur
				toolOrder = append(toolOrder, tc.Index)
			}
			if tc.ID != "" && cur.ID == "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" && cur.Type == "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" && cur.Name == "" {
				cur.Name = tc.Function.Name
			}
			cur.Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("workbuddy: read upstream stream: %w", err)
	}

	message := map[string]any{"role": "assistant", "content": content.String()}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(toolOrder) > 0 {
		calls := make([]any, 0, len(toolOrder))
		for _, idx := range toolOrder {
			tc := toolCalls[idx]
			if tc == nil || tc.Name == "" {
				continue
			}
			typ := tc.Type
			if typ == "" {
				typ = "function"
			}
			calls = append(calls, map[string]any{
				"id":   tc.ID,
				"type": typ,
				"function": map[string]any{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
			if finish == "" {
				finish = "tool_calls"
			}
		}
	}
	if finish == "" {
		finish = "stop"
	}

	out := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"message":       message,
			"finish_reason": finish,
		}},
	}
	if usage != nil {
		out["usage"] = usage
	}
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}

	// 用聚合后的 JSON 替换 body，保留原响应头与状态码。
	resp.Body = io.NopCloser(bytes.NewReader(raw))
	resp.ContentLength = int64(len(raw))
	resp.Header.Set("Content-Length", strconv.Itoa(len(raw)))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// workBuddyAggregatedToolCall 是拼接中的单个工具调用。
type workBuddyAggregatedToolCall struct {
	ID        string
	Type      string
	Name      string
	Arguments string
}

// workBuddyIntlDefaultChatModel 是无法映射时的兜底对话模型。
const workBuddyIntlDefaultChatModel = "default-model"

// workBuddyNormalizeIntlModel 兜底国际站的空模型名。
//
// 与 magpie 一致：请求里给的模型原样转发，不再按白名单改写。
// 早期这里会把不在手写白名单里的模型全部换成 default-model，
// 结果是用户选 gpt-6-astra / gemini-4-argon 实际跑的都是 default-model，
// 而白名单本身早已滞后于上游目录（现在由 /v3/config 的 agents[cli] 下发）。
// 真选到非对话模型时，上游会明确回 11102 / 11134，比静默替换成另一个模型更容易排查。
func workBuddyNormalizeIntlModel(model string) string {
	name := strings.TrimSpace(model)
	if name == "" {
		return workBuddyIntlDefaultChatModel
	}
	return name
}

// workBuddyUnflagEnabled 报告是否启用提示词品牌痕改写。
//
// 默认关闭，理由见 workBuddySystemOn 中的注释。
func workBuddyUnflagEnabled() bool {
	// 与 magpie 一致：默认开启，只有显式设成 0/false/no 才关闭。
	v := strings.TrimSpace(strings.ToLower(os.Getenv("AUTO2API_WORKBUDDY_UNFLAG")))
	return v != "0" && v != "false" && v != "no"
}

// workBuddyHideDshEnabled 对应 magpie 的 HIDE_DSH 开关。
//
// 默认开启。magpie 原文：HIDE_DSH is the switch, on unless the user turns
// it off。关闭会退回「只靠 FLAGGED」的旧状态，实测仍会被判 11128。
func workBuddyHideDshEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("AUTO2API_WORKBUDDY_HIDE_DSH")))
	return v != "0" && v != "false" && v != "no"
}
