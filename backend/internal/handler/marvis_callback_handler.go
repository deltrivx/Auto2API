package handler

// Marvis 的授权回调页。
//
// 这是**公开**端点：用户扫码完成后，QQ / 微信把浏览器 302 到这里，
// 我们接住 code，换出登录态写进票据，然后给用户看一个「可以关掉本页」的提示。
// 前端那边在轮询 /admin/marvis/oauth/poll，拿到结果后自动完成下一步。
//
// 之所以由我们自己接回调而不是让用户复制地址：Auto2API 是 Web 服务，
// 有稳定的公开地址；让用户从地址栏里抠一串 code 纯属多余。

import (
	"fmt"
	"net/http"

	"github.com/DeltrivX/Auto2API/internal/service"
	"github.com/gin-gonic/gin"
)

// MarvisOAuthCallback 处理上游授权完成后的跳转。
//
// GET /api/v1/marvis/oauth/callback?login_type=QC&code=...&state=...
func (h *OpenAIGatewayHandler) MarvisOAuthCallback(c *gin.Context) {
	code := c.Query("code")
	state := c.Query("state")
	loginType := c.Query("login_type")

	if code == "" {
		writeMarvisCallbackPage(c, http.StatusBadRequest, false,
			"没有收到授权码，请重新生成授权链接再扫码。", state)
		return
	}
	if state == "" {
		writeMarvisCallbackPage(c, http.StatusBadRequest, false,
			"回调缺少 state，无法定位本次登录。", "")
		return
	}

	svc := service.MarvisOAuthServiceForCallback()
	if svc == nil {
		writeMarvisCallbackPage(c, http.StatusServiceUnavailable, false,
			"服务尚未就绪，请稍后在界面上重试。", state)
		return
	}

	if err := svc.CompleteSignIn(c.Request.Context(), state, code, normalizeCallbackLoginType(loginType), ""); err != nil {
		writeMarvisCallbackPage(c, http.StatusBadRequest, false,
			"登录失败："+err.Error(), state)
		return
	}

	writeMarvisCallbackPage(c, http.StatusOK, true,
		"登录成功，可以关闭本页回到 Auto2API。", state)
}

// normalizeCallbackLoginType 把上游带回的 QC / WX 归一成 1 / 2。
func normalizeCallbackLoginType(raw string) string {
	switch raw {
	case "QC", "qc", "1":
		return service.MarvisLoginTypeQC
	case "WX", "wx", "WXAPP", "wxapp", "2":
		return service.MarvisLoginTypeWX
	}
	return ""
}

// writeMarvisCallbackPage 输出一个极简的结果页。
//
// 用内联 HTML 而不是跳回前端：这个页面可能在手机浏览器里打开，
// 回前端还要加载整套 SPA，慢且容易被后端地址不通卡住。
func writeMarvisCallbackPage(c *gin.Context, status int, ok bool, message, state string) {
	color := "#dc2626"
	icon := "✕"
	title := "登录失败"
	if ok {
		color = "#16a34a"
		icon = "✓"
		title = "登录成功"
	}
	next := "可以关闭本页。"
	if ok {
		next = "已通知 Auto2API，请回到刚才的页面继续。"
	}
	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="zh-CN"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s · 腾讯 Marvis</title>
<style>
  body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
       font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;background:#f8fafc}
  .card{background:#fff;border-radius:16px;box-shadow:0 8px 30px rgba(15,23,42,.08);
        padding:40px 32px;max-width:420px;text-align:center}
  .icon{width:56px;height:56px;border-radius:50%%;background:%s;color:#fff;font-size:28px;
        line-height:56px;margin:0 auto 20px}
  h1{font-size:20px;margin:0 0 12px;color:#0f172a}
  p{margin:0;color:#475569;font-size:14px;line-height:1.6}
  .hint{margin-top:16px;font-size:12px;color:#94a3b8}
</style></head>
<body><div class="card">
  <div class="icon">%s</div>
  <h1>%s</h1>
  <p>%s</p>
  <p class="hint">%s</p>
</div></body></html>`, title, color, icon, title, message, next)

	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.String(status, html)
}
