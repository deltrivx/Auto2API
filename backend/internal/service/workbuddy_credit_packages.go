package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// WorkBuddy 积分包明细。
//
// 上游 get-user-resource-summary 只给包代码与余量，没有到期时间。
// 包的名称、周期与到期来自 get-user-resource-free-packages —— 该接口
// 需要先拿到 PackageCodes，因此是两步：先 summary 取代码，再查详情。
//
// 这个接口是 magpie 用到的（/billing/meter/get-user-resource-free-packages），
// AutoBuddy 的面板上「64.25 积分 平台奖励积分 11/21 到期」正是这个来源。

// WorkBuddyCreditPackage 是一个积分包的明细。
//
// 与 AutoBuddy 的「全部积分包」弹窗对齐：每个包单独展示名称、到期日、
// 剩余/总量、已用量与使用进度条——上游按批次发放，同一类型的包可能有
// 几十个且到期日各不相同，只看汇总会丢失这些信息。
type WorkBuddyCreditPackage struct {
	PackageCode string  `json:"package_code"`
	Name        string  `json:"name"`
	Remain      float64 `json:"remain"`
	Total       float64 `json:"total"`
	Used        float64 `json:"used"`
	// ExpireAt 是包的周期结束时间；零值表示不限期。
	ExpireAt *time.Time `json:"expire_at"`
	// ExpireDate 是展示用的到期日期（YYYY-MM-DD），空表示不限期。
	ExpireDate string `json:"expire_date"`
	// DaysLeft 是距到期的天数，负数表示已过期。
	DaysLeft int `json:"days_left"`
	// Source 是包来源（赠送/购买/活动）。
	Source string `json:"source"`
	// UsePercent 是已使用百分比（0-100），供进度条使用。
	UsePercent float64 `json:"use_percent"`
}

// workBuddyRawPackageDetail 是详情接口的一行。
// workBuddyRawPackageDetail 是详情接口的一行。
//
// 字段名以实测为准：总量叫 CapacitySize（不是 Capacity），
// 另有 *Precise 后缀的精确值，优先用精确值。
type workBuddyRawPackageDetail struct {
	PackageCode string `json:"PackageCode"`
	PackageName string `json:"PackageName"`
	// 容量字段：上游用 Cycle* 系列表示「本周期」的额度，
	// 不带 Cycle 的 CapacitySize/CapacityUsed 实测恒为 0。
	// 类型也不统一（数字与字符串混用），统一用 RawMessage 接收再解析。
	CycleCapacitySize          json.RawMessage `json:"CycleCapacitySize"`
	CycleCapacityRemain        json.RawMessage `json:"CycleCapacityRemain"`
	CycleCapacityUsed          json.RawMessage `json:"CycleCapacityUsed"`
	CycleCapacitySizePrecise   json.RawMessage `json:"CycleCapacitySizePrecise"`
	CycleCapacityRemainPrecise json.RawMessage `json:"CycleCapacityRemainPrecise"`
	CycleCapacityUsedPrecise   json.RawMessage `json:"CycleCapacityUsedPrecise"`
	// 兜底字段（部分包只填这两项）。
	CapacityRemain   json.RawMessage `json:"CapacityRemain"`
	CapacitySize     json.RawMessage `json:"CapacitySize"`
	CycleEndTime     string          `json:"CycleEndTime"`
	DeductionEndTime int64           `json:"DeductionEndTime"`
	ExpiredTime      json.RawMessage `json:"ExpiredTime"`
	SubProductName   string          `json:"SubProductName"`
	FeeType          int             `json:"FeeType"`
}

// WorkBuddyCreditPackagesOf 返回账号的积分包明细（含到期）。
func WorkBuddyCreditPackagesOf(ctx context.Context, account *Account) ([]WorkBuddyCreditPackage, error) {
	// 第一步：summary 拿包代码。这里直接请求原始响应——内部 WorkBuddyCredits
	// 结构只保留聚合后的分组，丢掉了 PackageCode，而详情接口需要它。
	rawSummary, err := workBuddyBillingPost(ctx, account,
		"/billing/meter/get-user-resource-summary", []byte("{}"))
	if err != nil {
		return nil, err
	}
	var summaryEnv struct {
		Data struct {
			Packages     []workBuddyRawPackageResp `json:"Packages"`
			CreditGroups []workBuddyRawPackageResp `json:"CreditGroups"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rawSummary, &summaryEnv); err != nil {
		return nil, err
	}
	// Packages 才带 PackageCode；CreditGroups 只有 Kind/Name，是聚合视图。
	// 因此取 PackageCode 必须用 Packages，缺失时再退回 CreditGroups。
	rawPkgs := summaryEnv.Data.Packages
	if len(rawPkgs) == 0 {
		rawPkgs = summaryEnv.Data.CreditGroups
	}
	codes := make([]string, 0, len(rawPkgs))
	fallback := map[string]workBuddyRawPackageResp{}
	for _, g := range rawPkgs {
		if g.PackageCode != "" {
			codes = append(codes, g.PackageCode)
			fallback[g.PackageCode] = g
		}
	}
	if len(codes) == 0 {
		// 上游没给包代码时退回聚合分组（CreditGroups），至少展示余量。
		fallbackOut := make([]WorkBuddyCreditPackage, 0, len(summaryEnv.Data.CreditGroups))
		for _, g := range summaryEnv.Data.CreditGroups {
			fallbackOut = append(fallbackOut, WorkBuddyCreditPackage{
				Name:   firstNonEmptyStr(g.PackageName, g.Name),
				Remain: parseCreditFloat(g.CycleRemainCapacity),
				Total:  parseCreditFloat(g.CycleTotalCapacity),
				Used:   parseCreditFloat(g.CycleUsedCapacity),
			})
		}
		return fallbackOut, nil
	}

	// 逐个包查询，而不是一次传全部代码。
	//
	// 实测：一次传 3 个代码时，pageSize 的额度会被其中一个包占满
	// （该包有 49 条发放记录），后面的包直接被截断，导致「上游有名称却
	// 显示未命名」。逐包查询才能拿到每个包的完整明细。
	now := time.Now()
	byCode := map[string][]workBuddyRawPackageDetail{}
	for _, code := range codes {
		body, _ := json.Marshal(map[string]any{
			"PackageCodes": []string{code},
			"pageNumber":   1,
			"pageSize":     100,
		})
		raw, err := workBuddyBillingPost(ctx, account, "/billing/meter/get-user-resource-free-packages", body)
		if err != nil {
			continue
		}
		var resp struct {
			Data struct {
				Accounts []workBuddyRawPackageDetail `json:"Accounts"`
			} `json:"data"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			continue
		}
		byCode[code] = resp.Data.Accounts
	}

	out := make([]WorkBuddyCreditPackage, 0, len(codes))
	for _, code := range codes {
		details := byCode[code]
		pkg := WorkBuddyCreditPackage{PackageCode: code}
		// 同一 PackageCode 可能有多条（多个发放批次），取最晚到期的那条作为
		// 该包的到期时间——运营者关心的是「这堆积分最晚什么时候用完」。
		for _, d := range details {
			// 精确值优先：上游同时给整数与浮点两套字段，且都是字符串。
			remain := workBuddyNum(d.CycleCapacityRemainPrecise)
			if remain == 0 {
				remain = workBuddyNum(d.CycleCapacityRemain)
			}
			if remain == 0 {
				remain = workBuddyNum(d.CapacityRemain)
			}
			total := workBuddyNum(d.CycleCapacitySizePrecise)
			if total == 0 {
				total = workBuddyNum(d.CycleCapacitySize)
			}
			if total == 0 {
				total = workBuddyNum(d.CapacitySize)
			}
			// 只累加总量与剩余，已用最后由「总量 - 剩余」统一算出。
			// 上游的 CycleCapacityUsed 是按批次各自记的，逐条累加会超过总量。
			pkg.Remain += remain
			pkg.Total += total
			if pkg.Name == "" {
				pkg.Name = d.PackageName
			}
			if pkg.Source == "" {
				pkg.Source = d.SubProductName
			}
			if n := int64(workBuddyNum(d.ExpiredTime)); n > 0 {
				// 时间戳可能是毫秒或秒。
				var t time.Time
				if n > 1e12 {
					t = time.UnixMilli(n)
				} else {
					t = time.Unix(n, 0)
				}
				if pkg.ExpireAt == nil || t.After(*pkg.ExpireAt) {
					pkg.ExpireAt = &t
				}
			} else if d.DeductionEndTime > 0 {
				t := time.UnixMilli(d.DeductionEndTime)
				if pkg.ExpireAt == nil || t.After(*pkg.ExpireAt) {
					pkg.ExpireAt = &t
				}
			} else if d.CycleEndTime != "" {
				if t, perr := time.ParseInLocation("2006-01-02 15:04:05", d.CycleEndTime, time.Local); perr == nil {
					if pkg.ExpireAt == nil || t.After(*pkg.ExpireAt) {
						pkg.ExpireAt = &t
					}
				}
			}
		}
		if pkg.ExpireAt != nil {
			pkg.DaysLeft = int(pkg.ExpireAt.Sub(now).Hours() / 24)
			pkg.ExpireDate = pkg.ExpireAt.Format("2006-01-02")
		}
		// 名称/余量缺失时用 summary 的原始值兜底。
		if fb, ok := fallback[code]; ok {
			if pkg.Name == "" {
				pkg.Name = fb.PackageName
				if pkg.Name == "" {
					pkg.Name = fb.Name
				}
			}
			if pkg.Remain == 0 {
				pkg.Remain = parseCreditFloat(fb.CycleRemainCapacity)
			}
			if pkg.Total == 0 {
				pkg.Total = parseCreditFloat(fb.CycleTotalCapacity)
			}
		}
		// 包名统一转中文：国际站返回英文包名。
		pkg.Name = workBuddyPackageNameCN(pkg.Name)
		// 已用量一律用「总量 - 剩余」推算，且必须放在所有兜底赋值之后，
		// 否则会被 fallback 里的原始值覆盖。
		pkg.Used = pkg.Total - pkg.Remain
		if pkg.Used < 0 {
			pkg.Used = 0
		}
		if pkg.Total > 0 {
			pkg.UsePercent = pkg.Used / pkg.Total * 100
			if pkg.UsePercent > 100 {
				pkg.UsePercent = 100
			}
			if pkg.UsePercent < 0 {
				pkg.UsePercent = 0
			}
		}
		out = append(out, pkg)
	}

	// 排序：有到期日的按到期时间升序（最近到期在前，便于优先消耗），
	// 无到期日的排在最后。
	sort.SliceStable(out, func(i, j int) bool {
		ei, ej := out[i].ExpireAt, out[j].ExpireAt
		if ei != nil && ej != nil {
			return ei.Before(*ej)
		}
		if ei != nil {
			return true
		}
		if ej != nil {
			return false
		}
		return out[i].Remain > out[j].Remain
	})
	return out, nil
}

// workBuddyBillingPost 向 billing 接口 POST 并返回原始响应。
func workBuddyBillingPost(ctx context.Context, account *Account, path string, body []byte) ([]byte, error) {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, site.Endpoint+path, bytesReader(body))
	if err != nil {
		return nil, err
	}
	token := account.workBuddyTokenOf()
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", workBuddySiteDomain(site))
	req.Header.Set("User-Agent", "WorkBuddy/5.9.3 workbuddy/5.9.3 CLI/2.165.0")

	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

// bytesReader 把字节切片包装成 io.Reader（避免额外导入 bytes）。
func bytesReader(b []byte) io.Reader {
	return &byteSliceReader{data: b}
}

type byteSliceReader struct {
	data []byte
	pos  int
}

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}

// workBuddyRawPackageResp 是 summary 响应里的一个包。
type workBuddyRawPackageResp struct {
	PackageCode         string `json:"PackageCode"`
	PackageName         string `json:"PackageName"`
	Name                string `json:"Name"`
	CycleTotalCapacity  string `json:"CycleTotalCapacity"`
	CycleRemainCapacity string `json:"CycleRemainCapacity"`
	CycleUsedCapacity   string `json:"CycleUsedCapacity"`
}

// firstNonEmptyStr 返回第一个非空字符串。
func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// workBuddyNum 把 JSON 里可能是数字、也可能是字符串的数值统一解析为 float64。
//
// 上游同一接口的不同字段类型不一致（CapacitySize 是字符串、CapacityRemain
// 是数字），逐字段猜类型不可靠，统一用这个函数解析。
func workBuddyNum(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return parseCreditFloat(str)
	}
	return 0
}

// workBuddyPackageNameCN 把上游积分包名映射为中文。
//
// 国际站返回英文包名（Free Plan Subscription、Bonus Pack 等），
// 国内站偶有英文混入；界面统一显示中文。未匹配的原样返回。
func workBuddyPackageNameCN(name string) string {
	if name == "" {
		return name
	}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "free plan") && strings.Contains(lower, "subscription"):
		return "免费套餐订阅"
	case strings.Contains(lower, "free plan"):
		return "免费套餐"
	case strings.Contains(lower, "pro plan") || strings.Contains(lower, "pro subscription"):
		return "专业版套餐"
	case strings.Contains(lower, "team plan"):
		return "团队版套餐"
	case strings.Contains(lower, "bonus pack") || strings.Contains(lower, "bonus credits"):
		return "赠送包"
	case strings.Contains(lower, "top-up") || strings.Contains(lower, "topup") || strings.Contains(lower, "top up"):
		return "充值包"
	case strings.Contains(lower, "reward"):
		return "奖励包"
	case strings.Contains(lower, "trial"):
		return "试用包"
	case strings.Contains(lower, "gift"):
		return "礼包"
	}
	return name
}
