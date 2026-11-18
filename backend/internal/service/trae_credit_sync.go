package service

import (
	"context"
	"sync"
	"time"

	"github.com/DeltrivX/Auto2API/internal/pkg/timezone"
)

// Trae 积分快照。
//
// 与 WorkBuddy / Marvis 的快照同构：一次拉全部 Trae 账号的额度，供仪表盘
// 卡片与账号管理共用。额度只来自上游接口（国内站积分 / 国际站美元），
// 按平台口径折算成积分。
//
// ⚠ 上游 used 是**周期累计值，不随自然日重置**（实测跨 0 点后继续累加），
// 所以「今日消耗」必须用当日基准做差，不能直接把 used 当今日用量。

// TraeAccountCredits 是单个 Trae 账号的额度。
type TraeAccountCredits struct {
	AccountID int64   `json:"account_id"`
	Account   string  `json:"account"`
	Platform  string  `json:"platform"`
	Total     float64 `json:"total"`
	Used      float64 `json:"used"`
	Available float64 `json:"available"`
	Unlimited bool    `json:"unlimited,omitempty"`
	Error     string  `json:"error,omitempty"`
}

// TraeCreditSnapshot 是一次快照。
type TraeCreditSnapshot struct {
	Accounts     []TraeAccountCredits `json:"accounts"`
	SyncedAt     *time.Time           `json:"synced_at"`
	TotalCredits float64              `json:"total_credits"`
	UsedCredits  float64              `json:"used_credits"`
	Available    float64              `json:"available_credits"`
	// AvailableCN / AvailableIntl 是按站点拆开的余额（均已折算成积分）。
	AvailableCN   float64 `json:"available_cn"`
	AvailableIntl float64 `json:"available_intl"`
	// TodayUsed 是今日消耗（积分）。
	//
	// 上游 used 是**周期累计值**，且不随自然日重置（实测跨 0 点后继续累加），
	// 所以不能直接当今日消耗。这里用「当天首次同步记基准，之后求差」：
	//   todayUsed = usedSum - 当日基准
	// 跨天自动换基准，进程重启后第一次同步的当天值会有偏差（基准丢失），
	// 这是可接受的：下一次同步就回到准确值。
	TodayUsed float64 `json:"today_used"`
}

// TraeCreditSyncService 周期性拉取 Trae 账号额度。
type TraeCreditSyncService struct {
	adminService AdminService
	interval     time.Duration

	mu          sync.RWMutex
	snapshot    *TraeCreditSnapshot
	lastSync    time.Time
	syncing     bool
	lastUsed    float64
	lastUsedDay string
}

// NewTraeCreditSyncService 构造快照服务。
func NewTraeCreditSyncService(adminService AdminService) *TraeCreditSyncService {
	return &TraeCreditSyncService{adminService: adminService, interval: 5 * time.Minute}
}

// Snapshot 返回当前快照（未同步过时为空壳）。
func (s *TraeCreditSyncService) Snapshot() *TraeCreditSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return &TraeCreditSnapshot{Accounts: []TraeAccountCredits{}}
	}
	cp := *s.snapshot
	return &cp
}

// Sync 拉取全部 Trae 账号的额度并更新快照。
func (s *TraeCreditSyncService) Sync(ctx context.Context) *TraeCreditSnapshot {
	s.mu.Lock()
	if s.syncing {
		cp := TraeCreditSnapshot{Accounts: []TraeAccountCredits{}}
		if s.snapshot != nil {
			cp = *s.snapshot
		}
		s.mu.Unlock()
		return &cp
	}
	s.syncing = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.syncing = false
		s.lastSync = time.Now()
		s.mu.Unlock()
	}()

	// status 传空：仓库层对 "active" 的处理除状态匹配外还会追加
	// schedulable = true，账号一关调度就从快照里消失，余额会凭空归零。
	// 这里按状态在内存里过滤即可。
	list, _, err := s.adminService.ListAccounts(ctx, 1, 500, "", "", "", "", 0, "", "", "")
	if err != nil {
		return s.Snapshot()
	}

	rows := make([]TraeAccountCredits, 0, len(list))
	var totalSum, usedSum, availSum, availCNSum, availIntlSum float64
	for i := range list {
		acc := &list[i]
		if !IsTraePlatform(acc.Platform) || acc.Status != StatusActive {
			continue
		}
		row := TraeAccountCredits{AccountID: acc.ID, Account: acc.Name, Platform: acc.Platform}
		credits, cErr := TraeCreditsSnapshot(ctx, acc)
		if cErr != nil {
			row.Error = cErr.Error()
		} else if credits != nil {
			// 按展示口径折算成积分：国际站 1 美元 = 100 积分，国内站本身就是积分。
			limitPts, usedPts := TraeCreditsAsPoints(credits)
			row.Total = limitPts
			row.Used = usedPts
			row.Available = limitPts - usedPts
			if row.Available < 0 {
				row.Available = 0
			}
			row.Unlimited = credits.Unlimited
			totalSum += row.Total
			usedSum += row.Used
			availSum += row.Available
			// 按站点分别累加余额，卡片副标题要分开显示国内版 / 国际版。
			if acc.Platform == PlatformTraeCN {
				availCNSum += row.Available
			} else {
				availIntlSum += row.Available
			}
		}
		rows = append(rows, row)
	}

	// 今日消耗 = 周期累计已用 - 当日基准。基准在每天第一次同步时记录，
	// 当天后续同步都跟它对减，这样跨 0 点会自然归零重新计。
	today := time.Now().In(timezone.Location()).Format("2006-01-02")
	var todayUsed float64
	if s.lastUsedDay == today {
		todayUsed = usedSum - s.lastUsed
		if todayUsed < 0 {
			// 周期重置或基准偏大时直接归零，不要显示负数。
			todayUsed = 0
		}
	}
	s.lastUsed = usedSum
	s.lastUsedDay = today

	s.mu.Lock()
	now := time.Now()
	s.snapshot = &TraeCreditSnapshot{
		Accounts:      rows,
		SyncedAt:      &now,
		TotalCredits:  totalSum,
		UsedCredits:   usedSum,
		Available:     availSum,
		AvailableCN:   availCNSum,
		AvailableIntl: availIntlSum,
		TodayUsed:     todayUsed,
	}
	cp := *s.snapshot
	s.mu.Unlock()
	return &cp
}

// Start 按间隔周期同步，直到 ctx 取消。
func (s *TraeCreditSyncService) Start(ctx context.Context) {
	go func() {
		// 启动后等几秒再同步：此时 HTTP 服务刚起来，立刻拉 6 个账号的额度
		// 会和首屏请求抢资源，反而让第一个用户等更久。
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second):
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
