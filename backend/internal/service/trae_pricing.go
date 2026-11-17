package service

import "strings"

// Trae 模型系列的兜底定价。
//
// Trae 的模型目录有 90+ 个条目，且随时会变，逐个写进价格表既维护不动也很快过期。
// 这里按模型系列做前缀匹配给一组保底价，保证任何 Trae 模型都能算出非零成本——
// 否则 actual_cost 恒为 0，仪表盘按平台拆分会把整个 Trae 平台过滤掉。
//
// 价格单位是 USD / token，量级参考各系列的公开定价（每百万 token 的常见档位）：
//
//	flash / mini / lite 档 ≈ $0.1~0.3 / 1M
//	pro / max / turbo 档  ≈ $1~3 / 1M
//	code 系列             ≈ $0.5 / 1M
//
// 真实扣费以 Trae 钱包为准，这里只用于本地记账与展示。
type traeSeriesPrice struct {
	prefix       string
	inputPerM    float64
	outputPerM   float64
	contextLimit int64
}

// traeSeriesPrices 按匹配顺序排列：越靠前的规则越具体，先命中先生效。
var traeSeriesPrices = []traeSeriesPrice{
	// Doubao / 豆包
	{"doubao-seed-2.1-turbo", 0.35, 1.40, 256_000},
	{"doubao-seed-2.1-pro", 0.55, 2.20, 256_000},
	{"doubao-seed-code", 0.28, 1.10, 256_000},
	{"doubao-seed-evolving", 0.30, 1.20, 256_000},
	{"doubao", 0.30, 1.20, 128_000},

	// GLM / 智谱
	{"glm-5.3-flashx", 0.15, 0.60, 200_000},
	{"glm-5.3-flash", 0.10, 0.40, 200_000},
	{"glm-5", 0.60, 2.20, 200_000},
	{"glm-4", 0.35, 1.40, 128_000},

	// DeepSeek
	{"deepseek-v4-pro", 0.55, 2.19, 200_000},
	{"deepseek-v4-flash", 0.14, 0.55, 200_000},
	{"deepseek-v4.1-flash", 0.14, 0.55, 200_000},

	// Kimi / 月之暗面
	{"kimi-k3.1-pro", 1.20, 4.80, 256_000},
	{"kimi-k3", 1.00, 4.00, 256_000},
	{"kimi-k2", 0.55, 2.20, 200_000},

	// MiniMax
	{"minimax-m3", 0.30, 1.20, 200_000},
	{"minimax-m2", 0.25, 1.00, 200_000},

	// 阶跃 / 通义
	{"step-", 0.35, 1.40, 128_000},
	{"qwen", 0.35, 1.40, 128_000},

	// 通用兜底：任何未匹配的 Trae 模型都按中档价记账，
	// 保证成本不为 0，宁可略高不要静默丢单。
	{"", 0.50, 2.00, 128_000},
}

// traeModelSeriesPricing 返回模型对应的兜底价格（US D per token）。
//
// 第二个返回值表示是否命中某个具体系列（而不是最后那条通用兜底），
// 供调用方在需要区分「精确定价」与「粗略兜底」时使用。
func traeModelSeriesPricing(model string) (inputPerToken, outputPerToken float64, contextLimit int64, matched bool) {
	lower := strings.ToLower(strings.TrimSpace(model))
	for i, rule := range traeSeriesPrices {
		if rule.prefix == "" || strings.HasPrefix(lower, rule.prefix) {
			return rule.inputPerM / 1_000_000,
				rule.outputPerM / 1_000_000,
				rule.contextLimit,
				rule.prefix != ""
		}
	}
	return 0, 0, 0, false
}
