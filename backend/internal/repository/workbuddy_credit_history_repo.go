package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/DeltrivX/Auto2API/internal/service"
)

// WorkBuddy 积分日汇总仓储。
//
// 表结构见 migrations/254_workbuddy_credit_history.sql。
// 账号列表的积分趋势图从这里读，写入由计费完成时触发。

type workBuddyCreditHistoryRepo struct {
	db *sql.DB
}

// NewWorkBuddyCreditHistoryRepo 构造积分历史仓储。
//
// 构造时把自身注册为 service 层的趋势数据源：service 不能直接依赖
// repository（会形成反向依赖），用 setter 注入是这里既有的约定。
func NewWorkBuddyCreditHistoryRepo(db *sql.DB) service.WorkBuddyCreditTrendStore {
	repo := &workBuddyCreditHistoryRepo{db: db}
	service.SetWorkBuddyCreditTrendStore(repo)
	return repo
}

// SumCreditsByDay 返回区间内每天的 [消耗, 获得] 积分。
//
// 消耗优先取 workbuddy_credit_history（含导入的历史数据）；该表没有记录的
// 日期回落到 usage_logs 的 actual_cost 聚合，保证曲线在任何阶段都有数据，
// 而不是一张全 0 的图。
func (r *workBuddyCreditHistoryRepo) SumCreditsByDay(ctx context.Context, from, to time.Time) (map[string][2]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT to_char(created_at, 'YYYY-MM-DD') AS d,
		       COALESCE(SUM(actual_cost), 0)::float8 AS consumed,
		       0::float8 AS earned
		FROM usage_logs
		WHERE created_at::date >= $1::date AND created_at::date <= $2::date
		GROUP BY 1
		UNION ALL
		SELECT to_char(day, 'YYYY-MM-DD') AS d,
		       COALESCE(SUM(consumed), 0)::float8 AS consumed,
		       COALESCE(SUM(earned), 0)::float8 AS earned
		FROM workbuddy_credit_history
		WHERE day >= $1::date AND day <= $2::date
		GROUP BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][2]float64)
	for rows.Next() {
		var day string
		var consumed, earned float64
		if err := rows.Scan(&day, &consumed, &earned); err != nil {
			return nil, err
		}
		// UNION ALL 可能让同一天出现两行（历史表 + 用量日志），累加合并。
		prev := out[day]
		out[day] = [2]float64{prev[0] + consumed, prev[1] + earned}
	}
	return out, rows.Err()
}

// CountCallsByDay 返回区间内每天的调用次数。
//
// 调用次数取自用量日志：积分历史表只记额度，不记请求条数。
func (r *workBuddyCreditHistoryRepo) CountCallsByDay(ctx context.Context, from, to time.Time) (map[string]int, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT to_char(created_at, 'YYYY-MM-DD') AS d, COUNT(*)::int AS n
		FROM usage_logs
		WHERE created_at >= $1 AND created_at <= $2
		GROUP BY 1`, from, to.Add(24*time.Hour))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]int)
	for rows.Next() {
		var day string
		var n int
		if err := rows.Scan(&day, &n); err != nil {
			return nil, err
		}
		out[day] = n
	}
	return out, rows.Err()
}

// AddDailyCredits 把消耗/获得累加到指定日期。
func (r *workBuddyCreditHistoryRepo) AddDailyCredits(ctx context.Context, day time.Time, consumed, earned float64, source string) error {
	if source == "" {
		source = "runtime"
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workbuddy_credit_history (day, consumed, earned, source)
		VALUES ($1::date, $2, $3, $4)`, day, consumed, earned, source)
	return err
}

// SumCreditsByDayForAccount 返回单个账号区间内每天的积分消耗。
//
// 账号维度只用 usage_logs：它带 account_id，能精确到账号；
// workbuddy_credit_history 是全局按天汇总，没有账号维度。
func (r *workBuddyCreditHistoryRepo) SumCreditsByDayForAccount(
	ctx context.Context, accountID int64, from, to time.Time,
) (map[string][2]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT to_char(created_at, 'YYYY-MM-DD') AS d,
		       COALESCE(SUM(actual_cost), 0)::float8 AS consumed
		FROM usage_logs
		WHERE account_id = $1
		  AND created_at::date >= $2::date
		  AND created_at::date <= $3::date
		GROUP BY 1`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string][2]float64)
	for rows.Next() {
		var day string
		var consumed float64
		if err := rows.Scan(&day, &consumed); err != nil {
			return nil, err
		}
		out[day] = [2]float64{consumed, 0}
	}
	return out, rows.Err()
}

// SumEarnedByDay 返回区间内每天由签到/任务获得的积分。
//
// 数据源是 workbuddy_task_runs（逐次执行明细）。workbuddy_credit_history
// 在重构后已无写入方，earned 会永久为 0，这里作为权威口径补上。
func (r *workBuddyCreditHistoryRepo) SumEarnedByDay(ctx context.Context, from, to time.Time) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT to_char(occurred_at, 'YYYY-MM-DD') AS d,
		       COALESCE(SUM(credit), 0)::float8 AS earned
		FROM workbuddy_task_runs
		WHERE occurred_at::date >= $1::date
		  AND occurred_at::date <= $2::date
		GROUP BY 1`, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]float64)
	for rows.Next() {
		var day string
		var earned float64
		if err := rows.Scan(&day, &earned); err != nil {
			return nil, err
		}
		out[day] = earned
	}
	return out, rows.Err()
}

// SumEarnedByDayForAccount 同上，但只统计指定账号。
func (r *workBuddyCreditHistoryRepo) SumEarnedByDayForAccount(
	ctx context.Context, accountID int64, from, to time.Time,
) (map[string]float64, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT to_char(occurred_at, 'YYYY-MM-DD') AS d,
		       COALESCE(SUM(credit), 0)::float8 AS earned
		FROM workbuddy_task_runs
		WHERE account_id = $1
		  AND occurred_at::date >= $2::date
		  AND occurred_at::date <= $3::date
		GROUP BY 1`, accountID, from, to)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make(map[string]float64)
	for rows.Next() {
		var day string
		var earned float64
		if err := rows.Scan(&day, &earned); err != nil {
			return nil, err
		}
		out[day] = earned
	}
	return out, rows.Err()
}
