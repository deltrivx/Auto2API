package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// WorkBuddy 账号积分查询。
//
// 移植自 sub2api 的 workbuddy_credits.go。
//
// WorkBuddy 按积分结算，上游已经把余额算好，网关只负责读取展示——不做
// 「token × 单价」的换算，那样必然与真实扣费对不上。
//
// 数据源：POST {endpoint}/billing/meter/get-user-resource-summary
// （计量域，无 /v2 前缀）。国内站与国际站共用同一接口，仅基域不同。

// WorkBuddyCreditGroup 是一个积分分组（套餐基础 / 平台奖励 / 其他）。
type WorkBuddyCreditGroup struct {
	Kind   string  `json:"kind"`
	Name   string  `json:"name"`
	Total  float64 `json:"total"`
	Remain float64 `json:"remain"`
	Used   float64 `json:"used"`
	Frozen float64 `json:"frozen"`
}

// WorkBuddyCredits 是账号的积分概况。
type WorkBuddyCredits struct {
	AccountID   int64                  `json:"account_id"`
	Account     string                 `json:"account"`
	Platform    string                 `json:"platform"`
	Total       float64                `json:"total"`
	Groups      []WorkBuddyCreditGroup `json:"groups"`
	PackageName string                 `json:"package_name,omitempty"`
	IsPaidUser  bool                   `json:"is_paid_user"`
	// CreditsInfoAvailable 为 false 表示本次未读到积分（凭据失效等）。
	CreditsInfoAvailable bool   `json:"credits_info_available"`
	Error                string `json:"error,omitempty"`
}

// workBuddyCreditRawGroup 是上游 CreditGroups / Packages 的一项。
type workBuddyCreditRawGroup struct {
	Kind                string `json:"Kind"`
	Name                string `json:"Name"`
	PackageCode         string `json:"PackageCode"`
	CycleTotalCapacity  string `json:"CycleTotalCapacity"`
	CycleRemainCapacity string `json:"CycleRemainCapacity"`
	CycleUsedCapacity   string `json:"CycleUsedCapacity"`
	CycleFrozenCapacity string `json:"CycleFrozenCapacity"`
}

// parseWorkBuddyCredits 解析上游响应的积分字段。
//
// 顶层与 data 内都可能承载数据（国际版把明细放在 data 下，国内版顶层也
// 有一份）。取非零者，避免任一侧为空就误判为无积分。
func parseWorkBuddyCredits(body []byte) (*WorkBuddyCredits, error) {
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			TotalCredits string                    `json:"TotalCredits"`
			PackageName  string                    `json:"SubscriptionPackageName"`
			IsPaidUser   bool                      `json:"IsPaidUser"`
			CreditGroups []workBuddyCreditRawGroup `json:"CreditGroups"`
			Packages     []workBuddyCreditRawGroup `json:"Packages"`
		} `json:"data"`
		TotalCredits string `json:"TotalCredits"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("积分响应解析失败: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("上游返回 code=%d: %s", resp.Code, resp.Msg)
	}

	out := &WorkBuddyCredits{
		Total:       parseCreditFloat(resp.Data.TotalCredits),
		IsPaidUser:  resp.Data.IsPaidUser,
		PackageName: strings.TrimSpace(resp.Data.PackageName),
	}
	if out.Total == 0 {
		out.Total = parseCreditFloat(resp.TotalCredits)
	}

	// CreditGroups 优先；缺失时用 Packages（两者的容量字段名一致）。
	raw := resp.Data.CreditGroups
	if len(raw) == 0 {
		raw = resp.Data.Packages
	}
	for _, g := range raw {
		name := strings.TrimSpace(g.Name)
		if name == "" {
			name = strings.TrimSpace(g.PackageCode)
		}
		kind := strings.TrimSpace(g.Kind)
		out.Groups = append(out.Groups, WorkBuddyCreditGroup{
			Kind: kind,
			// 统一显示中文：国际站返回的是英文包名。
			Name:   workBuddyCreditGroupNameCN(kind, name),
			Total:  parseCreditFloat(g.CycleTotalCapacity),
			Remain: parseCreditFloat(g.CycleRemainCapacity),
			Used:   parseCreditFloat(g.CycleUsedCapacity),
			Frozen: parseCreditFloat(g.CycleFrozenCapacity),
		})
	}

	// 总量缺失时用各分组剩余之和兜底。
	if out.Total == 0 {
		var sum float64
		for _, g := range out.Groups {
			sum += g.Remain
		}
		out.Total = sum
	}
	out.CreditsInfoAvailable = true
	return out, nil
}

// parseCreditFloat 解析上游以字符串返回的积分值（"3821.52000006"）。
func parseCreditFloat(raw string) float64 {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return v
}

// FetchCredits 读取账号的积分余额。
func (s *WorkBuddyOAuthService) FetchCredits(ctx context.Context, account *Account) *WorkBuddyCredits {
	out := &WorkBuddyCredits{}
	if account == nil {
		out.Error = "账号不存在"
		return out
	}
	out.AccountID = account.ID
	out.Account = account.Name
	out.Platform = account.Platform

	if !isWorkBuddyPlatform(account.Platform) {
		out.Error = "该平台不支持积分查询"
		return out
	}

	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		out.Error = "未知站点"
		return out
	}

	token := account.workBuddyTokenOf()
	if strings.TrimSpace(token.Access) == "" {
		out.Error = "账号缺少 access token"
		return out
	}

	url := strings.TrimRight(site.Endpoint, "/") + "/billing/meter/get-user-resource-summary"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte("{}")))
	if err != nil {
		out.Error = err.Error()
		return out
	}
	applyWorkBuddyHeaders(account, req.Header)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		out.Error = "读取积分失败: " + err.Error()
		return out
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		out.Error = fmt.Sprintf("读取积分失败（HTTP %d）: %s", resp.StatusCode, truncateRunes(string(body), 160))
		return out
	}

	parsed, err := parseWorkBuddyCredits(body)
	if err != nil {
		out.Error = err.Error()
		return out
	}
	parsed.AccountID = out.AccountID
	parsed.Account = out.Account
	parsed.Platform = out.Platform
	return parsed
}

// truncateRunes 按字符截断，避免把超长错误体整个塞进响应。
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// WorkBuddyCreditsOf 是包级积分获取入口，供没有 OAuthService 实例的
// 调用方（如后台同步服务）使用。
func WorkBuddyCreditsOf(ctx context.Context, account *Account) (*WorkBuddyCredits, error) {
	raw, err := workBuddyBillingPost(ctx, account, "/billing/meter/get-user-resource-summary", []byte("{}"))
	if err != nil {
		return nil, err
	}
	out, err := parseWorkBuddyCredits(raw)
	if err != nil {
		return nil, err
	}
	if out.AccountID == 0 {
		out.AccountID = account.ID
		out.Account = account.Name
		out.Platform = account.Platform
	}
	return out, nil
}

// workBuddyCreditGroupNameCN 把上游积分组名称映射为中文。
//
// 国际站返回英文名（Plan Base Credits 等），国内站偶尔也有英文；
// 界面统一显示中文，避免中英混排。未知名称原样返回。
func workBuddyCreditGroupNameCN(kind, name string) string {
	// 优先按 Kind 映射：Kind 是稳定的枚举，不受上游文案变化影响。
	switch strings.ToLower(kind) {
	case "plan_base":
		return "套餐基础积分"
	case "plan_bonus":
		return "套餐赠送积分"
	case "addon", "top_up", "topup":
		return "充值积分"
	case "benefit", "activity":
		return "平台奖励积分"
	case "gift":
		return "赠送积分"
	case "refund":
		return "退款返还积分"
	}
	// Kind 缺失时按名称关键词兜底。
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "base"):
		return "套餐基础积分"
	case strings.Contains(lower, "bonus"):
		return "套餐赠送积分"
	case strings.Contains(lower, "top") && strings.Contains(lower, "up"):
		return "充值积分"
	case strings.Contains(lower, "reward"):
		return "平台奖励积分"
	case strings.Contains(lower, "gift"):
		return "赠送积分"
	}
	return name
}
