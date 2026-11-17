package service

import (
	"context"
	"sync"
	"time"
)

// Marvis 积分快照。
//
// 与 WorkBuddy 的快照形状保持一致，方便前端用同一套卡片渲染：
// 总数、已用、今日新增。Marvis 没有签到/任务体系，签到与任务两项恒为 0，
// 前端据此决定显示哪几行。
//
// 数据直接读上游钱包（/v3/marvis_get_wallet），不落本地台账——
// 用户在客户端或其他地方消耗的额度，只有上游数据是准确的。

// MarvisAccountCredits 是单个账号的积分。
type MarvisAccountCredits struct {
	AccountID int64  `json:"account_id"`
	Account   string `json:"account"`
	// LoginType 是授权方式：1=QQ、2=微信（凭据里的 login_type，缺省按 QQ）。
	// 卡片副标题要按授权方式分别汇总余额，所以必须带上。
	LoginType string `json:"login_type"`
	// Total / Used / Available 均为积分（已由 token 换算）。
	Total       float64 `json:"total"`
	Used        float64 `json:"used"`
	Available   float64 `json:"available"`
	UsedPercent float64 `json:"used_percent"`
	Error       string  `json:"error,omitempty"`
	// AutoPaused 表示本次同步因额度归零而自动停调。
	AutoPaused bool `json:"auto_paused,omitempty"`
}

// MarvisCreditSnapshot 是全部 Marvis 账号的积分汇总。
type MarvisCreditSnapshot struct {
	Accounts []MarvisAccountCredits `json:"accounts"`
	SyncedAt time.Time              `json:"synced_at"`
	// TotalCredits 是全部账号的总额度之和。
	TotalCredits float64 `json:"total_credits"`
	// UsedCredits 是全部账号的已用额度之和。
	UsedCredits float64 `json:"used_credits"`
	// AvailableCredits 是全部账号的可用额度之和。
	AvailableCredits float64 `json:"available_credits"`
	// AvailableQQ / AvailableWX 是按授权方式拆开的可用余额：
	// QQ 登录的账号之和 / 微信登录的账号之和。
	AvailableQQ float64 `json:"available_qq"`
	AvailableWX float64 `json:"available_wx"`
	// TodayUsed 是今日消耗（用上次快照的差值估算，跨天归零）。
	TodayUsed float64 `json:"today_used"`
	Syncing   bool    `json:"syncing"`
}

// MarvisCreditSyncService 维护 Marvis 积分快照。
type MarvisCreditSyncService struct {
	adminService AdminService
	interval     time.Duration

	mu       sync.RWMutex
	snapshot *MarvisCreditSnapshot
	syncing  bool
	lastSync time.Time

	// 今日消耗用差值推算：记录上一次采样时的累计已用量与其日期。
	lastUsedTotal float64
	lastUsedDay   string
}

// NewMarvisCreditSyncService 构造同步服务。
func NewMarvisCreditSyncService(adminService AdminService, interval time.Duration) *MarvisCreditSyncService {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	return &MarvisCreditSyncService{
		adminService: adminService,
		interval:     interval,
	}
}

// Interval 返回同步间隔。
func (s *MarvisCreditSyncService) Interval() time.Duration { return s.interval }

// Snapshot 返回最近一次快照（不触发同步）。
func (s *MarvisCreditSyncService) Snapshot() *MarvisCreditSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return &MarvisCreditSnapshot{Accounts: []MarvisAccountCredits{}}
	}
	cp := *s.snapshot
	return &cp
}

// Sync 拉取全部 Marvis 账号的钱包并更新快照。
func (s *MarvisCreditSyncService) Sync(ctx context.Context) *MarvisCreditSnapshot {
	s.mu.Lock()
	if s.syncing {
		cp := MarvisCreditSnapshot{Accounts: []MarvisAccountCredits{}}
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

	accounts, err := s.marvisAccounts(ctx)
	if err != nil {
		return s.Snapshot()
	}

	rows := make([]MarvisAccountCredits, 0, len(accounts))
	var totalSum, usedSum, availSum, availQQSum, availWXSum float64
	for i := range accounts {
		acc := accounts[i]
		row := MarvisAccountCredits{
			AccountID: acc.ID,
			Account:   acc.Name,
			LoginType: marvisCredentialsOf(&accounts[i]).LoginType,
		}
		wallet, wErr := FetchMarvisWallet(ctx, &accounts[i])
		if wErr != nil {
			row.Error = wErr.Error()
		} else {
			// 上游钱包按 token 计量，快照对外一律换算成积分
			// （1 积分 = 1000 token），与账号列表、明细弹窗口径一致。
			row.Total = TokensToCredits(wallet.TotalTokens)
			row.Used = TokensToCredits(wallet.UsedTokens)
			row.Available = TokensToCredits(wallet.AvailTokens)
			if wallet.TotalTokens > 0 {
				row.UsedPercent = float64(wallet.UsedTokens) / float64(wallet.TotalTokens) * 100
			}
			totalSum += row.Total
			usedSum += row.Used
			availSum += row.Available
			// 按授权方式分别累加余额，卡片的副标题要分开显示 QQ / 微信。
			if row.LoginType == MarvisLoginTypeWX {
				availWXSum += row.Available
			} else {
				availQQSum += row.Available
			}

			// 积分归零即停调：额度用尽的账号继续被调度只会一路报错。
			// 恢复靠充值或人工重新开启，这里只管单向关闭。
			if wallet.AvailTokens <= 0 && acc.Schedulable {
				if _, sErr := s.adminService.SetAccountSchedulable(ctx, acc.ID, false); sErr == nil {
					row.AutoPaused = true
				}
			}
		}
		rows = append(rows, row)
	}

	s.mu.Lock()
	today := time.Now().Format("2006-01-02")
	var todayUsed float64
	if s.lastUsedDay == today {
		// 同一天内：本次累计已用 - 上次累计已用 = 今日新增消耗。
		todayUsed = usedSum - s.lastUsedTotal
		if todayUsed < 0 {
			todayUsed = 0
		}
	}
	s.lastUsedTotal = usedSum
	s.lastUsedDay = today

	s.snapshot = &MarvisCreditSnapshot{
		Accounts:         rows,
		SyncedAt:         time.Now(),
		TotalCredits:     totalSum,
		UsedCredits:      usedSum,
		AvailableCredits: availSum,
		AvailableQQ:      availQQSum,
		AvailableWX:      availWXSum,
		TodayUsed:        todayUsed,
	}
	cp := *s.snapshot
	s.mu.Unlock()
	return &cp
}

// marvisAccounts 列出全部 Marvis 账号。
//
// 这里**不能**传 status="active"：仓库层对 active 的处理除了 status 匹配，
// 还会追加 schedulable = true（account_repo.go 的 accountListFilteredQuery）。
// 一旦账号被置为不调度（额度用尽自动停调、或人工关闭），就整个查不出来，
// 卡片上的余额会变成 0 —— 而余额跟调不调度毫无关系。
//
// 所以按平台查询，再在内存里只保留状态正常的账号（排除 error / disabled）。
func (s *MarvisCreditSyncService) marvisAccounts(ctx context.Context) ([]Account, error) {
	const pageSize = 200
	list, _, err := s.adminService.ListAccounts(ctx, 1, pageSize, PlatformMarvis, "", "", "", 0, "", "", "")
	if err != nil {
		return nil, err
	}
	out := make([]Account, 0, len(list))
	for i := range list {
		if list[i].Status == StatusActive {
			out = append(out, list[i])
		}
	}
	return out, nil
}

// Start 按间隔周期同步，直到 ctx 取消。
func (s *MarvisCreditSyncService) Start(ctx context.Context) {
	go func() {
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
