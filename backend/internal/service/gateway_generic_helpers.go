package service

import (
	"regexp"
	"strconv"

	"github.com/cespare/xxhash/v2"
)

// 从已移除平台的专属文件里迁出的通用辅助函数。
//
// 这些函数与具体平台无关，被共享链路（会话摘要、错误脱敏等）引用。

// sensitiveQueryParamRegex 匹配 URL 查询串中的敏感参数值，用于日志脱敏。
var sensitiveQueryParamRegex = regexp.MustCompile(`(?i)([?&](?:key|client_secret|access_token|refresh_token)=)[^&"\s]+`)

// shortHash 使用 XXHash64 + Base36 生成短 hash。
//
// XXHash64 比 SHA256 快约 10 倍，Base36 比 Hex 短约 20%。
func shortHash(data []byte) string {
	h := xxhash.Sum64(data)
	return strconv.FormatUint(h, 36)
}

// sanitizeUpstreamErrorMessage 对上游错误消息做脱敏，抹掉查询参数中的敏感值。
func sanitizeUpstreamErrorMessage(msg string) string {
	if msg == "" {
		return msg
	}
	return sensitiveQueryParamRegex.ReplaceAllString(msg, `$1***`)
}
