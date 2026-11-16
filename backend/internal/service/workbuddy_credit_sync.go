package service

import (
	"context"
	"encoding/json"
	"github.com/redis/go-redis/v9"
	"log"
	"net/http"
	"sync"
	"time"
)

// WorkBuddy 积分与任务状态的云端同步。
//
// 积分不是「本系统执行了多少」——账号可能在桌面端、小程序或别处签到/做任务，
// 那些积分同样到账。因此面板上的所有积分数字都以**上游为唯一事实来源**，
// 本地台账只用于展示「本系统执行过什么」。
//
// 同步策略（与其它账号状态服务一致）：
//   - 后台定时拉取，间隔可配，默认 10 分钟；
//   - 前台页面打开时立即触发一次（ForceSync），让数字尽快反映真实状态；
//   - 结果写入内存快照，页面读取快照而不是每次打上游，避免账号多时把
//     上游打限流。

// WorkBuddyAccountSnapshot 是单个账号的云端积分快照。
type WorkBuddyAccountSnapshot struct {
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	// CheckinCredits 是签到活动累计积分（上游 total_credits）。
	CheckinCredits float64 `json:"checkin_credits"`
	StreakDays     int     `json:"streak_days"`
	TodayCheckedIn bool    `json:"today_checked_in"`
	DailyCredit    float64 `json:"daily_credit"`
	// TotalCredits 是账号当前可用总积分（上游 resource summary）。
	TotalCredits float64 `json:"total_credits"`
	// PackageCredits 是各积分包余额之和。
	PackageCredits float64 `json:"package_credits"`
	// TaskClaimable 是可领奖的任务数。
	TaskClaimable int `json:"task_claimable"`
	// TaskClaimed 是已领奖的任务数。
	TaskClaimed int `json:"task_claimed"`
	// TodayTaskCredits 是该账号当前任务池里已完成任务的积分合计。
	TodayTaskCredits float64 `json:"today_task_credits"`
	// Trend 是该账号近 30 天的每日积分趋势（来自上游 heatmap）。
	// 账号列表的缩略图直接从这里取，不必再逐账号请求上游。
	Trend []WorkBuddyDailyPoint `json:"trend,omitempty"`
	// AutoClaimed 是本轮自动领走的任务数与其积分。
	AutoClaimed        int       `json:"auto_claimed"`
	AutoClaimedCredits float64   `json:"auto_claimed_credits"`
	UpdatedAt          time.Time `json:"updated_at"`
	Err                string    `json:"err,omitempty"`
	// AutoPaused 表示本次同步因积分归零而自动停调。
	AutoPaused bool `json:"auto_paused,omitempty"`
}

// WorkBuddyCreditSnapshot 是全账号的同步快照。
type WorkBuddyCreditSnapshot struct {
	Accounts []WorkBuddyAccountSnapshot `json:"accounts"`
	SyncedAt time.Time                  `json:"synced_at"`
	// CheckinCredits 是全部账号的签到累计积分之和。
	CheckinCredits float64 `json:"checkin_credits"`
	// TotalCredits 是全部账号当前可用积分之和。
	TotalCredits float64 `json:"total_credits"`
	// TaskClaimable 是可领奖任务总数（自动领取后仍剩余的）。
	TaskClaimable int `json:"task_claimable"`
	// TodayTaskCredits 是全部账号的任务积分之和（上游任务状态口径）。
	TodayTaskCredits float64 `json:"today_task_credits"`
	// Daily 是近期每日活跃积分（上游 heatmap 的 score），供趋势图使用。
	Daily []WorkBuddyDailyPoint `json:"daily"`
	// AutoClaimedCredits 是本轮自动领取到的积分总和。
	AutoClaimedCredits float64 `json:"auto_claimed_credits"`
	Syncing            bool    `json:"syncing"`
}

// WorkBuddyCreditSyncService 维护积分快照。
type WorkBuddyCreditSyncService struct {
	adminService AdminService
	interval     time.Duration
	// rdb 是可选的 Redis，用于跨实例共享快照。
	rdb *redis.Client

	mu       sync.RWMutex
	snapshot *WorkBuddyCreditSnapshot
	syncing  bool
	lastSync time.Time
}

// NewWorkBuddyCreditSyncService 构造同步服务。
func NewWorkBuddyCreditSyncService(adminService AdminService, rdb *redis.Client) *WorkBuddyCreditSyncService {
	return &WorkBuddyCreditSyncService{
		adminService: adminService,
		interval:     5 * time.Minute,
		rdb:          rdb,
	}
}

// Start 启动后台循环。
func (s *WorkBuddyCreditSyncService) Start(ctx context.Context) {
	go func() {
		// 启动后立刻做首次同步：之前等 5 秒，加上串行处理 8 个账号，
		// 冷启动要 13 秒才出数。现在并发执行，等待也去掉。
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.Sync(ctx)
		ticker := time.NewTicker(s.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Sync(ctx)
			}
		}
	}()
}

// Snapshot 返回当前快照；从未同步过时返回空快照。
//
// 优先读 Redis：多实例部署时各实例共享同一份积分数据，不会出现
// 两个实例显示不同数字的情况。Redis 不可用时退回进程内快照。
func (s *WorkBuddyCreditSyncService) Snapshot() *WorkBuddyCreditSnapshot {
	if s.rdb != nil {
		raw, err := s.rdb.Get(context.Background(), workBuddySnapshotKey).Bytes()
		if err == nil {
			var snap WorkBuddyCreditSnapshot
			if json.Unmarshal(raw, &snap) == nil {
				snap.Syncing = s.isSyncing()
				return &snap
			}
		}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return &WorkBuddyCreditSnapshot{Accounts: []WorkBuddyAccountSnapshot{}}
	}
	out := *s.snapshot
	out.Syncing = s.syncing
	return &out
}

// isSyncing 报告当前是否有同步在进行。
func (s *WorkBuddyCreditSyncService) isSyncing() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.syncing
}

// workBuddySnapshotKey 是积分快照的 Redis 键。
const workBuddySnapshotKey = "auto2api:workbuddy:credit_snapshot"

// workBuddySnapshotTTL 是快照的过期时间：同步频率是 5 分钟，留 2 倍余量。
const workBuddySnapshotTTL = 10 * time.Minute

// ForceSync 在后台触发一次同步（页面打开时调用），立即返回当前快照。
// ForceSync 触发一次同步并**等待结果**。
//
// 之前是「后台跑、立即返回旧快照」——首次打开页面时旧快照是空的，
// 前端只能轮询等它填上，于是顶部四个数字会先显示一段时间的 0。
//
// 现在改成同步等待（最多 20 秒）：这段时间内页面显示加载态而不是 0，
// 拿到真实数字后一次性渲染。已有数据时仍然立即返回，不阻塞。
func (s *WorkBuddyCreditSyncService) ForceSync(ctx context.Context) *WorkBuddyCreditSnapshot {
	// 已有数据：先返回，同时后台刷新（避免每次打开都等）。
	if existing := s.Snapshot(); len(existing.Accounts) > 0 {
		s.mu.RLock()
		syncing := s.syncing
		s.mu.RUnlock()
		if !syncing {
			go s.Sync(context.WithoutCancel(ctx))
		}
		return existing
	}

	// 无数据：同步跑一轮，最多等 20 秒。
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.Sync(context.WithoutCancel(ctx))
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	case <-ctx.Done():
	}
	return s.Snapshot()
}

// HasData 报告快照里是否已有账号数据。
func (s *WorkBuddyCreditSyncService) HasData() bool {
	return len(s.Snapshot().Accounts) > 0
}

// Sync 从上游拉取全部国内站账号的积分与任务状态。
func (s *WorkBuddyCreditSyncService) Sync(ctx context.Context) {
	s.mu.Lock()
	if s.syncing {
		s.mu.Unlock()
		return
	}
	s.syncing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.lastSync = time.Now()
		s.mu.Unlock()
	}()

	accounts, _, err := s.adminService.ListAccounts(ctx, 1, 500, "", "", "", "", 0, "", "", "")
	if err != nil {
		log.Printf("[wb-sync] 读取账号失败: %v", err)
		return
	}
	targets := make([]*Account, 0, len(accounts))
	for i := range accounts {
		a := &accounts[i]
		// 两个站点都纳入同步：
		//   - 国内站：签到 + 任务 + 积分全套；
		//   - 国际站：没有签到/任务接口，但**有积分余额与 heatmap**，
		//     账号列表的缩略图需要它，不参与会导致国际版没有趋势图。
		if isWorkBuddyPlatform(a.Platform) && a.Status == StatusActive {
			targets = append(targets, a)
		}
	}

	snap := &WorkBuddyCreditSnapshot{
		Accounts: make([]WorkBuddyAccountSnapshot, 0, len(targets)),
		SyncedAt: time.Now(),
	}

	// 并发同步：串行时 8 个账号光是账号间隔就要 2.8 秒，
	// 加上每个账号内部的多次往返，冷启动要十几秒才出数。
	// 并发度限制在 6，兼顾速度与上游的并发容忍度。
	const concurrency = 6
	results := make([]WorkBuddyAccountSnapshot, len(targets))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, acc := range targets {
		wg.Add(1)
		go func(idx int, a *Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[idx] = s.syncAccount(ctx, a)
		}(i, acc)
	}
	wg.Wait()
	snap.Accounts = results

	// 积分归零即停调：额度用尽的账号继续被调度只会一路报错。
	// 与 Marvis 侧同一条规则，恢复靠充值或人工重新开启。
	for i := range results {
		row := &results[i]
		if row.Err != "" || row.TotalCredits > 0 {
			continue
		}
		for _, a := range targets {
			if a.ID != row.AccountID || !a.Schedulable {
				continue
			}
			if _, err := s.adminService.SetAccountSchedulable(ctx, a.ID, false); err == nil {
				row.AutoPaused = true
			}
			break
		}
	}

	// 趋势汇总：heatmap 已在 syncAccount 内拿到并写进各账号的 Trend，
	// 这里直接聚合，不再重复请求上游（此前每个账号要被请求两次）。
	byDay := map[string]float64{}
	for _, a := range results {
		for _, p := range a.Trend {
			byDay[p.Date] += p.Score
		}
	}
	snap.Daily = workBuddyBuildDailySeries(byDay, 30)

	for _, a := range snap.Accounts {
		snap.CheckinCredits += a.CheckinCredits
		snap.TotalCredits += a.TotalCredits
		snap.TaskClaimable += a.TaskClaimable
		snap.AutoClaimedCredits += a.AutoClaimedCredits
		snap.TodayTaskCredits += a.TodayTaskCredits
	}

	s.mu.Lock()
	s.snapshot = snap
	s.mu.Unlock()
	// 同步写一份到 Redis，供其它实例读取。
	if s.rdb != nil {
		if raw, err := json.Marshal(snap); err == nil {
			_ = s.rdb.Set(context.WithoutCancel(ctx), workBuddySnapshotKey, raw, workBuddySnapshotTTL).Err()
		}
	}
	log.Printf("[wb-sync] 已同步 %d 个账号的积分", len(snap.Accounts))
}

// syncAccount 同步单个账号。
func (s *WorkBuddyCreditSyncService) syncAccount(ctx context.Context, acc *Account) WorkBuddyAccountSnapshot {
	row := WorkBuddyAccountSnapshot{
		AccountID:   acc.ID,
		AccountName: acc.Name,
		UpdatedAt:   time.Now(),
	}

	// 1. 签到活动：仅国内站提供（国际站该接口 404）。
	// total_credits 是上游权威的累计签到积分。
	if acc.Platform == PlatformWorkBuddyCN {
		if st, err := WorkBuddyCheckinStatusOf(ctx, acc); err != nil {
			row.Err = err.Error()
		} else {
			row.StreakDays = st.StreakDays
			row.TodayCheckedIn = st.TodayCheckedIn
			row.DailyCredit = st.DailyCredit
			row.CheckinCredits = st.TotalCredits
		}
	}

	// 2. 积分余额：summary 的 TotalCredits + 各包余量。
	if credits, err := WorkBuddyCreditsOf(ctx, acc); err == nil {
		row.TotalCredits = credits.Total
		for _, g := range credits.Groups {
			row.PackageCredits += g.Remain
		}
	} else if row.Err == "" {
		row.Err = err.Error()
	}

	// 3. 每日趋势：上游 heatmap 给该账号 365 天数据，取近 30 天。
	if points, herr := workBuddyHeatmapOf(ctx, acc); herr == nil && len(points) > 0 {
		byDate := make(map[string]float64, len(points))
		for _, p := range points {
			byDate[p.Date] = p.Score
		}
		latest := ""
		for d := range byDate {
			if d > latest {
				latest = d
			}
		}
		if latest != "" {
			if end, perr := time.Parse("2006-01-02", latest); perr == nil {
				row.Trend = make([]WorkBuddyDailyPoint, 0, 30)
				for i := 29; i >= 0; i-- {
					key := end.AddDate(0, 0, -i).Format("2006-01-02")
					row.Trend = append(row.Trend, WorkBuddyDailyPoint{Date: key, Score: byDate[key]})
				}
			}
		}
	}

	// 4. 任务状态：仅国内站（国际站无任务接口）。
	if acc.Platform != PlatformWorkBuddyCN {
		return row
	}
	// 可领的一律自动领走，再统计结果。
	//
	// 面板不该出现「能领却没领」的任务——运营者要的是最终积分，而不是一堆
	// 待点按钮。领奖串行执行并带间隔，与手动执行同一条路径。
	if tasks, err := WorkBuddyTasksOf(ctx, acc); err == nil {
		claimable := make([]WorkBuddyTask, 0, len(tasks))
		for _, t := range tasks {
			if t.Status == "completed" && t.RewardCredit > 0 {
				claimable = append(claimable, t)
			}
		}
		for i, t := range claimable {
			if ctx.Err() != nil {
				break
			}
			if _, cerr := WorkBuddyClaimTask(ctx, acc, t.Code); cerr != nil {
				continue
			}
			row.AutoClaimed++
			row.AutoClaimedCredits += t.RewardCredit
			if i < len(claimable)-1 {
				select {
				case <-ctx.Done():
				case <-time.After(600 * time.Millisecond):
				}
			}
		}
		// 领完后重新统计，剩余的可领即领取失败的那部分。
		//
		// 只有 daily（每日任务）计入 TodayTaskCredits。上游返回的
		// accept_status 是任务池的累积状态，跨天不会回到未完成——一次性
		// 任务（first_buddy、skill_share 等）领过之后永远是 claimed。
		// 若把它们也算进「今日」，这个数字会在每天零点后依旧保持昨天的值，
		// 并且只增不减，与实际「今天赚了多少」脱节。
		//
		// once 类任务的积分仍然计入账号总积分（total_credits 由上游权威
		// 提供），这里只影响「今日任务积分」这个派生指标。
		if fresh, ferr := WorkBuddyTasksOf(ctx, acc); ferr == nil {
			for _, t := range fresh {
				isDaily := workBuddyTaskPeriod(t.Code) == "daily"
				switch t.Status {
				case "completed":
					if t.RewardCredit > 0 {
						row.TaskClaimable++
						if isDaily {
							row.TodayTaskCredits += t.RewardCredit
						}
					}
				case "claimed":
					row.TaskClaimed++
					if t.RewardCredit > 0 && isDaily {
						row.TodayTaskCredits += t.RewardCredit
					}
				}
			}
		}
	}

	return row
}

// WorkBuddyDailyPoint 是趋势图上的一天。
type WorkBuddyDailyPoint struct {
	Date string `json:"date"`
	// Score 是上游 heatmap 的活跃分（与积分同量级，反映当天活动量）。
	Score float64 `json:"score"`
	// Credits 是当天实际到账的积分（签到按 daily_credit 计，任务按台账计）。
	Credits float64 `json:"credits"`
}

// workBuddyHeatmapOf 读取账号近一年的每日活跃分。
func workBuddyHeatmapOf(ctx context.Context, account *Account) ([]WorkBuddyDailyPoint, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/heatmap", nil)
	if err != nil {
		return nil, err
	}
	rawCells, _ := data["cells"].([]any)
	out := make([]WorkBuddyDailyPoint, 0, len(rawCells))
	for _, raw := range rawCells {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		date := workBuddyStringField(m, "date")
		if date == "" {
			continue
		}
		out = append(out, WorkBuddyDailyPoint{
			Date:  date,
			Score: workBuddyTaskFloat(m, "score"),
		})
	}
	return out, nil
}

// workBuddyBuildDailySeries 把按天的数据补成连续序列（缺失日期补 0）。
func workBuddyBuildDailySeries(byDay map[string]float64, days int) []WorkBuddyDailyPoint {
	if days <= 0 {
		days = 30
	}
	// 以数据里最大的日期为准，而不是 now：上游按站点时区给日期，
	// 用本地时钟可能在跨日时错位一天。
	latest := ""
	for d := range byDay {
		if d > latest {
			latest = d
		}
	}
	if latest == "" {
		return []WorkBuddyDailyPoint{}
	}
	end, err := time.Parse("2006-01-02", latest)
	if err != nil {
		return []WorkBuddyDailyPoint{}
	}
	out := make([]WorkBuddyDailyPoint, 0, days)
	for i := days - 1; i >= 0; i-- {
		key := end.AddDate(0, 0, -i).Format("2006-01-02")
		out = append(out, WorkBuddyDailyPoint{Date: key, Score: byDay[key], Credits: byDay[key]})
	}
	return out
}
