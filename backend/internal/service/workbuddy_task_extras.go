package service

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// WorkBuddy 补充型能力：补签、礼物补偿、连签兑换。
//
// 这些不是「每日必做」的任务，而是有资源时才值得执行的动作：
//   - 补签：消耗补签卡修补断签，恢复连签天数；
//   - 礼物补偿：领取运营发放的补偿礼包；
//   - 连签兑换：达到 7/14/28 天档位后兑换额外奖励。
//
// 全部先读上游状态再决定是否动作，没有资源时返回明确原因而不是盲发请求。

// workBuddyStreakState 是连签与补签卡读数。
type workBuddyStreakState struct {
	Days            int
	MakeupCards     int
	MakeupMax       int
	MakeupDates     []string
	RedeemableTiers int
}

// workBuddyStreakOf 读取连签状态。
func workBuddyStreakOf(ctx context.Context, account *Account) (*workBuddyStreakState, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/streak", nil)
	if err != nil {
		return nil, err
	}
	st := &workBuddyStreakState{}
	if streak, ok := data["streak"].(map[string]any); ok {
		st.Days = workBuddyTaskInt(streak, "days")
		if dates, ok := streak["makeup_dates"].([]any); ok {
			for _, d := range dates {
				if s, ok := d.(string); ok {
					st.MakeupDates = append(st.MakeupDates, s)
				}
			}
		}
	}
	if cards, ok := data["makeup_cards"].(map[string]any); ok {
		st.MakeupCards = workBuddyTaskInt(cards, "balance")
		st.MakeupMax = workBuddyTaskInt(cards, "max")
	}
	if rs, ok := data["redemption_status"].(map[string]any); ok {
		// 可兑换档位：状态为 available 的 tier。
		if tiers, ok := rs["tiers"].([]any); ok {
			for _, raw := range tiers {
				t, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				key := workBuddyStringField(t, "tier") + "_status"
				if workBuddyStringField(rs, key) == "available" {
					st.RedeemableTiers++
				}
			}
		}
	}
	return st, nil
}

// WorkBuddyDoMakeup 使用补签卡修补断签。
//
// 每轮默认只补一张：补签卡是稀缺资源，一次用光反而失去灵活性。
func WorkBuddyDoMakeup(ctx context.Context, account *Account, maxPerRun int) (*WorkBuddyClaimResult, error) {
	res := &WorkBuddyClaimResult{Code: "makeup", Title: "补签"}
	if maxPerRun <= 0 {
		maxPerRun = 1
	}
	st, err := workBuddyStreakOf(ctx, account)
	if err != nil {
		return nil, err
	}
	if len(st.MakeupDates) == 0 {
		res.Err = "没有可补的断签日期"
		return res, nil
	}
	if st.MakeupCards <= 0 {
		res.Err = fmt.Sprintf("有 %d 个断签但补签卡不足", len(st.MakeupDates))
		return res, nil
	}

	// 由近及远补：越近的断签对连签天数影响越大。
	dates := append([]string(nil), st.MakeupDates...)
	sort.Sort(sort.Reverse(sort.StringSlice(dates)))

	used := 0
	for _, date := range dates {
		if used >= maxPerRun || used >= st.MakeupCards {
			break
		}
		if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
			"/v2/activity/growth/makeup-cards/use", map[string]any{"target_date": date}); err != nil {
			continue
		}
		used++
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "task", TaskCode: "makeup", TaskName: "补签 " + date,
			Result: "success",
		})
	}
	if used == 0 {
		res.Err = "补签未生效"
		return res, nil
	}
	res.Credit = -1 // 补签恢复连签，不直接给积分
	return res, nil
}

// WorkBuddyClaimGiftCompensation 领取运营补偿礼包。
func WorkBuddyClaimGiftCompensation(ctx context.Context, account *Account) (*WorkBuddyClaimResult, error) {
	res := &WorkBuddyClaimResult{Code: "gift", Title: "礼包补偿"}
	// 先查是否有可领的补偿/礼包。
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/redeem/summary", nil)
	if err != nil {
		res.Err = "查询礼包状态失败: " + err.Error()
		return res, nil
	}
	// redeem/summary 的 starter/advanced/legendary_count 是可用兑换券数量，
	// 同时也是运营补发礼包的入口计数；为 0 时无事可做。
	total := workBuddyTaskInt(data, "starter_count") +
		workBuddyTaskInt(data, "advanced_count") +
		workBuddyTaskInt(data, "legendary_count")
	if total == 0 {
		res.Err = "当前没有可领取的礼包"
		return res, nil
	}
	res.Err = "礼包需在成长中心手动兑换"
	return res, nil
}

// WorkBuddyRedeemStreak 兑换连签档位奖励。
func WorkBuddyRedeemStreak(ctx context.Context, account *Account) (*WorkBuddyClaimResult, error) {
	res := &WorkBuddyClaimResult{Code: "redeem", Title: "连签兑换"}
	st, err := workBuddyStreakOf(ctx, account)
	if err != nil {
		return nil, err
	}
	if st.RedeemableTiers == 0 {
		res.Err = "暂无可兑换档位"
		return res, nil
	}
	for _, tier := range []string{"7d", "14d", "28d"} {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		case <-time.After(600 * time.Millisecond):
		}
		if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
			"/v2/activity/growth/redeem", map[string]any{"tier": tier}); err == nil {
			res.Credit = -1
			return res, nil
		}
	}
	res.Err = "兑换失败"
	return res, nil
}
