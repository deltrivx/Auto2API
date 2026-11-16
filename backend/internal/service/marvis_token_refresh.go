package service

import (
	"context"
	"sync"
	"time"
)

// Marvis 的 access_token 由上游 Cookie 决定，微信登录只有 2 小时有效期
// （QQ 登录约 30 天）。到期后所有调用返回 401 4100403「登录态过期」，
// 额度同步随即读不到 data 字段，账号被判为零额度并自动停用。
//
// 上游提供 marvis_refresh_token 续期端点，并且**成对轮换** token：续期成功后
// 旧的 access/refresh 立即失效。因此同一账号的续期必须严格串行，两个并发
// 请求会把第二个请求的 refresh_token 打成废值，账号彻底失效。

const (
	// marvisRefreshLeadTime 是提前续期的窗口。
	//
	// token 只有 2 小时寿命，留 30 分钟缓冲：既能覆盖一轮同步的间隔，
	// 又不至于每轮都去续期（续期本身也消耗上游配额）。
	marvisRefreshLeadTime = 30 * time.Minute
)

// marvisRefreshLocks 按账号串行化续期。
//
// 用的是进程内锁：Auto2API 以单实例部署，多实例下需要换成 Redis 锁。
// 键为账号 ID，避免不同账号互相阻塞。
type marvisRefreshLocks struct {
	mu    sync.Mutex
	locks map[int64]*sync.Mutex
}

func newMarvisRefreshLocks() *marvisRefreshLocks {
	return &marvisRefreshLocks{locks: make(map[int64]*sync.Mutex)}
}

func (l *marvisRefreshLocks) get(accountID int64) *sync.Mutex {
	l.mu.Lock()
	defer l.mu.Unlock()
	m, ok := l.locks[accountID]
	if !ok {
		m = &sync.Mutex{}
		l.locks[accountID] = m
	}
	return m
}

// marvisNeedsRefresh 判断账号是否需要续期。
//
// 没有 access_token 或 refresh_token 的账号不处理——那是凭据缺失，
// 只能重新授权，续期只会返回错误。
func marvisNeedsRefresh(account *Account, now time.Time) bool {
	if account == nil {
		return false
	}
	creds := marvisCredentialsOf(account)
	if creds.AccessToken == "" || creds.RefreshToken == "" {
		return false
	}
	if creds.ExpiresAt == 0 {
		// 上游没给过期时间时保守处理：不去猜，交给调用失败后的人工介入。
		return false
	}
	return time.Unix(creds.ExpiresAt, 0).Sub(now) <= marvisRefreshLeadTime
}

// refreshMarvisAccount 为单个账号续期并落库。
//
// 调用方必须已经持有该账号的串行锁（见 marvisRefreshLocks）。
func (s *MarvisWalletSyncService) refreshMarvisAccount(ctx context.Context, account *Account) bool {
	if s == nil || s.admin == nil || account == nil {
		return false
	}
	creds := marvisCredentialsOf(account)
	refreshed, err := newMarvisOAuthForRefresh().RefreshToken(ctx, MarvisRefreshRequest{
		OpenID:       creds.OpenID,
		AccessToken:  creds.AccessToken,
		RefreshToken: creds.RefreshToken,
		GUID:         creds.GUID,
		LoginType:    creds.LoginType,
	})
	if err != nil {
		s.log.Warn("marvis_token_refresh_failed",
			"account_id", account.ID, "error", err)
		return false
	}

	// 上游成对轮换：新 token 必须整组写回，漏写任一项都会让账号在下次
	// 调用时失效。在原凭据上覆盖 token 字段，其余（头像、指纹、模型映射）保留。
	next := make(map[string]any, len(account.Credentials))
	for k, v := range account.Credentials {
		next[k] = v
	}
	next["access_token"] = refreshed.AccessToken
	if refreshed.RefreshToken != "" {
		next["refresh_token"] = refreshed.RefreshToken
	}
	if refreshed.OpenID != "" {
		next["openid"] = refreshed.OpenID
	}
	if refreshed.ExpiresAt > 0 {
		next["expires_at"] = float64(refreshed.ExpiresAt)
	}

	if _, err := s.admin.UpdateAccount(ctx, account.ID, &UpdateAccountInput{
		Credentials: next,
	}); err != nil {
		s.log.Warn("marvis_token_refresh_persist_failed",
			"account_id", account.ID, "error", err)
		return false
	}
	s.log.Info("marvis_token_refreshed",
		"account_id", account.ID,
		"expires_at", time.Unix(refreshed.ExpiresAt, 0).Format(time.RFC3339))
	return true
}

// newMarvisOAuthForRefresh 构造仅用于续期的 OAuth 服务。
//
// 续期只需要签名与 HTTP 调用，用标准构造函数即可。
func newMarvisOAuthForRefresh() *MarvisOAuthService {
	return NewMarvisOAuthService()
}
