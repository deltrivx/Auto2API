package service

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// WorkBuddy 积分任务看板。
//
// 页面第三部分是「任务方格」：以任务为维度展示，而不是以账号为维度。
// 任务本身来自上游 /v2/activity/growth/tasks —— 活动随时增减，因此
// 这里每次请求都实时聚合，不缓存任务清单、不硬编码任务代码。
//
// 聚合方式：逐个账号拉任务列表，按 task_code 合并，统计每项在多少账号
// 上出现、已完成多少、可领多少。账号多时并发拉取，但受 concurrency
// 限制——上游对同 IP 的并发请求敏感。

// WorkBuddyTaskBoardItem 是看板上的一个任务格。
type WorkBuddyTaskBoardItem struct {
	Code string `json:"code"`
	// Title 取任意账号返回的标题（上游对同一 code 的标题一致）。
	Title       string `json:"title"`
	Description string `json:"description"`
	TaskDesc    string `json:"task_desc"`
	IconURL     string `json:"icon_url"`
	Tag         string `json:"tag"`
	BadgeName   string `json:"badge_name"`
	JumpURL     string `json:"jump_url"`
	// Risk 是推导出的风险等级：low / medium / elevated / high。
	Risk string `json:"risk"`
	// Auto 表示能否由程序执行；false 时前端显示「人工」并禁用自动。
	Auto bool `json:"auto"`
	// Period 是任务周期：daily（每日可做）/ once（单次完成即可）。
	// 单次任务完成后进度不随天数重置，勾选框也应置灰。
	Period string `json:"period"`
	// Reward 是单项奖励积分（同一任务在不同账号上一致）。
	Reward float64 `json:"reward"`
	// 统计
	AccountTotal   int `json:"account_total"`
	AccountDone    int `json:"account_done"`
	AccountClaimed int `json:"account_claimed"`
	// AccountClaimable 是「已完成但未领奖」的账号数——直接可执行的那部分。
	AccountClaimable int `json:"account_claimable"`
	// ValidEnd 非空表示任务有截止时间。
	ValidEnd string `json:"valid_end,omitempty"`
	// Status 是聚合状态：none / partial / done。
	Status string `json:"status"`
}

// WorkBuddyTaskBoard 是看板数据。
type WorkBuddyTaskBoard struct {
	Items []WorkBuddyTaskBoardItem `json:"items"`
	// AccountCount 参与聚合的账号数。
	AccountCount int `json:"account_count"`
	// RefreshedAt 是本次聚合时间。
	RefreshedAt time.Time `json:"refreshed_at"`
	// Errors 记录拉取失败的账号，便于运营者判断数据是否完整。
	Errors []string `json:"errors,omitempty"`
}

// workBuddyTaskBoardConcurrency 是并发拉取任务列表的上限。
//
// 账号多时这个值直接决定首屏时间：22 个账号、每账号约 7 次请求，
// 4 路并发需要 40 轮；提到 12 路只需 13 轮，且仍在实测稳定的范围内。
const workBuddyTaskBoardConcurrency = 12

// WorkBuddyTaskBoardOf 聚合所有账号的任务，生成看板数据。
// WorkBuddyTaskBoardOf 返回看板数据。
//
// 缓存优先：命中直接返回（Redis 共享，多实例一致）；未命中时抢一把分布式锁，
// 抢到的实例同步聚合后写回，其余实例返回上一次结果而不是同时打上游。
func WorkBuddyTaskBoardOf(ctx context.Context, accounts []*Account) *WorkBuddyTaskBoard {
	cache := WorkBuddyBoardCacheOf()

	if cached := cache.Get(ctx); cached != nil {
		return cached
	}

	// 未命中：只有抢到锁的实例负责重建。
	if cache != nil && !cache.TryLock(ctx) {
		// 别的实例正在重建，返回空看板让前端稍后轮询。
		return &WorkBuddyTaskBoard{Items: []WorkBuddyTaskBoardItem{}, RefreshedAt: time.Now()}
	}
	defer func() {
		if cache != nil {
			cache.Unlock(ctx)
		}
	}()

	fresh := workBuddyBuildTaskBoard(ctx, accounts)
	cache.Set(ctx, fresh)
	return fresh
}

// InvalidateWorkBuddyTaskBoardCache 让缓存立即失效（执行任务后调用）。
func InvalidateWorkBuddyTaskBoardCache() {
	if cache := WorkBuddyBoardCacheOf(); cache != nil {
		cache.Invalidate(context.Background())
	}
}

// workBuddyBuildTaskBoard 真正执行聚合。
func workBuddyBuildTaskBoard(ctx context.Context, accounts []*Account) *WorkBuddyTaskBoard {
	board := &WorkBuddyTaskBoard{RefreshedAt: time.Now()}
	if len(accounts) == 0 {
		board.Items = []WorkBuddyTaskBoardItem{}
		return board
	}

	type result struct {
		tasks []WorkBuddyTask
		err   error
	}
	results := make([]result, len(accounts))

	sem := make(chan struct{}, workBuddyTaskBoardConcurrency)
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Add(1)
		go func(i int, acc *Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tasks, err := WorkBuddyTasksOf(ctx, acc)
			if err == nil {
				// 签到 / 抽奖 / 盲盒是独立接口，不在成长任务列表里，
				// 必须单独拉取后合并——否则看板上永远看不到「每日签到」。
				tasks = append(tasks, WorkBuddyActivityTasksOf(ctx, acc)...)
			}
			results[i] = result{tasks: tasks, err: err}
		}(i, acc)
	}
	wg.Wait()

	// 按 task_code 聚合
	byCode := map[string]*WorkBuddyTaskBoardItem{}
	// normalizedIndex 把归一化代码映射到实际使用的代码，用于跨接口去重。
	normalizedIndex := map[string]string{}
	order := []string{}
	var errs []string

	for i, res := range results {
		if res.err != nil {
			errs = append(errs, accounts[i].Name+": "+res.err.Error())
			continue
		}
		board.AccountCount++
		for _, t := range res.tasks {
			item, ok := byCode[t.Code]
			if !ok {
				// 不同代码但同一目标（徽章 vs 成长任务）：合并到已有条目，
				// 不新建卡片，避免出现两张「领取一只 Buddy」。
				if existCode, exist := normalizedIndex[byCodeNormalized(t.Code)]; exist {
					item = byCode[existCode]
					byCode[t.Code] = item
					ok = true
				}
			}
			if !ok {
				item = &WorkBuddyTaskBoardItem{
					Code:        t.Code,
					Title:       t.Title,
					Description: t.Description,
					TaskDesc:    t.TaskDesc,
					IconURL:     t.IconURL,
					Tag:         t.Tag,
					BadgeName:   t.BadgeName,
					JumpURL:     t.JumpURL,
					Reward:      t.RewardCredit,
					Risk:        workBuddyTaskRiskOf(t.Code, t.Title),
					Auto:        workBuddyTaskIsAuto(t.Code),
					Period:      workBuddyTaskPeriod(t.Code),
					ValidEnd:    t.ValidEnd,
				}
				byCode[t.Code] = item
				normalizedIndex[byCodeNormalized(t.Code)] = t.Code
				order = append(order, t.Code)
			}
			item.AccountTotal++
			switch t.Status {
			case "completed":
				item.AccountDone++
				// 只有真的有奖励积分才算「可领」：奖励为 0 的任务领了也不到账，
				// 计入可领会让右下角的数字虚高。
				if t.RewardCredit > 0 {
					item.AccountClaimable++
				}
			case "claimed":
				item.AccountDone++
				item.AccountClaimed++
			}
		}
	}

	items := make([]WorkBuddyTaskBoardItem, 0, len(order))
	for _, code := range order {
		it := byCode[code]
		switch {
		case it.AccountDone == 0:
			it.Status = "none"
		case it.AccountDone >= it.AccountTotal:
			it.Status = "done"
		default:
			it.Status = "partial"
		}
		items = append(items, *it)
	}

	// 排序：可领的排前面，然后按奖励从高到低，最后按标题稳定排序。
	sort.SliceStable(items, func(i, j int) bool {
		if (items[i].AccountClaimable > 0) != (items[j].AccountClaimable > 0) {
			return items[i].AccountClaimable > 0
		}
		if items[i].Reward != items[j].Reward {
			return items[i].Reward > items[j].Reward
		}
		return items[i].Title < items[j].Title
	})

	board.Items = items
	board.Errors = errs
	return board
}

// WorkBuddyTaskAccountResult 是某个账号在某个任务上的执行结果。
type WorkBuddyTaskAccountResult struct {
	AccountID   int64  `json:"account_id"`
	AccountName string `json:"account_name"`
	Platform    string `json:"platform"`
	// Status 是上游 accept_status：claimed / completed / accepted / in_progress / not_accepted
	Status string `json:"status"`
	// Result 是执行结果：success / failed / skipped / not_run
	Result string  `json:"result"`
	Credit float64 `json:"credit"`
	Detail string  `json:"detail"`
	// Claimable 表示这个账号上该任务已完成、可直接领奖。
	Claimable bool `json:"claimable"`
}

// WorkBuddyTaskDetail 是任务详情弹窗的数据。
type WorkBuddyTaskDetail struct {
	Code        string `json:"code"`
	Title       string `json:"title"`
	Description string `json:"description"`
	IconURL     string `json:"icon_url"`
	Risk        string `json:"risk"`
	Auto        bool   `json:"auto"`
	// Period 是周期类型：once（单次）/ daily（每日）。
	Period   string                       `json:"period"`
	Reward   float64                      `json:"reward"`
	ValidEnd string                       `json:"valid_end,omitempty"`
	Accounts []WorkBuddyTaskAccountResult `json:"accounts"`
}

// WorkBuddyTaskDetailOf 返回某任务在各账号上的状态。
func WorkBuddyTaskDetailOf(ctx context.Context, accounts []*Account, code string) *WorkBuddyTaskDetail {
	detail := &WorkBuddyTaskDetail{Code: code, Accounts: make([]WorkBuddyTaskAccountResult, 0, len(accounts))}

	type result struct {
		tasks []WorkBuddyTask
		err   error
	}
	results := make([]result, len(accounts))
	sem := make(chan struct{}, workBuddyTaskBoardConcurrency)
	var wg sync.WaitGroup
	for i, acc := range accounts {
		wg.Add(1)
		go func(i int, acc *Account) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tasks, err := WorkBuddyTasksOf(ctx, acc)
			if err == nil {
				tasks = append(tasks, WorkBuddyActivityTasksOf(ctx, acc)...)
			}
			results[i] = result{tasks: tasks, err: err}
		}(i, acc)
	}
	wg.Wait()

	for i, res := range results {
		acc := accounts[i]
		row := WorkBuddyTaskAccountResult{
			AccountID:   acc.ID,
			AccountName: acc.Name,
			Platform:    acc.Platform,
			Result:      "not_run",
		}
		if res.err != nil {
			row.Result = "failed"
			row.Detail = res.err.Error()
			detail.Accounts = append(detail.Accounts, row)
			continue
		}
		found := false
		for _, t := range res.tasks {
			if t.Code != code {
				continue
			}
			found = true
			row.Status = t.Status
			row.Credit = t.RewardCredit
			row.Claimable = t.Status == "completed"
			if detail.Title == "" {
				detail.Title = t.Title
				detail.Description = t.TaskDesc
				if detail.Description == "" {
					detail.Description = t.Description
				}
				detail.IconURL = t.IconURL
				detail.Reward = t.RewardCredit
				detail.ValidEnd = t.ValidEnd
				detail.Risk = workBuddyTaskRiskOf(t.Code, t.Title)
				detail.Auto = workBuddyTaskIsAuto(t.Code)
				detail.Period = workBuddyTaskPeriod(t.Code)
			}
			break
		}
		if !found {
			row.Detail = "该账号没有此项任务"
		}
		detail.Accounts = append(detail.Accounts, row)
	}
	return detail
}

// WorkBuddyTaskRunSummary 是一次批量执行的结果汇总。
type WorkBuddyTaskRunSummary struct {
	Code    string                       `json:"code"`
	Total   int                          `json:"total"`
	Success int                          `json:"success"`
	Failed  int                          `json:"failed"`
	Credit  float64                      `json:"credit"`
	Items   []WorkBuddyTaskAccountResult `json:"items"`
}

// WorkBuddyRunTaskOnAccounts 在多个账号上执行同一个任务。
//
// 串行执行并留间隔：上游对同 IP 的并发请求敏感，尤其这类非模型接口。
func WorkBuddyRunTaskOnAccounts(ctx context.Context, accounts []*Account, code string, auto bool) *WorkBuddyTaskRunSummary {
	out := &WorkBuddyTaskRunSummary{Code: code, Items: make([]WorkBuddyTaskAccountResult, 0, len(accounts))}
	for i, acc := range accounts {
		if ctx.Err() != nil {
			break
		}
		row := WorkBuddyTaskAccountResult{AccountID: acc.ID, AccountName: acc.Name, Platform: acc.Platform}
		if !workBuddyTaskIsAuto(code) {
			row.Result = "skipped"
			row.Detail = "该任务需要人工完成"
			out.Items = append(out.Items, row)
			continue
		}
		res, err := WorkBuddyRunSingleTask(ctx, acc, code)
		switch {
		case err != nil:
			row.Result = "failed"
			row.Detail = err.Error()
			out.Failed++
		case res != nil && res.Err != "":
			row.Result = "failed"
			row.Detail = res.Err
			out.Failed++
		default:
			row.Result = "success"
			if res != nil {
				row.Credit = res.Credit
				out.Credit += res.Credit
			}
			out.Success++
		}
		out.Items = append(out.Items, row)
		out.Total++
		if i < len(accounts)-1 {
			select {
			case <-ctx.Done():
			case <-time.After(1500 * time.Millisecond):
			}
		}
	}
	_ = auto
	return out
}

// byCodeNormalized 归一化任务代码，用于跨接口去重。
//
// 徽章接口的代码形如 badge_first_buddy，成长任务是 first_buddy；
// 去掉 badge_ 前缀与常见的下划线、大小写差异后比较即可识别同一目标。
func byCodeNormalized(code string) string {
	c := strings.ToLower(strings.TrimPrefix(code, "badge_"))
	c = strings.ReplaceAll(c, "_", "")
	c = strings.ReplaceAll(c, "-", "")
	return c
}
