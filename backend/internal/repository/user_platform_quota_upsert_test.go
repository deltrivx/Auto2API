//go:build integration

package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DeltrivX/Auto2API/ent/schema/mixins"
	"github.com/DeltrivX/Auto2API/ent/userplatformquota"
	"github.com/stretchr/testify/require"
)

func TestUpsertForUser_NewUserInsertsAllRecords(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	daily := 10.0
	weekly := 50.0
	monthly := 200.0
	records := []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &daily, WeeklyLimitCredits: &weekly, MonthlyLimitCredits: &monthly},
		{UserID: userID, Platform: "openai", DailyLimitCredits: &daily},
	}
	require.NoError(t, repo.UpsertForUser(ctx, userID, records))

	got, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestUpsertForUser_PartialUpdateSoftDeletesMissingPlatforms(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	d1 := 10.0
	d2 := 20.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d1},
		{UserID: userID, Platform: "openai", DailyLimitCredits: &d1},
	}))
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d2},
		{UserID: userID, Platform: "gemini", DailyLimitCredits: &d1},
	}))

	active, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	platforms := map[string]float64{}
	for _, r := range active {
		require.NotNil(t, r.DailyLimitCredits)
		platforms[r.Platform] = *r.DailyLimitCredits
	}
	require.Len(t, platforms, 2)
	require.InDelta(t, 20.0, platforms["anthropic"], 1e-9)
	require.InDelta(t, 10.0, platforms["gemini"], 1e-9)
	_, openaiActive := platforms["openai"]
	require.False(t, openaiActive, "openai should be soft-deleted")
}

func TestUpsertForUser_PreservesUsageAndWindowStart(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	d := 10.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d},
	}))

	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "anthropic", 3.5, now))

	newD := 50.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &newD},
	}))

	rec, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, rec)
	require.InDelta(t, 50.0, *rec.DailyLimitCredits, 1e-9, "limit should update")
	require.InDelta(t, 3.5, rec.DailyUsageCredits, 1e-9, "usage must be preserved")
	require.NotNil(t, rec.DailyWindowStart, "window_start must be preserved")
}

func TestUpsertForUser_ReactivatesSoftDeleted(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	d := 10.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d},
	}))
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{}))

	gone, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.Nil(t, gone, "anthropic should be soft-deleted (not active)")

	d2 := 20.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d2},
	}))

	back, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, back, "anthropic should be active again")
	require.InDelta(t, 20.0, *back.DailyLimitCredits, 1e-9)

	allRows, err := client.UserPlatformQuota.Query().
		Where(userplatformquota.UserIDEQ(userID), userplatformquota.PlatformEQ("anthropic")).
		All(ctx)
	require.NoError(t, err)
	activeCount := 0
	for _, r := range allRows {
		if r.DeletedAt == nil {
			activeCount++
		}
	}
	require.Equal(t, 1, activeCount, "should have exactly one active row")
}

func TestUpsertForUser_EmptyClearsAll(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	d := 10.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d},
		{UserID: userID, Platform: "openai", DailyLimitCredits: &d},
	}))

	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{}))

	got, err := repo.ListByUser(ctx, userID)
	require.NoError(t, err)
	require.Empty(t, got)
}

// TestUpsertForUser_UnlimitedRecordSoftDeletesAndDiscardsUsage 锁定语义：三档全空的记录视为未配置，
// 等价于该平台不在列表里 → 软删既有行并放弃其累计用量；之后再配置限额会新建行、从新窗口起算。
func TestUpsertForUser_UnlimitedRecordSoftDeletesAndDiscardsUsage(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	userID := mustCreateUserForQuota(t, client)
	repo := NewUserPlatformQuotaRepository(client)

	d := 10.0
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d},
	}))
	now := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
	require.NoError(t, repo.IncrementUsageWithReset(ctx, userID, "anthropic", 7.5, now))

	// 清空限额：提交三档全空的记录
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic"},
	}))
	gone, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.Nil(t, gone, "an unlimited record must soft-delete the row instead of keeping it with NULL limits")

	// 重新配置限额：新行从零起算
	require.NoError(t, repo.UpsertForUser(ctx, userID, []UserPlatformQuotaRecord{
		{UserID: userID, Platform: "anthropic", DailyLimitCredits: &d},
	}))
	back, err := repo.GetByUserPlatform(ctx, userID, "anthropic")
	require.NoError(t, err)
	require.NotNil(t, back)
	require.InDelta(t, 0, back.DailyUsageCredits, 1e-9, "usage restarts from zero")
	require.Nil(t, back.DailyWindowStart, "window restarts")

	// 软删 mixin 默认隐藏已删行，计数全部行需显式跳过
	all, err := client.UserPlatformQuota.Query().
		Where(userplatformquota.UserIDEQ(userID), userplatformquota.PlatformEQ("anthropic")).
		All(mixins.SkipSoftDelete(ctx))
	require.NoError(t, err)
	require.Len(t, all, 2, "one soft-deleted history row plus one active row")
}
