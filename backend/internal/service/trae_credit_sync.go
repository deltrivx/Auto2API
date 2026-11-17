package service

import (
	"context"
	"sync"
	"time"
)

// Trae 积分快照。
//
// 与 WorkBuddy / Marvis 的快照同构：一次拉全部 Trae 账号的额度，供仪表盘
// 卡片与账号管理共用。额度只来自上游接口（国内站积分 / 国际站美元），
// 按平台口径折算成积分（1 美元 = 1 积分），不做任何本地记账或重置逻辑——
// 上游每日凌晨自行重置，下一次拉取自然反映新周期。

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
	TodayUsed    float64              `json:"today_used"`
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

	list, _, err := s.adminService.ListAccounts(ctx, 1, 200, "", "", "active", "", 0, "", "", "")
	if err != nil {
		return s.Snapshot()
	}

	rows := make([]TraeAccountCredits, 0, len(list))
	var totalSum, usedSum, availSum float64
	for i := range list {
		acc := &list[i]
		if !IsTraePlatform(acc.Platform) {
			continue
		}
		row := TraeAccountCredits{AccountID: acc.ID, Account: acc.Name, Platform: acc.Platform}
		credits, cErr := TraeCreditsSnapshot(ctx, acc)
		if cErr != nil {
			row.Error = cErr.Error()
		} else if credits != nil {
			row.Total = credits.Limit
			row.Used = credits.Used
			row.Available = credits.Limit - credits.Used
			if row.Available < 0 {
				row.Available = 0
			}
			row.Unlimited = credits.Unlimited
			totalSum += row.Total
			usedSum += row.Used
			availSum += row.Available
		}
		rows = append(rows, row)
	}

	s.mu.Lock()
	today := time.Now().Format("2006-01-02")
	var todayUsed float64
	if s.lastUsedDay == today {
		todayUsed = usedSum - s.lastUsed
		if todayUsed < 0 {
			todayUsed = 0
		}
	}
	s.lastUsed = usedSum
	s.lastUsedDay = today

	now := time.Now()
	s.snapshot = &TraeCreditSnapshot{
		Accounts:     rows,
		SyncedAt:     &now,
		TotalCredits: totalSum,
		UsedCredits:  usedSum,
		Available:    availSum,
		TodayUsed:    todayUsed,
	}
	cp := *s.snapshot
	s.mu.Unlock()
	return &cp
}

// Start 按间隔周期同步，直到 ctx 取消。
func (s *TraeCreditSyncService) Start(ctx context.Context) {
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
