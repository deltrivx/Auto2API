package service

// 腾讯 Marvis 的额度同步。
//
// 上游每天 0 点把当日额度重置回 1000 万计费 token，但钱包接口只在被调用时
// 才返回最新值。账号列表若每次都现场请求上游，打开页面会等一串串行 HTTP；
// 因此照 WorkBuddy 的做法在后台定时拉取并缓存，前端读缓存。
//
// 缓存写在 accounts.extra 的 marvis_wallet_* 字段里 —— 与 WorkBuddy 的
// workbuddy_credits_* 同一思路：进程重启不丢，也不需要额外的表。

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// MarvisWalletExtraKey 是 accounts.extra 里存钱包快照的键。
const MarvisWalletExtraKey = "marvis_wallet"

// 同步间隔。额度是「天」级重置，读写量不高；5 分钟足够让卡片跟上。
const marvisWalletSyncInterval = 5 * time.Minute

// MarvisWalletSnapshot 是一个账号的钱包快照。
type MarvisWalletSnapshot struct {
	UpdatedAt    time.Time `json:"updated_at"`
	TotalTokens  int64     `json:"total_tokens"`
	UsedTokens   int64     `json:"used_tokens"`
	FrozenTokens int64     `json:"frozen_tokens"`
	AvailTokens  int64     `json:"avail_tokens"`
	Error        string    `json:"error,omitempty"`
	// ResetDay 是这份数据所属的「额度日」（本地时间的 YYYY-MM-DD）。
	// 跨日之后旧快照不再可信，需要立刻重取。
	ResetDay string `json:"reset_day"`
}

// MarvisWalletSyncService 定时把 Marvis 账号的额度写进 accounts.extra。
type MarvisWalletSyncService struct {
	admin  AdminService
	syncMu sync.Mutex
	log    *slog.Logger
}

// NewMarvisWalletSyncService 构造同步服务。
func NewMarvisWalletSyncService(admin AdminService) *MarvisWalletSyncService {
	return &MarvisWalletSyncService{admin: admin, log: slog.Default()}
}

// Start 启动后台循环。
func (s *MarvisWalletSyncService) Start(ctx context.Context) {
	if s == nil || s.admin == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
			return
		default:
		}
		s.SyncOnce(ctx)
		ticker := time.NewTicker(marvisWalletSyncInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.SyncOnce(ctx)
			}
		}
	}()
}

// marvisLocalDay 返回本地时间的日期键，用于判断是否跨过额度重置点。
func marvisLocalDay(t time.Time) string {
	return t.Format("2006-01-02")
}

// SyncOnce 同步全部 Marvis 账号的额度。
func (s *MarvisWalletSyncService) SyncOnce(ctx context.Context) {
	if s == nil || s.admin == nil {
		return
	}
	// 单实例内串行：避免上一轮还没跑完又起一轮，把上游打成并发。
	if !s.syncMu.TryLock() {
		return
	}
	defer s.syncMu.Unlock()

	accounts, _, err := s.admin.ListAccounts(ctx, 1, 500, PlatformMarvis, "", "", "", 0, "", "", "")
	if err != nil {
		s.log.Warn("marvis_wallet_sync_list_failed", "error", err)
		return
	}

	now := time.Now()
	today := marvisLocalDay(now)
	for i := range accounts {
		acc := &accounts[i]
		if acc.Status != StatusActive {
			continue
		}
		snapshot := s.syncAccount(ctx, acc, today)
		if snapshot == nil {
			continue
		}
		if err := s.persist(ctx, acc, snapshot); err != nil {
			s.log.Warn("marvis_wallet_sync_persist_failed", "account_id", acc.ID, "error", err)
		}
	}
}

// syncAccount 拉取一个账号的钱包并返回快照。
func (s *MarvisWalletSyncService) syncAccount(ctx context.Context, acc *Account, today string) *MarvisWalletSnapshot {
	// 跨日时即使缓存里还有值也要重取：那是昨天的额度。
	if cached := MarvisWalletOfAccount(acc); cached != nil && cached.ResetDay == today && cached.Error == "" {
		if time.Since(cached.UpdatedAt) < marvisWalletSyncInterval {
			return nil
		}
	}
	wallet, err := FetchMarvisWallet(ctx, acc)
	if err != nil {
		s.log.Warn("marvis_wallet_fetch_failed", "account_id", acc.ID, "error", err)
		return &MarvisWalletSnapshot{
			UpdatedAt: time.Now(),
			ResetDay:  today,
			Error:     err.Error(),
		}
	}
	return &MarvisWalletSnapshot{
		UpdatedAt:    time.Now(),
		TotalTokens:  wallet.TotalTokens,
		UsedTokens:   wallet.UsedTokens,
		FrozenTokens: wallet.FrozenTokens,
		AvailTokens:  wallet.AvailTokens,
		ResetDay:     today,
	}
}

// persist 把快照写回 accounts.extra。
func (s *MarvisWalletSyncService) persist(ctx context.Context, acc *Account, snapshot *MarvisWalletSnapshot) error {
	extra := make(map[string]any, len(acc.Extra)+1)
	for k, v := range acc.Extra {
		extra[k] = v
	}
	extra[MarvisWalletExtraKey] = map[string]any{
		"updated_at":    snapshot.UpdatedAt.UTC().Format(time.RFC3339),
		"total_tokens":  snapshot.TotalTokens,
		"used_tokens":   snapshot.UsedTokens,
		"frozen_tokens": snapshot.FrozenTokens,
		"avail_tokens":  snapshot.AvailTokens,
		"reset_day":     snapshot.ResetDay,
		"error":         snapshot.Error,
	}
	acc.Extra = extra
	return s.admin.UpdateAccountExtra(ctx, acc.ID, extra)
}

// MarvisWalletOfAccount 从 accounts.extra 读回缓存的快照。
func MarvisWalletOfAccount(acc *Account) *MarvisWalletSnapshot {
	if acc == nil || acc.Extra == nil {
		return nil
	}
	raw, ok := acc.Extra[MarvisWalletExtraKey].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	num := func(k string) int64 {
		switch v := raw[k].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		case int:
			return int64(v)
		}
		return 0
	}
	str := func(k string) string {
		if v, ok := raw[k].(string); ok {
			return v
		}
		return ""
	}
	out := &MarvisWalletSnapshot{
		TotalTokens:  num("total_tokens"),
		UsedTokens:   num("used_tokens"),
		FrozenTokens: num("frozen_tokens"),
		AvailTokens:  num("avail_tokens"),
		ResetDay:     str("reset_day"),
		Error:        str("error"),
	}
	if ts, err := time.Parse(time.RFC3339, str("updated_at")); err == nil {
		out.UpdatedAt = ts
	}
	return out
}
