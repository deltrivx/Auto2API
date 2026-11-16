package admin

import (
	"strconv"
	"time"

	"github.com/DeltrivX/Auto2API/internal/pkg/response"
	"github.com/DeltrivX/Auto2API/internal/service"
	"github.com/gin-gonic/gin"
)

// WorkBuddy 积分任务接口：签到与成长任务。
//
// 全部按账号操作，执行由操作者显式触发；不做无人值守的任务流水线。

// WorkBuddyTaskHandler 提供积分任务相关接口。
type WorkBuddyTaskHandler struct {
	adminService service.AdminService
	// creditSync 提供云端积分快照；为空时积分接口返回空数据而不是报错。
	creditSync *service.WorkBuddyCreditSyncService
	// marvisCreditSync 提供 Marvis 积分快照（与 WorkBuddy 同构）。
	marvisCreditSync *service.MarvisCreditSyncService
}

// NewWorkBuddyTaskHandler 构造积分任务 handler。
func NewWorkBuddyTaskHandler(
	adminService service.AdminService,
	creditSync *service.WorkBuddyCreditSyncService,
) *WorkBuddyTaskHandler {
	return &WorkBuddyTaskHandler{adminService: adminService, creditSync: creditSync}
}

// workBuddyAccount 解析路径里的账号 ID 并确认是 WorkBuddy 账号。
func (h *WorkBuddyTaskHandler) workBuddyAccount(c *gin.Context) (*service.Account, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		response.BadRequest(c, "账号 ID 无效")
		return nil, false
	}
	account, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return nil, false
	}
	if !service.IsWorkBuddyPlatformID(account.Platform) {
		response.BadRequest(c, "该账号不是 WorkBuddy 平台")
		return nil, false
	}
	return account, true
}

// CheckinStatus 返回账号的签到状态。
// GET /admin/workbuddy/accounts/:id/checkin
func (h *WorkBuddyTaskHandler) CheckinStatus(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	st, err := service.WorkBuddyCheckinStatusOf(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, st)
}

// Checkin 执行签到。
// POST /admin/workbuddy/accounts/:id/checkin
func (h *WorkBuddyTaskHandler) Checkin(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	res, err := service.WorkBuddyDoCheckin(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// Tasks 返回账号的成长任务列表。
// GET /admin/workbuddy/accounts/:id/tasks
func (h *WorkBuddyTaskHandler) Tasks(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	tasks, err := service.WorkBuddyTasksOf(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, tasks)
}

// Claim 领取可领任务奖励。
// POST /admin/workbuddy/accounts/:id/tasks/claim
func (h *WorkBuddyTaskHandler) Claim(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	var req struct {
		IncludeHighRisk bool `json:"include_high_risk"`
		WaitSeconds     int  `json:"wait_seconds"`
	}
	_ = c.ShouldBindJSON(&req)

	wait := time.Duration(req.WaitSeconds) * time.Second
	if wait <= 0 {
		wait = 10 * time.Second
	}
	results, err := service.WorkBuddyClaimAll(c.Request.Context(), account, req.IncludeHighRisk, wait)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	var total float64
	for _, r := range results {
		if r.Err == "" {
			total += r.Credit
		}
	}
	response.Success(c, gin.H{"results": results, "total_credit": total})
}

// Catalog 返回任务清单。
// GET /admin/workbuddy/tasks/catalog
func (h *WorkBuddyTaskHandler) Catalog(c *gin.Context) {
	groups := service.WorkBuddyTaskCatalog()
	total, auto := 0, 0
	for _, g := range groups {
		total += len(g.Items)
		for _, it := range g.Items {
			if it.Auto {
				auto++
			}
		}
	}
	response.Success(c, gin.H{
		"groups": groups, "total": total, "auto": auto, "manual": total - auto,
	})
}

// Runs 返回执行历史。
// GET /admin/workbuddy/tasks/runs?limit=50
func (h *WorkBuddyTaskHandler) Runs(c *gin.Context) {
	limit := 50
	if v := c.Query("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := service.WorkBuddyTaskRuns(c.Request.Context(), limit)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, runs)
}

// Summary 返回任务台账与积分聚合。
// GET /admin/workbuddy/tasks/summary
func (h *WorkBuddyTaskHandler) Summary(c *gin.Context) {
	tasks, err := service.WorkBuddyTaskSummary(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	credits, err := service.WorkBuddyCreditSummaryOf(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 叠加「上游已领奖」的历史基线：接入系统之前拿到的积分不会进台账，
	// 只靠台账会让「总积分」长期偏低。
	if accounts, aerr := h.workBuddyTaskAccounts(c); aerr == nil {
		baseline, claimed := service.WorkBuddyEarnedBaseline(c.Request.Context(), accounts)
		credits.BaselineCredit = baseline
		credits.ClaimedTaskCount = claimed
		credits.TotalCredit += baseline
	}
	response.Success(c, gin.H{"tasks": tasks, "credits": credits})
}

// RunSingleTask 执行单个任务。
// POST /admin/workbuddy/accounts/:id/tasks/:code/run
func (h *WorkBuddyTaskHandler) RunSingleTask(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	code := c.Param("code")
	res, err := service.WorkBuddyRunSingleTask(c.Request.Context(), account, code)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, res)
}

// AutoConfig 读写账号的自动任务开关。
// GET  /admin/workbuddy/accounts/:id/auto-task
// PUT  /admin/workbuddy/accounts/:id/auto-task
func (h *WorkBuddyTaskHandler) AutoConfig(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	if c.Request.Method == "GET" {
		autoCheckin, autoClaim, includeHighRisk := service.WorkBuddyAutoTaskConfigOfAccount(account)
		response.Success(c, gin.H{
			"auto_checkin":      autoCheckin,
			"auto_claim":        autoClaim,
			"include_high_risk": includeHighRisk,
		})
		return
	}

	var req struct {
		AutoCheckin     bool `json:"auto_checkin"`
		AutoClaim       bool `json:"auto_claim"`
		IncludeHighRisk bool `json:"include_high_risk"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	creds := service.SetWorkBuddyAutoTaskConfig(account, req.AutoCheckin, req.AutoClaim, req.IncludeHighRisk)
	if _, err := h.adminService.UpdateAccount(c.Request.Context(), account.ID, &service.UpdateAccountInput{
		Credentials: creds,
	}); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{
		"auto_checkin":      req.AutoCheckin,
		"auto_claim":        req.AutoClaim,
		"include_high_risk": req.IncludeHighRisk,
	})
}

// Board 返回任务看板：任务为维度，聚合全部账号的执行情况。
// GET /admin/workbuddy/tasks/board
func (h *WorkBuddyTaskHandler) Board(c *gin.Context) {
	accounts, err := h.workBuddyTaskAccounts(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.WorkBuddyTaskBoardOf(c.Request.Context(), accounts))
}

// TaskDetail 返回单个任务在各账号上的执行情况。
// GET /admin/workbuddy/tasks/:code/detail
func (h *WorkBuddyTaskHandler) TaskDetail(c *gin.Context) {
	code := c.Param("code")
	accounts, err := h.workBuddyTaskAccounts(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, service.WorkBuddyTaskDetailOf(c.Request.Context(), accounts, code))
}

// RunTaskAll 在所有账号上执行同一个任务。
// POST /admin/workbuddy/tasks/:code/run
func (h *WorkBuddyTaskHandler) RunTaskAll(c *gin.Context) {
	code := c.Param("code")
	var req struct {
		AccountIDs []int64 `json:"account_ids"`
		Auto       bool    `json:"auto"`
	}
	_ = c.ShouldBindJSON(&req)

	accounts, err := h.workBuddyTaskAccounts(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// 指定了账号就只跑这些账号；否则跑全部（前端全选）。
	if len(req.AccountIDs) > 0 {
		want := map[int64]bool{}
		for _, id := range req.AccountIDs {
			want[id] = true
		}
		filtered := accounts[:0]
		for _, a := range accounts {
			if want[a.ID] {
				filtered = append(filtered, a)
			}
		}
		accounts = filtered
	}
	service.InvalidateWorkBuddyTaskBoardCache()
	response.Success(c, service.WorkBuddyRunTaskOnAccounts(c.Request.Context(), accounts, code, req.Auto))
}

// CreditPackages 返回账号的积分包明细（含到期）。
// GET /admin/workbuddy/accounts/:id/credits/packages
func (h *WorkBuddyTaskHandler) CreditPackages(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	pkgs, err := service.WorkBuddyCreditPackagesOf(c.Request.Context(), account)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, pkgs)
}

// workBuddyTaskAccounts 返回全部启用的 WorkBuddy 账号。
func (h *WorkBuddyTaskHandler) workBuddyTaskAccounts(c *gin.Context) ([]*service.Account, error) {
	// 显式过滤 platform=workbuddy_cn 且 status=active。
	//
	// 不传过滤条件时 ListAccounts 只返回「可调度」账号，正在做任务测试而
	// 暂时关闭调度的账号会被漏掉；这里按平台+状态取，与调度状态无关。
	all, _, err := h.adminService.ListAccounts(c.Request.Context(), 1, 500, service.PlatformWorkBuddyCN, "", "active", "", 0, "", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]*service.Account, 0, len(all))
	for i := range all {
		a := &all[i]
		// 国际站没有成长任务与互动模块（实测 checkin/tasks 接口均 404），
		// 只把国内站账号纳入看板，避免国际账号在列表与弹窗里显示为空行。
		if a.Platform == service.PlatformWorkBuddyCN && a.Status == service.StatusActive {
			out = append(out, a)
		}
	}
	return out, nil
}

// CreditTrend 返回近 N 天的积分变化（签到 + 任务）。
// GET /admin/workbuddy/tasks/trend?days=7
func (h *WorkBuddyTaskHandler) CreditTrend(c *gin.Context) {
	days := 7
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	trend, err := service.WorkBuddyTaskCreditTrend(c.Request.Context(), days)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, trend)
}

// CreditSnapshot 返回云端积分快照。
//
// GET /admin/workbuddy/credits/snapshot?sync=1
//
// 带 sync=1 时在后台触发一次上游同步——页面打开时用它，保证数字不是
// 十分钟前的旧值。响应始终是当前快照（可能还在同步中）。
func (h *WorkBuddyTaskHandler) CreditSnapshot(c *gin.Context) {
	if h.creditSync == nil {
		response.Success(c, gin.H{"accounts": []any{}, "synced_at": nil})
		return
	}
	if c.Query("sync") == "1" {
		response.Success(c, h.creditSync.ForceSync(c.Request.Context()))
		return
	}
	response.Success(c, h.creditSync.Snapshot())
}

// ExecuteTasks 在指定账号上执行指定任务（含事件上报）。
//
// POST /admin/workbuddy/tasks/execute
//
// body: {"task_codes":["Library_read"],"account_ids":[35],"allow_report":true,"allow_high_risk":false}
//
// allow_report 默认 false：只领奖、不上报事件。这是保守默认值——上报
// 事件本质是模拟客户端行为，需要操作者明确开启。
func (h *WorkBuddyTaskHandler) ExecuteTasks(c *gin.Context) {
	var req struct {
		TaskCodes     []string `json:"task_codes"`
		AccountIDs    []int64  `json:"account_ids"`
		AllowReport   bool     `json:"allow_report"`
		AllowHighRisk bool     `json:"allow_high_risk"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "请求无效: "+err.Error())
		return
	}
	if len(req.TaskCodes) == 0 {
		response.BadRequest(c, "请指定要执行的任务")
		return
	}

	accounts, err := h.workBuddyTaskAccounts(c)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if len(req.AccountIDs) > 0 {
		want := map[int64]bool{}
		for _, id := range req.AccountIDs {
			want[id] = true
		}
		filtered := accounts[:0]
		for _, a := range accounts {
			if want[a.ID] {
				filtered = append(filtered, a)
			}
		}
		accounts = filtered
	}

	opts := service.WorkBuddyTaskExecOptionsOf(req.AllowReport, req.AllowHighRisk)
	results := service.WorkBuddyExecuteTasks(c.Request.Context(), accounts, req.TaskCodes, opts)
	// 执行会改变任务状态，让看板缓存立即失效，下一次打开就是新数据。
	service.InvalidateWorkBuddyTaskBoardCache()
	response.Success(c, gin.H{"results": results})
}

// AccountCreditTrend 从快照读取单个账号的积分趋势。
//
// 账号列表里每个账号都有缩略图，逐账号请求上游会让一页 20 个账号
// 变成 20 次上游调用。趋势已经随后台同步写进快照，这里直接读，
// 命中即毫秒级返回；快照里没有时再回退到实时拉取。
//
// GET /admin/workbuddy/accounts/:id/credits/trend
func (h *WorkBuddyTaskHandler) AccountCreditTrend(c *gin.Context) {
	days := 14
	if v := c.Query("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}

	// 1. 快照优先。
	if h.creditSync != nil {
		snap := h.creditSync.Snapshot()
		for _, a := range snap.Accounts {
			if a.AccountID != account.ID || len(a.Trend) == 0 {
				continue
			}
			trend := a.Trend
			if days < len(trend) {
				trend = trend[len(trend)-days:]
			}
			out := make([]service.WorkBuddyCreditDailySummary, 0, len(trend))
			for _, p := range trend {
				out = append(out, service.WorkBuddyCreditDailySummary{Date: p.Date, Credits: p.Score})
			}
			response.Success(c, out)
			return
		}
	}

	// 2. 快照缺失：实时拉取该账号的 heatmap。
	response.Success(c, service.WorkBuddyAccountCreditTrend(c.Request.Context(), account, days))
}

// AccountCreditsDetail 返回账号的完整积分明细（汇总 + 各个积分包）。
//
// GET /admin/workbuddy/accounts/:id/credits/detail
//
// 与账号列表的积分单元格相比，这里额外返回**每个积分包**的名称、到期日、
// 剩余/总量与使用进度——上游按批次发放，同一类型的包可能有几十个且到期
// 日各不相同，只看汇总会丢失「哪部分快过期」这类关键信息。
func (h *WorkBuddyTaskHandler) AccountCreditsDetail(c *gin.Context) {
	account, ok := h.workBuddyAccount(c)
	if !ok {
		return
	}
	ctx := c.Request.Context()

	pkgs, pkgErr := service.WorkBuddyCreditPackagesOf(ctx, account)
	credits, credErr := service.WorkBuddyCreditsOf(ctx, account)

	resp := gin.H{
		"account_id":  account.ID,
		"account":     account.Name,
		"platform":    account.Platform,
		"packages":    pkgs,
		"package_err": "",
	}
	if pkgErr != nil {
		resp["package_err"] = pkgErr.Error()
	}
	if credErr == nil && credits != nil {
		resp["total"] = credits.Total
		resp["groups"] = credits.Groups
		resp["package_name"] = credits.PackageName
	} else if credErr != nil {
		resp["error"] = credErr.Error()
	}
	// 各包剩余之和：与总量对比可发现汇总口径差异。
	var sumRemain float64
	for _, p := range pkgs {
		sumRemain += p.Remain
	}
	resp["package_remain_sum"] = sumRemain
	response.Success(c, resp)
}

// MarvisCreditSnapshot 返回 Marvis 积分快照。
//
// GET /admin/workbuddy/credits/marvis?sync=1
//
// 与 WorkBuddy 快照同一形状，前端用同一套卡片渲染。带 sync=1 时
// 现拉一次上游钱包，保证打开页面看到的不是十分钟前的旧值。
func (h *WorkBuddyTaskHandler) MarvisCreditSnapshot(c *gin.Context) {
	if h.marvisCreditSync == nil {
		response.Success(c, gin.H{
			"accounts":          []any{},
			"synced_at":         nil,
			"total_credits":     0,
			"used_credits":      0,
			"available_credits": 0,
			"today_used":        0,
		})
		return
	}
	if c.Query("sync") == "1" {
		response.Success(c, h.marvisCreditSync.Sync(c.Request.Context()))
		return
	}
	response.Success(c, h.marvisCreditSync.Snapshot())
}

// WithMarvisCreditSync 注入 Marvis 积分快照服务。
//
// 用不可变式 setter 而不是再加一个构造参数：现有构造点不多，但改动
// 构造函数会牵动 wire 装配，setter 让 wiring 只多一行。
func (h *WorkBuddyTaskHandler) WithMarvisCreditSync(sync *service.MarvisCreditSyncService) *WorkBuddyTaskHandler {
	h.marvisCreditSync = sync
	return h
}
