package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// WorkBuddy 账号调用算法。
//
// 移植自 AutoBuddy 的 gateway/session_sticky.py 与 gateway/cooldown.py。
//
// 两件事：
//
//  1. 会话粘性（session sticky）：同一个对话应当始终落在同一账号上。
//     WorkBuddy 的账号带上下文记忆，跨账号切换会让对话失去连贯性，也会
//     因上下文不连续而被上游判定为异常。因此按「会话键」绑定账号，并在
//     TTL 内保持。
//
//  2. 冷却与熔断（cooldown / breaker）：不同错误对应不同的恢复预期，
//     统一的固定冷却要么恢复太早（继续失败），要么太晚（浪费账号）。
//     这里按错误类型分级退避，并在连续失败时熔断整个账号。

// WorkBuddy 冷却参数。
const (
	// workBuddySoftRateBase 是 429（软限流）的初始冷却时长。
	workBuddySoftRateBase = 600 * time.Second
	// workBuddySoftRateMax 是软限流指数退避的上限。
	workBuddySoftRateMax = 7200 * time.Second
	// workBuddyHardCooldownHour 是 402（配额耗尽）的解封时刻，次日该小时。
	workBuddyHardCooldownHour = 4
	// workBuddyNotFoundCooldown 是 404（模型或端点不存在）的固定冷却。
	workBuddyNotFoundCooldown = 60 * time.Second
	// workBuddyChannelCooldown 是渠道拦截（内容策略）的冷却。
	workBuddyChannelCooldown = 1800 * time.Second
	// workBuddyBreakerThreshold 是触发熔断的连续失败次数。
	workBuddyBreakerThreshold = 3
	// workBuddyBreakerBase 是熔断的初始时长。
	workBuddyBreakerBase = 1800 * time.Second
	// workBuddyBreakerMax 是熔断时长的上限。
	workBuddyBreakerMax = 21600 * time.Second
)

// WorkBuddy 会话粘性参数。
const (
	// workBuddyStickyTTL 是绑定关系的存活时长。
	workBuddyStickyTTL = 1800 * time.Second
	// workBuddyStickyMaxBindings 是绑定表容量上限，超出后按最旧淘汰。
	workBuddyStickyMaxBindings = 2000
)

// workBuddyCooldownKind 是冷却的成因分类。
type workBuddyCooldownKind string

const (
	workBuddyCooldownSoftRate  workBuddyCooldownKind = "soft_rate"  // 429
	workBuddyCooldownHardQuota workBuddyCooldownKind = "hard_quota" // 402
	workBuddyCooldownNotFound  workBuddyCooldownKind = "not_found"  // 404
	workBuddyCooldownChannel   workBuddyCooldownKind = "channel"    // 内容策略拦截
	workBuddyCooldownBreaker   workBuddyCooldownKind = "breaker"    // 连续失败熔断
)

// workBuddyCooldown 描述一个账号的冷却状态。
type workBuddyCooldown struct {
	Kind     workBuddyCooldownKind
	Until    time.Time
	Failures int    // 连续失败次数，驱动退避与熔断
	Reason   string // 上游给出的原因，便于排查
}

// workBuddyCooldownTracker 管理账号冷却状态。
//
// 与 AutoBuddy 一样保存在内存中：进程重启后所有账号立即可用，
// 这是可接受的——上游的限流窗口远长于重启间隔。
type workBuddyCooldownTracker struct {
	mu     sync.Mutex
	states map[string]*workBuddyCooldown
}

func newWorkBuddyCooldownTracker() *workBuddyCooldownTracker {
	return &workBuddyCooldownTracker{states: make(map[string]*workBuddyCooldown)}
}

// workBuddyActive 返回账号当前是否可用；冷却已过期的会被自动清除。
func (t *workBuddyCooldownTracker) workBuddyActive(accountID string, now time.Time) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[accountID]
	if !ok {
		return true
	}
	if now.After(st.Until) {
		delete(t.states, accountID)
		return true
	}
	return false
}

// workBuddyRemaining 返回账号剩余冷却时长；无冷却时返回 0。
func (t *workBuddyCooldownTracker) workBuddyRemaining(accountID string, now time.Time) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	st, ok := t.states[accountID]
	if !ok || now.After(st.Until) {
		return 0
	}
	return st.Until.Sub(now)
}

// workBuddyRecordSuccess 在请求成功后清除失败计数。
func (t *workBuddyCooldownTracker) workBuddyRecordSuccess(accountID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if st, ok := t.states[accountID]; ok && st.Kind == workBuddyCooldownBreaker {
		// 熔断期内的成功说明账号已恢复，直接清除。
		delete(t.states, accountID)
		return
	}
	delete(t.states, accountID)
}

// workBuddyRecordFailure 记录一次失败并按错误类型设置冷却。
//
// 返回本次设置的冷却时长，便于日志与监控。
func (t *workBuddyCooldownTracker) workBuddyRecordFailure(
	accountID string,
	kind workBuddyCooldownKind,
	reason string,
	now time.Time,
) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()

	st := t.states[accountID]
	if st == nil {
		st = &workBuddyCooldown{}
		t.states[accountID] = st
	}
	st.Failures++
	st.Kind = kind
	st.Reason = reason

	var d time.Duration
	switch kind {
	case workBuddyCooldownSoftRate:
		// 指数退避：base * 2^(n-1)，封顶 max。
		d = workBuddySoftRateBase
		for i := 1; i < st.Failures && d < workBuddySoftRateMax; i++ {
			d *= 2
		}
		if d > workBuddySoftRateMax {
			d = workBuddySoftRateMax
		}
	case workBuddyCooldownHardQuota:
		// 配额耗尽：等到次日固定时刻。
		next := time.Date(now.Year(), now.Month(), now.Day(),
			workBuddyHardCooldownHour, 0, 0, 0, now.Location())
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		d = next.Sub(now)
	case workBuddyCooldownNotFound:
		d = workBuddyNotFoundCooldown
	case workBuddyCooldownChannel:
		d = workBuddyChannelCooldown
	case workBuddyCooldownBreaker:
		d = workBuddyBreakerBase
		for i := 1; i < st.Failures/2 && d < workBuddyBreakerMax; i++ {
			d *= 2
		}
		if d > workBuddyBreakerMax {
			d = workBuddyBreakerMax
		}
	default:
		d = workBuddySoftRateBase
	}

	// 连续失败达到阈值时升级为熔断。
	if st.Failures >= workBuddyBreakerThreshold && kind != workBuddyCooldownBreaker &&
		kind != workBuddyCooldownHardQuota {
		st.Kind = workBuddyCooldownBreaker
		d = workBuddyBreakerBase
		if st.Failures > workBuddyBreakerThreshold {
			d = workBuddyBreakerMax
		}
	}

	st.Until = now.Add(d)
	return d
}

// workBuddyClassifyFailure 把 HTTP 状态与上游错误码归类为冷却类型。
//
// 与 AutoBuddy 的判定一致：
//   - 429 → 软限流（可退避恢复）
//   - 402 / 配额耗尽 → 硬冷却（等重置）
//   - 404 → 短冷却
//   - 内容策略拒绝 → 渠道冷却
//   - 其它 4xx/5xx → 计为失败，累计触发熔断
func workBuddyClassifyFailure(statusCode int, message string) (workBuddyCooldownKind, bool) {
	lower := strings.ToLower(message)

	switch {
	case statusCode == 429:
		return workBuddyCooldownSoftRate, true
	case statusCode == 402:
		return workBuddyCooldownHardQuota, true
	case statusCode == 404:
		return workBuddyCooldownNotFound, true
	case strings.Contains(lower, "insufficient") && strings.Contains(lower, "credit"):
		return workBuddyCooldownHardQuota, true
	case strings.Contains(lower, "quota") && strings.Contains(lower, "exceed"):
		return workBuddyCooldownHardQuota, true
	case strings.Contains(lower, "content policy"), strings.Contains(lower, "content_policy"),
		strings.Contains(lower, "sensitive"):
		return workBuddyCooldownChannel, true
	default:
		return workBuddyCooldownBreaker, false
	}
}

// ---- 会话粘性 ------------------------------------------------------------

// workBuddyStickyBinding 是一条会话→账号绑定。
type workBuddyStickyBinding struct {
	AccountID string
	BoundAt   time.Time
}

// workBuddyStickyTable 维护会话与账号的绑定关系。
type workBuddyStickyTable struct {
	mu       sync.Mutex
	bindings map[string]workBuddyStickyBinding
}

func newWorkBuddyStickyTable() *workBuddyStickyTable {
	return &workBuddyStickyTable{bindings: make(map[string]workBuddyStickyBinding)}
}

// workBuddyBound 返回会话当前绑定的账号；已过期或不存在时返回空。
func (t *workBuddyStickyTable) workBuddyBound(sessionKey string, now time.Time) string {
	if sessionKey == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	b, ok := t.bindings[sessionKey]
	if !ok {
		return ""
	}
	if now.Sub(b.BoundAt) > workBuddyStickyTTL {
		delete(t.bindings, sessionKey)
		return ""
	}
	return b.AccountID
}

// workBuddyBind 把会话绑定到账号，并在超容量时淘汰最旧绑定。
func (t *workBuddyStickyTable) workBuddyBind(sessionKey, accountID string, now time.Time) {
	if sessionKey == "" || accountID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.bindings) >= workBuddyStickyMaxBindings {
		t.workBuddyEvictOldestLocked()
	}
	t.bindings[sessionKey] = workBuddyStickyBinding{AccountID: accountID, BoundAt: now}
}

// workBuddyUnbind 解除会话绑定（账号失败时调用，让下一轮重新选）。
func (t *workBuddyStickyTable) workBuddyUnbind(sessionKey string) {
	if sessionKey == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.bindings, sessionKey)
}

// workBuddyEvictOldestLocked 淘汰最旧的绑定；调用方需持有锁。
func (t *workBuddyStickyTable) workBuddyEvictOldestLocked() {
	type kv struct {
		key string
		at  time.Time
	}
	all := make([]kv, 0, len(t.bindings))
	for k, v := range t.bindings {
		all = append(all, kv{k, v.BoundAt})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].at.Before(all[j].at) })

	// 淘汰最旧的 1/4，避免每次插入都做全量排序。
	drop := len(all) / 4
	if drop < 1 {
		drop = 1
	}
	for i := 0; i < drop && i < len(all); i++ {
		delete(t.bindings, all[i].key)
	}
}

// workBuddyDeriveSessionKey 从请求内容派生会话键。
//
// 优先使用客户端显式给出的会话标识；没有时退化为「首个用户消息 + 模型」
// 的摘要——同一段对话的后续轮次会带同样的首条消息，因而命中同一账号。
func workBuddyDeriveSessionKey(sessionHeader, model, firstUserMessage string) string {
	if s := strings.TrimSpace(sessionHeader); s != "" {
		return "h:" + s
	}
	if firstUserMessage == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("wb-session:" + model + "\x00" + firstUserMessage))
	return "d:" + hex.EncodeToString(sum[:8])
}

// ---- 账号选择 ------------------------------------------------------------

// workBuddyCandidate 是一个可调度账号。
type workBuddyCandidate struct {
	ID       string
	Priority int // 数值越小优先级越高
	Load     int // 当前并发数
	Failures int // 历史失败次数
}

// workBuddyPickAccount 从候选中挑选一个账号。
//
// 排序依据（与 AutoBuddy 的 smart 模式一致）：
//  1. 优先复用会话已绑定的账号（粘性）
//  2. 其次按优先级升序
//  3. 同优先级时按当前负载升序
//  4. 再按失败次数升序
//
// 调用方需先过滤掉冷却中的账号。
func workBuddyPickAccount(
	sessionKey string,
	candidates []workBuddyCandidate,
	sticky *workBuddyStickyTable,
	now time.Time,
) (workBuddyCandidate, bool) {
	if len(candidates) == 0 {
		return workBuddyCandidate{}, false
	}

	// 1. 粘性命中。
	if sticky != nil && sessionKey != "" {
		if bound := sticky.workBuddyBound(sessionKey, now); bound != "" {
			for _, c := range candidates {
				if c.ID == bound {
					return c, true
				}
			}
			// 绑定账号已不可用：解绑，走重新选择。
			sticky.workBuddyUnbind(sessionKey)
		}
	}

	// 2. 排序选择。
	sorted := make([]workBuddyCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Load != b.Load {
			return a.Load < b.Load
		}
		return a.Failures < b.Failures
	})

	chosen := sorted[0]
	if sticky != nil && sessionKey != "" {
		sticky.workBuddyBind(sessionKey, chosen.ID, now)
	}
	return chosen, true
}

// workBuddySessionKeyFromHeaders 从请求头取会话标识。
func workBuddySessionKeyFromHeaders(h map[string]string) string {
	for _, k := range []string{
		workBuddySessionHeader,
		"X-Session-Id",
		"Session-Id",
		"X-Conversation-Id",
	} {
		if v := strings.TrimSpace(h[k]); v != "" {
			return v
		}
	}
	return ""
}

// String 便于日志输出。
func (c workBuddyCooldownKind) String() string { return string(c) }

// workBuddyCooldownSummary 汇总所有账号的冷却状态，供监控与排查使用。
func (t *workBuddyCooldownTracker) workBuddyCooldownSummary(now time.Time) map[string]string {
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[string]string, len(t.states))
	for id, st := range t.states {
		if now.After(st.Until) {
			continue
		}
		out[id] = fmt.Sprintf("%s until %s (%s)",
			st.Kind, st.Until.Format(time.RFC3339), st.Reason)
	}
	return out
}
