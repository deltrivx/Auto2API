package service

import (
	"fmt"
	"strings"
	"time"
)

// WorkBuddy 积分体系。
//
// 移植自 AutoBuddy 的 gateway/task_credits.py 与 gateway/credit_floor.py。
//
// WorkBuddy 上游按「积分」结算，额度来自每日签到与任务领奖。本系统把积分
// 作为唯一的计费单位：用户充值得到积分，调用按积分扣减，不再使用美元。
//
// 单位约定：1 积分 = 最小计费粒度，内部以 float64 存储（与上游返回值精度
// 对齐），对外展示时保留两位小数。

// 签到积分规则。
const (
	// WorkBuddyCheckinCreditsPerDay 是每连签一天获得的积分。
	WorkBuddyCheckinCreditsPerDay = 100

	// WorkBuddyCheckinEnergyTypical 是签到附带能量的典型值。
	WorkBuddyCheckinEnergyTypical = 0
)

// 每日任务积分的实测区间，用于预估与展示。
const (
	WorkBuddyTaskDailyTypicalMin = 600
	WorkBuddyTaskDailyTypicalMax = 7100
)

// workBuddyClaimReward 是一项可领奖任务的奖励。
type workBuddyClaimReward struct {
	Credit int
	Energy int
}

// workBuddyKnownClaimRewards 是已知可精确计算的任务奖励表。
var workBuddyKnownClaimRewards = map[string]workBuddyClaimReward{
	"Expert_lighthouse": {Credit: 100, Energy: 5},
	"RichMeow_Chat":     {Credit: 100, Energy: 5},
}

// WorkBuddyCheckinCredits 按连签天数计算签到积分。
//
// 规则：每天 100 积分，连签 16 天即 1600 积分。天数异常（负数/非数值）按 0 处理。
func WorkBuddyCheckinCredits(streakDays int) int {
	if streakDays < 0 {
		return 0
	}
	return streakDays * WorkBuddyCheckinCreditsPerDay
}

// WorkBuddyKnownClaimCredits 汇总已知领奖任务的积分。
//
// codes 为空时统计全部已知任务；否则只统计命中的那些。
func WorkBuddyKnownClaimCredits(codes []string) int {
	if len(codes) == 0 {
		total := 0
		for _, r := range workBuddyKnownClaimRewards {
			total += r.Credit
		}
		return total
	}
	total := 0
	for _, c := range codes {
		if r, ok := workBuddyKnownClaimRewards[c]; ok {
			total += r.Credit
		}
	}
	return total
}

// workBuddyPendingEstimate 是未完成任务能拿到的积分预估。
type workBuddyPendingEstimate struct {
	// Exact 是可精确计算的部分（签到 + 已知任务）。
	Exact float64
	// Items 是逐项说明，便于展示给用户。
	Items []string
}

// WorkBuddyEstimatePending 估算剩余任务可获得多少积分。
//
// 只统计有确定规则的部分：签到按连签天数精确计算，已知任务按奖励表累加。
// 服务端下发的奖励（旅行礼物、连签兑换等）无法预估，不在此列。
func WorkBuddyEstimatePending(streakDays int, remainingTasks []string) workBuddyPendingEstimate {
	est := workBuddyPendingEstimate{}
	if streakDays > 0 {
		c := WorkBuddyCheckinCredits(streakDays)
		est.Exact += float64(c)
		est.Items = append(est.Items, fmt.Sprintf("签到（连签 %d 天）：+%d", streakDays, c))
	}
	for _, t := range remainingTasks {
		for code, reward := range workBuddyKnownClaimRewards {
			if code == t || strings.Contains(strings.ToLower(t), strings.ToLower(code)) {
				est.Exact += float64(reward.Credit)
				est.Items = append(est.Items, fmt.Sprintf("%s：+%d", code, reward.Credit))
				break
			}
		}
	}
	return est
}

// WorkBuddyDescribeCreditRules 把积分规则渲染成可读文本，供界面展示。
func WorkBuddyDescribeCreditRules() string {
	var b strings.Builder
	b.WriteString("任务积分规则：\n\n")
	b.WriteString("【签到】精确可算\n")
	b.WriteString(fmt.Sprintf("  · %d 积分/天 × 连签天数\n", WorkBuddyCheckinCreditsPerDay))
	b.WriteString(fmt.Sprintf("  · 例：连签 16 天 = %d 积分\n\n", WorkBuddyCheckinCredits(16)))
	b.WriteString("【领奖】已知项\n")
	for code, r := range workBuddyKnownClaimRewards {
		b.WriteString(fmt.Sprintf("  · %s：+%d 积分 +%d 能量\n", code, r.Credit, r.Energy))
	}
	b.WriteString("\n【服务端下发】不可预估，只能读实际返回值\n")
	b.WriteString("  · 旅行礼物 / 连签兑换 / 新手礼包补偿\n\n")
	b.WriteString("【历史量级】每日任务实际区间\n")
	b.WriteString(fmt.Sprintf("  · %d ~ %d 积分/天（取决于当天跑了多少任务）\n",
		WorkBuddyTaskDailyTypicalMin, WorkBuddyTaskDailyTypicalMax))
	return b.String()
}

// WorkBuddyCreditsSnapshot 是一个账号的积分快照。
//
// 对应 accounts.extra 中的 workbuddy_credits_* 字段。
type WorkBuddyCreditsSnapshot struct {
	Total       float64   `json:"total"`
	Remaining   float64   `json:"remaining"`
	DayStart    float64   `json:"day_start"`
	DayEarned   float64   `json:"day_earned"`
	DayConsumed float64   `json:"day_consumed"`
	DayDate     string    `json:"day_date"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// WorkBuddyCreditsRemainingOf 从账号 extra 读出剩余积分。
//
// 同时兼容浮点与整数两种存储形态（上游返回值精度不一）。
func WorkBuddyCreditsRemainingOf(extra map[string]any) (float64, bool) {
	if extra == nil {
		return 0, false
	}
	v, ok := extra["workbuddy_credits_remaining"]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case int:
		return float64(n), true
	}
	return 0, false
}

// WorkBuddyCreditFloorReached 判断账号积分是否已低于可用下限。
//
// 积分为 0 或负数时账号无法继续服务，调度应跳过它。
func WorkBuddyCreditFloorReached(extra map[string]any) bool {
	remaining, ok := WorkBuddyCreditsRemainingOf(extra)
	return ok && remaining <= 0
}
