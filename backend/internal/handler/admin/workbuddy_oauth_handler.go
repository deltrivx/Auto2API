package admin

import (
	"strconv"

	"github.com/DeltrivX/Auto2API/internal/pkg/response"
	"github.com/DeltrivX/Auto2API/internal/service"
	"github.com/gin-gonic/gin"
)

// WorkBuddyOAuthHandler 处理 WorkBuddy 两个站点的登录流程。
//
// 与 Antigravity / Grok 的 OAuth 不同，WorkBuddy 的授权在浏览器里完成，
// 服务端只能通过 state 轮询结果，因此流程拆成两步：
//
//	POST /api/v1/admin/workbuddy/oauth/auth-url   生成授权链接
//	POST /api/v1/admin/workbuddy/oauth/poll       轮询换取 token
//	POST /api/v1/admin/workbuddy/oauth/refresh    用 refresh token 续期
type WorkBuddyOAuthHandler struct {
	workBuddyOAuth *service.WorkBuddyOAuthService
	adminService   service.AdminService
}

func NewWorkBuddyOAuthHandler(workBuddyOAuth *service.WorkBuddyOAuthService, adminService service.AdminService) *WorkBuddyOAuthHandler {
	return &WorkBuddyOAuthHandler{workBuddyOAuth: workBuddyOAuth, adminService: adminService}
}

// WorkBuddyGenerateAuthURLRequest 是生成授权链接的请求体。
type WorkBuddyGenerateAuthURLRequest struct {
	// Platform 是 workbuddy_cn 或 workbuddy_intl。
	Platform string `json:"platform" binding:"required"`
}

// GenerateAuthURL 生成 WorkBuddy 授权链接。
//
// POST /api/v1/admin/workbuddy/oauth/auth-url
func (h *WorkBuddyOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req WorkBuddyGenerateAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.workBuddyOAuth.GenerateAuthURL(c.Request.Context(), req.Platform)
	if err != nil {
		response.BadRequest(c, "生成授权链接失败: "+err.Error())
		return
	}

	response.Success(c, result)
}

// WorkBuddyPollRequest 是轮询换取 token 的请求体。
type WorkBuddyPollRequest struct {
	// Platform 是 workbuddy_cn 或 workbuddy_intl。
	Platform string `json:"platform" binding:"required"`
	// State 是 auth-url 返回的 state。
	State string `json:"state" binding:"required"`
}

// Poll 轮询直到浏览器授权完成，返回账号凭据。
//
// 未完成授权时返回 pending=true，前端继续轮询即可；不要把它当作错误。
//
// POST /api/v1/admin/workbuddy/oauth/poll
func (h *WorkBuddyOAuthHandler) Poll(c *gin.Context) {
	var req WorkBuddyPollRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.workBuddyOAuth.Poll(c.Request.Context(), req.Platform, req.State)
	if err != nil {
		response.BadRequest(c, "登录失败: "+err.Error())
		return
	}
	response.Success(c, result)
}

// WorkBuddyRefreshRequest 是刷新 token 的请求体。
type WorkBuddyRefreshRequest struct {
	Platform     string `json:"platform" binding:"required"`
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// Refresh 用 refresh token 换一组新 token。
//
// POST /api/v1/admin/workbuddy/oauth/refresh
func (h *WorkBuddyOAuthHandler) Refresh(c *gin.Context) {
	var req WorkBuddyRefreshRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}

	result, err := h.workBuddyOAuth.Refresh(c.Request.Context(), req.Platform, req.RefreshToken)
	if err != nil {
		response.BadRequest(c, "刷新失败: "+err.Error())
		return
	}
	response.Success(c, result)
}

// Credits 返回账号的积分余额。
//
// GET /api/v1/admin/workbuddy/accounts/:id/credits
//
// WorkBuddy 按积分结算，上游已经把余额算好，这里只读取展示。
func (h *WorkBuddyOAuthHandler) Credits(c *gin.Context) {
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
	response.Success(c, h.workBuddyOAuth.FetchCredits(c.Request.Context(), account))
}

// Fingerprint 返回账号的硬件指纹。
//
// GET /api/v1/admin/workbuddy/accounts/:id/fingerprint
//
// 指纹按账号 uid 稳定派生（machineId / MAC / 硬件画像 / UA），
// 上游据此判断「同一台设备」还是「多个账号挤在一台机器」。
func (h *WorkBuddyOAuthHandler) Fingerprint(c *gin.Context) {
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
	if !service.IsWorkBuddyPlatformID(account.Platform) {
		response.BadRequest(c, "该账号不是 WorkBuddy 平台")
		return
	}
	response.Success(c, service.WorkBuddyFingerprintOf(account))
}

// CreditTrend 返回近 N 天的积分消耗趋势。
//
// GET /api/v1/admin/workbuddy/accounts/:id/credits/trend?days=14
func (h *WorkBuddyOAuthHandler) CreditTrend(c *gin.Context) {
	days := 14
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
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
	// 按账号返回：账号列表的缩略图必须每个账号各不相同。
	response.Success(c, service.WorkBuddyAccountCreditTrend(c.Request.Context(), account, days))
}
