package service

// WorkBuddy 官方今日消耗。
//
// 上游 /billing/meter/get-user-request-usage 返回逐条请求，每条带 credit
// 字段（官方扣掉的积分）。按 requestTime 过滤当天即为今日消耗。
//
// 这与本地 usage_logs.actual_cost 完全是两套口径：后者按我们自己配置的
// 模型单价算钱，与上游实际扣费差两个数量级，不能用来展示。

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"
)

// workBuddyUsagePageSize 是上游单页上限（实测 3000，超过会被截断）。
const workBuddyUsagePageSize = 3000

// workBuddyUsageMaxPages 是翻页上限，防止异常时死循环。
const workBuddyUsageMaxPages = 20

// workBuddyRequestUsage 拉取一个账号当天已扣的积分合计。
//
// 翻页方式是「以本页最新一条 requestTime 作为下一页起点」，与上游约定一致。
func workBuddyRequestUsage(ctx context.Context, account *Account, start, end string) (float64, int, error) {
	seen := make(map[string]struct{})
	credit := 0.0
	count := 0
	cursor := start

	for page := 0; page < workBuddyUsageMaxPages; page++ {
		body, _ := json.Marshal(map[string]any{
			"startTime": cursor,
			"endTime":   end,
			"pageNum":   1,
			"pageSize":  workBuddyUsagePageSize,
		})
		raw, err := workBuddyBillingPost(ctx, account, "/billing/meter/get-user-request-usage", body)
		if err != nil {
			return credit, count, err
		}
		var payload struct {
			Data struct {
				Data []map[string]any `json:"data"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return credit, count, err
		}
		rows := payload.Data.Data
		newest := ""
		for _, r := range rows {
			id, _ := r["requestId"].(string)
			t, _ := r["requestTime"].(string)
			if id == "" {
				continue
			}
			key := id + "@" + t
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			if v, ok := workBuddyFloatValue(r["credit"]); ok {
				credit += v
			}
			count++
			if t > newest {
				newest = t
			}
		}
		if len(rows) < workBuddyUsagePageSize || newest == "" {
			break
		}
		cursor = newest
	}
	return credit, count, nil
}

// workBuddyFloatValue 从上游返回的任意数字类型里取值。
func workBuddyFloatValue(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case json.Number:
		f, err := t.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// workBuddyTodayUsageForAccounts 并发拉取一组账号的今日消耗。
//
// 并发度限 4：24 个账号全量翻页一次要几十秒，串行会让接口超时。
func workBuddyTodayUsageForAccounts(ctx context.Context, accounts []*Account) (cn, intl float64) {
	today := time.Now().Format("2006-01-02")
	start := today + " 00:00:00"
	end := today + " 23:59:59"

	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)

	for _, acc := range accounts {
		acc := acc
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			credit, _, err := workBuddyRequestUsage(ctx, acc, start, end)
			if err != nil {
				return
			}
			mu.Lock()
			if acc.Platform == PlatformWorkBuddyIntl {
				intl += credit
			} else {
				cn += credit
			}
			mu.Unlock()
		}()
	}
	wg.Wait()
	return cn, intl
}
