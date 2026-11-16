package repository

// 自增 ID 的空洞回收。
//
// 光把序列退回 MAX(id)+1 只能防止「尾部膨胀」，中间的空洞照样留着：
// 删掉 id=3 之后，序列仍是 MAX+1，下一个新行还是拿最大的号。反复增删
// 几十次，表里可能只剩 5 行而 ID 已经到 200。
//
// 这里补上真正的回收：新建行时先找最小的可用空位并用它。做法是在插入
// 前把序列临时指向那个空位：
//
//	SELECT setval(seq, LEAK_MIN, false)   -- 下一次 nextval 返回该值
//	INSERT ...                            -- 拿到这个号
//	SELECT setval(seq, MAX(id)+1, false)  -- 序列恢复到合法上限
//
// 只在「确认有空洞」时做，且全程在同一个事务里 —— 否则并发插入会撞号
// （唯一键冲突）或把序列留在危险位置。

import (
	"context"
	"database/sql"
	"fmt"
)

// idGapRecycler 是回收空位所需的最小执行接口。
type idGapRecycler interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryInt64 执行一个返回单个整数的查询。
func queryInt64(ctx context.Context, exec idGapRecycler, query string, args ...any) (int64, error) {
	rows, err := exec.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return 0, fmt.Errorf("no row")
	}
	var out int64
	if err := rows.Scan(&out); err != nil {
		return 0, err
	}
	return out, nil
}

// findLowestFreeID 返回 table 中最小的可用 ID。
//
// 实现方式是找第一个「前面存在空位」的位置：把所有 id 排序后逐行比对
// 期望值。行数不多（账号/分组/密钥都是百级），全表扫描的代价可以忽略，
// 而且比维护一张空闲表更不容易出错。
//
// 返回 0 表示没有空位可用（尾部连续，交给序列自然增长）。
func findLowestFreeID(ctx context.Context, exec idGapRecycler, table string) (int64, error) {
	if exec == nil || table == "" {
		return 0, nil
	}
	// DISTINCT 后逐行比较：第一个 id != 期望值的位置就是最小空位。
	query := fmt.Sprintf(`
		SELECT COALESCE(
		  (SELECT expected FROM (
		     SELECT id, ROW_NUMBER() OVER (ORDER BY id) AS expected
		     FROM (SELECT DISTINCT id FROM %s) t
		   ) x WHERE id <> expected LIMIT 1),
		  0)`, table)
	gap, err := queryInt64(ctx, exec, query)
	if err != nil {
		return 0, fmt.Errorf("find free id for %s: %w", table, err)
	}
	return gap, nil
}

// beginGapReuse 在事务里为一个即将插入的新行占下最小空位。
//
// 返回 (占用的 id, 恢复函数)。调用方必须在同一个事务里、紧接着执行插入，
// 并在插入完成后调用恢复函数把序列拉回合法上限。
//
// 并发安全依赖**事务级 advisory lock**：同一张表的空位分配串行化，
// 不同表互不阻塞。不加锁时两个事务可能同时看到同一个空位。
func beginGapReuse(ctx context.Context, exec idGapRecycler, table, seq string) (int64, func(), error) {
	noop := func() {}
	if exec == nil || table == "" || seq == "" {
		return 0, noop, nil
	}

	// 表级串行：锁键由表名派生，Postgres 的 pg_advisory_xact_lock
	// 在事务结束时自动释放。
	lockQuery := fmt.Sprintf(`SELECT pg_advisory_xact_lock(hashtext('id_gap:%s'))`, table)
	if _, err := exec.ExecContext(ctx, lockQuery); err != nil {
		return 0, noop, fmt.Errorf("lock %s for id reuse: %w", table, err)
	}

	gap, err := findLowestFreeID(ctx, exec, table)
	if err != nil {
		return 0, noop, err
	}
	if gap <= 0 {
		// 没有空洞，走正常自增。
		return 0, noop, nil
	}
	if err := useID(ctx, exec, seq, gap); err != nil {
		return 0, noop, err
	}
	restore := func() {
		// 用 WithoutCancel：恢复必须执行，即使请求已被取消。
		_ = restoreSequence(context.WithoutCancel(ctx), exec, table, seq)
	}
	return gap, restore, nil
}

// useID 让序列下一次 nextval 返回 id。
func useID(ctx context.Context, exec idGapRecycler, seq string, id int64) error {
	if exec == nil || seq == "" || id <= 0 {
		return nil
	}
	query := fmt.Sprintf(`SELECT setval('%s', %d, false)`, seq, id)
	if _, err := exec.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("prime sequence %s to %d: %w", seq, id, err)
	}
	return nil
}

// restoreSequence 把序列恢复到 MAX(id)+1。
func restoreSequence(ctx context.Context, exec idGapRecycler, table, seq string) error {
	if exec == nil || table == "" || seq == "" {
		return nil
	}
	query := fmt.Sprintf(
		`SELECT setval('%s', COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)`, seq, table)
	if _, err := exec.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("restore sequence %s: %w", seq, err)
	}
	return nil
}

// prepareIDReuse 在插入前把序列指向最小空位，返回插入后要执行的恢复函数。
//
// 串行化用 session 级 advisory lock：拿不到锁就放弃回收（返回 nil），
// 让本次插入走正常自增 —— 抢不到锁说明有并发在建行，此时回退最安全。
func prepareIDReuse(ctx context.Context, exec idGapRecycler, table, seq string) (func(), error) {
	if exec == nil || table == "" || seq == "" {
		return nil, nil
	}
	lockKey := fmt.Sprintf("id_gap:%s", table)
	got, lockErr := queryInt64(ctx, exec,
		`SELECT CASE WHEN pg_try_advisory_lock(hashtext($1)) THEN 1 ELSE 0 END`, lockKey)
	if lockErr != nil || got != 1 {
		return nil, fmt.Errorf("skip id reuse for %s", table)
	}
	unlock := func() {
		_, _ = exec.ExecContext(context.WithoutCancel(ctx),
			`SELECT pg_advisory_unlock(hashtext($1))`, lockKey)
	}

	gap, err := findLowestFreeID(ctx, exec, table)
	if err != nil || gap <= 0 {
		unlock()
		return nil, err
	}
	if err := useID(ctx, exec, seq, gap); err != nil {
		unlock()
		return nil, err
	}
	return func() {
		_ = restoreSequence(context.WithoutCancel(ctx), exec, table, seq)
		unlock()
	}, nil
}
