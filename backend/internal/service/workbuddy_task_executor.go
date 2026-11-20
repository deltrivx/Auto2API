package service

import (
	"strings"
	"context"
	"fmt"
	"time"
)

// WorkBuddy 任务执行器：把「上报事件 → 确认状态 → 领奖」串成一条链路。
//
// 这是让几十个账号无需登录客户端即可完成任务的核心。执行分三步：
//  1. 检查任务当前状态，已完成直接领奖；
//  2. 未完成则按事件方案上报行为事件（可选，受开关控制）；
//  3. 短轮询确认上游是否把任务推进为 completed，是则领奖。
//
// 任何一步失败都不阻断其它任务，最终逐项汇报结果。

// WorkBuddyTaskExecResult 是单个任务的执行结果。
type WorkBuddyTaskExecResult struct {
	TaskCode string `json:"task_code"`
	TaskName string `json:"task_name"`
	// Outcome 取值：claimed（领奖成功）/ reported（已上报待达标）/
	// skipped（无需执行）/ unsupported（无上报方案）/ failed
	Outcome string  `json:"outcome"`
	Credit  float64 `json:"credit"`
	Message string  `json:"message,omitempty"`
}

// workBuddyTaskExecOptions 控制执行策略。
type workBuddyTaskExecOptions struct {
	// AllowReport 为 false 时只领奖，不上报事件（默认保守模式）。
	AllowReport bool
	// AllowHighRisk 为 false 时跳过高风险任务。
	AllowHighRisk bool
	// ConfirmWait 是上报后等待上游落定状态的最长时间。
	ConfirmWait time.Duration
}

// defaultWorkBuddyTaskExecOptions 是默认策略。
func defaultWorkBuddyTaskExecOptions() workBuddyTaskExecOptions {
	return workBuddyTaskExecOptions{
		AllowReport:   false,
		AllowHighRisk: false,
		ConfirmWait:   8 * time.Second,
	}
}

// WorkBuddyExecuteTask 在单个账号上执行一个任务。
func WorkBuddyExecuteTask(
	ctx context.Context,
	account *Account,
	task WorkBuddyTask,
	opts workBuddyTaskExecOptions,
) WorkBuddyTaskExecResult {
	res := WorkBuddyTaskExecResult{TaskCode: task.Code, TaskName: task.Title}

	if !opts.AllowHighRisk && workBuddyTaskRiskOf(task.Code, task.Title) == WorkBuddyRiskHigh {
		res.Outcome = "skipped"
		res.Message = "高风险任务已跳过"
		return res
	}

	// 已完成：直接领奖。
	if task.Status == "completed" {
		return workBuddyClaimTaskResult(ctx, account, task, res)
	}
	if task.Status == "claimed" {
		res.Outcome = "skipped"
		res.Message = "已领取过"
		return res
	}

	// 未完成：仅当允许上报且存在方案时才尝试推进。
	if !opts.AllowReport {
		res.Outcome = "skipped"
		res.Message = "任务未完成（未开启事件上报）"
		return res
	}
	// 互动模块（签到/抽奖/盲盒）走各自的专用接口，不需要事件上报。
	if IsWorkBuddyActivityTask(task.Code) {
		claim, aerr := WorkBuddyRunActivityTask(ctx, account, task.Code)
		switch {
		case aerr != nil:
			res.Outcome = "failed"
			res.Message = aerr.Error()
		case claim != nil && claim.Err != "":
			res.Outcome = "skipped"
			res.Message = claim.Err
		default:
			res.Outcome = "claimed"
			if claim != nil {
				res.Credit = claim.Credit
			}
		}
		return res
	}

	// 需要前置数据或时间窗口的任务：各自有专用链路。
	lower := strings.ToLower(task.Code)
	switch {
	case task.Code == "first_buddy":
		if credit, err := workBuddyEnsureFirstBuddy(ctx, account); err != nil {
			res.Outcome, res.Message = "failed", err.Error()
			return res
		} else if done, _ := workBuddyTaskReached(ctx, account, task.Code); done {
			res.Credit = credit
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		res.Outcome, res.Message = "reported", "已领养并上报，等待上游确认"
		return res
	case strings.Contains(lower, "appearance"):
		if _, err := workBuddyApplyTheme(ctx, account); err != nil {
			res.Outcome, res.Message = "failed", err.Error()
			return res
		}
		if done, _ := workBuddyTaskReached(ctx, account, task.Code); done {
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		res.Outcome, res.Message = "reported", "已应用主题并上报"
		return res
	case strings.Contains(lower, "template"):
		if err := workBuddyRunTemplateTask(ctx, account); err != nil {
			res.Outcome, res.Message = "failed", err.Error()
			return res
		}
		if done, _ := workBuddyTaskReached(ctx, account, task.Code); done {
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		res.Outcome, res.Message = "reported", "已用真实场景上报模板事件"
		return res
	case strings.Contains(lower, "expert"):
		if err := workBuddyRunExpertTask(ctx, account); err != nil {
			res.Outcome, res.Message = "failed", err.Error()
			return res
		}
		if done, _ := workBuddyTaskReached(ctx, account, task.Code); done {
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		res.Outcome, res.Message = "reported", "已上报专家遥测"
		return res
	case lower == "black_cat":
		if err := workBuddyRunBlackCatTask(ctx, account); err != nil {
			res.Outcome, res.Message = "skipped", err.Error()
			return res
		}
		if done, _ := workBuddyTaskReached(ctx, account, task.Code); done {
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		res.Outcome, res.Message = "reported", "夜间对话已完成"
		return res
	}

	// 分享技能：走「创建分享渠道 + 上报渠道事件」的专用链路，
	// 不是单纯的事件上报，因此单独处理。
	if task.Code == "skill_share" {
		if err := WorkBuddyRunSkillShare(ctx, account); err != nil {
			res.Outcome = "failed"
			res.Message = err.Error()
			return res
		}
		if workBuddyWaitTaskCompleted(ctx, account, task.Code, opts.ConfirmWait) {
			refreshed := task
			refreshed.Status = "completed"
			return workBuddyClaimTaskResult(ctx, account, refreshed, res)
		}
		res.Outcome = "reported"
		res.Message = "已创建分享渠道并上报事件，上游尚未确认达标"
		return res
	}

	plan, ok := WorkBuddyEventPlanOf(task.Code)
	if !ok {
		res.Outcome = "unsupported"
		res.Message = "该任务没有可用的自动完成方案，需在客户端操作"
		return res
	}
	// 桌面端对话：6 连事件链 + 真实会话 id。
	if task.Code == "desktop_chat_1_time" || task.Code == "RichMeow_Chat" &&
		plan.NeedsRealSession && len(plan.Events) == 1 && plan.Events[0] == "desktop_chat_sequence" {
		if err := WorkBuddyDesktopChatTask(ctx, account); err != nil {
			res.Outcome = "failed"
			res.Message = err.Error()
			return res
		}
		if workBuddyWaitTaskCompleted(ctx, account, task.Code, opts.ConfirmWait) {
			refreshed := task
			refreshed.Status = "completed"
			return workBuddyClaimTaskResult(ctx, account, refreshed, res)
		}
		res.Outcome = "reported"
		res.Message = "已上报桌面对话事件链，上游尚未确认达标"
		return res
	}

	if plan.NeedsRealSession {
		// 聊天类任务需要真实会话：先真发一次对话拿到 conversationId 与回复，
		// 再按 send → message → response 三段上报。这条路径会产生真实的
		// 模型调用，因此同样受 AllowReport 开关控制（已在上方判断）。
		return workBuddyRunChatTask(ctx, account, task, res, opts)
	}

	events := WorkBuddyBuildReportEvents(plan)
	if err := WorkBuddyReportEvents(ctx, account, events); err != nil {
		res.Outcome = "failed"
		res.Message = "上报事件失败: " + err.Error()
		return res
	}

	// 上报后上游异步落定，短轮询确认是否变成 completed。
	if workBuddyWaitTaskCompleted(ctx, account, task.Code, opts.ConfirmWait) {
		refreshed := task
		refreshed.Status = "completed"
		return workBuddyClaimTaskResult(ctx, account, refreshed, res)
	}

	res.Outcome = "reported"
	res.Message = "已上报事件，上游尚未确认完成"
	return res
}

// workBuddyWaitTaskCompleted 轮询等待任务变为 completed。
func workBuddyWaitTaskCompleted(ctx context.Context, account *Account, code string, wait time.Duration) bool {
	if wait <= 0 {
		return false
	}
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
		tasks, err := WorkBuddyTasksOf(ctx, account)
		if err != nil {
			continue
		}
		for _, t := range tasks {
			if t.Code != code {
				continue
			}
			if t.Status == "completed" {
				return true
			}
			// 已领奖也意味着不需要再领。
			if t.Status == "claimed" {
				return false
			}
		}
	}
	return false
}

// workBuddyClaimTaskResult 领取奖励并填充结果。
func workBuddyClaimTaskResult(
	ctx context.Context,
	account *Account,
	task WorkBuddyTask,
	res WorkBuddyTaskExecResult,
) WorkBuddyTaskExecResult {
	claim, err := WorkBuddyClaimTask(ctx, account, task.Code)
	if err != nil {
		res.Outcome = "failed"
		res.Message = err.Error()
		return res
	}
	if claim != nil && claim.Err != "" {
		res.Outcome = "failed"
		res.Message = claim.Err
		return res
	}
	credit := task.RewardCredit
	if credit < 0 {
		credit = 0
	}
	res.Outcome = "claimed"
	res.Credit = credit
	return res
}

// WorkBuddyExecuteTasks 在多个账号上批量执行任务。
//
// 严格串行并留间隔：这类埋点接口对同 IP 的并发请求最敏感，并发是触发
// 上游风控的首要原因。
func WorkBuddyExecuteTasks(
	ctx context.Context,
	accounts []*Account,
	taskCodes []string,
	opts workBuddyTaskExecOptions,
) map[string][]WorkBuddyTaskExecResult {
	out := make(map[string][]WorkBuddyTaskExecResult, len(accounts))
	for i, acc := range accounts {
		if ctx.Err() != nil {
			break
		}
		tasks, err := WorkBuddyTasksOf(ctx, acc)
		if err != nil {
			out[acc.Name] = []WorkBuddyTaskExecResult{{
				Outcome: "failed", Message: "读取任务失败: " + err.Error(),
			}}
			continue
		}
		// 互动模块（签到/抽奖/盲盒）与成长任务分开取。
		tasks = append(tasks, WorkBuddyActivityTasksOf(ctx, acc)...)

		results := make([]WorkBuddyTaskExecResult, 0, len(taskCodes))
		for _, code := range taskCodes {
			var target *WorkBuddyTask
			for j := range tasks {
				if tasks[j].Code == code {
					target = &tasks[j]
					break
				}
			}
			if target == nil {
				results = append(results, WorkBuddyTaskExecResult{
					TaskCode: code, Outcome: "skipped", Message: "该账号没有此任务",
				})
				continue
			}
			results = append(results, WorkBuddyExecuteTask(ctx, acc, *target, opts))
			// 任务之间也留间隔，避免短时间高频请求。
			select {
			case <-ctx.Done():
			case <-time.After(1200 * time.Millisecond):
			}
		}
		out[acc.Name] = results
		if i < len(accounts)-1 {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
		}
	}
	return out
}

// workBuddyExecSummary 汇总执行结果。
func workBuddyExecSummary(results map[string][]WorkBuddyTaskExecResult) string {
	var claimed, failed, reported int
	var credit float64
	for _, list := range results {
		for _, r := range list {
			switch r.Outcome {
			case "claimed":
				claimed++
				credit += r.Credit
			case "failed":
				failed++
			case "reported":
				reported++
			}
		}
	}
	return fmt.Sprintf("领奖 %d 项（%.0f 积分），上报 %d 项，失败 %d 项",
		claimed, credit, reported, failed)
}

// WorkBuddyTaskExecOptionsOf 供 handler 构造执行选项。
func WorkBuddyTaskExecOptionsOf(allowReport, allowHighRisk bool) workBuddyTaskExecOptions {
	opts := defaultWorkBuddyTaskExecOptions()
	opts.AllowReport = allowReport
	opts.AllowHighRisk = allowHighRisk
	return opts
}

// workBuddyRunChatTask 执行聊天类任务：真实对话 + 三段事件上报 + 领奖。
func workBuddyRunChatTask(
	ctx context.Context,
	account *Account,
	task WorkBuddyTask,
	res WorkBuddyTaskExecResult,
	opts workBuddyTaskExecOptions,
) WorkBuddyTaskExecResult {
	plan, _ := WorkBuddyEventPlanOf(task.Code)
	rounds := plan.Repeat
	if rounds <= 0 {
		rounds = 1
	}
	// 上游进度是按次数累加的，目标次数未知时按方案给定次数跑；
	// 每轮跑完都回读一次状态，达标即停，避免多跑浪费模型额度。
	for i := 0; i < rounds; i++ {
		if ctx.Err() != nil {
			res.Outcome = "failed"
			res.Message = "执行被取消"
			return res
		}
		prompt := workBuddyChatPromptFor(i)
		convID, reply, err := WorkBuddyChatOnce(ctx, account, prompt, "glm-5.2")
		if err != nil {
			res.Outcome = "failed"
			res.Message = err.Error()
			return res
		}
		if err := WorkBuddyReportChatEvents(ctx, account, convID, prompt, reply, "glm-5.2"); err != nil {
			res.Outcome = "failed"
			res.Message = "上报会话事件失败: " + err.Error()
			return res
		}
		// 回读状态，已达标就不再继续。
		if done, status := workBuddyTaskReached(ctx, account, task.Code); done || status == "completed" {
			return workBuddyClaimTaskResult(ctx, account, task, res)
		}
		select {
		case <-ctx.Done():
		case <-time.After(1500 * time.Millisecond):
		}
	}
	// 全部轮次跑完仍未达标：如实汇报。
	res.Outcome = "reported"
	res.Message = "已完成对话并上报，上游尚未确认达标"
	return res
}

// workBuddyTaskReached 判断任务是否已达标（completed / claimed）。
func workBuddyTaskReached(ctx context.Context, account *Account, code string) (bool, string) {
	tasks, err := WorkBuddyTasksOf(ctx, account)
	if err != nil {
		return false, ""
	}
	for _, t := range tasks {
		if t.Code != code {
			continue
		}
		return t.Status == "claimed", t.Status
	}
	return false, ""
}
