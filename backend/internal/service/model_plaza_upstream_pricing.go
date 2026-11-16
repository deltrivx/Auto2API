package service

import (
	"context"
	"sync"
	"time"

	"github.com/DeltrivX/Auto2API/internal/pkg/pagination"
)

// WorkBuddy 的模型价格只存在于上游 /v3/config 的 credits 字段里。
//
// 这些模型（auto、balanced-model、codewise-* 等）是 WorkBuddy 的私有路由
// 模型，通用价格库没有它们的条目，面板里也没有可维护的官方价。把价格写进
// 数据库只会变成一份会过期的副本，所以广场每次组装数据时向上游现取，
// 并用一个短 TTL 缓存挡住重复请求。

// upstreamPricingTTL 是上游价格缓存的有效期。
//
// 上游不常调价，但用户刷新广场时希望看到实时值，取 5 分钟：
// 既避免每次请求都打上游，又不会让人看到隔夜的价格。
const upstreamPricingTTL = 5 * time.Minute

// upstreamPricingEntry 是单个平台的缓存条目。
type upstreamPricingEntry struct {
	rates    map[string]float64
	fetchedAt time.Time
}

// upstreamPricingCache 按平台缓存上游模型价格（模型 ID → 积分倍率）。
type upstreamPricingCache struct {
	mu      sync.RWMutex
	entries map[string]upstreamPricingEntry
	// inflight 防止同一平台并发触发多次上游请求。
	inflight map[string]*sync.WaitGroup
}

func newUpstreamPricingCache() *upstreamPricingCache {
	return &upstreamPricingCache{
		entries:  make(map[string]upstreamPricingEntry),
		inflight: make(map[string]*sync.WaitGroup),
	}
}

// get 返回缓存的价格表；缓存过期返回 nil。
func (c *upstreamPricingCache) get(platform string) map[string]float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[platform]
	if !ok || time.Since(entry.fetchedAt) > upstreamPricingTTL {
		return nil
	}
	return entry.rates
}

// put 写入缓存。
func (c *upstreamPricingCache) put(platform string, rates map[string]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[platform] = upstreamPricingEntry{rates: rates, fetchedAt: time.Now()}
}

// workBuddyUpstreamRates 返回某平台的上游官方积分倍率（模型 ID → 倍率）。
//
// 取不到时返回 nil，调用方继续走原有的价格回落链——广场宁可显示
// 「未标价」也不要展示一个编造的数字。
func (s *ModelPlazaService) workBuddyUpstreamRates(ctx context.Context, platform string) map[string]float64 {
	if !isWorkBuddyPlatform(platform) || s.accountRepo == nil {
		return nil
	}
	if cached := s.upstreamPricing.get(platform); cached != nil {
		return cached
	}

	account, err := s.activeAccountForPlatform(ctx, platform)
	if err != nil || account == nil {
		return nil
	}

	svc := &AccountTestService{}
	items, err := svc.FetchWorkBuddyModelPricing(ctx, account)
	if err != nil || len(items) == 0 {
		return nil
	}

	rates := make(map[string]float64, len(items))
	for _, it := range items {
		if it.HasRate {
			rates[it.ID] = it.Credits
		}
	}
	s.upstreamPricing.put(platform, rates)
	return rates
}

// activeAccountForPlatform 取一个该平台的可用账号，用于向上游查询。
func (s *ModelPlazaService) activeAccountForPlatform(ctx context.Context, platform string) (*Account, error) {
	params := pagination.PaginationParams{Page: 1, PageSize: 1}
	accounts, _, err := s.accountRepo.ListWithFilters(ctx, params, platform, "", StatusActive, "", 0, "")
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, nil
	}
	return &accounts[0], nil
}

// applyUpstreamRates 用上游倍率填充渠道里未标价的模型。
//
// 上游只给「积分倍率」，不是一个完整的输入/输出价格表，所以这里把它写成
// 按次计价的单价：每次调用消耗 rate 积分。有渠道自有价格的模型不动——
// 渠道配置是运营者显式设定的，优先级高于上游挂牌价。
func applyUpstreamRates(models []SupportedModel, rates map[string]float64) {
	if len(rates) == 0 {
		return
	}
	for i := range models {
		if !pricingNeedsFallback(models[i].Pricing) {
			continue
		}
		rate, ok := rates[models[i].Name]
		if !ok {
			continue
		}
		p := &ChannelModelPricing{
			Platform:        models[i].Pricing.Platform,
			Models:          append([]string(nil), models[i].Pricing.Models...),
			BillingMode:     BillingModePerRequest,
			PerRequestPrice: floatPtr(rate),
		}
		models[i].Pricing = p
	}
}

// floatPtr 返回 v 的指针。
func floatPtr(v float64) *float64 { return &v }
