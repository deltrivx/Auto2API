package service

// 统一积分档位表。
//
// Auto2API 的计价单位是「积分」，而 WorkBuddy 有一套官方积分计费体系，
// 因此以它为锚：同一个模型在各平台应当落到相近的积分档位，否则会出现
// 「同样跑 deepseek-v4-flash，WorkBuddy 扣 112、Marvis 只扣 1」的荒谬差距。
//
// 档位值取自 WorkBuddy 线上真实计价的统计中位数（USD/1M token 口径的积分值），
// 不是凭空拍的：
//   glm-5.3       ≈ 687
//   deepseek-v4-pro ≈ 507
//   kimi-k2.5     ≈ 129
//   deepseek-v4-flash ≈ 112
//   hy4free / hy3 ≈ 100
//   wb-lite       ≈ 29
//
// 匹配顺序自上而下，先命中先生效，越具体的模型放在越前面。

import "strings"

// creditTier 是一个积分档位。
type creditTier struct {
	name string
	// prefixes 是该档位覆盖的模型名前缀（小写）。
	prefixes []string
	// inputPer1M / outputPer1M 是每百万 token 的积分消耗。
	inputPer1M  float64
	outputPer1M float64
	// contextLimit 是上下文上限，只用于展示。
	contextLimit int64
}

// creditTiers 是全局档位表，顺序即优先级。
var creditTiers = []creditTier{
	{
		name:        "旗舰",
		prefixes:    []string{"glm-5.3", "glm-5.2", "glm-5"},
		inputPer1M:  687,
		outputPer1M: 642,
		contextLimit: 200_000,
	},
	{
		name:        "高级",
		prefixes:    []string{"deepseek-v4-pro", "deepseek-v4.1-pro", "doubao-seed-2.1-pro", "kimi-k3.1-pro", "kimi-k3"},
		inputPer1M:  507,
		outputPer1M: 494,
		contextLimit: 200_000,
	},
	{
		name:        "标准",
		prefixes:    []string{"kimi-k2.5", "kimi-k2.8", "doubao-seed-2.1-turbo", "minimax-m3", "qwen4-max", "qwen3.8-max"},
		inputPer1M:  129,
		outputPer1M: 130,
		contextLimit: 200_000,
	},
	{
		name:        "高速",
		prefixes:    []string{"deepseek-v4.1-flash", "deepseek-v4-flash", "mimo-v2.6-flash", "glm-5.3-flash", "doubao-seed-code", "qwen3.8-flash", "minimax-m3.1-flash", "dola-seed"},
		inputPer1M:  112,
		outputPer1M: 90,
		contextLimit: 200_000,
	},
	{
		name:        "轻量",
		prefixes:    []string{"hy3", "hy4", "step-", "doubao", "qwen", "minimax", "kimi-k2"},
		inputPer1M:  100,
		outputPer1M: 97,
		contextLimit: 128_000,
	},
	{
		name:        "迷你",
		prefixes:    []string{"wb-lite", "lite", "mini", "nano"},
		inputPer1M:  29,
		outputPer1M: 29,
		contextLimit: 128_000,
	},
}

// creditTierFor 按模型名找档位。返回的是每百万 token 的积分值。
//
// 未命中任何档位时返回 false，由调用方决定是否使用通用兜底。
func creditTierFor(model string) (inputPer1M, outputPer1M float64, contextLimit int64, matched bool) {
	lower := strings.ToLower(strings.TrimSpace(model))
	for _, tier := range creditTiers {
		for _, p := range tier.prefixes {
			if strings.HasPrefix(lower, p) {
				return tier.inputPer1M, tier.outputPer1M, tier.contextLimit, true
			}
		}
	}
	return 0, 0, 0, false
}

// creditTierCost 按档位算一次请求的积分成本。
//
// 缓存读取按输入档的 10% 计（与 WorkBuddy 口径一致）。
func creditTierCost(model string, tokens UsageTokens) float64 {
	inPer1M, outPer1M, _, matched := creditTierFor(model)
	if !matched {
		// 未命中档位的模型按「标准」档兜底，宁可略高也不要静默免费。
		inPer1M, outPer1M = 129, 130
	}
	input := tokens.InputTokens + tokens.ImageInputTokens + tokens.CacheCreationTokens
	cached := tokens.CacheReadTokens
	const perM = 1_000_000
	cost := float64(input)/perM*inPer1M +
		float64(cached)/perM*inPer1M*0.1 +
		float64(tokens.OutputTokens)/perM*outPer1M
	return cost
}
