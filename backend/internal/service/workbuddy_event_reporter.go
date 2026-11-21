package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

// WorkBuddy 行为事件上报。
//
// 上游的成长任务靠 /v2/report 上报行为事件累加进度，而不是「客户端真的
// 被点了」。事件信封有严格的域划分——桌面事件与 web 事件用**不同的
// machineId 与 ideType**，混用会被上游当作无效上报（返回 OK 但不计数）。
//
// 信封结构（实测自参考实现）：
//
//	{"common": {"userId","userNickname","ideName","ideType",
//	            "machineId","mode","userAgent","os","timezone"},
//	 "events": [{...单条事件...}]}
//
// 两条经验写在参考实现的注释里，这里照做：
//   - 时间戳单调递增且带抖动：同毫秒批量落库会被识别为机器；
//   - 抖动压在 1 秒内：签到链有超时窗口。

// workBuddyReportDomain 是上报使用的设备域。
type workBuddyReportDomain string

const (
	// workBuddyDomainDesktop 是桌面客户端域，用于大部分成长任务。
	workBuddyDomainDesktop workBuddyReportDomain = "desktop"
	// workBuddyDomainWeb 是 web 端域，用于资料库等页面点击类任务。
	// 该域必须使用指纹里的 webMachineId，否则事件不计数。
	workBuddyDomainWeb workBuddyReportDomain = "web"
	// workBuddyDomainChat 是智能体会话域，用于聊天类任务。
	workBuddyDomainChat workBuddyReportDomain = "web-Agents"
)

// workBuddyDomainProfile 是一个域的信封字段。
type workBuddyDomainProfile struct {
	MachineID string
	IDE       string // ideName
	IDEType   string // ideType
	UA        string
	OS        string
	OSVersion string
	Arch      string
}

// workBuddyDomainProfileOf 取账号在某个域下的信封字段。
//
// 全部来自账号自身的指纹，保证同一账号在各接口上是同一台设备，
// 但桌面域与 web 域用各自的 machineId（上游按域分别记账）。
func workBuddyDomainProfileOf(account *Account, fp workBuddyFingerprint, domain workBuddyReportDomain) workBuddyDomainProfile {
	if domain == workBuddyDomainChat {
		return workBuddyChatDomainProfile(account, fp)
	}
	if domain == workBuddyDomainWeb {
		mid := fp.WebMachineID
		if mid == "" {
			mid = fp.MachineID
		}
		return workBuddyDomainProfile{
			MachineID: mid,
			IDE:       "web",
			IDEType:   "web",
			UA:        fp.UA,
			OS:        fp.OS,
			OSVersion: fp.OSVersion,
			Arch:      fp.Arch,
		}
	}
	return workBuddyDomainProfile{
		MachineID: fp.MachineID,
		IDE:       "WorkBuddy",
		IDEType:   "WorkBuddy",
		UA:        workBuddyReportUserAgent,
		OS:        fp.OS,
		OSVersion: fp.OSVersion,
		Arch:      fp.Arch,
	}
}

// 事件抖动区间（毫秒）。
const (
	workBuddyEventDriftMinMS = 100
	workBuddyEventDriftMaxMS = 800
	workBuddyReportDelayMax  = 300
)

// workBuddyReportUserAgent 是桌面域上报使用的 UA。
const workBuddyReportUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) " +
	"AppleWebKit/537.36 (KHTML, like Gecko) WorkBuddy/5.9.3"

// workBuddyReportEvent 是一条待上报的事件。
type workBuddyReportEvent struct {
	// Code 是 eventCode。
	Code string
	// Domain 决定使用哪个设备域的信封。
	Domain workBuddyReportDomain
	// Fields 是事件自身的额外字段（pageURL / elementId / 模板 id 等）。
	Fields map[string]any
}

// WorkBuddyReportEvents 上报一批事件。
//
// 事件按 Domain 分组分别上报：不同域不能混在同一个 common 信封里。
func WorkBuddyReportEvents(ctx context.Context, account *Account, events []workBuddyReportEvent) error {
	if len(events) == 0 {
		return nil
	}
	// 按域分组，保持原顺序。
	grouped := map[workBuddyReportDomain][]workBuddyReportEvent{}
	order := make([]workBuddyReportDomain, 0, 2)
	for _, ev := range events {
		if _, ok := grouped[ev.Domain]; !ok {
			order = append(order, ev.Domain)
		}
		grouped[ev.Domain] = append(grouped[ev.Domain], ev)
	}
	for _, domain := range order {
		if err := workBuddyReportDomainBatch(ctx, account, domain, grouped[domain]); err != nil {
			return err
		}
	}
	return nil
}

// workBuddyReportDomainBatch 上报同一域的一批事件。
func workBuddyReportDomainBatch(ctx context.Context, account *Account, domain workBuddyReportDomain, events []workBuddyReportEvent) error {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	token := account.workBuddyTokenOf()
	if strings.TrimSpace(token.Access) == "" {
		return fmt.Errorf("账号缺少 access_token")
	}
	uid := account.workBuddyUIDOf()
	fp := account.workBuddyFingerprint()
	profile := workBuddyDomainProfileOf(account, fp, domain)

	baseTS := time.Now().UnixMilli()
	drift := int64(0)
	list := make([]map[string]any, 0, len(events))
	for _, ev := range events {
		drift += int64(workBuddyEventDriftMinMS + rand.Intn(workBuddyEventDriftMaxMS-workBuddyEventDriftMinMS))
		// 事件是**平铺**的：上游要求 eventCode 直接位于数组元素上。
		// 传 {"common":{...},"events":[...]} 会返回
		// 10001 "eventCode is empty or not string"（实测）。
		item := map[string]any{
			"eventCode":    ev.Code,
			"timestamp":    baseTS + drift,
			"reportDelay":  rand.Intn(workBuddyReportDelayMax),
			"userId":       uid,
			"userNickname": account.Name,
			"ideName":      profile.IDE,
			"ideType":      profile.IDEType,
			"machineId":    profile.MachineID,
			"sessionId":    fp.SessionID,
			"mode":         "CLOUD",
			"product":      "SaaS",
			"userAgent":    profile.UA,
			"os":           profile.OS,
			"arch":         profile.Arch,
			"osVersion":    profile.OSVersion,
			"timezone":     fp.Timezone,
			"cpuCores":     fp.CPUCores,
			"memorySize":   fp.MemorySize,
		}
		for k, v := range ev.Fields {
			item[k] = v
		}
		list = append(list, item)
	}

	body, err := json.Marshal(list)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, site.Endpoint+"/v2/report", strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", workBuddySiteDomain(site))
	req.Header.Set("User-Agent", profile.UA)
	req.Header.Set("Origin", site.Endpoint)
	req.Header.Set("Referer", site.Endpoint+"/profile/growth-center")

	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("上报事件失败（HTTP %d）", resp.StatusCode)
	}
	return nil
}
