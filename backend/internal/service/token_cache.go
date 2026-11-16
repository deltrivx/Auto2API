package service

import (
	"context"
	"time"
)

// GeminiTokenCache 是 OAuth token 缓存接口。
//
// 名字保留 Gemini 前缀是为了不改动 30+ 处既有引用；实际与平台无关，
// 所有 OAuth 平台共用同一套缓存与刷新锁。
type GeminiTokenCache interface {
	// GetAccessToken 读取缓存的 access token；cacheKey 需在同一 token 作用域内稳定。
	GetAccessToken(ctx context.Context, cacheKey string) (string, error)
	// SetAccessToken 写入 access token 并设置 TTL。
	SetAccessToken(ctx context.Context, cacheKey string, token string, ttl time.Duration) error
	// DeleteAccessToken 使缓存失效。
	DeleteAccessToken(ctx context.Context, cacheKey string) error

	// AcquireRefreshLock 尝试取得刷新锁；返回 true 表示由本请求负责刷新。
	AcquireRefreshLock(ctx context.Context, cacheKey string, ttl time.Duration) (bool, error)
	// ReleaseRefreshLock 释放刷新锁。
	ReleaseRefreshLock(ctx context.Context, cacheKey string) error
}
