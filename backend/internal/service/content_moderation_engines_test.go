package service

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestContentModerationEngineProfilesPreserveLegacy(t *testing.T) {
	repo := &contentModerationTestSettingRepo{values: map[string]string{SettingKeyContentModerationConfig: `{"enabled":true,"mode":"pre_block","api_key":"old-openai-key","base_url":"https://openai.example","model":"omni","thresholds":{"sexual":0.65},"auto_ban_enabled":true}`}}
	s := &ContentModerationService{settingRepo: repo}
	view, err := s.GetConfig(context.Background())
	require.NoError(t, err)
	require.Equal(t, "openai", view.Engine)
	require.Equal(t, 1, view.APIKeyCount)
	require.Equal(t, ContentModerationDefaultThresholds(), view.EngineConfigs["typesafe"].Thresholds)
	engine := "typesafe"
	base := "https://typesafe.example"
	model := "jev-test"
	keys := []string{"new-typesafe-key"}
	thresholds := map[string]float64{"sexual": 0.92}
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine, BaseURL: &base, Model: &model, APIKeys: &keys, Thresholds: &thresholds})
	require.NoError(t, err)
	require.Equal(t, base, view.BaseURL)
	require.Equal(t, 0.92, view.Thresholds["sexual"])
	require.True(t, view.AutoBanEnabled)
	require.Equal(t, "pre_block", view.Mode)
	var stored ContentModerationConfig
	require.NoError(t, json.Unmarshal([]byte(repo.values[SettingKeyContentModerationConfig]), &stored))
	require.Equal(t, "https://openai.example", stored.BaseURL)
	require.Equal(t, []string{"old-openai-key"}, stored.APIKeys)
	require.Equal(t, keys, stored.TypeSafe.APIKeys)
	serialized, err := json.Marshal(view)
	require.NoError(t, err)
	require.NotContains(t, string(serialized), "new-typesafe-key")
	require.NotContains(t, string(serialized), "old-openai-key")
	engine = "openai"
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine})
	require.NoError(t, err)
	require.Equal(t, "omni", view.Model)
	require.Equal(t, 0.65, view.Thresholds["sexual"])
	require.Equal(t, "jev-test", view.EngineConfigs["typesafe"].Model)
	require.Equal(t, 0.92, view.EngineConfigs["typesafe"].Thresholds["sexual"])
	engine = "typesafe"
	view, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine, EngineConfigs: map[string]UpdateContentModerationEngineInput{"typesafe": {ClearAPIKey: true}}})
	require.NoError(t, err)
	require.Zero(t, view.APIKeyCount)
	require.Equal(t, 1, view.EngineConfigs["openai"].APIKeyCount)
}

func TestContentModerationEngineThresholdDefaultsAreIndependent(t *testing.T) {
	openai := moderationEngineDefaults(ContentModerationEngineOpenAI)
	typeSafe := moderationEngineDefaults(ContentModerationEngineTypeSafe)
	require.Equal(t, openai.Thresholds, typeSafe.Thresholds)
	typeSafe.Thresholds["sexual"] = 0.8
	require.Equal(t, 0.65, openai.Thresholds["sexual"])
	require.Equal(t, openai.Thresholds, moderationEngineDefaults(ContentModerationEngineTypeSafe).Thresholds)
}

func TestContentModerationEngineRuntimeSwitchKeepsInFlightSnapshot(t *testing.T) {
	cfg := defaultContentModerationConfig()
	cfg.APIKeys = []string{"openai-key"}
	cfg.TypeSafe = moderationEngineDefaults("typesafe")
	cfg.TypeSafe.APIKeys = []string{"typesafe-key"}
	cfg.TypeSafe.Thresholds["sexual"] = 0.91
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	repo := &contentModerationRuntimeSettingRepo{values: map[string]string{
		SettingKeyRiskControlEnabled:      "true",
		SettingKeyContentModerationConfig: string(raw),
	}}
	s := runtimeCacheTestService(repo, time.Hour)
	old, err := s.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	engine := "typesafe"
	_, err = s.UpdateConfig(context.Background(), UpdateContentModerationConfigInput{Engine: &engine})
	require.NoError(t, err)
	current, err := s.loadRuntimeSnapshot(context.Background())
	require.NoError(t, err)
	require.Equal(t, "typesafe", current.config.Engine)
	require.Equal(t, []string{"typesafe-key"}, current.config.apiKeys())
	require.Equal(t, 0.91, current.config.Thresholds["sexual"])
	require.Equal(t, "openai", old.config.Engine)
	require.Equal(t, []string{"openai-key"}, old.config.apiKeys())
	status, err := s.GetStatus(context.Background())
	require.NoError(t, err)
	require.Equal(t, "typesafe", status.Engine)
	require.Equal(t, scopedModerationKeyHash("typesafe-key", "typesafe"), status.APIKeyStatuses[0].KeyHash)
}
