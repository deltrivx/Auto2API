package routes

import (
	"github.com/DeltrivX/Auto2API/internal/handler"
	"github.com/DeltrivX/Auto2API/internal/server/middleware"
	"github.com/DeltrivX/Auto2API/internal/service"

	"github.com/gin-gonic/gin"
)

// RegisterUserRoutes 注册用户相关路由（需要认证）
func RegisterUserRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	jwtAuth middleware.JWTAuthMiddleware,
	auditLog middleware.AuditLogMiddleware,
	settingService *service.SettingService,
	panelRateLimiter *middleware.PanelRateLimiter,
) {
	authenticated := v1.Group("")
	authenticated.Use(gin.HandlerFunc(jwtAuth))
	authenticated.Use(middleware.BackendModeUserGuard(settingService))
	// 面板全局按用户限流：防止单个账号高频刷接口打爆数据库
	authenticated.Use(panelRateLimiter.Global())
	// 用户管理面变更类操作入审计（含 TOTP 启用/禁用、step-up 验证、密码修改等安全事件）
	authenticated.Use(gin.HandlerFunc(auditLog))
	{
		// 积分体系只读接口：普通用户的仪表盘与账号管理需要用。
		//
		// 这些接口只读取云端快照与账号积分，不触发签到/任务执行等写操作，
		// 因此对普通用户开放；写操作仍只限管理员。
		// 账号管理：普通用户可查看全部账号、管理自己创建的账号。
		//
		// 归属校验在 handler 内完成（requireOwnership）：
		//   - 查看：全部账号，但他人账号的凭证脱敏；
		//   - 编辑/删除/测试/刷新等：仅自己创建的账号；
		//   - 复制账号：仅管理员（不在本组）。
		if h.Admin != nil && h.Admin.Account != nil {
			acc := authenticated.Group("/accounts")
			{
				// 查询：全部可见
				acc.GET("", h.Admin.Account.List)
				acc.GET("/:id", h.Admin.Account.GetByID)
				acc.GET("/:id/stats", h.Admin.Account.GetStats)
				acc.GET("/:id/usage", h.Admin.Account.GetUsage)
				acc.GET("/:id/today-stats", h.Admin.Account.GetTodayStats)
				// 测试连接需要模型列表；只读，对所有可见账号开放。
				acc.GET("/:id/models", h.Admin.Account.GetAvailableModels)
				acc.POST("/usage/batch", h.Admin.Account.GetBatchUsage)
				acc.POST("/today-stats/batch", h.Admin.Account.GetBatchTodayStats)
				acc.POST("/check-mixed-channel", h.Admin.Account.CheckMixedChannel)

				// 创建：owner 由服务端按当前用户写入
				acc.POST("", h.Admin.Account.Create)

				// 修改类：handler 内校验归属
				acc.PUT("/:id", h.Admin.Account.Update)
				acc.DELETE("/:id", h.Admin.Account.Delete)
				acc.POST("/:id/test", h.Admin.Account.Test)
				acc.POST("/:id/refresh", h.Admin.Account.Refresh)
				acc.POST("/:id/clear-error", h.Admin.Account.ClearError)
				acc.POST("/:id/clear-rate-limit", h.Admin.Account.ClearRateLimit)
				acc.POST("/:id/recover-state", h.Admin.Account.RecoverState)
				acc.POST("/:id/set-privacy", h.Admin.Account.SetPrivacy)
				acc.POST("/:id/reset-quota", h.Admin.Account.ResetQuota)
				acc.POST("/:id/refresh-tier", h.Admin.Account.RefreshTier)
				acc.POST("/:id/apply-oauth-credentials", h.Admin.Account.ApplyOAuthCredentials)
				acc.POST("/:id/revert-proxy-fallback", h.Admin.Account.RevertProxyFallback)
				// 账号级操作：单账号维度全部开放，归属校验在 handler 内。
				acc.POST("/:id/schedulable", h.Admin.Account.SetSchedulable)
				acc.GET("/:id/temp-unschedulable", h.Admin.Account.GetTempUnschedulable)
				acc.DELETE("/:id/temp-unschedulable", h.Admin.Account.ClearTempUnschedulable)
				acc.POST("/:id/cookie-auth", h.Admin.OAuth.CookieAuth)
				acc.POST("/generate-auth-url", h.Admin.OAuth.GenerateAuthURL)
				acc.POST("/exchange-code", h.Admin.OAuth.ExchangeCode)
				acc.POST("/generate-setup-token-url", h.Admin.OAuth.GenerateSetupTokenURL)
				acc.POST("/exchange-setup-token-code", h.Admin.OAuth.ExchangeSetupTokenCode)
				acc.POST("/setup-token-cookie-auth", h.Admin.OAuth.SetupTokenCookieAuth)
				acc.POST("/:id/models/sync-upstream", h.Admin.Account.SyncUpstreamModels)
				acc.POST("/models/sync-upstream-preview", h.Admin.Account.SyncUpstreamModelsPreview)
				// 上游倍率：单账号维度。
				acc.GET("/upstream-billing-rates", h.Admin.Account.GetUpstreamBillingRates)
				acc.POST("/:id/upstream-billing-probe", h.Admin.Account.ProbeUpstreamBilling)
				acc.PUT("/:id/upstream-billing-probe", h.Admin.Account.SetUpstreamBillingProbeEnabled)
				acc.GET("/upstream-billing-probe/settings", h.Admin.Account.GetUpstreamBillingProbeSettings)
				acc.PUT("/upstream-billing-probe/settings", h.Admin.Account.UpdateUpstreamBillingProbeSettings)
				// 数据导入导出：普通用户也可导出/导入自己的账号。
				acc.GET("/data", h.Admin.Account.ExportData)
				acc.POST("/data", h.Admin.Account.ImportData)
				acc.POST("/:id/duplicate", h.Admin.Account.Duplicate)
			}
		}

		if h.Admin != nil && h.Admin.WorkBuddyTask != nil {
			wb := authenticated.Group("/workbuddy")
			{
				wb.GET("/credits/snapshot", h.Admin.WorkBuddyTask.CreditSnapshot)
				// Trae 积分快照：仪表盘积分卡与 WorkBuddy 卡并列展示，
				// 缺这条会让普通用户的 ready 条件永远为 false（卡片停在骨架）。
				wb.GET("/credits/trae", h.Admin.WorkBuddyTask.TraeCreditSnapshot)
				wb.GET("/tasks/board", h.Admin.WorkBuddyTask.Board)
				wb.GET("/tasks/trend", h.Admin.WorkBuddyTask.CreditTrend)
				wb.GET("/tasks/catalog", h.Admin.WorkBuddyTask.Catalog)
				wb.GET("/tasks/runs", h.Admin.WorkBuddyTask.Runs)
				wb.GET("/tasks/summary", h.Admin.WorkBuddyTask.Summary)
				if h.Admin.WorkBuddyOAuth != nil {
					wb.GET("/accounts/:id/credits", h.Admin.WorkBuddyOAuth.Credits)
					// 硬件指纹：查看类能力，普通用户对所有可见账号开放。
					wb.GET("/accounts/:id/fingerprint", h.Admin.WorkBuddyOAuth.Fingerprint)
					// 添加账号所需的授权链接：普通用户要能自己建号。
					wb.POST("/oauth/auth-url", h.Admin.WorkBuddyOAuth.GenerateAuthURL)
					wb.POST("/oauth/poll", h.Admin.WorkBuddyOAuth.Poll)
				}
				wb.GET("/accounts/:id/credits/detail", h.Admin.WorkBuddyTask.AccountCreditsDetail)
				wb.GET("/accounts/:id/credits/packages", h.Admin.WorkBuddyTask.CreditPackages)
				wb.GET("/accounts/:id/credits/trend", h.Admin.WorkBuddyTask.AccountCreditTrend)
				wb.GET("/accounts/:id/tasks", h.Admin.WorkBuddyTask.Tasks)
				wb.GET("/accounts/:id/checkin", h.Admin.WorkBuddyTask.CheckinStatus)
				wb.GET("/accounts/:id/auto-task", h.Admin.WorkBuddyTask.AutoConfig)
				wb.GET("/tasks/:code/detail", h.Admin.WorkBuddyTask.TaskDetail)
			}
		}

		// 分组列表：添加/批量编辑账号时需要选分组，普通用户只读。
		if h.Admin != nil && h.Admin.Group != nil {
			groupsRO := authenticated.Group("/groups")
			{
				groupsRO.GET("", h.Admin.Group.List)
				groupsRO.GET("/all", h.Admin.Group.GetAll)
			}
		}

		// TLS 指纹配置：添加账号时用于选择指纹，普通用户只读。
		if h.Admin != nil && h.Admin.TLSFingerprintProfile != nil {
			tlsFP := authenticated.Group("/tls-fingerprint-profiles")
			{
				tlsFP.GET("", h.Admin.TLSFingerprintProfile.List)
				tlsFP.GET("/:id", h.Admin.TLSFingerprintProfile.GetByID)
			}
		}

		// 用户接口
		user := authenticated.Group("/user")
		{
			user.GET("/profile", h.User.GetProfile)
			if h.MCPOAuth != nil {
				user.GET("/mcp/config", h.MCPOAuth.ConnectionConfig)
				user.GET("/mcp/grants", h.MCPOAuth.ListGrants)
				user.DELETE("/mcp/grants/:id", h.MCPOAuth.RevokeGrant)
				user.GET("/mcp/authorize", h.MCPOAuth.Preview)
				user.POST("/mcp/authorize", h.MCPOAuth.Decide)
			}
			user.PUT("/password", h.User.ChangePassword)
			user.PUT("", h.User.UpdateProfile)
			user.GET("/aff", h.User.GetAffiliate)
			user.POST("/aff/transfer", h.User.TransferAffiliateQuota)
			user.POST("/account-bindings/email/send-code", h.User.SendEmailBindingCode)
			user.POST("/account-bindings/email", h.User.BindEmailIdentity)
			user.DELETE("/account-bindings/:provider", h.User.UnbindIdentity)
			user.POST("/auth-identities/bind/start", h.User.StartIdentityBinding)
			user.GET("/api-keys/:id/usage/daily", panelRateLimiter.Heavy(), h.Usage.GetMyAPIKeyDailyUsage)
			user.GET("/platform-quotas", h.User.GetMyPlatformQuotas)

			// 通知邮箱管理
			notifyEmail := user.Group("/notify-email")
			{
				notifyEmail.POST("/send-code", h.User.SendNotifyEmailCode)
				notifyEmail.POST("/verify", h.User.VerifyNotifyEmail)
				notifyEmail.PUT("/toggle", h.User.ToggleNotifyEmail)
				notifyEmail.DELETE("", h.User.RemoveNotifyEmail)
			}

			// TOTP 双因素认证
			totp := user.Group("/totp")
			{
				totp.GET("/status", h.Totp.GetStatus)
				totp.GET("/verification-method", h.Totp.GetVerificationMethod)
				totp.POST("/send-code", h.Totp.SendVerifyCode)
				totp.POST("/setup", h.Totp.InitiateSetup)
				totp.POST("/enable", h.Totp.Enable)
				totp.POST("/disable", h.Totp.Disable)
				// 敏感操作二次验证：授予当前会话一段时间的 step-up 权限
				totp.POST("/step-up", h.Totp.StepUp)
			}

			passkeys := user.Group("/passkeys")
			{
				passkeys.GET("", h.Passkey.List)
				passkeys.POST("/register/begin", h.Passkey.BeginRegistration)
				passkeys.POST("/register/finish", h.Passkey.FinishRegistration)
				passkeys.PATCH("/:id", h.Passkey.Rename)
				passkeys.DELETE("/:id", h.Passkey.Delete)
			}
		}

		// API Key管理
		keys := authenticated.Group("/keys")
		{
			keys.GET("", h.APIKey.List)
			keys.GET("/:id", h.APIKey.GetByID)
			keys.POST("", h.APIKey.Create)
			keys.PUT("/:id", h.APIKey.Update)
			keys.DELETE("/:id", h.APIKey.Delete)
		}

		// 用户可用分组（非管理员接口）
		groups := authenticated.Group("/groups")
		{
			groups.GET("/available", h.APIKey.GetAvailableGroups)
			groups.GET("/rates", h.APIKey.GetUserGroupRates)
		}

		// 用户可用渠道（非管理员接口）
		channels := authenticated.Group("/channels")
		{
			channels.GET("/available", h.AvailableChannel.List)
		}

		// 使用记录（聚合统计属重查询，叠加更严格的按用户限流）
		usage := authenticated.Group("/usage")
		usage.Use(panelRateLimiter.Heavy())
		{
			usage.GET("", h.Usage.List)
			usage.GET("/errors", h.Usage.ListErrors)
			usage.GET("/errors/:id", h.Usage.GetErrorDetail)
			usage.GET("/:id", h.Usage.GetByID)
			usage.GET("/stats", h.Usage.Stats)
			// User dashboard endpoints
			usage.GET("/dashboard/stats", h.Usage.DashboardStats)
			usage.GET("/dashboard/trend", h.Usage.DashboardTrend)
			usage.GET("/dashboard/models", h.Usage.DashboardModels)
			usage.GET("/dashboard/snapshot-v2", h.Usage.DashboardSnapshotV2)
			usage.POST("/dashboard/api-keys-usage", h.Usage.DashboardAPIKeysUsage)
		}

		// 公告（用户可见）
		announcements := authenticated.Group("/announcements")
		{
			announcements.GET("", h.Announcement.List)
			announcements.POST("/:id/read", h.Announcement.MarkRead)
		}

		// 卡密兑换
		redeem := authenticated.Group("/redeem")
		{
			redeem.POST("", h.Redeem.Redeem)
			redeem.GET("/history", h.Redeem.GetHistory)
		}

		// 用户订阅
		subscriptions := authenticated.Group("/subscriptions")
		{
			subscriptions.GET("", h.Subscription.List)
			subscriptions.GET("/active", h.Subscription.GetActive)
			subscriptions.GET("/progress", h.Subscription.GetProgress)
			subscriptions.GET("/summary", h.Subscription.GetSummary)
		}

		// 渠道监控（用户只读）
		monitors := authenticated.Group("/channel-monitors")
		{
			monitors.GET("", h.ChannelMonitor.List)
			monitors.GET("/:id/status", h.ChannelMonitor.GetStatus)
		}

		// V2 passive views require feature on + mode=v2.
		monitorV2 := authenticated.Group("/channel-monitor-v2")
		monitorV2.Use(panelRateLimiter.Heavy())
		monitorV2.Use(channelMonitorModeV2Guard(settingService))
		{
			monitorV2.GET("/dimensions", h.ChannelMonitorV2.Dimensions)
			monitorV2.GET("/snapshot", h.ChannelMonitorV2.Snapshot)
			monitorV2.GET("/models", h.ChannelMonitorV2.Models)
			monitorV2.GET("/matrix", h.ChannelMonitorV2.Matrix)
			monitorV2.GET("/errors", h.ChannelMonitorV2.Errors)
			monitorV2.GET("/users", h.ChannelMonitorV2.Users)
		}
	}
}
