package repository

import (
	"context"
	"fmt"

	dbent "github.com/DeltrivX/Auto2API/ent"
	"github.com/DeltrivX/Auto2API/ent/group"
	"github.com/DeltrivX/Auto2API/internal/service"
)

const simpleModeDefaultGroupDescription = "Auto-created default group"

func ensureSimpleModeDefaultGroups(ctx context.Context, client *dbent.Client) error {
	if client == nil {
		return fmt.Errorf("nil ent client")
	}

	// 本分支只服务 WorkBuddy：国内站与国际站各一个默认分组。
	// 历史上还会为 openai/grok/antigravity 等平台预置分组与图像生成回填，
	// 那些平台已移除。
	for _, platform := range []string{service.PlatformWorkBuddyCN, service.PlatformWorkBuddyIntl, service.PlatformMarvis} {
		name := platform + "-default"
		if err := createGroupIfNotExists(ctx, client, name, platform); err != nil {
			return err
		}
	}

	return nil
}

func createGroupIfNotExists(ctx context.Context, client *dbent.Client, name, platform string) error {
	exists, err := client.Group.Query().
		Where(group.NameEQ(name)).
		Exist(ctx)
	if err != nil {
		return fmt.Errorf("check group exists %s: %w", name, err)
	}
	if exists {
		return nil
	}

	_, err = client.Group.Create().
		SetName(name).
		SetDescription(simpleModeDefaultGroupDescription).
		SetPlatform(platform).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1.0).
		SetIsExclusive(false).
		SetAllowImageGeneration(service.DefaultAllowImageGenerationForPlatform(platform)).
		Save(ctx)
	if err != nil {
		if dbent.IsConstraintError(err) {
			// Concurrent server startups may race on creation; treat as success.
			return nil
		}
		return fmt.Errorf("create default group %s: %w", name, err)
	}
	return nil
}
