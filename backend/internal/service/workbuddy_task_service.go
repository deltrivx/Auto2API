package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// WorkBuddy 积分任务：每日签到与成长任务。
//
// 上游接口（国内站 copilot.tencent.com，均为实测确认）：
//
//	POST /v2/billing/meter/checkin-activity-status   签到状态（必须 POST，GET 返回 404）
//	POST /v2/billing/meter/daily-checkin             执行签到
//	GET  /v2/activity/growth/tasks                   成长任务列表
//	POST /v2/activity/growth/tasks/accept            领取任务
//	POST /activity/growth/tasks/{code}/claim         领取奖励
//
// 与参考实现（AutoBuddy / magpie）的关键差异：本系统是网关，积分任务只做
// 「查询 + 按需执行」，不自动跑任务流水线——执行由操作者显式触发，避免
// 多账号并发触发上游风控。

// WorkBuddyCheckinStatus 是签到状态。
type WorkBuddyCheckinStatus struct {
	Active          bool     `json:"active"`
	TodayCheckedIn  bool     `json:"today_checked_in"`
	StreakDays      int      `json:"streak_days"`
	DailyCredit     float64  `json:"daily_credit"`
	TodayCredit     float64  `json:"today_credit"`
	CheckinDates    []string `json:"checkin_dates"`
	NextStreakDay   int      `json:"next_streak_day"`
	StreakBonusDays int      `json:"streak_bonus_days"`
	// TotalCredits 是签到活动的累计积分（上游权威口径）。
	TotalCredits float64 `json:"total_credits"`
}

// WorkBuddyCheckinResult 是执行签到后的结果。
type WorkBuddyCheckinResult struct {
	// AlreadyChecked 表示今天已经签过。
	AlreadyChecked bool    `json:"already_checked"`
	Credit         float64 `json:"credit"`
	StreakDays     int     `json:"streak_days"`
	Message        string  `json:"message,omitempty"`
}

// WorkBuddyTask 是一个成长任务。
//
// 字段全部来自上游 /v2/activity/growth/tasks：活动是动态的，任务列表、
// 奖励、图标、进度、有效期都会随运营调整变化，因此这里不做任何硬编码，
// 上游给什么就展示什么。
type WorkBuddyTask struct {
	Code         string  `json:"code"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	TaskDesc     string  `json:"task_desc"`
	Status       string  `json:"status"`
	RewardCredit float64 `json:"reward_credit"`
	RewardEnergy int     `json:"reward_energy"`
	RewardBuddy  bool    `json:"reward_buddy"`
	TaskType     string  `json:"task_type"`
	JumpURL      string  `json:"jump_url"`
	// 展示用元数据
	IconURL    string `json:"icon_url"`
	Tag        string `json:"tag"`
	BadgeName  string `json:"badge_name"`
	IsNew      bool   `json:"is_new"`
	IsPinned   bool   `json:"is_pinned"`
	Locked     bool   `json:"locked"`
	HasReward  bool   `json:"has_reward"`
	ValidStart string `json:"valid_start,omitempty"`
	ValidEnd   string `json:"valid_end,omitempty"`
	// Progress 是任务进度（当前/目标），用于进度条。
	ProgressCurrent int `json:"progress_current"`
	ProgressTarget  int `json:"progress_target"`
}

// WorkBuddyClaimResult 是领取奖励的结果。
type WorkBuddyClaimResult struct {
	Code   string  `json:"code"`
	Title  string  `json:"title"`
	Credit float64 `json:"credit"`
	Err    string  `json:"error,omitempty"`
}

// workBuddyActivityClient 调用活动相关接口。
func workBuddyActivityClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// workBuddyActivityRequest 发起一次活动接口请求并解出 data 字段。
//
// 上游统一返回 {code, msg, data}；code != 0 视为业务失败。
func workBuddyActivityRequest(ctx context.Context, account *Account, method, path string, body any) (map[string]any, error) {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	token := account.workBuddyTokenOf()
	if token.Access == "" {
		return nil, fmt.Errorf("账号缺少 access_token")
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, site.Endpoint+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", workBuddySiteDomain(site))
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", "WorkBuddy/5.9.3 workbuddy/5.9.3 CLI/2.165.0")
	req.Header.Set("Origin", site.Endpoint)
	req.Header.Set("Referer", site.Endpoint+"/profile/growth-center")

	resp, err := workBuddyActivityClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("上游返回非 JSON（HTTP %d）: %s", resp.StatusCode, truncateRunes(string(raw), 160))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("上游错误 %d: %s", env.Code, env.Msg)
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	return env.Data, nil
}

// WorkBuddyCheckinStatusOf 读取账号的签到状态。
func WorkBuddyCheckinStatusOf(ctx context.Context, account *Account) (*WorkBuddyCheckinStatus, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
		"/v2/billing/meter/checkin-activity-status", map[string]any{})
	if err != nil {
		return nil, err
	}
	st := &WorkBuddyCheckinStatus{}
	st.Active = workBuddyTaskBool(data, "active")
	st.TodayCheckedIn = workBuddyTaskBool(data, "today_checked_in") || workBuddyTaskBool(data, "todayCheckedIn")
	st.StreakDays = workBuddyTaskInt(data, "streak_days")
	st.DailyCredit = workBuddyTaskFloat(data, "daily_credit")
	st.TodayCredit = workBuddyTaskFloat(data, "today_credit")
	st.NextStreakDay = workBuddyTaskInt(data, "next_streak_day")
	st.StreakBonusDays = workBuddyTaskInt(data, "streak_bonus_days")
	st.TotalCredits = workBuddyTaskFloat(data, "total_credits")
	if dates, ok := data["checkin_dates"].([]any); ok {
		for _, d := range dates {
			if s, ok := d.(string); ok {
				st.CheckinDates = append(st.CheckinDates, s)
			}
		}
	}
	return st, nil
}

// WorkBuddyDoCheckin 执行每日签到。
//
// 已签到直接返回，不重复请求上游——重复签到会被上游判为异常。
func WorkBuddyDoCheckin(ctx context.Context, account *Account) (*WorkBuddyCheckinResult, error) {
	st, err := WorkBuddyCheckinStatusOf(ctx, account)
	if err != nil {
		return nil, err
	}
	if !st.Active {
		return &WorkBuddyCheckinResult{Message: "签到活动当前未开放"}, nil
	}
	if st.TodayCheckedIn {
		return &WorkBuddyCheckinResult{
			AlreadyChecked: true,
			Credit:         st.TodayCredit,
			StreakDays:     st.StreakDays,
			Message:        "今日已签到",
		}, nil
	}
	data, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
		"/v2/billing/meter/daily-checkin", map[string]any{})
	if err != nil {
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "checkin", Result: "failed", Detail: err.Error(),
		})
		return nil, err
	}
	res := &WorkBuddyCheckinResult{
		Credit:     workBuddyTaskFloat(data, "credit"),
		StreakDays: workBuddyTaskInt(data, "streak_days"),
		Message:    workBuddyStringField(data, "message"),
	}
	recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
		AccountID: account.ID, AccountName: account.Name,
		Kind: "checkin", TaskCode: "checkin", TaskName: "每日签到",
		Result: "success", Credit: res.Credit, StreakDays: res.StreakDays,
		Detail: res.Message,
	})
	return res, nil
}

// WorkBuddyTasksOf 读取账号的成长任务列表。
func WorkBuddyTasksOf(ctx context.Context, account *Account) ([]WorkBuddyTask, error) {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/v2/activity/growth/tasks", nil)
	if err != nil {
		return nil, err
	}
	rawTasks, _ := data["tasks"].([]any)
	out := make([]WorkBuddyTask, 0, len(rawTasks))
	for _, raw := range rawTasks {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		code := workBuddyStringField(m, "task_code")
		if code == "" {
			continue
		}
		task := WorkBuddyTask{
			Code:         code,
			Title:        workBuddyStringField(m, "title"),
			Description:  workBuddyStringField(m, "description"),
			TaskDesc:     workBuddyStringField(m, "task_desc"),
			Status:       workBuddyStringField(m, "accept_status"),
			RewardCredit: workBuddyTaskFloat(m, "reward_credit"),
			RewardEnergy: workBuddyTaskInt(m, "reward_energy"),
			RewardBuddy:  workBuddyTaskBool(m, "reward_buddy"),
			TaskType:     workBuddyStringField(m, "task_type"),
			JumpURL:      workBuddyStringField(m, "jump_url"),
			IconURL:      workBuddyStringField(m, "icon_url"),
			Tag:          workBuddyStringField(m, "tag"),
			BadgeName:    workBuddyStringField(m, "badge_name"),
			IsNew:        workBuddyTaskBool(m, "is_new"),
			IsPinned:     workBuddyTaskBool(m, "is_pinned"),
			Locked:       workBuddyTaskBool(m, "locked"),
			HasReward:    workBuddyTaskBool(m, "has_reward"),
			ValidStart:   workBuddyStringField(m, "valid_start"),
			ValidEnd:     workBuddyStringField(m, "valid_end"),
		}
		if pg, ok := m["progress"].(map[string]any); ok {
			task.ProgressCurrent = workBuddyTaskInt(pg, "current")
			task.ProgressTarget = workBuddyTaskInt(pg, "target")
		}
		out = append(out, task)
	}
	return out, nil
}

// WorkBuddyClaimTask 领取单项任务的奖励。
func WorkBuddyClaimTask(ctx context.Context, account *Account, code string) (*WorkBuddyClaimResult, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("任务编码为空")
	}
	if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
		"/activity/growth/tasks/"+code+"/claim", map[string]any{}); err != nil {
		// 失败也落台账：任务未完成时上游返回 400，这条记录正是
		// 「为什么没领到」的唯一凭据。
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "claim", TaskCode: code, Result: "failed", Detail: err.Error(),
		})
		return &WorkBuddyClaimResult{Code: code, Err: err.Error()}, nil
	}
	return &WorkBuddyClaimResult{Code: code}, nil
}

// WorkBuddyClaimAll 领取所有已完成（未领取）的任务奖励。
//
// 上游进度是异步落定的：刚满足条件的任务可能几秒后才变成 completed，
// 因此这里带一轮短轮询补领——否则这些任务要等到下次操作才会被领走。
//
// includeRisk 为 false 时跳过高风险任务（见 workBuddyTaskRisk）。
func WorkBuddyClaimAll(ctx context.Context, account *Account, includeRisk bool, wait time.Duration) ([]WorkBuddyClaimResult, error) {
	tasks, err := WorkBuddyTasksOf(ctx, account)
	if err != nil {
		return nil, err
	}

	byCode := map[string]WorkBuddyTask{}
	for _, t := range tasks {
		byCode[t.Code] = t
	}

	results := make([]WorkBuddyClaimResult, 0, len(tasks))
	pending := make([]string, 0, len(tasks))

	claimOne := func(code string) {
		task := byCode[code]
		res, err := WorkBuddyClaimTask(ctx, account, code)
		if err != nil {
			results = append(results, WorkBuddyClaimResult{Code: code, Title: task.Title, Err: err.Error()})
			return
		}
		if res.Err != "" {
			res.Title = task.Title
			results = append(results, *res)
			return
		}
		results = append(results, WorkBuddyClaimResult{
			Code:   code,
			Title:  task.Title,
			Credit: task.RewardCredit,
		})
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "claim", TaskCode: code, TaskName: task.Title,
			Result: "success", Credit: task.RewardCredit,
		})
	}

	for _, t := range tasks {
		if t.Status != "completed" {
			// accepted / in_progress：可能马上就达标，记下来轮询补领
			if t.Status == "accepted" || t.Status == "in_progress" {
				pending = append(pending, t.Code)
			}
			continue
		}
		if !includeRisk && workBuddyTaskRiskOf(t.Code, t.Title) == WorkBuddyRiskHigh {
			results = append(results, WorkBuddyClaimResult{
				Code: t.Code, Title: t.Title, Err: "高风险任务已跳过",
			})
			continue
		}
		claimOne(t.Code)
		// 上游对连续操作敏感，逐项之间留间隔。
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case <-time.After(800 * time.Millisecond):
		}
	}

	if len(pending) == 0 || wait <= 0 {
		return results, nil
	}

	// 轮询补领：每轮重新拉取状态，只领本轮新达标的。
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) && len(pending) > 0 {
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case <-time.After(2 * time.Second):
		}
		fresh, err := WorkBuddyTasksOf(ctx, account)
		if err != nil {
			continue
		}
		status := map[string]string{}
		for _, t := range fresh {
			status[t.Code] = t.Status
			byCode[t.Code] = t
		}
		remaining := pending[:0]
		for _, code := range pending {
			if status[code] != "completed" {
				remaining = append(remaining, code)
				continue
			}
			claimOne(code)
		}
		pending = remaining
	}
	return results, nil
}

// 风险等级取值。上游不提供风险分级，这里按「任务行为对账号的自然度影响」
// 推导，四档便于运营者按风险偏好选择执行范围。
const (
	WorkBuddyRiskLow      = "low"      // 低风险：站内只读或轻交互
	WorkBuddyRiskMedium   = "medium"   // 中等风险：需要多次交互或会话
	WorkBuddyRiskElevated = "elevated" // 偏高风险：跨端 / 分享 / 邀请
	WorkBuddyRiskHigh     = "high"     // 高风险：需人工环节或强社交行为
)

// workBuddyTaskRiskRules 是风险判定规则表。
//
// 关键点：规则按「行为特征」而不是「任务代码清单」组织，用前缀/关键词
// 匹配。活动任务是动态增减的，硬编码一份 code→风险 的映射表很快就会
// 与现实脱节——新任务默认会落进「常规」，而旧任务下线后规则成了死代码。
//
// 这里只保留与行为语义强相关的少量规则，其余归为常规（low）。
var workBuddyTaskRiskRules = []struct {
	// match 判断任务是否命中该档风险。
	match func(code, title string) bool
	risk  string
}{
	// 高风险：社交分享 / 邀请 / 需人工认证——这类行为最容易被判异常。
	{match: func(code, title string) bool {
		// skill_share 已实现自动完成（创建分享渠道 + 上报事件），归为常规；
		// 其余邀请/认证类仍需人工，维持高风险。
		if code == "skill_share" {
			return false
		}
		return workBuddyContainsAny(code, "share", "invite", "student_verify", "wechat_oa", "subscribe")
	}, risk: WorkBuddyRiskHigh},
	// 偏高风险：跨端调用、特定时段、专家类深度功能。
	{match: func(code, title string) bool {
		return workBuddyContainsAny(code, "desktop_chat", "night_owl", "lighthouse", "philanthropy", "black_cat", "qq")
	}, risk: WorkBuddyRiskElevated},
	// 中等风险：需要多轮交互或消耗资源的技能/模板/专家。
	{match: func(code, title string) bool {
		return workBuddyContainsAny(code, "chat_5", "chat_3", "template_5", "expert", "skill_1", "automation")
	}, risk: WorkBuddyRiskMedium},
}

// workBuddyContainsAny 报告 s 是否包含任一子串（大小写不敏感）。
func workBuddyContainsAny(s string, subs ...string) bool {
	lower := strings.ToLower(s)
	for _, sub := range subs {
		if strings.Contains(lower, strings.ToLower(sub)) {
			return true
		}
	}
	return false
}

// workBuddyTaskRiskOf 推导任务的风险等级。
//
// 需要人工的任务（如微信认证、公众号关注）一律视为高风险：它们无法由
// 程序完成，也不应该被自动调度反复触发。
func workBuddyTaskRiskOf(code, title string) string {
	if !workBuddyTaskIsAuto(code) {
		return WorkBuddyRiskHigh
	}
	for _, rule := range workBuddyTaskRiskRules {
		if rule.match(code, title) {
			return rule.risk
		}
	}
	return WorkBuddyRiskLow
}

// workBuddyTaskIsAuto 判断任务是否可以由程序尝试完成。
//
// 少数任务必须人工在微信/桌面端操作，程序无法代劳；把它们标出来，
// 前端显示「人工」标记并禁用自动执行。
func workBuddyTaskIsAuto(code string) bool {
	switch code {
	case "wb_wechat_oa_subscribe_task", "task_student_verify":
		// 这两项确实需要微信端人工操作（扫码关注、学生认证），
		// 任何 API 都无法代劳。
		// 注意 skill_share 不在此列：它可以通过创建分享渠道 + 上报事件完成。
		return false
	default:
		return true
	}
}

func workBuddyTaskBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

func workBuddyTaskInt(m map[string]any, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func workBuddyTaskFloat(m map[string]any, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	}
	return 0
}

// workBuddySiteDomain 返回站点的 X-Domain 取值。
//
// 上游按 X-Domain 校验请求来源，取值是不带协议的主机名。
func workBuddySiteDomain(site *workBuddySite) string {
	if site == nil {
		return ""
	}
	return strings.TrimPrefix(strings.TrimPrefix(site.Endpoint, "https://"), "http://")
}

// WorkBuddyTaskCatalogItem 是任务清单里的一项。
type WorkBuddyTaskCatalogItem struct {
	Code string `json:"code"`
	Name string `json:"name"`
	// Risk 取 normal / high；high 表示默认不自动执行。
	Risk string `json:"risk"`
	// Auto 表示是否可以由程序完成（false 表示需要人工环节）。
	Auto bool `json:"auto"`
	// Note 是给操作者的说明。
	Note string `json:"note,omitempty"`
}

// WorkBuddyTaskCatalogGroup 是任务清单的一个分组。
type WorkBuddyTaskCatalogGroup struct {
	Group string                     `json:"group"`
	Items []WorkBuddyTaskCatalogItem `json:"items"`
}

// WorkBuddyTaskCatalog 是完整任务清单。
//
// 清单是静态常量：上游增减任务时只改这一处，前端不再硬编码。
// 内容综合两个参考实现的实测结果（AutoBuddy 的 TASK_CATALOG 与
// magpie 的插件能力），并标注了风险等级与是否需要人工。
func WorkBuddyTaskCatalog() []WorkBuddyTaskCatalogGroup {
	return []WorkBuddyTaskCatalogGroup{
		{Group: "成长任务", Items: []WorkBuddyTaskCatalogItem{
			{Code: "first_buddy", Name: "首只 Buddy（领养引导）", Risk: "high", Auto: false,
				Note: "需先在桌面端完成一次领养，是后续多数任务的前置条件"},
			{Code: "create_canvas", Name: "创建画布", Risk: "normal", Auto: true},
			{Code: "playbook_prompt", Name: "使用剧本文案", Risk: "normal", Auto: true},
			{Code: "Library_read", Name: "体验资料库", Risk: "normal", Auto: true},
			{Code: "chat_5", Name: "和 AI 聊天 5 次", Risk: "normal", Auto: true},
			{Code: "skill_1", Name: "尝鲜热门技能", Risk: "high", Auto: true},
			{Code: "template_5", Name: "使用 5 个模板", Risk: "normal", Auto: true},
			{Code: "automation_1", Name: "设置自动化任务", Risk: "normal", Auto: true},
			{Code: "expert_5", Name: "召唤 5 次专家", Risk: "normal", Auto: true},
			{Code: "Expert_team_use_3", Name: "召唤 3 次专家团", Risk: "normal", Auto: true},
			{Code: "Expert_lighthouse", Name: "灯塔专家", Risk: "high", Auto: true},
			{Code: "Expert_Philanthropy", Name: "公益专家", Risk: "high", Auto: true},
			{Code: "Hp_Appearance", Name: "和平精英主题", Risk: "normal", Auto: true},
			{Code: "Buddy_App", Name: "发现应用", Risk: "normal", Auto: true},
			{Code: "Buddy_App_QQ", Name: "企鹅教师助手", Risk: "high", Auto: true},
			{Code: "Model_chat_GLM5.2", Name: "GLM-5.2 模型对话", Risk: "normal", Auto: true},
			{Code: "black_cat", Name: "设计/自动化/灵感", Risk: "high", Auto: true},
			{Code: "wb_wechat_oa_subscribe_task", Name: "关注官方公众号", Risk: "high", Auto: false,
				Note: "需微信扫码关注「腾讯 WorkBuddy」公众号并满 24 小时"},
			{Code: "RichMeow_Chat", Name: "桌面端对话 1 次", Risk: "normal", Auto: true},
			{Code: "skill_share", Name: "分享一个技能给好友", Risk: "high", Auto: true},
		}},
		{Group: "互动玩法", Items: []WorkBuddyTaskCatalogItem{
			{Code: "checkin", Name: "每日签到", Risk: "normal", Auto: true},
			{Code: "lottery", Name: "大转盘抽奖", Risk: "normal", Auto: true},
			{Code: "blindbox", Name: "盲盒开启", Risk: "normal", Auto: true},
			{Code: "buddy", Name: "Buddy 展示", Risk: "normal", Auto: true},
			{Code: "travel", Name: "喵喵旅行", Risk: "normal", Auto: true},
		}},
		{Group: "开学季活动", Items: []WorkBuddyTaskCatalogItem{
			{Code: "task_student_verify", Name: "微信学生认证", Risk: "high", Auto: false,
				Note: "人工环节，需在微信内完成认证"},
			{Code: "expert_use", Name: "专家使用", Risk: "normal", Auto: true},
			{Code: "share_invite", Name: "分享邀请", Risk: "high", Auto: true},
			{Code: "chat_3_times", Name: "对话 3 次", Risk: "normal", Auto: true},
			{Code: "desktop_chat_1_time", Name: "桌面端对话", Risk: "high", Auto: true},
		}},
		{Group: "夜猫子", Items: []WorkBuddyTaskCatalogItem{
			{Code: "night_owl", Name: "夜猫子任务（23:00–08:00）", Risk: "high", Auto: true,
				Note: "仅在该时段内计入进度，要求真实对话；每天 1 次 × 累计 3 天"},
		}},
	}
}

// WorkBuddyTaskCatalogByCode 返回 code → 清单项的索引。
func WorkBuddyTaskCatalogByCode() map[string]WorkBuddyTaskCatalogItem {
	out := map[string]WorkBuddyTaskCatalogItem{}
	for _, g := range WorkBuddyTaskCatalog() {
		for _, it := range g.Items {
			out[it.Code] = it
		}
	}
	return out
}

// WorkBuddyRunSingleTask 执行单个任务：先接取，再领奖。
//
// 用于操作者手动触发某项任务。上游任务大多需要客户端侧行为配合
// （点画布、用模板），这里能做的只有「接取 + 领奖」两步；已完成的
// 任务直接走领奖。
func WorkBuddyRunSingleTask(ctx context.Context, account *Account, code string) (*WorkBuddyClaimResult, error) {
	if strings.TrimSpace(code) == "" {
		return nil, fmt.Errorf("任务编码为空")
	}
	// 签到 / 抽奖 / 盲盒是独立接口，走各自的执行路径。
	if IsWorkBuddyActivityTask(code) {
		return WorkBuddyRunActivityTask(ctx, account, code)
	}
	tasks, err := WorkBuddyTasksOf(ctx, account)
	if err != nil {
		return nil, err
	}
	var target *WorkBuddyTask
	for i := range tasks {
		if tasks[i].Code == code {
			target = &tasks[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("该账号没有任务 %s", code)
	}

	// 未接取的任务先接取，否则领奖会被上游拒绝。
	if target.Status == "not_accepted" {
		_, _ = workBuddyActivityRequest(ctx, account, http.MethodPost,
			"/v2/activity/growth/tasks/accept", map[string]any{"task_codes": []string{code}})
	}

	// accepted / in_progress：任务已接取但未达标。参考实现（AutoBuddy 的
	// t_library / t_theme 等）在这里补发事件推进进度，而不是直接放弃。
	// 只有不需要真实会话的方案才尝试，避免发出上游不认的空事件。
	if target.Status == "accepted" || target.Status == "in_progress" {
		if plan, ok := WorkBuddyEventPlanOf(code); ok && !plan.NeedsRealSession {
			_ = WorkBuddyReportEvents(ctx, account, WorkBuddyBuildReportEvents(plan))
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(4 * time.Second):
			}
			// 回读状态：达标就立刻领奖。
			if tasks2, err := WorkBuddyTasksOf(ctx, account); err == nil {
				for i := range tasks2 {
					if tasks2[i].Code == code && tasks2[i].Status == "completed" {
						return WorkBuddyClaimTask(ctx, account, code)
					}
				}
			}
		}
		return &WorkBuddyClaimResult{
			Code:  code,
			Title: target.Title,
			Err:   "任务未完成：已上报行为，等待上游确认",
		}, nil
	}
	if target.Status == "claimed" {
		return &WorkBuddyClaimResult{
			Code:  code,
			Title: target.Title,
			Err:   "已领取过奖励",
		}, nil
	}

	res, err := WorkBuddyClaimTask(ctx, account, code)
	if err != nil {
		return nil, err
	}
	res.Title = target.Title
	if res.Err != "" {
		recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
			AccountID: account.ID, AccountName: account.Name,
			Kind: "task", TaskCode: code, TaskName: target.Title,
			Result: "failed", Detail: res.Err,
		})
		return res, nil
	}
	res.Credit = target.RewardCredit
	recordWorkBuddyTaskRun(ctx, &WorkBuddyTaskRun{
		AccountID: account.ID, AccountName: account.Name,
		Kind: "task", TaskCode: code, TaskName: target.Title,
		Result: "success", Credit: target.RewardCredit,
	})
	return res, nil
}

// WorkBuddyAutoTaskConfigOfAccount 读取账号的自动任务开关（对 service 外暴露）。
func WorkBuddyAutoTaskConfigOfAccount(a *Account) (autoCheckin, autoClaim, includeHighRisk bool) {
	cfg := workBuddyAutoTaskConfigOf(a)
	return cfg.AutoCheckin, cfg.AutoClaim, cfg.IncludeHighRisk
}

// SetWorkBuddyAutoTaskConfig 写入账号的自动任务开关。
//
// 开关随凭据存放，因此这里只返回需要合并进 credentials 的片段，
// 由调用方与已有凭据合并保存。
func SetWorkBuddyAutoTaskConfig(a *Account, autoCheckin, autoClaim, includeHighRisk bool) map[string]any {
	merged := map[string]any{}
	for k, v := range a.Credentials {
		merged[k] = v
	}
	merged["auto_task"] = map[string]any{
		"auto_checkin":      autoCheckin,
		"auto_claim":        autoClaim,
		"include_high_risk": includeHighRisk,
	}
	return merged
}
