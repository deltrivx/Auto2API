package service

import (
	"testing"
)

func TestWorkBuddySchedulingCompat(t *testing.T) {
	a := &Account{
		ID:       1,
		Platform: PlatformWorkBuddyCN,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"access_token": "test",
			"uid":          "u1",
		},
		Schedulable: true,
	}
	t.Logf("Platform=%s", a.Platform)
	t.Logf("IsOpenAICompatible=%v", a.IsOpenAICompatible())
	t.Logf("IsSchedulable=%v", a.IsSchedulable())
	t.Logf("NormalizePlatform=%v", NormalizeOpenAICompatiblePlatform(a.Platform))
	t.Logf("UsesOpenAIGateway=%v", a.IsOpenAICompatible())
	t.Logf("isModelMappingSupported=%v", a.isModelMappingSupported("glm-5.3-flash"))
}
