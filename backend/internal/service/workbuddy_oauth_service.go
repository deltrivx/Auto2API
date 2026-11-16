package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// WorkBuddy 登录服务的对外接口。
//
// workbuddy_oauth.go 里的实现是内部函数（workBuddy* 前缀），这里把它们
// 导出成管理端可直接调用的服务，并补上 handler 需要的返回结构。

// WorkBuddyOAuthService 是 WorkBuddy 登录服务。
type WorkBuddyOAuthService struct {
	impl *workBuddyOAuthService
	// trendStore 读取积分日汇总，供账号列表的趋势图使用。
	trendStore WorkBuddyCreditTrendStore
}

// NewWorkBuddyOAuthService 构造 WorkBuddy 登录服务。
func NewWorkBuddyOAuthService(
	trendStore WorkBuddyCreditTrendStore,
	runStore WorkBuddyTaskRunStore,
	boardCache *WorkBuddyBoardCache,
) *WorkBuddyOAuthService {
	if boardCache != nil {
		SetWorkBuddyBoardCache(boardCache)
	}
	// 注入即注册：这两个存储都通过包级变量被 service 内其它函数读取，
	// 在这里显式引用一次，否则 wire 会把它们的 provider 当成无用依赖裁掉，
	// 趋势图与执行台账会静默失效。
	if trendStore != nil {
		SetWorkBuddyCreditTrendStore(trendStore)
	}
	if runStore != nil {
		SetWorkBuddyTaskRunStore(runStore)
	}
	return &WorkBuddyOAuthService{impl: newWorkBuddyOAuthService(), trendStore: trendStore}
}

// WorkBuddyAuthURLResult 是生成授权链接的返回。
type WorkBuddyAuthURLResult struct {
	// AuthURL 是需要在浏览器打开的授权地址。
	AuthURL string `json:"auth_url"`
	// State 是后续轮询的凭据。
	State string `json:"state"`
	// Platform 回显平台，便于前端区分两个站点。
	Platform string `json:"platform"`
	// ExpiresAt 是本次登录会话的过期时间（epoch 秒）。
	ExpiresAt int64 `json:"expires_at"`
}

// WorkBuddyPollResult 是轮询结果。
//
// Pending 为 true 表示用户尚未在浏览器完成授权，前端应继续轮询；
// 此时其余字段为空，不是错误。
type WorkBuddyPollResult struct {
	Pending bool `json:"pending"`
	// 以下字段在 Pending 为 false 时有效。
	AccessToken    string `json:"access_token,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	RefreshExpires int64  `json:"refresh_expires_at,omitempty"`
	Domain         string `json:"domain,omitempty"`
	TokenType      string `json:"token_type,omitempty"`
	UID            string `json:"uid,omitempty"`
	EnterpriseID   string `json:"enterprise_id,omitempty"`
	AccountName    string `json:"account_name,omitempty"`
	Platform       string `json:"platform"`
}

// WorkBuddyRefreshResult 是刷新结果。
type WorkBuddyRefreshResult struct {
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	ExpiresAt      int64  `json:"expires_at,omitempty"`
	RefreshExpires int64  `json:"refresh_expires_at,omitempty"`
	Domain         string `json:"domain,omitempty"`
	TokenType      string `json:"token_type,omitempty"`
}

// GenerateAuthURL 生成指定站点的授权链接。
func (s *WorkBuddyOAuthService) GenerateAuthURL(ctx context.Context, platform string) (*WorkBuddyAuthURLResult, error) {
	site := workBuddySiteForPlatform(platform)
	if site == nil {
		return nil, fmt.Errorf("不支持的平台: %s", platform)
	}
	signIn, err := s.impl.workBuddyStartSignIn(ctx, site)
	if err != nil {
		return nil, err
	}
	return &WorkBuddyAuthURLResult{
		AuthURL:   signIn.AuthURL,
		State:     signIn.State,
		Platform:  site.ID,
		ExpiresAt: signIn.ExpiresAt.Unix(),
	}, nil
}

// Poll 轮询一次登录结果。
//
// 浏览器授权是异步的，因此这里区分「还没好」（Pending）与「失败」：
// 前者让前端继续轮询，后者才报错。
func (s *WorkBuddyOAuthService) Poll(ctx context.Context, platform, state string) (*WorkBuddyPollResult, error) {
	site := workBuddySiteForPlatform(platform)
	if site == nil {
		return nil, fmt.Errorf("不支持的平台: %s", platform)
	}
	state = strings.TrimSpace(state)
	if state == "" {
		return nil, errors.New("state 不能为空")
	}

	result, err := s.impl.workBuddyPollOnce(ctx, site, state)
	if err != nil {
		// 授权未完成属于正常等待，不当成错误抛给前端。
		if errors.Is(err, errWorkBuddySignInPending) {
			return &WorkBuddyPollResult{Pending: true, Platform: site.ID}, nil
		}
		return nil, err
	}

	return &WorkBuddyPollResult{
		Pending:        false,
		AccessToken:    result.Token.Access,
		RefreshToken:   result.Token.Refresh,
		ExpiresAt:      result.Token.Expires,
		RefreshExpires: result.Token.RefreshExpires,
		Domain:         result.Token.Domain,
		TokenType:      result.Token.TokenType,
		UID:            result.UID,
		EnterpriseID:   result.EnterpriseID,
		AccountName:    result.Name,
		Platform:       site.ID,
	}, nil
}

// Refresh 用 refresh token 换新 token。
func (s *WorkBuddyOAuthService) Refresh(ctx context.Context, platform, refreshToken string) (*WorkBuddyRefreshResult, error) {
	site := workBuddySiteForPlatform(platform)
	if site == nil {
		return nil, fmt.Errorf("不支持的平台: %s", platform)
	}
	refreshToken = strings.TrimSpace(refreshToken)
	if refreshToken == "" {
		return nil, errors.New("refresh_token 不能为空")
	}

	token := workBuddyToken{Refresh: refreshToken}
	next, err := workBuddyRefresh(ctx, nil, site, token, nil)
	if err != nil {
		return nil, err
	}
	return &WorkBuddyRefreshResult{
		AccessToken:    next.Access,
		RefreshToken:   next.Refresh,
		ExpiresAt:      next.Expires,
		RefreshExpires: next.RefreshExpires,
		Domain:         next.Domain,
		TokenType:      next.TokenType,
	}, nil
}
