package admin

import (
	"os"
	"strconv"
	"strings"

	"github.com/DeltrivX/Auto2API/internal/pkg/response"
	"github.com/DeltrivX/Auto2API/internal/service"
	"github.com/gin-gonic/gin"
)

// MarvisOAuthHandler 处理腾讯 Marvis 的登录流程。
//
// 与 WorkBuddy 完全一致的三步交互（生成链接 → 扫码 → 自动完成），差别只在
// 谁去拿结果：WorkBuddy 是服务端拿 state 问上游，Marvis 是上游把 code
// 送到我们的回调地址。用户侧不需要复制任何东西。
//
//	POST /api/v1/admin/marvis/oauth/auth-url   生成授权链接
//	POST /api/v1/admin/marvis/oauth/poll       轮询登录结果
//	POST /api/v1/admin/marvis/oauth/refresh    用 refresh token 续期
//	GET  /api/v1/marvis/oauth/callback         上游授权完成后的落地页（公开）
type MarvisOAuthHandler struct {
	marvisOAuth  *service.MarvisOAuthService
	adminService service.AdminService
}

func NewMarvisOAuthHandler(marvisOAuth *service.MarvisOAuthService, adminService service.AdminService) *MarvisOAuthHandler {
	return &MarvisOAuthHandler{marvisOAuth: marvisOAuth, adminService: adminService}
}

// MarvisAuthURLRequest 是生成授权链接的请求体。
type MarvisAuthURLRequest struct {
	// LoginType 是 qq 或 wechat，空值默认 qq。
	LoginType string `json:"login_type"`
}

// GenerateAuthURL 生成 QQ / 微信授权链接。
//
// 回调指向本服务的公开地址，用户扫码后由我们接住 code —— 交互上
// 与 WorkBuddy 完全一致（生成链接 → 扫码 → 自动完成）。
//
// POST /api/v1/admin/marvis/oauth/auth-url
func (h *MarvisOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req MarvisAuthURLRequest
	// 允许空 body：默认 QQ 登录。
	_ = c.ShouldBindJSON(&req)

	result, err := h.marvisOAuth.StartSignIn(req.LoginType, publicBaseURL(c))
	if err != nil {
		response.BadRequest(c, "生成授权链接失败: "+err.Error())
		return
	}
	response.Success(c, result)
}

// publicBaseURL 推断本服务的对外根地址。
//
// 优先用 MARVIS_PUBLIC_URL 环境变量（反向代理后面无法从请求头可靠推断），
// 其次用 X-Forwarded-Proto + Host。
func publicBaseURL(c *gin.Context) string {
	if v := strings.TrimSpace(os.Getenv("MARVIS_PUBLIC_URL")); v != "" {
		return v
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	if proto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); proto != "" {
		scheme = proto
	}
	host := c.Request.Host
	if fwd := strings.TrimSpace(c.GetHeader("X-Forwarded-Host")); fwd != "" {
		host = fwd
	}
	if host == "" {
		return ""
	}
	return scheme + "://" + host
}

// MarvisPollRequest 是轮询登录结果的请求体。
type MarvisPollRequest struct {
	State string `json:"state" binding:"required"`
}

// Poll 轮询登录结果。
//
// 未完成授权时返回 pending=true，前端继续轮询即可；不要当作错误。
//
// POST /api/v1/admin/marvis/oauth/poll
func (h *MarvisOAuthHandler) Poll(c *gin.Context) {
	var req MarvisPollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	result, completed, errMsg, err := h.marvisOAuth.PollSignIn(req.State)
	if err != nil {
		response.BadRequest(c, "登录失败: "+err.Error())
		return
	}
	if errMsg != "" {
		response.BadRequest(c, "登录失败: "+errMsg)
		return
	}
	if !completed {
		response.Success(c, gin.H{"pending": true})
		return
	}
	response.Success(c, result)
}

// MarvisExchangeRequest 是用 code 换登录态的请求体。
type MarvisExchangeRequest struct {
	// Input 是用户粘贴的内容：完整回调地址、纯 code 或整段 JSON。
	Input string `json:"input" binding:"required"`
	// GUID 是 auth-url 返回的设备标识。
	GUID string `json:"guid" binding:"required"`
	// LoginType 是 qq 或 wechat。
	LoginType string `json:"login_type"`
	// State 可选，回调地址里带了就用那个。
	State string `json:"state"`
}

// Exchange 用回调里的 code 换取登录态。
//
// POST /api/v1/admin/marvis/oauth/exchange
func (h *MarvisOAuthHandler) Exchange(c *gin.Context) {
	var req MarvisExchangeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	result, err := h.marvisOAuth.ExchangeCode(c.Request.Context(), service.MarvisExchangeRequest{
		Input:     req.Input,
		GUID:      req.GUID,
		LoginType: req.LoginType,
		State:     req.State,
	})
	if err != nil {
		response.BadRequest(c, "登录失败: "+err.Error())
		return
	}
	response.Success(c, result)
}

// MarvisRefreshRequest 是续期的请求体。
type MarvisRefreshRequest struct {
	OpenID       string `json:"openid" binding:"required"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token" binding:"required"`
	GUID         string `json:"guid"`
	LoginType    string `json:"login_type"`
}

// Refresh 用 refresh token 换一组新 token。
//
// POST /api/v1/admin/marvis/oauth/refresh
func (h *MarvisOAuthHandler) Refresh(c *gin.Context) {
	var req MarvisRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	creds, err := h.marvisOAuth.RefreshToken(c.Request.Context(), service.MarvisRefreshRequest{
		OpenID:       req.OpenID,
		AccessToken:  req.AccessToken,
		RefreshToken: req.RefreshToken,
		GUID:         req.GUID,
		LoginType:    req.LoginType,
	})
	if err != nil {
		response.BadRequest(c, "刷新失败: "+err.Error())
		return
	}
	response.Success(c, creds)
}

// Fingerprint 返回账号的设备标识信息。
//
// GET /api/v1/admin/marvis/accounts/:id/fingerprint
func (h *MarvisOAuthHandler) Fingerprint(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "账号 ID 无效")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !service.IsMarvisPlatform(account.Platform) {
		response.BadRequest(c, "该账号不是腾讯 Marvis 平台")
		return
	}
	response.Success(c, service.MarvisFingerprintView(account))
}

// Wallet 返回账号的令牌钱包。
//
// GET /api/v1/admin/marvis/accounts/:id/wallet
//
// Marvis 按 token 结算：每天 1000 万计费 token，缓存命中不计费、输入输出打五折。
// 上游 /v3/marvis_get_wallet 已经把余额算好，这里只读取展示。
func (h *MarvisOAuthHandler) Wallet(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "账号 ID 无效")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !service.IsMarvisPlatform(account.Platform) {
		response.BadRequest(c, "该账号不是腾讯 Marvis 平台")
		return
	}
	wallet, err := service.FetchMarvisWallet(c.Request.Context(), account)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	// 返回与 WorkBuddy 积分卡片同形状的结构，前端同一个组件直接渲染。
	response.Success(c, gin.H{
		"account_id":             account.ID,
		"account":                account.Name,
		"platform":               account.Platform,
		"total":                  wallet.TotalTokens,
		"used":                   wallet.UsedTokens,
		"available":              wallet.AvailTokens,
		"frozen":                 wallet.FrozenTokens,
		"used_percent":           wallet.UsedPercent(),
		"package_name":           "Marvis 令牌",
		"is_paid_user":           false,
		"credits_info_available": true,
		"groups": []gin.H{
			{
				"name":      "令牌用量",
				"total":     wallet.TotalTokens,
				"used":      wallet.UsedTokens,
				"remaining": wallet.AvailTokens,
			},
		},
		"wallet": wallet,
	})
}

// DiscoverModels 现场探测各槽位实际跑的模型。
//
// GET /api/v1/admin/marvis/accounts/:id/discover-models
//
// 上游没有模型目录接口，但每个槽位响应都会带真实模型名。这接口把它问出来，
// 用于给账号生成准确的 model_mapping，而不是依赖代码里那张快照表。
func (h *MarvisOAuthHandler) DiscoverModels(c *gin.Context) {
	accountID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "账号 ID 无效")
		return
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), accountID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if !service.IsMarvisPlatform(account.Platform) {
		response.BadRequest(c, "该账号不是腾讯 Marvis 平台")
		return
	}
	probes := service.MarvisDiscoverSlots(c.Request.Context(), account)
	mapping := make(map[string]string, len(probes))
	for _, probe := range probes {
		if probe.OK && probe.ActualModel != "" {
			mapping[probe.ActualModel] = probe.Slot
		}
	}
	response.Success(c, gin.H{
		"probes":  probes,
		"mapping": mapping,
	})
}

// Models 返回 Marvis 可用的模型目录。
//
// GET /api/v1/admin/marvis/models
func (h *MarvisOAuthHandler) Models(c *gin.Context) {
	response.Success(c, gin.H{
		"models": service.MarvisModels(),
	})
}
