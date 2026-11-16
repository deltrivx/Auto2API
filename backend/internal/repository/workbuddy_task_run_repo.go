package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/DeltrivX/Auto2API/internal/service"
)

// WorkBuddy 积分任务执行台账仓储。
//
// 表结构见 migrations/257_workbuddy_task_runs.sql。

type workBuddyTaskRunRepo struct {
	db *sql.DB
}

// NewWorkBuddyTaskRunRepo 构造执行台账仓储并注入 service。
func NewWorkBuddyTaskRunRepo(db *sql.DB) service.WorkBuddyTaskRunStore {
	repo := &workBuddyTaskRunRepo{db: db}
	service.SetWorkBuddyTaskRunStore(repo)
	return repo
}

// InsertRun 写入一条执行记录。
func (r *workBuddyTaskRunRepo) InsertRun(ctx context.Context, run *service.WorkBuddyTaskRun) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workbuddy_task_runs
			(account_id, account_name, kind, task_code, task_name, result, credit, streak_days, detail, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		run.AccountID, run.AccountName, run.Kind, run.TaskCode, run.TaskName,
		run.Result, run.Credit, run.StreakDays, run.Detail, run.OccurredAt)
	return err
}

// ListRuns 返回最近的执行记录。
func (r *workBuddyTaskRunRepo) ListRuns(ctx context.Context, limit int) ([]service.WorkBuddyTaskRun, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, account_id, account_name, kind, task_code, task_name,
		       result, credit::float8, streak_days, detail, occurred_at
		FROM workbuddy_task_runs
		ORDER BY occurred_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.WorkBuddyTaskRun, 0, limit)
	for rows.Next() {
		var run service.WorkBuddyTaskRun
		if err := rows.Scan(&run.ID, &run.AccountID, &run.AccountName, &run.Kind,
			&run.TaskCode, &run.TaskName, &run.Result, &run.Credit,
			&run.StreakDays, &run.Detail, &run.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// SummarizeTasks 按任务聚合最近状态与成功次数。
func (r *workBuddyTaskRunRepo) SummarizeTasks(ctx context.Context) ([]service.WorkBuddyTaskSummaryItem, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT task_code,
		       MAX(task_name) AS task_name,
		       COUNT(*)::int AS total,
		       SUM(CASE WHEN result = 'success' THEN 1 ELSE 0 END)::int AS success,
		       MAX(occurred_at) AS last_at
		FROM workbuddy_task_runs
		WHERE task_code <> ''
		GROUP BY task_code
		ORDER BY last_at DESC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]service.WorkBuddyTaskSummaryItem, 0, 32)
	for rows.Next() {
		var item service.WorkBuddyTaskSummaryItem
		if err := rows.Scan(&item.TaskCode, &item.TaskName, &item.Total, &item.Success, &item.LastAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 补最近一次的结果与说明：单独查一次比在 SQL 里做窗口函数更直观。
	for i := range out {
		_ = r.db.QueryRowContext(ctx, `
			SELECT result, detail FROM workbuddy_task_runs
			WHERE task_code = $1 ORDER BY occurred_at DESC LIMIT 1`, out[i].TaskCode).
			Scan(&out[i].LastResult, &out[i].LastDetail)
	}
	return out, nil
}

// SummarizeCredits 汇总今日与累计积分。
func (r *workBuddyTaskRunRepo) SummarizeCredits(ctx context.Context, day time.Time) (*service.WorkBuddyCreditSummary, error) {
	out := &service.WorkBuddyCreditSummary{}
	err := r.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN kind LIKE '%checkin%' AND occurred_at::date = $1::date
			                  THEN credit ELSE 0 END), 0)::float8 AS today_checkin,
			COALESCE(SUM(CASE WHEN kind LIKE '%claim%' AND occurred_at::date = $1::date
			                  THEN credit ELSE 0 END), 0)::float8 AS today_task,
			COALESCE(SUM(credit), 0)::float8 AS total
		FROM workbuddy_task_runs
		WHERE result = 'success'`, day).
		Scan(&out.TodayCheckinCredit, &out.TodayTaskCredit, &out.TotalCredit)
	if err != nil {
		return nil, err
	}
	out.TodayTotalCredit = out.TodayCheckinCredit + out.TodayTaskCredit
	return out, nil
}
