package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Grok 平台的 handler 桩。
//
// Grok 的 handler 实现文件已删除，但路由与调用点仍引用这些方法。
// 它们统一返回 503，明确表示平台不可用。

// GrokImages 处理 Grok 图片请求；平台已移除。
func (h *OpenAIGatewayHandler) GrokImages(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{"type": "platform_removed", "message": "grok platform has been removed"},
	})
}

// handleGrokMedia 处理 Grok 媒体请求；平台已移除。
func (h *OpenAIGatewayHandler) handleGrokMedia(c *gin.Context, _ ...any) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{"type": "platform_removed", "message": "grok platform has been removed"},
	})
}

// 以下方法在 Grok 平台移除后统一返回 503。

func removedPlatformResponse(c *gin.Context, platform string) {
	c.JSON(http.StatusServiceUnavailable, gin.H{
		"error": gin.H{"type": "platform_removed", "message": platform + " platform has been removed"},
	})
}

// GrokVideoGeneration 处理视频生成请求；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoGeneration(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokVideoStatus 处理视频状态查询；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoStatus(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokVideoContent 处理视频内容获取；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoContent(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokVideoEdit 处理视频编辑；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoEdit(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokVideoExtend 处理视频延长；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoExtend(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokRealtime 处理实时语音；平台已移除。
func (h *OpenAIGatewayHandler) GrokRealtime(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}

// GrokVoice 处理语音合成；平台已移除。
func (h *OpenAIGatewayHandler) GrokVoice(c *gin.Context, _ ...any) {
	removedPlatformResponse(c, "grok")
}

// GrokVideoExtension 处理视频扩展；平台已移除。
func (h *OpenAIGatewayHandler) GrokVideoExtension(c *gin.Context) {
	removedPlatformResponse(c, "grok")
}
