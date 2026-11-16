package service

import (
	"encoding/json"
	"fmt"
	"strings"
)

func parseAnthropicBetaHeader(header string) []string {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil
	}
	if strings.HasPrefix(header, "[") && strings.HasSuffix(header, "]") {
		var parsed []any
		if err := json.Unmarshal([]byte(header), &parsed); err == nil {
			tokens := make([]string, 0, len(parsed))
			for _, item := range parsed {
				token := strings.TrimSpace(fmt.Sprint(item))
				if token != "" {
					tokens = append(tokens, token)
				}
			}
			return tokens
		}
	}
	var tokens []string
	for _, part := range strings.Split(header, ",") {
		t := strings.TrimSpace(part)
		if t != "" {
			tokens = append(tokens, t)
		}
	}
	return tokens
}

// ResolveBedrockBetaTokens 解析 beta header 并返回 token 列表。
//
// Bedrock 自动注入（body 推导补齐 token）已随平台移除：WorkBuddy 走
// OpenAI 兼容协议，不接受 beta token。这里退化为纯 header 解析。
func ResolveBedrockBetaTokens(betaHeader string, _ []byte, _ string) []string {
	return parseAnthropicBetaHeader(betaHeader)
}
