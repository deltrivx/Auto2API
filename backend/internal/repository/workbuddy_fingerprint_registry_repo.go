package repository

import (
	"context"
	"database/sql"

	"github.com/DeltrivX/Auto2API/internal/service"
)

// WorkBuddy 硬件指纹登记表仓储。
//
// 表结构见 migrations/255_workbuddy_fingerprint_registry.sql。
// profile_key 与 uid 都有唯一索引，是「账号不撞号」的最终保证。

type workBuddyFingerprintRegistryRepo struct {
	db *sql.DB
}

// NewWorkBuddyFingerprintRegistryRepo 构造登记表仓储并注入 service。
func NewWorkBuddyFingerprintRegistryRepo(db *sql.DB) service.WorkBuddyFingerprintRegistry {
	repo := &workBuddyFingerprintRegistryRepo{db: db}
	service.SetWorkBuddyFingerprintRegistry(repo)
	return repo
}

// ProfileKeyInUse 报告组合是否已被其它账号占用。
func (r *workBuddyFingerprintRegistryRepo) ProfileKeyInUse(ctx context.Context, profileKey string, accountID int64) (bool, error) {
	var existing int64
	err := r.db.QueryRowContext(ctx, `
		SELECT account_id FROM workbuddy_fingerprint_registry WHERE profile_key = $1`, profileKey).Scan(&existing)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// 该组合属于别的账号 → 已占用；属于本账号 → 可复用。
	return existing != accountID, nil
}

// Register 登记账号与组合的对应关系，已存在则更新。
func (r *workBuddyFingerprintRegistryRepo) Register(ctx context.Context, accountID int64, profileKey, uid string, salt int) error {
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO workbuddy_fingerprint_registry (account_id, profile_key, uid, salt)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (account_id) DO UPDATE
		   SET profile_key = EXCLUDED.profile_key,
		       uid         = EXCLUDED.uid,
		       salt        = EXCLUDED.salt`, accountID, profileKey, uid, salt)
	return err
}
