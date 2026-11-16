package service

// Ollama Cloud 平台已整体移除。
//
// 本分支只服务 WorkBuddy，不连接 Ollama 官方主机，也没有对应的用量窗口。
// 下面几个符号仍被通用路径引用，这里给出最小实现：判定恒为 false、
// 请求体原样返回、活动记录为空操作。

// isOllamaCloudBaseURL 判定是否为 Ollama Cloud 主机；平台已移除，恒为 false。
func isOllamaCloudBaseURL(_ string) bool { return false }

// clampOllamaCloudAnthropicMessagesMaxTokens 原样返回请求体。
func clampOllamaCloudAnthropicMessagesMaxTokens(_ *Account, _ string, body []byte) []byte {
	return body
}

// scheduleOllamaCloudUsageActivity 空操作。
func scheduleOllamaCloudUsageActivity(_ ...any) {}

// IsOllamaCloudUsageAccount 判定账号是否需要 Ollama 用量刷新；平台已移除，恒为 false。
func IsOllamaCloudUsageAccount(_ *Account) bool { return false }

// applyOllamaCloudRawChatCompletionsRequest 原样返回请求体。
func applyOllamaCloudRawChatCompletionsRequest(_ *Account, body []byte) []byte { return body }

// applyOllamaCloudRawChatCompletionsResponse 原样返回响应体。
func applyOllamaCloudRawChatCompletionsResponse(_ *Account, body []byte) []byte { return body }

// applyOllamaCloudRawChatCompletionsSSELine 原样返回 SSE 行。
func applyOllamaCloudRawChatCompletionsSSELine(_ *Account, line string) string { return line }

// clampOllamaCloudUpstreamMaxTokens 原样返回请求体。
func clampOllamaCloudUpstreamMaxTokens(_ *Account, body []byte) []byte { return body }

// ollamaCloudResponsesMaxOutputTokensClamp 不生效，返回 ok=false。
func ollamaCloudResponsesMaxOutputTokensClamp(_ *Account, _ string, _ []byte) (int, bool) {
	return 0, false
}

// OllamaCloudUsageSnapshot 是 Ollama 用量快照（平台已移除，仅保留结构）。
type OllamaCloudUsageSnapshot struct{}

// OllamaCloudUsageState 是 Ollama 账号的用量状态（平台已移除）。
// 字段保留以便 DTO 影射；Eligible 恒为 false，前端不会展示该模块。
type OllamaCloudUsageState struct {
	AccountID               int64                     `json:"account_id"`
	Eligible                bool                      `json:"eligible"`
	Configured              bool                      `json:"configured"`
	AutoRefreshEnabled      bool                      `json:"auto_refresh_enabled"`
	EncryptionKeyConfigured bool                      `json:"encryption_key_configured"`
	Snapshot                *OllamaCloudUsageSnapshot `json:"snapshot,omitempty"`
}

// OllamaCloudUsageStateFromAccount 返回空状态（平台已移除）。
func OllamaCloudUsageStateFromAccount(account *Account) *OllamaCloudUsageState {
	if account == nil {
		return &OllamaCloudUsageState{}
	}
	return &OllamaCloudUsageState{AccountID: account.ID}
}

// Ollama Cloud extra 键（平台已移除；保留常量供 DTO 隐射与历史数据清理）。
const (
	OllamaCloudUsageSessionExtraKey     = "ollama_cloud_usage_session"
	OllamaCloudUsageAutoRefreshExtraKey = "ollama_cloud_usage_auto_refresh"
	OllamaCloudUsageSnapshotExtraKey    = "ollama_cloud_usage_snapshot"
)
