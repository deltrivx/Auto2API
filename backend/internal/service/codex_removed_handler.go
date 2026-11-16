package service

import "fmt"

// handler 层仍在引用的 Codex 符号。Codex 已移除，这些实现全部表达「能力不存在」。

// BuildCodexModelsManifestForGroup 构建分组模型清单。Codex 移除后返回空。
func (s *GatewayService) BuildCodexModelsManifestForGroup(_ ...any) ([]byte, error) {
	return nil, nil
}

// CodexModelsManifestETag 计算清单 ETag。返回空串。
func CodexModelsManifestETag(_ []byte) string { return "" }

// CodexModelsManifestETagMatches 判定 ETag 是否匹配。恒为 false。
func CodexModelsManifestETagMatches(_ string, _ string) bool { return false }

// ForwardAlphaSearch 转发 Alpha 搜索。Codex 移除后不支持。
func (s *OpenAIGatewayService) ForwardAlphaSearch(_ ...any) (*OpenAIModelsResponse, error) {
	return nil, errAlphaSearchRemoved
}

var errAlphaSearchRemoved = fmt.Errorf("alpha search is not supported")

// IsExplicitImageGenerationIntent 判定显式图像生成意图。WorkBuddy 不支持。
func IsExplicitImageGenerationIntent(_ ...any) bool { return false }
