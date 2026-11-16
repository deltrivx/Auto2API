package service

import (
	"context"
	"sync"
	"time"
)

// WorkBuddy 积分消耗趋势。
//
// 账号列表的积分单元格除了余额，还要显示近 N 天的消耗柱状图。
// 数据源是 workbuddy_credit_history（按天汇总），逐次调用记录只在内存里，
// 进程重启即丢，不足以画曲线。

// WorkBuddyCreditDailySummary 是按天的积分汇总。
type WorkBuddyCreditDailySummary struct {
	// Date 是 YYYY-MM-DD。
	Date string `json:"date"`
	// Credits 是当日消耗的积分。
	Credits float64 `json:"credits"`
	// Earned 是当日获得的积分（签到 / 任务奖励）。
	Earned float64 `json:"earned,omitempty"`
	// Calls 是当日调用次数。
	Calls int `json:"calls"`
}

// WorkBuddyCreditTrendStore 是趋势数据所需的仓储能力。
type WorkBuddyCreditTrendStore interface {
	// SumCreditsByDay 返回 [from, to] 区间内每天的消耗与获得积分。
	SumCreditsByDay(ctx context.Context, from, to time.Time) (map[string][2]float64, error)
	// CountCallsByDay 返回区间内每天的调用次数。
	CountCallsByDay(ctx context.Context, from, to time.Time) (map[string]int, error)
	// SumCreditsByDayForAccount 返回单个账号区间内每天的 [消耗, 获得] 积分。
	SumCreditsByDayForAccount(ctx context.Context, accountID int64, from, to time.Time) (map[string][2]float64, error)
}

// WorkBuddyCreditCreditHistoryStore 是写入侧：把当日汇总累加进表。
type WorkBuddyCreditCreditHistoryStore interface {
	// AddDailyCredits 把 consumed/earned 累加到指定日期。
	AddDailyCredits(ctx context.Context, day time.Time, consumed, earned float64, source string) error
}

// workBuddyCreditTrendStore 由 repository 在启动时注入。
var workBuddyCreditTrendStore WorkBuddyCreditTrendStore

// SetWorkBuddyCreditTrendStore 注入趋势仓储。
func SetWorkBuddyCreditTrendStore(store WorkBuddyCreditTrendStore) {
	workBuddyCreditTrendStore = store
}

// WorkBuddyCreditTrend 返回近 days 天每天的积分消耗，缺失日期补 0。
//
// days 超出 [1, 60] 时回落到 14。数据不可用时返回全 0 序列而不是空数组——
// 前端据此仍能画出坐标轴，不会因为一次查询失败就整块消失。
func WorkBuddyCreditTrend(ctx context.Context, days int) []WorkBuddyCreditDailySummary {
	if days <= 0 || days > 60 {
		days = 14
	}
	now := time.Now()
	order := make([]string, 0, days)
	buckets := make(map[string]*WorkBuddyCreditDailySummary, days)
	for i := days - 1; i >= 0; i-- {
		day := now.AddDate(0, 0, -i).Format("2006-01-02")
		buckets[day] = &WorkBuddyCreditDailySummary{Date: day}
		order = append(order, day)
	}

	if workBuddyCreditTrendStore != nil {
		from := now.AddDate(0, 0, -(days - 1))
		if sums, err := workBuddyCreditTrendStore.SumCreditsByDay(ctx, from, now); err == nil {
			for day, pair := range sums {
				if b, ok := buckets[day]; ok {
					b.Credits = pair[0]
					b.Earned = pair[1]
				}
			}
		}
		if calls, err := workBuddyCreditTrendStore.CountCallsByDay(ctx, from, now); err == nil {
			for day, n := range calls {
				if b, ok := buckets[day]; ok {
					b.Calls = n
				}
			}
		}
	}

	out := make([]WorkBuddyCreditDailySummary, 0, days)
	for _, day := range order {
		out = append(out, *buckets[day])
	}
	return out
}

// WorkBuddyAccountCreditTrend 返回**单个账号**近 days 天的积分趋势。
//
// 账号列表里的缩略图必须每个账号各不相同，因此数据必须按账号取：
//   - 优先用上游 heatmap（该账号 365 天的每日活跃分，最权威）；
//   - 上游不可用时退回本地台账按账号过滤。
//
// 之前的实现只聚合全站数据，导致所有账号的缩略图长得一模一样。
// accountTrendCache 缓存单个账号的趋势，避免同一账号被反复请求上游。
//
// 账号列表滚动、刷新都会触发该接口；上游 heatmap 一次返回 365 天，
// 没必要每次都拉。缓存 3 分钟，与快照同步周期匹配。
var (
	accountTrendCacheMu sync.RWMutex
	accountTrendCache   = map[int64]accountTrendEntry{}
)

type accountTrendEntry struct {
	points []WorkBuddyCreditDailySummary
	at     time.Time
}

const accountTrendTTL = 3 * time.Minute

func WorkBuddyAccountCreditTrend(ctx context.Context, account *Account, days int) []WorkBuddyCreditDailySummary {
	if days <= 0 || days > 60 {
		days = 14
	}
	if account == nil {
		return make([]WorkBuddyCreditDailySummary, 0, days)
	}

	// 缓存命中：直接切片返回。
	accountTrendCacheMu.RLock()
	entry, ok := accountTrendCache[account.ID]
	accountTrendCacheMu.RUnlock()
	if ok && time.Since(entry.at) < accountTrendTTL && len(entry.points) >= days {
		out := make([]WorkBuddyCreditDailySummary, days)
		copy(out, entry.points[len(entry.points)-days:])
		return out
	}
	// 1. 上游 heatmap：该账号自己的每日数据。
	if account != nil {
		if points, err := workBuddyHeatmapOf(ctx, account); err == nil && len(points) > 0 {
			byDate := make(map[string]float64, len(points))
			for _, p := range points {
				byDate[p.Date] = p.Score
			}
			// 以数据里最大的日期为准补齐序列（与站点时区一致）。
			latest := ""
			for d := range byDate {
				if d > latest {
					latest = d
				}
			}
			if latest != "" {
				if end, perr := time.Parse("2006-01-02", latest); perr == nil {
					// 缓存固定的 30 天，请求不同 days 时从中切片。
					full := make([]WorkBuddyCreditDailySummary, 0, 30)
					for i := 29; i >= 0; i-- {
						key := end.AddDate(0, 0, -i).Format("2006-01-02")
						full = append(full, WorkBuddyCreditDailySummary{Date: key, Credits: byDate[key]})
					}
					accountTrendCacheMu.Lock()
					accountTrendCache[account.ID] = accountTrendEntry{points: full, at: time.Now()}
					accountTrendCacheMu.Unlock()

					if days <= len(full) {
						out := make([]WorkBuddyCreditDailySummary, days)
						copy(out, full[len(full)-days:])
						return out
					}
					return full
				}
			}
		}
	}

	// 2. 回退：本地台账按账号过滤（只在没有上游数据时用）。
	if workBuddyCreditTrendStore != nil && account != nil {
		now := time.Now()
		from := now.AddDate(0, 0, -(days - 1))
		if sums, err := workBuddyCreditTrendStore.SumCreditsByDayForAccount(ctx, account.ID, from, now); err == nil {
			out := make([]WorkBuddyCreditDailySummary, 0, days)
			for i := days - 1; i >= 0; i-- {
				key := now.AddDate(0, 0, -i).Format("2006-01-02")
				out = append(out, WorkBuddyCreditDailySummary{Date: key, Credits: sums[key][0]})
			}
			return out
		}
	}

	// 3. 都没有：返回全 0 序列（前端仍能画出基线）。
	out := make([]WorkBuddyCreditDailySummary, 0, days)
	now := time.Now()
	for i := days - 1; i >= 0; i-- {
		out = append(out, WorkBuddyCreditDailySummary{Date: now.AddDate(0, 0, -i).Format("2006-01-02")})
	}
	return out
}
