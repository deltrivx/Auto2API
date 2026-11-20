package service

import (
	"encoding/json"
	"context"
	"log"
	"math/rand"
	"sync"
	"time"
)

// WorkBuddy 积分任务的定时调度：自动签到与自动领奖。
//
// 设计约束（与参考实现的差异）：
//   - 串行：同一账号的签到与领奖串行执行，账号之间也串行——并发请求
//     在同 IP 下容易被上游判为异常。
//   - 间隔：每个账号之间休眠，进一步降低触发风控的概率。
//   - 幂等：签到前先查状态，已签则不动作，避免重复签到的异常判定。
//   - 可关：默认跟随每个账号的 auto_task_config，默认关闭。

// WorkBuddyTaskScheduler 定时执行签到与领奖。
type WorkBuddyTaskScheduler struct {
	adminService AdminService
	// interval 是两轮调度之间的间隔。
	interval time.Duration
	// perAccountDelay 是相邻账号之间的间隔。
	perAccountDelay time.Duration

	mu      sync.Mutex
	running bool
	lastRun time.Time
	lastLog string
}

// NewWorkBuddyTaskScheduler 构造调度器。
func NewWorkBuddyTaskScheduler(adminService AdminService) *WorkBuddyTaskScheduler {
	return &WorkBuddyTaskScheduler{
		adminService:    adminService,
		interval:        30 * time.Minute,
		perAccountDelay: 3 * time.Second,
	}
}

// Start 启动后台调度循环。
func (s *WorkBuddyTaskScheduler) Start(ctx context.Context) {
	go func() {
		// 启动后先等一段时间，避开服务启动高峰。
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Minute):
		}
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			s.RunOnce(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// RunOnce 执行一轮调度。
//
// 只处理开启了自动任务的账号；任何单个账号失败都不会中断整轮。
func (s *WorkBuddyTaskScheduler) RunOnce(ctx context.Context) {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.running = false
		s.lastRun = time.Now()
		s.mu.Unlock()
	}()

	accounts, err := s.autoTaskAccounts(ctx)
	if err != nil {
		log.Printf("[wb-task] 读取自动任务账号失败: %v", err)
		return
	}
	if len(accounts) == 0 {
		return
	}
	log.Printf("[wb-task] 本轮自动任务：%d 个账号", len(accounts))

	// 账号顺序随机化：固定顺序会让上游看到「每 5 分钟同一批账号按同样次序
	// 操作」，非常规律。打乱后每个账号被触达的时刻都是随机的。
	rand.Shuffle(len(accounts), func(i, j int) {
		accounts[i], accounts[j] = accounts[j], accounts[i]
	})

	for i, item := range accounts {
		if ctx.Err() != nil {
			return
		}
		// 账号之间抖动 3-15 秒（基准 3 秒 + 最多 12 秒随机），
		// 避免所有账号在同一分钟内被访问。
		if i > 0 {
			time.Sleep(randomDuration(3*time.Second, 12*time.Second))
		}
		s.runAccount(ctx, item)
	}
}

// randomDuration 返回 [min, min+maxJitter) 之间的随机时长。
func randomDuration(min, maxJitter time.Duration) time.Duration {
	if maxJitter <= 0 {
		return min
	}
	return min + time.Duration(rand.Int63n(int64(maxJitter)))
}

// WorkBuddyAutoTaskAccount 是一个待处理账号及其自动任务开关。
type WorkBuddyAutoTaskAccount struct {
	Account         *Account
	AutoCheckin     bool
	AutoClaim       bool
	IncludeHighRisk bool
	// AllowReport 为 true 时对未完成任务上报事件推进进度。
	AllowReport bool
}

// runAccount 处理单个账号。
func (s *WorkBuddyTaskScheduler) runAccount(ctx context.Context, item WorkBuddyAutoTaskAccount) {
	account := item.Account
	ran, failed := false, false
	defer func() {
		status := "skipped"
		if failed {
			status = "failed"
		} else if ran {
			status = "success"
		}
		if err := s.markRun(ctx, account, status); err != nil {
			log.Printf("[wb-task] %s 回写执行状态失败: %v", account.Name, err)
		}
	}()

	if item.AutoCheckin {
		st, err := WorkBuddyCheckinStatusOf(ctx, account)
		if err != nil {
			log.Printf("[wb-task] %s 签到状态查询失败: %v", account.Name, err)
		} else if st.Active && !st.TodayCheckedIn {
			res, err := WorkBuddyDoCheckin(ctx, account)
			if err != nil {
				failed = true
				log.Printf("[wb-task] %s 签到失败: %v", account.Name, err)
			} else {
				ran = true
				log.Printf("[wb-task] %s 签到成功 +%.2f（连签 %d 天）",
					account.Name, res.Credit, res.StreakDays)
			}
		}
	}
	if item.AutoClaim {
		// 自动执行：先对可上报的任务上报事件推进进度，再统一领奖。
		// 走与手动执行完全相同的链路，只是无需界面触发。
		if item.AllowReport {
			workBuddyAutoAdvanceTasks(ctx, account, item.IncludeHighRisk)
		}
		results, err := WorkBuddyClaimAll(ctx, account, item.IncludeHighRisk, 10*time.Second)
		if err != nil {
			failed = true
			log.Printf("[wb-task] %s 领奖失败: %v", account.Name, err)
			return
		}
		ran = true
		var okCount int
		var total float64
		for _, r := range results {
			if r.Err == "" {
				okCount++
				total += r.Credit
			}
		}
		if okCount > 0 {
			log.Printf("[wb-task] %s 领奖 %d 项，共 %.2f 积分", account.Name, okCount, total)
		}
	}
}

// markRun 回写本账号的执行时间与结果。
func (s *WorkBuddyTaskScheduler) markRun(ctx context.Context, account *Account, status string) error {
	if s == nil || s.adminService == nil || account == nil {
		return nil
	}
	return s.adminService.MarkWorkBuddyAutoTaskRun(ctx, account.ID, time.Now(), status)
}

// autoTaskAccounts 返回开启了自动任务的 WorkBuddy 账号。
func (s *WorkBuddyTaskScheduler) autoTaskAccounts(ctx context.Context) ([]WorkBuddyAutoTaskAccount, error) {
	return s.adminService.WorkBuddyAutoTaskAccounts(ctx)
}

// workBuddyAutoTaskConfig 是账号的自动任务开关。
type workBuddyAutoTaskConfig struct {
	AutoCheckin     bool
	AutoClaim       bool
	IncludeHighRisk bool
	// Hour 是每天允许执行的小时（0-23）。凭据里 daily_auto_hour 为空时按 13 处理。
	Hour int
}

// workBuddyAutoTaskConfigOf 从账号凭据里读出自动任务开关。
//
// 开关随凭据存放：账号被复制或迁移时配置一起走，不需要额外对表。
//
// 两种存放形式都要认：
//   - 新：平铺的 daily_auto_enabled / daily_auto_tasks / daily_auto_hour（前端实际写入的格式）
//   - 旧：嵌套的 auto_task 对象
//
// 之前只读 auto_task，而账号里存的是 daily_auto_*，结果调度器永远拿到空配置，
// 表现为「自动任务开着但从不执行」。
func workBuddyAutoTaskConfigOf(a *Account) workBuddyAutoTaskConfig {
	cfg := workBuddyAutoTaskConfig{Hour: 13}
	if a == nil || a.Credentials == nil {
		return cfg
	}

	if m, ok := a.Credentials["auto_task"].(map[string]any); ok {
		cfg.AutoCheckin = cfg.AutoCheckin || workBuddyTaskBool(m, "auto_checkin")
		cfg.AutoClaim = cfg.AutoClaim || workBuddyTaskBool(m, "auto_claim")
		cfg.IncludeHighRisk = cfg.IncludeHighRisk || workBuddyTaskBool(m, "include_high_risk")
	}

	if !workBuddyTaskBool(a.Credentials, "daily_auto_enabled") {
		return cfg
	}
	if h := int(workBuddyTaskFloat(a.Credentials, "daily_auto_hour")); h >= 0 && h <= 23 {
		cfg.Hour = h
	}
	tasks := workBuddyStringList(a.Credentials["daily_auto_tasks"])
	has := func(name string) bool {
		for _, t := range tasks {
			if t == name {
				return true
			}
		}
		return false
	}
	if has("checkin") {
		cfg.AutoCheckin = true
	}
	for _, claim := range []string{"accept_all", "claim", "task", "chat_5", "daily_activity", "travel", "query", "glm52"} {
		if has(claim) {
			cfg.AutoClaim = true
			break
		}
	}
	if has("high_risk") || has("include_high_risk") {
		cfg.IncludeHighRisk = true
	}
	return cfg
}

// workBuddyStringList 把凭据里的数组字段解析成字符串切片。
func workBuddyStringList(v any) []string {
	switch raw := v.(type) {
	case []string:
		return raw
	case []any:
		out := make([]string, 0, len(raw))
		for _, item := range raw {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		if raw == "" {
			return nil
		}
		var out []string
		if err := json.Unmarshal([]byte(raw), &out); err == nil {
			return out
		}
	}
	return nil
}

// workBuddyAutoAdvanceTasks 对账号上未完成的任务执行自动完成。
//
// 关键原则：**先查上游状态，已完成的直接跳过**。上游是唯一事实来源——
// 账号可能在桌面端、小程序或别的工具里已经完成过，重复执行只会浪费请求
// 并增加被判定异常的风险。
//
// 覆盖范围（都要求任务尚未完成）：
//   - 互动模块（签到/抽奖/盲盒/旅行/兑换/补签）：走各自专用接口；
//   - 分享技能：创建分享渠道 + 上报渠道事件；
//   - 桌面端对话：6 连事件链 + 真实会话；
//   - 纯事件类（资料库等）：按映射上报。
//
// 不自动执行：需要真实会话的聊天任务（会消耗模型额度）、需人工的微信类任务。
func workBuddyAutoAdvanceTasks(ctx context.Context, account *Account, includeHighRisk bool) {
	tasks, err := WorkBuddyTasksOf(ctx, account)
	if err != nil {
		return
	}
	// 合并互动模块状态：签到/抽奖等是否已完成，上游体现在各自接口里。
	tasks = append(tasks, WorkBuddyActivityTasksOf(ctx, account)...)

	for _, t := range tasks {
		if ctx.Err() != nil {
			return
		}
		// 已完成 / 已领奖 / 进行中：一律跳过。
		if t.Status == "completed" || t.Status == "claimed" || t.Status == "in_progress" {
			continue
		}
		if !includeHighRisk && workBuddyTaskRiskOf(t.Code, t.Title) == WorkBuddyRiskHigh {
			continue
		}

		// 互动模块：走各自接口。
		if IsWorkBuddyActivityTask(t.Code) {
			if _, err := WorkBuddyRunActivityTask(ctx, account, t.Code); err == nil {
				time.Sleep(randomDuration(2*time.Second, 5*time.Second))
			}
			continue
		}
		// 分享技能。
		if t.Code == "skill_share" {
			if err := WorkBuddyRunSkillShare(ctx, account); err == nil {
				time.Sleep(randomDuration(2*time.Second, 4*time.Second))
			}
			continue
		}
		// 桌面端对话：6 连事件链。
		if t.Code == "RichMeow_Chat" || t.Code == "desktop_chat_1_time" {
			if err := WorkBuddyDesktopChatTask(ctx, account); err == nil {
				time.Sleep(randomDuration(2*time.Second, 5*time.Second))
			}
			continue
		}
		// 纯事件类。
		plan, ok := WorkBuddyEventPlanOf(t.Code)
		if !ok || plan.NeedsRealSession {
			continue
		}
		if err := WorkBuddyReportEvents(ctx, account, WorkBuddyBuildReportEvents(plan)); err != nil {
			continue
		}
		time.Sleep(randomDuration(1*time.Second, 3*time.Second))
	}
}
