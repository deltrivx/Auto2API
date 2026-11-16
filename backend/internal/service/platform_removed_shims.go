package service

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// 已移除平台的空实现。
//
// 本分支只服务 WorkBuddy，其它平台（OpenCode / Cline / Gemini / Antigravity
// / Grok 等）已从产品中移除。但共享链路（测试、网关、限流）里仍保留了按
// 平台分派的调用点，为了避免大范围改动引入回归，这里把它们统一收敛为空操作：
// 判定函数一律返回 false，钩子函数一律什么都不做。
//
// 这些函数的行为等价于「该平台不存在」，与平台清单（platforms.go）保持一致。

// rememberOpenCodeInboundBody 是 OpenCode 入站 body 缓存钩子。
func rememberOpenCodeInboundBody(c *gin.Context, body []byte) {}

// applyOpenCodeSessionHeader 是 OpenCode 会话头注入钩子。
func applyOpenCodeSessionHeader(c *gin.Context, account *Account, targetURL string, headers http.Header, bodies ...[]byte) {
}

// applyOpenCodeUpstreamUserAgent 是 OpenCode 上游 UA 收敛钩子。
func applyOpenCodeUpstreamUserAgent(account *Account, targetURL string, headers http.Header) {}

// isOfficialOpenCodeHost 判断目标是否为 OpenCode 官方主机；该平台已移除。
func isOfficialOpenCodeHost(targetURL string) bool { return false }

// isOfficialCommandCodeHost 判断目标是否为 Command Code 官方主机；该平台已移除。
func isOfficialCommandCodeHost(targetURL string) bool { return false }

// openCodeSessionHintBody 返回 OpenCode 会话提示体；该平台已移除。
func openCodeSessionHintBody(promptCacheKey string) []byte { return nil }
