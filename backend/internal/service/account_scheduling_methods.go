package service

import (
	"context"
	"time"
)

// Account 的调度判定方法。
//
// 原先定义在 antigravity_quota_scope.go，但这些方法与平台无关，被共享的
// 网关调度与限流逻辑引用。Antigravity 移除后迁到这里。

// IsSchedulableForModel 结合模型级限流判断是否可调度。
//
// 保持旧签名以兼容既有调用方；默认使用 context.Background()。
func (a *Account) IsSchedulableForModel(requestedModel string) bool {
	return a.IsSchedulableForModelWithContext(context.Background(), requestedModel)
}

// IsSchedulableForModelWithContext 结合模型级限流判断是否可调度。
func (a *Account) IsSchedulableForModelWithContext(ctx context.Context, requestedModel string) bool {
	if a == nil {
		return false
	}
	if !a.IsSchedulable() {
		return false
	}
	if a.isModelRateLimitedWithContext(ctx, requestedModel) {
		return false
	}
	return true
}

// GetRateLimitRemainingTime 获取模型级限流的剩余时间。
//
// 返回 0 表示未限流或已过期。
func (a *Account) GetRateLimitRemainingTime(requestedModel string) time.Duration {
	return a.GetRateLimitRemainingTimeWithContext(context.Background(), requestedModel)
}

// GetRateLimitRemainingTimeWithContext 获取模型级限流的剩余时间。
//
// 返回 0 表示未限流或已过期。
func (a *Account) GetRateLimitRemainingTimeWithContext(ctx context.Context, requestedModel string) time.Duration {
	if a == nil {
		return 0
	}
	return a.GetModelRateLimitRemainingTimeWithContext(ctx, requestedModel)
}
