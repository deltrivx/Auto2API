package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// WorkBuddy 看板的共享缓存。
//
// 缓存放 Redis 而不是进程内存：部署到多实例后，每个实例的看板必须看到
// 同一份数据；进程内缓存会让不同实例各自维护一份，用户刷新页面时可能
// 在不同实例间看到 30 秒的差异。
//
// 设计要点：
//   - 读：命中直接返回，无锁；
//   - 写：先置「刷新中」标记（SETNX），避免多实例同时打到上游；
//   - 失效：执行任务后删除键，下次读取立即重建。

const (
	// workBuddyBoardCacheKey 是看板的 Redis 键。
	workBuddyBoardCacheKey = "auto2api:workbuddy:task_board"
	// workBuddyBoardLockKey 是「正在刷新」的分布式锁。
	workBuddyBoardLockKey = "auto2api:workbuddy:task_board:lock"
	// workBuddyBoardTTL：缓存有效期。超过后视为过期，需要重建。
	workBuddyBoardTTL = 30 * time.Second
	// workBuddyBoardLockTTL：刷新锁的租期，防止实例崩溃后锁不释放。
	workBuddyBoardLockTTL = 60 * time.Second
)

// WorkBuddyBoardCache 是看板的 Redis 缓存。
type WorkBuddyBoardCache struct {
	rdb *redis.Client
}

// NewWorkBuddyBoardCache 构造看板缓存。
func NewWorkBuddyBoardCache(rdb *redis.Client) *WorkBuddyBoardCache {
	return &WorkBuddyBoardCache{rdb: rdb}
}

// Get 读取缓存看板；未命中返回 nil。
func (c *WorkBuddyBoardCache) Get(ctx context.Context) *WorkBuddyTaskBoard {
	if c == nil || c.rdb == nil {
		return nil
	}
	raw, err := c.rdb.Get(context.WithoutCancel(ctx), workBuddyBoardCacheKey).Bytes()
	if err != nil {
		return nil
	}
	var board WorkBuddyTaskBoard
	if err := json.Unmarshal(raw, &board); err != nil {
		return nil
	}
	return &board
}

// Set 写入缓存看板。
func (c *WorkBuddyBoardCache) Set(ctx context.Context, board *WorkBuddyTaskBoard) {
	if c == nil || c.rdb == nil || board == nil {
		return
	}
	raw, err := json.Marshal(board)
	if err != nil {
		return
	}
	_ = c.rdb.Set(context.WithoutCancel(ctx), workBuddyBoardCacheKey, raw, workBuddyBoardTTL).Err()
}

// Invalidate 删除缓存，使下次读取重建。
func (c *WorkBuddyBoardCache) Invalidate(ctx context.Context) {
	if c == nil || c.rdb == nil {
		return
	}
	_ = c.rdb.Del(context.WithoutCancel(ctx), workBuddyBoardCacheKey).Err()
}

// TryLock 抢占刷新锁，返回 true 表示本实例负责这一轮刷新。
//
// 多实例部署时，缓存过期瞬间所有实例都会想重建；用 SETNX 只放行一个，
// 其余实例继续用旧缓存返回，避免同时打 154 次上游。
func (c *WorkBuddyBoardCache) TryLock(ctx context.Context) bool {
	if c == nil || c.rdb == nil {
		return true // 无 Redis 时退化为单实例行为
	}
	ok, err := c.rdb.SetNX(context.WithoutCancel(ctx), workBuddyBoardLockKey,
		fmt.Sprintf("locked:%d", time.Now().UnixNano()), workBuddyBoardLockTTL).Result()
	return err == nil && ok
}

// Unlock 释放刷新锁。
func (c *WorkBuddyBoardCache) Unlock(ctx context.Context) {
	if c == nil || c.rdb == nil {
		return
	}
	_ = c.rdb.Del(context.WithoutCancel(ctx), workBuddyBoardLockKey).Err()
}

// workBuddyBoardCache 是包级单例，由 NewWorkBuddyOAuthService 注入。
var workBuddyBoardCache *WorkBuddyBoardCache

// SetWorkBuddyBoardCache 注入共享缓存。
func SetWorkBuddyBoardCache(cache *WorkBuddyBoardCache) {
	workBuddyBoardCache = cache
}

// WorkBuddyBoardCacheOf 返回当前的共享缓存（可能为 nil）。
func WorkBuddyBoardCacheOf() *WorkBuddyBoardCache {
	return workBuddyBoardCache
}
