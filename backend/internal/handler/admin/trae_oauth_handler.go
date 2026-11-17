package admin

import (
	"math"
	"strconv"
	"strings"

	"github.com/DeltrivX/Auto2API/internal/pkg/response"
	"github.com/DeltrivX/Auto2API/internal/service"

	"github.com/gin-gonic/gin"
)

// TraeOAuthHandler 处理 Trae 的登录与账号能力。
//
// 与 Marvis 同一条路：授权页只认 127.0.0.1 回调，服务器收不到，所以是
// 「生成链接 → 浏览器授权 → 粘贴回调地址 → 换登录态」。
//
//	POST /api/v1/admin/trae/oauth/auth-url          生成授权链接
//	POST /api/v1/admin/trae/oauth/callback          粘贴回调地址完成授权
//	GET  /api/v1/admin/trae/accounts/:id/models     拉取模型目录
//	GET  /api/v1/admin/trae/accounts/:id/credits    读取额度
//	GET  /api/v1/admin/trae/accounts/:id/device     读取设备指纹
//	POST /api/v1/admin/trae/accounts/:id/device     重新分配设备指纹
//	POST /api/v1/admin/trae/accounts/:id/refresh    续期登录态
type TraeOAuthHandler struct {
	trae         *service.TraeOAuthService
	adminService service.AdminService
}

// NewTraeOAuthHandler 构造 handler。
func NewTraeOAuthHandler(trae *service.TraeOAuthService, adminService service.AdminService) *TraeOAuthHandler {
	return &TraeOAuthHandler{trae: trae, adminService: adminService}
}

// traeAuthURLRequest 是生成授权链接的请求体。
type traeAuthURLRequest struct {
	// Platform 取 trae_cn / trae_intl。
	Platform string `json:"platform"`
}

// traeCallbackRequest 是粘贴回调地址的请求体。
type traeCallbackRequest struct {
	State string `json:"state"`
	// CallbackURL 是浏览器地址栏里的完整回调地址。
	CallbackURL string `json:"callback_url"`
}

// GenerateAuthURL 生成授权链接。
func (h *TraeOAuthHandler) GenerateAuthURL(c *gin.Context) {
	var req traeAuthURLRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求体不合法: "+err.Error())
		return
	}
	platform := strings.TrimSpace(req.Platform)
	if !service.IsTraePlatform(platform) {
		response.BadRequest(c, "不支持的平台: "+platform)
		return
	}
	instruction, err := h.trae.StartAuth(platform)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	response.Success(c, instruction)
}

// CompleteAuth 用粘贴回来的回调地址完成授权。
func (h *TraeOAuthHandler) CompleteAuth(c *gin.Context) {
	var req traeCallbackRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求体不合法: "+err.Error())
		return
	}
	result, err := h.trae.CompleteAuth(c.Request.Context(), req.State, req.CallbackURL)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	response.Success(c, result)
}

// Models 返回账号的模型目录。
func (h *TraeOAuthHandler) Models(c *gin.Context) {
	account, ok := h.accountOf(c)
	if !ok {
		return
	}
	models, err := h.trae.Models(c.Request.Context(), account)
	if err != nil {
		response.Error(c, 502, err.Error())
		return
	}
	response.Success(c, models)
}

// Credits 返回账号额度（已折算成积分）。
//
// 展示口径全平台统一为积分：国际站上游按美元计费，这里用 TraeCreditsAsPoints
// 折算（1 美元 = 100 积分）后再返回。原生值与 native_unit 一并带上，便于排查。
//
// 注意：不能把原生值直接吐给前端 —— 账号列表的积分格就是这么被当成积分显示的，
// 结果国际站账号看起来像「1 美元 = 1 积分」。
func (h *TraeOAuthHandler) Credits(c *gin.Context) {
	account, ok := h.accountOf(c)
	if !ok {
		return
	}
	credits, err := h.trae.Credits(c.Request.Context(), account)
	if err != nil {
		response.Error(c, 502, err.Error())
		return
	}
	limitPts, usedPts := service.TraeCreditsAsPoints(credits)
	response.Success(c, gin.H{
		"platform":       credits.Platform,
		"native_unit":    credits.NativeUnit,
		"native_limit":   credits.Limit,
		"native_used":    credits.Used,
		"limit":          limitPts,
		"used":           usedPts,
		"available":      math.Max(0, limitPts-usedPts),
		"unlimited":      credits.Unlimited,
		"plan":           credits.Plan,
		"credit_per_usd": service.TraeCreditsPerUSD(),
		"updated_at":     credits.UpdatedAt,
	})
}

// Device 返回账号当前设备指纹（不含私钥）。
func (h *TraeOAuthHandler) Device(c *gin.Context) {
	account, ok := h.accountOf(c)
	if !ok {
		return
	}
	response.Success(c, h.trae.DeviceInfo(account))
}

// RotateDevice 重新分配设备指纹，并把新设备写回账号。
func (h *TraeOAuthHandler) RotateDevice(c *gin.Context) {
	account, ok := h.accountOf(c)
	if !ok {
		return
	}
	creds, err := h.trae.RotateDevice(account)
	if err != nil {
		response.InternalError(c, err.Error())
		return
	}
	updated, err := h.adminService.UpdateAccount(c.Request.Context(), account.ID, &service.UpdateAccountInput{Credentials: creds})
	if err != nil {
		response.InternalError(c, "保存设备失败: "+err.Error())
		return
	}
	response.Success(c, h.trae.DeviceInfo(updated))
}

// Refresh 续期登录态并写回账号。
func (h *TraeOAuthHandler) Refresh(c *gin.Context) {
	account, ok := h.accountOf(c)
	if !ok {
		return
	}
	creds, err := h.trae.Refresh(c.Request.Context(), account)
	if err != nil {
		response.Error(c, 502, err.Error())
		return
	}
	if _, err := h.adminService.UpdateAccount(c.Request.Context(), account.ID, &service.UpdateAccountInput{Credentials: creds}); err != nil {
		response.InternalError(c, "保存登录态失败: "+err.Error())
		return
	}
	response.Success(c, gin.H{"refreshed": true})
}

// accountOf 解析 :id 并取账号。
func (h *TraeOAuthHandler) accountOf(c *gin.Context) (*service.Account, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "账号 ID 不合法")
		return nil, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.NotFound(c, "账号不存在")
		return nil, false
	}
	if !service.IsTraePlatform(account.Platform) {
		response.BadRequest(c, "该账号不是 Trae 平台")
		return nil, false
	}
	return account, true
}
