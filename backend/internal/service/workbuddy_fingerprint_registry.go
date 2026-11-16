package service

import (
	"context"
	"sync"
)

// WorkBuddy 硬件指纹的全局登记。
//
// 画像由维度组合派生，组合空间数万量级；但要保证「已有账号绝不撞号」，
// 还需要一份跨进程的占用记录——只靠进程内 map，重启后就会重复分配。
//
// 这里定义 service 侧的登记接口，具体存储在 repository 层（带唯一索引的
// 表），service 通过 SetWorkBuddyFingerprintRegistry 注入。

// WorkBuddyFingerprintRegistry 记录已占用的硬件组合。
type WorkBuddyFingerprintRegistry interface {
	// ProfileKeyInUse 报告某个组合是否已被（除 accountID 之外的）账号占用。
	ProfileKeyInUse(ctx context.Context, profileKey string, accountID int64) (bool, error)
	// Register 登记账号与组合的对应关系（幂等）。
	Register(ctx context.Context, accountID int64, profileKey, uid string, salt int) error
}

var (
	workBuddyFingerprintRegistryMu sync.RWMutex
	workBuddyFingerprintRegistry   WorkBuddyFingerprintRegistry
)

// SetWorkBuddyFingerprintRegistry 注入登记表。
func SetWorkBuddyFingerprintRegistry(reg WorkBuddyFingerprintRegistry) {
	workBuddyFingerprintRegistryMu.Lock()
	workBuddyFingerprintRegistry = reg
	workBuddyFingerprintRegistryMu.Unlock()
}

// workBuddyFingerprintRegistryOrNil 返回当前注入的登记表。
func workBuddyFingerprintRegistryOrNil() WorkBuddyFingerprintRegistry {
	workBuddyFingerprintRegistryMu.RLock()
	defer workBuddyFingerprintRegistryMu.RUnlock()
	return workBuddyFingerprintRegistry
}

// workBuddyAssignUniqueProfile 为账号选一个全局唯一的硬件画像。
//
// 流程：
//  1. 按 uid 派生画像（salt=0）
//  2. 若该组合已被其它账号占用，salt 递增重新派生
//  3. 最多尝试 workBuddyFingerprintMaxSalt 次；仍未找到则返回最后一次结果
//     （组合空间数万量级，实际不会走到这里）
//
// 登记表不可用时退化为纯派生——单进程内唯一性仍然成立。
func workBuddyAssignUniqueProfile(ctx context.Context, accountID int64, uid string) (workBuddyFingerprintProfile, int) {
	reg := workBuddyFingerprintRegistryOrNil()
	for salt := 0; salt < workBuddyFingerprintMaxSalt; salt++ {
		profile := workBuddyDeriveProfile(uid, salt)
		if reg == nil {
			return profile, salt
		}
		inUse, err := reg.ProfileKeyInUse(ctx, workBuddyProfileKey(profile), accountID)
		if err != nil || !inUse {
			return profile, salt
		}
	}
	return workBuddyDeriveProfile(uid, workBuddyFingerprintMaxSalt-1), workBuddyFingerprintMaxSalt - 1
}

// workBuddyRegisterFingerprint 把账号与其画像登记进表（尽力而为）。
func workBuddyRegisterFingerprint(ctx context.Context, accountID int64, profile workBuddyFingerprintProfile, uid string, salt int) {
	reg := workBuddyFingerprintRegistryOrNil()
	if reg == nil || accountID <= 0 {
		return
	}
	_ = reg.Register(ctx, accountID, workBuddyProfileKey(profile), uid, salt)
}
