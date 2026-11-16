package service

import (
	"regexp"
	"strings"
)

// WorkBuddy 上游会把「从非官方客户端/未授权渠道发起的调用」判为 11128
// 「Illegal API invocation from an unapproved channel」。magpie 的
// packages/workbuddy/index.mjs 里有两层处理，本文件完整移植第二层。
//
// 两层的关系（magpie 源码注释原文）：
//
//	FLAGGED's comma gets a chat past the live 11128 check; this is for a
//	check that reads the prompt afterwards.
//
// 也就是说 FLAGGED（见 workbuddy_sanitize.go）只负责过掉实时的 11128 校验，
// 而 hide_dsh 处理的是**之后读提示词**的检查。背景是 NancyTom 在 X 上报告：
//
//	an account used from local coding tool through magpie was banned, and a
//	reply there had Tencent recognize dsh's built-in prompt
//
// 只移植 FLAGGED、漏掉 hide_dsh，就会表现为「过一阵子又不行」或「偶发 11128」。

// dshLineRule 是「DSH 自己写的整行」的改写规则。
//
// 这些行来自 dsh 0.2.1（@deepseek-ai/dsh-system-prompt 与
// dsh-harness-dev-context）：开场句、checkout 行、Web GUI 行，以及运行上下文
// 里以 user 消息发出去的那两句。只改这几行，其它内容原样。
var dshLineRules = []struct {
	re   *regexp.Regexp
	repl string
}{
	// 开场句：整句去掉产品名。
	//
	// magpie 的规则写成 ",? powered by"，是因为 FLAGGED 会先把
	// "running on a local coding tool" 改写成 ", powered by local coding tool"。
	// 若 FLAGGED 被关闭，原始写法必须也能命中，所以这里把两种都写进来。
	// （实测漏掉 "running on a" 形态会得到 "running on a the agent harness"。）
	{regexp.MustCompile(`(?i)You are an AI agent,? running on a local coding tool\.?`), "You are an AI agent."},
	{regexp.MustCompile(`(?i)You are an AI agent,? powered by local coding tool\.?`), "You are an AI agent."},
	// Web GUI 行：去掉产品名，保留 Web GUI。
	{regexp.MustCompile(`(?i)local coding tool (Web GUI)`), "$1"},
	// implementation checkout 行：产品名换成中性的 agent。
	{regexp.MustCompile(`(?i)local coding tool (implementation checkout)`), "agent $1"},
	// 「DSH itself」是 dsh 指代自己，换成 the agent itself。
	{regexp.MustCompile(`(?i)\bDSH itself\b`), "the agent itself"},
}

// 名字落在路径、文件名或标识符里时不算「散文」，必须保留。
//
// 例如 /root/DSH/、C:\DSH\、DSH.md、DSH_HOME、~/.dsh、dsh-session: 都是
// 客户端实际使用的路径/命令/URI，改写它们会让工具调用失败。
//
// magpie 原文（Go 的 RE2 支持 lookbehind 需要替换成等价写法）：
//
//	IN_PATH_BEFORE = (?<![\w/\\.~:@-])
//	IN_PATH_AFTER  = (?![\w/\\@-]|\.\w)
//
// Go 的 regexp 是 RE2，不支持 lookaround。这里用「捕获前置字符 + 判断」的
// 方式实现同样的语义：先匹配可选的前置字符，再在替换函数里决定是否改写。
var dshProseBare = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9/_\.~:@\-])((?:the )?local coding tool)([^A-Za-z0-9/_\\@\-]|\.\w|$)`)

var dshProseDSH = regexp.MustCompile(`(^|[^A-Za-z0-9/_\\\.~:@\-])(DSH)([^A-Za-z0-9/_\\@\-]|\.\w|$)`)

// workBuddyHideDshText 把 dsh 写的文本里的产品名换成中性说法。
//
// said 为 true 表示这是 user/assistant 的消息：只应用 dsh 自己那几行，
// 不改散落的裸名——因为用户可能真的在说 DSH，改写会篡改用户原话。
func workBuddyHideDshText(text string, said bool) string {
	if text == "" {
		return text
	}
	for _, r := range dshLineRules {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	if said {
		return text
	}
	// 系统提示词与工具描述里，散落的裸名也一并中性化。
	text = dshProseBare.ReplaceAllStringFunc(text, func(m string) string {
		sub := dshProseBare.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		article := "the "
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(sub[2])), "the ") {
			article = ""
		}
		return sub[1] + article + "agent harness" + sub[3]
	})
	text = dshProseDSH.ReplaceAllStringFunc(text, func(m string) string {
		sub := dshProseDSH.FindStringSubmatch(m)
		if len(sub) < 4 {
			return m
		}
		return sub[1] + "the agent harness" + sub[3]
	})
	return text
}

// workBuddyHideDsh 对一条 chat 请求体应用 hide_dsh。
//
// 完整对应 magpie 的 hideDsh(chat)：
//   - messages[] 的 content（字符串或多模态的 text part），role=tool 的结果不动
//   - role 为 system/developer 的按系统提示词处理，其余按「用户/助手原话」处理
//   - chat.system（Anthropic Messages 形态）
//   - tools 里每个 description 字段（递归）
//
// 返回是否有改动。
func workBuddyHideDsh(payload map[string]any) bool {
	if payload == nil {
		return false
	}
	changed := false

	// part 处理：字符串或 [{type:text, text}] 数组。
	rewriteParts := func(content any, said bool) (any, bool) {
		switch v := content.(type) {
		case string:
			next := workBuddyHideDshText(v, said)
			if next == v {
				return content, false
			}
			return next, true
		case []any:
			out := make([]any, 0, len(v))
			here := false
			for _, raw := range v {
				part, ok := raw.(map[string]any)
				if !ok {
					out = append(out, raw)
					continue
				}
				// 非文本 part（图片等）原样保留。
				if typ, _ := part["type"].(string); typ != "" && typ != "text" {
					out = append(out, raw)
					continue
				}
				text, ok := part["text"].(string)
				if !ok {
					out = append(out, raw)
					continue
				}
				next := workBuddyHideDshText(text, said)
				if next == text {
					out = append(out, raw)
					continue
				}
				here = true
				clone := make(map[string]any, len(part))
				for k, val := range part {
					clone[k] = val
				}
				clone["text"] = next
				out = append(out, clone)
			}
			if !here {
				return content, false
			}
			return out, true
		}
		return content, false
	}

	if msgs, ok := payload["messages"].([]any); ok {
		for _, raw := range msgs {
			m, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			role, _ := m["role"].(string)
			// tool 的结果是文件/命令说的话，不改。
			if role == "tool" {
				continue
			}
			said := role != "system" && role != "developer"
			if content, has := m["content"]; has {
				if next, ok := rewriteParts(content, said); ok {
					m["content"] = next
					changed = true
				}
			}
		}
	}

	// Anthropic Messages 形态的顶层 system。
	if sys, has := payload["system"]; has {
		if next, ok := rewriteParts(sys, false); ok {
			payload["system"] = next
			changed = true
		}
	}

	// tools[].function.description / tools[].description，递归。
	if tools, ok := payload["tools"]; ok {
		if workBuddyHideDshDescriptions(tools) {
			changed = true
		}
	}

	return changed
}

// workBuddyHideDshDescriptions 递归改写任意层级上的 description 字符串。
func workBuddyHideDshDescriptions(node any) bool {
	changed := false
	switch v := node.(type) {
	case map[string]any:
		for k, val := range v {
			if k == "description" {
				if s, ok := val.(string); ok {
					next := workBuddyHideDshText(s, false)
					if next != s {
						v[k] = next
						changed = true
					}
					continue
				}
			}
			if workBuddyHideDshDescriptions(val) {
				changed = true
			}
		}
	case []any:
		for _, item := range v {
			if workBuddyHideDshDescriptions(item) {
				changed = true
			}
		}
	}
	return changed
}
