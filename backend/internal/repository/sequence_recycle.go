package repository

import (
	"context"
	"database/sql"
	"fmt"
)

// 自增序列回收。
//
// 本项目一律物理删除，因此删除后必须把对应表的自增序列回退到
// 「当前最大 ID + 1」，否则反复删除新增会让 ID 一路膨胀、与实际行数脱节。
//
// 用法：在删除事务提交前调用 recycleSequence(tx, "groups", "groups_id_seq")。
// 传 nil tx 时退化为直接用 sql 连接执行（用于没有事务的删除路径）。

// recycleSequence 把 seq 重置为 table 的 MAX(id)+1。
//
// 表名与序列名来自代码常量，不接收外部输入，因此这里的安全性不依赖参数化；
// 仍用 Sprintf 拼接是为了让 SQL 可读（Postgres 不支持参数化标识符）。
func recycleSequence(ctx context.Context, exec interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}, table, seq string) error {
	if exec == nil || table == "" || seq == "" {
		return nil
	}
	query := fmt.Sprintf(
		`SELECT setval('%s', COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)`, seq, table)
	if _, err := exec.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("recycle sequence %s: %w", seq, err)
	}
	return nil
}

// recycleSequenceEnt 用 ent client 执行序列回收。
//
// 大多数仓储持有的是 ent client 而不是裸 sql.DB，这里统一走 client 的
// ExecContext（ent 会透传到驱动）。
func recycleSequenceEnt(ctx context.Context, client execer, table, seq string) error {
	if client == nil || table == "" || seq == "" {
		return nil
	}
	query := fmt.Sprintf(
		`SELECT setval('%s', COALESCE((SELECT MAX(id) FROM %s), 0) + 1, false)`, seq, table)
	if _, err := client.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("recycle sequence %s: %w", seq, err)
	}
	return nil
}

// execer 是 *rtent.Client 的最小接口。
type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}
