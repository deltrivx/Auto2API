// Package errcode 把上游错误归到固定几类，供日志与指标统一使用。
//
// 之前 auth 过期、风控、限流、参数错误、上游故障混在一起，只能靠人工翻日志区分。
// 这里只做归类与计数，不改变任何请求行为。
package errcode

import (
	"strings"
	"sync"
)

// Class 是错误类别。
type Class string

const (
	// AuthExpired 登录态或凭据失效，需要重新授权。
	AuthExpired Class = "auth_expired"
	// RiskBlocked 被上游风控拦截（如 Marvis 4100404）。
	RiskBlocked Class = "risk_blocked"
	// RateLimited 触发限流或额度耗尽。
	RateLimited Class = "rate_limited"
	// BadRequest 请求参数或模型不受支持。
	BadRequest Class = "bad_request"
	// UpstreamError 上游 5xx。
	UpstreamError Class = "upstream_error"
	// Unknown 无法归类。
	Unknown Class = "unknown"
)

type marker struct {
	class Class
	subs  []string
}

// markers 按优先级排列：风控要先于鉴权判断，否则 403 + 4100404 会被误归为鉴权失败。
var markers = []marker{
	{RiskBlocked, []string{"4100404", "risk_control", "risk control", "风控"}},
	{AuthExpired, []string{"4100403", "invalid_grant", "token_expired", "token expired", "登录态过期", "invalid api key", "unauthorized"}},
	{RateLimited, []string{"rate limit", "rate_limit", "too many requests", "quota exceeded", "额度"}},
	{BadRequest, []string{"invalid_request", "invalid request", "param is invalid", "bad request", "model not found"}},
}

// Classify 依据状态码与响应体归类。
func Classify(status int, body string) Class {
	b := strings.ToLower(body)
	for _, m := range markers {
		for _, s := range m.subs {
			if strings.Contains(b, s) {
				return m.class
			}
		}
	}
	switch {
	case status == 401 || status == 403:
		return AuthExpired
	case status == 429:
		return RateLimited
	case status >= 400 && status < 500:
		return BadRequest
	case status >= 500:
		return UpstreamError
	}
	return Unknown
}

var (
	mu       sync.Mutex
	counters = map[string]map[string]int64{}
)

// Observe 记录一次上游错误，按平台与类别累计。
func Observe(platform string, status int, body string) {
	class := Classify(status, body)
	if platform == "" {
		platform = "unknown"
	}
	mu.Lock()
	if counters[platform] == nil {
		counters[platform] = map[string]int64{}
	}
	counters[platform][string(class)]++
	mu.Unlock()
}

// Snapshot 返回当前计数的副本：平台 -> 类别 -> 次数。
func Snapshot() map[string]map[string]int64 {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]map[string]int64, len(counters))
	for p, classes := range counters {
		cp := make(map[string]int64, len(classes))
		for c, n := range classes {
			cp[c] = n
		}
		out[p] = cp
	}
	return out
}
