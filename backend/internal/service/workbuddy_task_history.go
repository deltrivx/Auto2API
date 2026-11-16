package service

import (
	"context"
	"sync"
	"time"
)

// WorkBuddy 积分任务的执行台账。
//
// 每次签到 / 领奖 / 单任务执行都落一行，供两处使用：
//   - 执行历史：最近 N 次操作的结果
//   - 任务台账：每个任务最近一次结果与累计成功次数
//
// service 层只定义接口，具体存储在 repository（workbuddy_task_runs 表）。

// WorkBuddyTaskRun 是一条执行记录。
type WorkBuddyTaskRun struct {
	ID          int64     `json:"id"`
	AccountID   int64     `json:"account_id"`
	AccountName string    `json:"account_name"`
	Kind        string    `json:"kind"`
	TaskCode    string    `json:"task_code"`
	TaskName    string    `json:"task_name"`
	Result      string    `json:"result"`
	Credit      float64   `json:"credit"`
	StreakDays  int       `json:"streak_days"`
	Detail      string    `json:"detail"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// WorkBuddyTaskSummaryItem 是单个任务的聚合视图。
type WorkBuddyTaskSummaryItem struct {
	TaskCode   string    `json:"task_code"`
	TaskName   string    `json:"task_name"`
	Total      int       `json:"total"`
	Success    int       `json:"success"`
	LastResult string    `json:"last_result"`
	LastAt     time.Time `json:"last_at"`
	LastDetail string    `json:"last_detail"`
}

// WorkBuddyCreditSummary 是积分聚合。
type WorkBuddyCreditSummary struct {
	TodayCheckinCredit float64 `json:"today_checkin_credit"`
	TodayTaskCredit    float64 `json:"today_task_credit"`
	TodayTotalCredit   float64 `json:"today_total_credit"`
	TotalCredit        float64 `json:"total_credit"`
	// BaselineCredit 是上游「已领奖」任务的历史基线，不计入今日。
	BaselineCredit float64 `json:"baseline_credit"`
	// ClaimedTaskCount 是已领奖的任务条数，用于展示基线来源。
	ClaimedTaskCount int `json:"claimed_task_count"`
}

// WorkBuddyTaskRunStore 是执行台账的存储能力。
type WorkBuddyTaskRunStore interface {
	InsertRun(ctx context.Context, run *WorkBuddyTaskRun) error
	ListRuns(ctx context.Context, limit int) ([]WorkBuddyTaskRun, error)
	SummarizeTasks(ctx context.Context) ([]WorkBuddyTaskSummaryItem, error)
	SummarizeCredits(ctx context.Context, day time.Time) (*WorkBuddyCreditSummary, error)
}

var workBuddyTaskRunStore WorkBuddyTaskRunStore

// SetWorkBuddyTaskRunStore 注入执行台账存储。
func SetWorkBuddyTaskRunStore(store WorkBuddyTaskRunStore) {
	workBuddyTaskRunStore = store
}

// recordWorkBuddyTaskRun 写入一条执行记录（尽力而为，失败不影响主流程）。
func recordWorkBuddyTaskRun(ctx context.Context, run *WorkBuddyTaskRun) {
	if workBuddyTaskRunStore == nil || run == nil {
		return
	}
	if run.OccurredAt.IsZero() {
		run.OccurredAt = time.Now()
	}
	_ = workBuddyTaskRunStore.InsertRun(ctx, run)
}

// WorkBuddyTaskRuns 返回最近的执行记录。
func WorkBuddyTaskRuns(ctx context.Context, limit int) ([]WorkBuddyTaskRun, error) {
	if workBuddyTaskRunStore == nil {
		return []WorkBuddyTaskRun{}, nil
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return workBuddyTaskRunStore.ListRuns(ctx, limit)
}

// WorkBuddyTaskSummary 返回按任务聚合的台账。
func WorkBuddyTaskSummary(ctx context.Context) ([]WorkBuddyTaskSummaryItem, error) {
	if workBuddyTaskRunStore == nil {
		return []WorkBuddyTaskSummaryItem{}, nil
	}
	return workBuddyTaskRunStore.SummarizeTasks(ctx)
}

// WorkBuddyCreditSummaryOf 返回今日与累计的积分聚合。
//
// 台账只记录「本系统执行过」的操作，首次上线时是空的；因此累计值额外
// 叠加一次「上游已领奖任务」的基线，避免面板一开始全是 0 而无从判断。
func WorkBuddyCreditSummaryOf(ctx context.Context) (*WorkBuddyCreditSummary, error) {
	if workBuddyTaskRunStore == nil {
		return &WorkBuddyCreditSummary{}, nil
	}
	return workBuddyTaskRunStore.SummarizeCredits(ctx, time.Now())
}

// WorkBuddyEarnedBaseline 汇总上游「已领奖」任务带来的积分。
//
// 这是历史基线：这些积分在系统接入之前就已经到账，台账里不会出现。
// 只看未领取或已领取状态，不发起任何写操作。
func WorkBuddyEarnedBaseline(ctx context.Context, accounts []*Account) (taskCredit float64, claimedCount int) {
	type result struct {
		tasks []WorkBuddyTask
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
			if err != nil {
				return
			}
			results[i] = result{tasks: tasks}
		}(i, acc)
	}
	wg.Wait()

	for _, res := range results {
		for _, t := range res.tasks {
			if t.Status == "claimed" {
				taskCredit += t.RewardCredit
				claimedCount++
			}
		}
	}
	return taskCredit, claimedCount
}

// WorkBuddyTaskTrendPoint 是趋势图上的一天。
type WorkBuddyTaskTrendPoint struct {
	Date          string  `json:"date"`
	CheckinCredit float64 `json:"checkin_credit"`
	TaskCredit    float64 `json:"task_credit"`
	TotalCredit   float64 `json:"total_credit"`
}

// WorkBuddyTaskCreditTrend 返回近 days 天每天的积分获得（签到 + 任务）。
//
// 数据来自执行台账：只有真正成功的操作才计入——这样才能反映「这个账号池
// 每天实际拿到了多少积分」，而不是上游账面余额的波动（余额还会被模型调用
// 消耗，混在一起看不出任务的效果）。
func WorkBuddyTaskCreditTrend(ctx context.Context, days int) ([]WorkBuddyTaskTrendPoint, error) {
	if days <= 0 || days > 90 {
		days = 7
	}
	points := make([]WorkBuddyTaskTrendPoint, 0, days)
	now := time.Now()
	byDay := map[string]WorkBuddyTaskTrendPoint{}
	if workBuddyTaskRunStore != nil {
		// 复用积分汇总：按天聚合需要新的仓储方法，这里先用最近 N 天的
		// 逐日查询拼出来（台账数据量小，逐日查询没有性能问题）。
		for i := days - 1; i >= 0; i-- {
			day := now.AddDate(0, 0, -i)
			key := day.Format("2006-01-02")
			sum, err := workBuddyTaskRunStore.SummarizeCredits(ctx, day)
			if err != nil {
				continue
			}
			byDay[key] = WorkBuddyTaskTrendPoint{
				Date:          key,
				CheckinCredit: sum.TodayCheckinCredit,
				TaskCredit:    sum.TodayTaskCredit,
				TotalCredit:   sum.TodayTotalCredit,
			}
		}
	}
	for i := days - 1; i >= 0; i-- {
		key := now.AddDate(0, 0, -i).Format("2006-01-02")
		if p, ok := byDay[key]; ok {
			points = append(points, p)
		} else {
			points = append(points, WorkBuddyTaskTrendPoint{Date: key})
		}
	}
	return points, nil
}
