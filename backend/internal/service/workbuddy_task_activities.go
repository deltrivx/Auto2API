package service

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/DeltrivX/Auto2API/internal/domain"
)

// WorkBuddy 互动模块（签到 / 抽奖 / 盲盒）作为任务并入看板。
//
// 这几个模块不走 /v2/activity/growth/tasks —— 它们是独立的接口，
// 只返回自身的「可执行次数」而不是任务定义。因此这里把它们动态转换为
// 与成长任务同构的条目，一并参与聚合、执行与统计。
//
// 全部字段实时读取；没有任何硬编码的任务清单。

// 内置模块的 task_code，前端按此识别并展示对应图标。
const (
	workBuddyActivityCheckin  = "activity_checkin"
	workBuddyActivityLottery  = "activity_lottery"
	workBuddyActivityBlindbox = "activity_blindbox"
	workBuddyActivityTravel   = "activity_travel"
	workBuddyActivityRedeem   = "activity_redeem"
	workBuddyActivityMakeup   = "activity_makeup"
	// workBuddyActivityGift 是新手礼包，走 codebuddy 域，每账号一次。
	workBuddyActivityGift = "activity_gift"
	// workBuddyActivityDailyActivity 是成长中心的每日活跃（chat_request_send）。
	workBuddyActivityDailyActivity = "activity_daily_activity"
)

// WorkBuddyActivityTasksOf 返回该账号上「互动模块」转换出的任务列表。
//
// 数据来自三个独立接口，任何一个失败都不影响其它两项：
//   - /v2/activity/growth/badges       徽章任务（含图标与可领状态）
//   - /v2/billing/meter/checkin-activity-status  签到
//   - /v2/activity/growth/lottery/summary        抽奖
//   - /v2/activity/growth/buddy/quota            盲盒
//
// 返回值与 WorkBuddyTasksOf 同构，可直接合并。
func WorkBuddyActivityTasksOf(ctx context.Context, account *Account) []WorkBuddyTask {
	out := make([]WorkBuddyTask, 0, 16)

	// 徽章任务：上游给什么就展示什么，含图标、可领状态与集合信息。
	// 徽章只作成就展示，不并入积分任务：badges 接口不返回任何奖励积分，
	// 混进来会出现一堆「0 积分」的任务格，而它们根本不产生收益。
	// 徽章数据由 workBuddyBadgeTasksOf 单独提供（当前不展示）。

	// 五个模块并发拉取：串行时每账号要等 5 个往返，账号一多首屏就慢。
	// 同一账号内部并发是安全的（不同接口、无共享状态）。
	var (
		checkinSt *WorkBuddyCheckinStatus
		checkinOK bool
		lotCh     int
		lotOK     bool
		quota     int
		quotaOK   bool
		travelSt  *workBuddyTravelState
		travelOK  bool
		streakSt  *workBuddyStreakState
		streakOK  bool
		wg        sync.WaitGroup
	)
	wg.Add(5)
	go func() {
		defer wg.Done()
		checkinSt, checkinOK = func() (*WorkBuddyCheckinStatus, bool) {
			st, err := WorkBuddyCheckinStatusOf(ctx, account)
			return st, err == nil
		}()
	}()
	go func() {
		defer wg.Done()
		lotCh, lotOK = func() (int, bool) {
			c, e, err := workBuddyLotteryChances(ctx, account)
			return c, err == nil && e
		}()
	}()
	go func() {
		defer wg.Done()
		quota, quotaOK = func() (int, bool) {
			q, e, err := workBuddyBuddyQuota(ctx, account)
			return q, err == nil && e
		}()
	}()
	go func() {
		defer wg.Done()
		travelSt, travelOK = func() (*workBuddyTravelState, bool) {
			st, err := workBuddyTravelStatusOf(ctx, account)
			return st, err == nil
		}()
	}()
	go func() {
		defer wg.Done()
		streakSt, streakOK = func() (*workBuddyStreakState, bool) {
			st, err := workBuddyStreakOf(ctx, account)
			return st, err == nil
		}()
	}()
	wg.Wait()

	// 签到：每日一次，已签则状态为 claimed。
	if st, ok := checkinSt, checkinOK; ok && st != nil && st.Active {
		status := "not_accepted"
		if st.TodayCheckedIn {
			status = "claimed"
		}
		out = append(out, WorkBuddyTask{
			Code:            workBuddyActivityCheckin,
			Title:           "每日签到",
			Description:     fmt.Sprintf("连签 %d 天，每日 %s 积分", st.StreakDays, trimFloat(st.DailyCredit)),
			TaskType:        "daily",
			Status:          status,
			RewardCredit:    st.DailyCredit,
			IconURL:         "",
			Tag:             "每日",
			ProgressCurrent: boolToInt(st.TodayCheckedIn),
			ProgressTarget:  1,
		})
	}

	// 抽奖：有次数即可抽。
	if chances, enabled := lotCh, lotOK; enabled {
		// chances=0 说明今天已经抽完，标为 claimed 让调度跳过。
		status := "not_accepted"
		if chances <= 0 {
			status = "claimed"
		}
		out = append(out, WorkBuddyTask{
			Code:        workBuddyActivityLottery,
			Title:       "大转盘抽奖",
			Description: fmt.Sprintf("当前可抽奖 %d 次，奖励随机", chances),
			TaskType:    "daily",
			Status:      status,
			// 抽奖奖励随机，上游不预告金额；用 -1 表示「变动」，
			// 前端据此显示「随机」而不是 0 积分。
			RewardCredit:    -1,
			Tag:             "每日",
			ProgressCurrent: workBuddyMaxInt(0, 1-boolToInt(chances > 0)),
			ProgressTarget:  1,
		})
	}

	// 盲盒：能量足够即可开。上游用 affordable 表示「当前能开几个」。
	if quota, enabled := quota, quotaOK; enabled && quota > 0 {
		out = append(out, WorkBuddyTask{
			Code:            workBuddyActivityBlindbox,
			Title:           "盲盒开启",
			Description:     fmt.Sprintf("当前可开启 %d 个（每次消耗 10 能量）", quota),
			TaskType:        "daily",
			Status:          "not_accepted",
			Tag:             "每日",
			ProgressCurrent: 0,
			ProgressTarget:  quota,
		})
	}

	// 喵喵旅行：Buddy 出行可带回奖励。
	//
	// 上游用 daily_limit_reached 表示「今天已经去过」，这是正常状态而不是
	// 失败——之前只看 state 会把这种情况误判成可执行，执行时才被上游拒绝。
	if st, ok := travelSt, travelOK; ok && st != nil {
		status := "not_accepted"
		desc := "派遣 Buddy 外出旅行可带回奖励"
		switch {
		case st.DailyLimitReached:
			status = "claimed"
			desc = "今日已出行（上游每日限一次）"
		case st.Ready:
			status = "completed"
			desc = "旅行结束，可领取奖励"
		case st.Traveling:
			status = "in_progress"
			desc = fmt.Sprintf("Buddy 正在「%s」旅行中", st.LocationName)
		}
		out = append(out, WorkBuddyTask{
			Code:         workBuddyActivityTravel,
			Title:        "喵喵旅行",
			Description:  desc,
			TaskType:     "daily",
			Status:       status,
			Tag:          "每日",
			RewardCredit: -1, // 奖励随机
		})
	}

	// 连签兑换 / 补签 / 礼包：有资源才展示，状态如实反映上游。
	if st, ok := streakSt, streakOK; ok && st != nil {
		if st.RedeemableTiers > 0 {
			out = append(out, WorkBuddyTask{
				Code:         workBuddyActivityRedeem,
				Title:        "连签兑换",
				Description:  fmt.Sprintf("有 %d 个连签档位可兑换（连签 %d 天）", st.RedeemableTiers, st.Days),
				TaskType:     "daily",
				Status:       "completed",
				Tag:          "每日",
				RewardCredit: -1,
			})
		}
		// 补签：有断签且补签卡充足时才出现。
		if len(st.MakeupDates) > 0 && st.MakeupCards > 0 {
			out = append(out, WorkBuddyTask{
				Code:         workBuddyActivityMakeup,
				Title:        "补签",
				Description:  fmt.Sprintf("%d 个断签，剩余补签卡 %d 张", len(st.MakeupDates), st.MakeupCards),
				TaskType:     "once",
				Status:       "completed",
				Tag:          "活动",
				RewardCredit: -1,
			})
		}
	}

	// 每日活跃：点亮成长中心连登，每天一条，幂等。
	if account != nil && account.Platform == domain.PlatformWorkBuddyCN {
		out = append(out, WorkBuddyTask{
			Code:         workBuddyActivityDailyActivity,
			Title:        "成长中心活跃",
			Description:  "上报一次对话事件，点亮当日活跃",
			TaskType:     "daily",
			Status:       "not_accepted",
			Tag:          "每日",
			RewardCredit: 0,
		})
	}

	// 新手礼包：仅国内版有该端点，一次性奖励，已领取时上游会明确拒绝。
	if account != nil && account.Platform == domain.PlatformWorkBuddyCN {
		out = append(out, WorkBuddyTask{
			Code:         workBuddyActivityGift,
			Title:        "新手礼包",
			Description:  "一次性礼包，首次领取后不再出现",
			TaskType:     "once",
			Status:       "not_accepted",
			Tag:          "活动",
			RewardCredit: -1,
		})
	}

	return out
}

// workBuddyTravelState 是旅行状态的完整读数。
type workBuddyTravelState struct {
	State string
	// Traveling 表示 Buddy 正在路上。
	Traveling bool
	// Ready 表示已到达、可领取奖励。
	Ready bool
	// DailyLimitReached 表示今日已出行（上游每日限一次）。
	DailyLimitReached bool
	LocationName      string
	RewardCredit      float64
	DurationHours     int
}

// workBuddyTravelStatusOf 读取旅行状态。
func workBuddyTravelStatusOf(ctx context.Context, account *Account) (*workBuddyTravelState, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/buddy/travel/status", nil)
	if err != nil {
		return nil, err
	}
	st := &workBuddyTravelState{
		State:             workBuddyStringField(data, "state"),
		DailyLimitReached: workBuddyTaskBool(data, "daily_limit_reached"),
		RewardCredit:      workBuddyTaskFloat(data, "reward_credit"),
		DurationHours:     workBuddyTaskInt(data, "duration_hours"),
	}
	st.Traveling = st.State == "traveling" || st.State == "departed"
	st.Ready = st.State == "finished" || st.State == "arrived"
	if loc, ok := data["location"].(map[string]any); ok {
		st.LocationName = workBuddyStringField(loc, "name")
	}
	return st, nil
}

// workBuddyRedeemClaimable 统计连签兑换中可领取的档位数。
func workBuddyRedeemClaimable(ctx context.Context, account *Account) (int, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/redeem/summary", nil)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, key := range []string{"starter_count", "advanced_count", "legendary_count"} {
		n += workBuddyTaskInt(data, key)
	}
	return n, nil
}

// workBuddyLotteryChances 读取抽奖次数。
func workBuddyLotteryChances(ctx context.Context, account *Account) (int, bool, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/lottery/summary", nil)
	if err != nil {
		return 0, false, err
	}
	enabled := true
	if m, ok := data["module"].(map[string]any); ok {
		enabled = workBuddyTaskBool(m, "enabled")
	}
	return workBuddyTaskInt(data, "chances"), enabled, nil
}

// workBuddyBuddyQuota 读取盲盒开启配额。
func workBuddyBuddyQuota(ctx context.Context, account *Account) (int, bool, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/buddy/quota", nil)
	if err != nil {
		return 0, false, err
	}
	quota := workBuddyTaskInt(data, "quota")
	if quota == 0 {
		quota = workBuddyTaskInt(data, "chances")
	}
	if quota == 0 {
		quota = workBuddyTaskInt(data, "remaining")
	}
	return quota, true, nil
}

// WorkBuddyRunActivityTask 执行互动模块任务。
func WorkBuddyRunActivityTask(ctx context.Context, account *Account, code string) (*WorkBuddyClaimResult, error) {
	switch code {
	case workBuddyActivityDailyActivity:
		if err := workBuddyReportDailyActivity(ctx, account); err != nil {
			recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
				AccountID: account.ID, AccountName: account.Name,
				Kind: "daily_activity", TaskCode: code, TaskName: "成长中心活跃",
				Result: "failed", Detail: err.Error(),
			})
			return &WorkBuddyClaimResult{Code: code, Title: "成长中心活跃", Err: err.Error()}, nil
		}
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "daily_activity", TaskCode: code, TaskName: "成长中心活跃",
			Result: "success",
		})
		return &WorkBuddyClaimResult{Code: code, Title: "成长中心活跃"}, nil
	case workBuddyActivityGift:
		credit, err := workBuddyClaimGift(ctx, account)
		if err != nil {
			recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
				AccountID: account.ID, AccountName: account.Name,
				Kind: "gift", TaskCode: code, TaskName: "新手礼包",
				Result: "failed", Detail: err.Error(),
			})
			return &WorkBuddyClaimResult{Code: code, Title: "新手礼包", Err: err.Error()}, nil
		}
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "gift", TaskCode: code, TaskName: "新手礼包",
			Result: "success", Credit: credit,
		})
		return &WorkBuddyClaimResult{Code: code, Title: "新手礼包", Credit: credit}, nil
	case workBuddyActivityCheckin:
		res, err := WorkBuddyDoCheckin(ctx, account)
		if err != nil {
			return nil, err
		}
		out := &WorkBuddyClaimResult{Code: code, Title: "每日签到", Credit: res.Credit}
		if res.AlreadyChecked {
			out.Err = "今日已签到"
		}
		return out, nil
	case workBuddyActivityLottery:
		// 抽奖按剩余次数抽完：上游 chances 可能是 1、3 甚至更多，
		// 只抽一次会留下次数，看起来就是「部分完成」。
		chances, _, err := workBuddyLotteryChances(ctx, account)
		if err != nil {
			return &WorkBuddyClaimResult{Code: code, Title: "大转盘抽奖", Err: err.Error()}, nil
		}
		if chances <= 0 {
			return &WorkBuddyClaimResult{Code: code, Title: "大转盘抽奖", Err: "今日已抽完"}, nil
		}
		// 单次上限：避免异常大的 chances 打出过多请求。
		if chances > 10 {
			chances = 10
		}
		var total float64
		var won int
		for i := 0; i < chances; i++ {
			// 抽奖必须带 client_token（幂等键），缺失会被上游判为 invalid request。
			data, derr := workBuddyActivityRequest(ctx, account, http.MethodPost,
				"/v2/activity/growth/lottery/draw", map[string]any{
					"client_token": "draw-" + workBuddyUUIDv5(workBuddyFingerprintNamespace,
						fmt.Sprintf("draw:%d:%d:%d", account.ID, time.Now().UnixNano(), i)),
				})
			if derr != nil {
				if won > 0 {
					break
				}
				return &WorkBuddyClaimResult{Code: code, Title: "大转盘抽奖", Err: derr.Error()}, nil
			}
			credit := workBuddyTaskFloat(data, "credit")
			if credit == 0 {
				credit = workBuddyTaskFloat(data, "prize_credit")
			}
			total += credit
			won++
			recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
				AccountID: account.ID, AccountName: account.Name,
				Kind: "task", TaskCode: code, TaskName: "大转盘抽奖",
				Result: "success", Credit: credit,
			})
			if i < chances-1 {
				select {
				case <-ctx.Done():
					return &WorkBuddyClaimResult{Code: code, Title: "大转盘抽奖", Credit: total}, nil
				case <-time.After(2 * time.Second):
				}
			}
		}
		return &WorkBuddyClaimResult{Code: code, Title: "大转盘抽奖", Credit: total}, nil
	case workBuddyActivityBlindbox:
		_, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
			"/v2/activity/growth/buddy/open", map[string]any{"count": 1})
		if err != nil {
			return &WorkBuddyClaimResult{Code: code, Title: "盲盒开启", Err: err.Error()}, nil
		}
		return &WorkBuddyClaimResult{Code: code, Title: "盲盒开启"}, nil
	case workBuddyActivityTravel:
		// 旅行：未出行则出发，已完成则领奖。
		st, _ := workBuddyTravelStatusOf(ctx, account)
		var traveling, ready, limitReached bool
		if st != nil {
			traveling, ready, limitReached = st.Traveling, st.Ready, st.DailyLimitReached
		}
		switch {
		case limitReached && !ready:
			return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行", Err: "今日已出行（上游每日限一次）"}, nil
		case ready:
			if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
				"/v2/activity/growth/buddy/travel/claim", map[string]any{}); err != nil {
				return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行", Err: err.Error()}, nil
			}
			return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行"}, nil
		case traveling:
			return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行", Err: "Buddy 正在旅行中"}, nil
		default:
			if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
				"/v2/activity/growth/buddy/travel/depart", map[string]any{"location_id": 1}); err != nil {
				return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行", Err: err.Error()}, nil
			}
			return &WorkBuddyClaimResult{Code: code, Title: "喵喵旅行", Err: "已派出 Buddy 旅行"}, nil
		}
	case workBuddyActivityMakeup:
		return WorkBuddyDoMakeup(ctx, account, 1)
	case workBuddyActivityRedeem:
		// 连签兑换：按档位逐个兑换。
		n, _ := workBuddyRedeemClaimable(ctx, account)
		if n == 0 {
			return &WorkBuddyClaimResult{Code: code, Title: "连签兑换", Err: "暂无可兑换档位"}, nil
		}
		for _, tier := range []string{"7d", "14d", "28d"} {
			if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
				"/v2/activity/growth/redeem", map[string]any{"tier": tier}); err == nil {
				return &WorkBuddyClaimResult{Code: code, Title: "连签兑换"}, nil
			}
		}
		return &WorkBuddyClaimResult{Code: code, Title: "连签兑换", Err: "兑换失败"}, nil
	default:
		return nil, fmt.Errorf("未知的互动任务: %s", code)
	}
}

// IsWorkBuddyActivityTask 判断是否为互动模块任务。
func IsWorkBuddyActivityTask(code string) bool {
	switch code {
	case workBuddyActivityCheckin, workBuddyActivityLottery, workBuddyActivityBlindbox,
		workBuddyActivityTravel, workBuddyActivityRedeem, workBuddyActivityMakeup,
		workBuddyActivityGift, workBuddyActivityDailyActivity:
		return true
	}
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func workBuddyMaxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// trimFloat 去掉浮点数尾部多余的 0。
func trimFloat(v float64) string {
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%.2f", v)
}

// workBuddyTaskPeriod 判断任务的周期类型。
//
// daily   每日可重复（签到、抽奖、盲盒、聊天类）
// once    单次完成即可（关注公众号、学生认证、领养 Buddy 等）
//
// 单次任务一旦完成就不再需要执行，进度也不应随天数重置。
func workBuddyTaskPeriod(code string) string {
	switch code {
	case workBuddyActivityMakeup:
		return "once"
	case workBuddyActivityCheckin, workBuddyActivityLottery, workBuddyActivityBlindbox,
		workBuddyActivityTravel, workBuddyActivityRedeem:
		return "daily"
	}
	// 上游 task_type 为 once/limit 一类的按单次处理；其余按命名推断。
	if workBuddyContainsAny(code, "first_buddy", "subscribe", "student_verify", "invite", "share") {
		return "once"
	}
	return "daily"
}

var _ = time.Now

// workBuddyBadgeTasksOf 读取徽章任务。
//
// 这是上游真正的「任务清单」：每个徽章对应一项成长目标，带 url 图标、
// claimable 状态与 task_type。数量随活动变化，因此必须实时拉取。
func workBuddyBadgeTasksOf(ctx context.Context, account *Account) []WorkBuddyTask {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/badges", nil)
	if err != nil {
		return nil
	}
	rawBadges, _ := data["badges"].([]any)
	out := make([]WorkBuddyTask, 0, len(rawBadges))
	for _, raw := range rawBadges {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		code := workBuddyStringField(m, "code")
		if code == "" {
			continue
		}
		// 徽章接口的 claimed / claimable 实测恒为 false（即使 earned 已是 true），
		// 不能用来判定能否领奖——按它取会把一堆已领过的徽章显示成「可领」。
		// 这里只认 earned：已达成即视为已完成，且因为徽章不带 reward_credit，
		// 前端不会把它算进可领积分。
		status := "not_accepted"
		if workBuddyTaskBool(m, "earned") {
			status = "claimed"
		}
		out = append(out, WorkBuddyTask{
			Code:         "badge_" + code,
			Title:        workBuddyStringField(m, "title"),
			Description:  workBuddyStringField(m, "badge_name"),
			IconURL:      workBuddyStringField(m, "url"),
			Tag:          "徽章",
			TaskType:     workBuddyStringField(m, "task_type"),
			Status:       status,
			RewardCredit: 0,
		})
	}
	return out
}
