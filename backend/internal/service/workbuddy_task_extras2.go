package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// 成长任务里「需要前置数据」的几类任务，对照 AutoBuddy 的
// gateway/vendor/workbuddy_daily.py 实现：
//
//   - 每日活跃：/v2/report 的 chat_request_send，点亮成长中心连登
//   - 首只 Buddy：buddy_agreement_view → /buddy/agreement → /buddy/first
//   - 主题皮肤：/v2/operation-platform/appearance/resources 取真 resource_key
//     再 /portal/user-asset/appearance/set，最后发 appearance_skin_apply
//   - 模板任务：/console/as/support/scenes 取真实场景 id，再发 4 条事件
//   - 专家任务：普通专家遥测（专家 id 取市场列表，不强制真实会话）
//   - 夜猫子：仅 23:00–08:00 计数，每天 1 次真实对话

// workBuddyReportDailyActivity 上报一次 chat_request_send，点亮当天活跃。
//
// 参考实现每天一条、幂等；这里同样不做幂等判断，由调度器保证每天只跑一轮。
func workBuddyReportDailyActivity(ctx context.Context, account *Account) error {
	convID := "wb-daily-" + uuid.NewString()
	now := time.Now().UnixMilli()
	err := WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{{
		Code:   "chat_request_send",
		Domain: workBuddyDomainChat,
		Fields: map[string]any{
			"timestamp":         now,
			"reportDelay":       0,
			"mode":              "craft",
			"conversationId":    convID,
			"requestId":         convID,
			"inputLength":       12,
			"requestModelId":    "deepseek-v4-flash",
			"requestModelName":  "DeepSeek V4 Flash",
			"presentAt":         now,
			"parentConversationId": convID,
			"rootRequestId":     convID,
			"agentName":         "default",
			"agentType":         "conversation",
		},
	}})
	if err != nil {
		return fmt.Errorf("上报每日活跃失败: %w", err)
	}
	return nil
}

// workBuddyHasBuddy 通过 /buddy/visible 判断账号是否已有 Buddy 实例。
func workBuddyHasBuddy(ctx context.Context, account *Account) bool {
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet, "/v2/activity/growth/buddy/visible", nil)
	if err != nil {
		return false
	}
	return workBuddyTaskBool(data, "has_buddy")
}

// workBuddyEnsureFirstBuddy 领养首只 Buddy，返回获得的积分。
//
// 无 Buddy 实例的账号，其它任务 accept 会被上游拒绝
// （prerequisite not met: first_buddy），所以它是前置步骤。
func workBuddyEnsureFirstBuddy(ctx context.Context, account *Account) (float64, error) {
	if workBuddyHasBuddy(ctx, account) {
		return 0, fmt.Errorf("已有 Buddy 实例")
	}

	if err := WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{{
		Code:   "buddy_agreement_view",
		Domain: workBuddyDomainDesktop,
	}}); err != nil {
		return 0, fmt.Errorf("上报协议查看失败: %w", err)
	}
	time.Sleep(2 * time.Second)

	if _, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
		"/v2/activity/growth/buddy/agreement", map[string]any{"agree": true}); err != nil {
		return 0, fmt.Errorf("同意 Buddy 协议失败: %w", err)
	}
	time.Sleep(3 * time.Second)

	data, err := workBuddyActivityRequest(ctx, account, http.MethodPost,
		"/v2/activity/growth/buddy/first", map[string]any{})
	if err != nil {
		return 0, fmt.Errorf("领养首只 Buddy 失败: %w", err)
	}
	credit := workBuddyTaskFloat(data, "credit")
	if !workBuddyHasBuddy(ctx, account) {
		return credit, fmt.Errorf("上游未确认 Buddy 实例，任务可能需稍后重试")
	}
	return credit, nil
}

// workBuddyThemeResource 是从主题目录里挑出的一条资源。
type workBuddyThemeResource struct {
	Key     string
	VIP     int
	Series  string
}

// workBuddyFetchTheme 读取主题目录，返回第一个可用的资源。
func workBuddyFetchTheme(ctx context.Context, account *Account) (*workBuddyThemeResource, error) {
	data, err := workBuddyActivityRequestWithBase(ctx, account, http.MethodPost,
		"https://www.workbuddy.cn/v2/operation-platform/appearance/resources", map[string]any{
			"platform": "client",
			"kind":     "theme",
			"version":  "5.9.3",
			"lang":     "zh-CN",
		})
	if err != nil {
		return nil, err
	}
	raw, _ := data["resources"].([]any)
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := workBuddyStringField(m, "id")
		if key == "" {
			continue
		}
		return &workBuddyThemeResource{
			Key:    key,
			VIP:    workBuddyTaskInt(m, "vip_level"),
			Series: workBuddyStringField(m, "series"),
		}, nil
	}
	return nil, fmt.Errorf("主题目录为空")
}

// workBuddyApplyTheme 应用主题并上报 skin_apply 事件。
func workBuddyApplyTheme(ctx context.Context, account *Account) (float64, error) {
	res, err := workBuddyFetchTheme(ctx, account)
	if err != nil {
		return 0, fmt.Errorf("读取主题目录失败: %w", err)
	}

	if _, err := workBuddyActivityRequestWithBase(ctx, account, http.MethodPost,
		"https://www.workbuddy.cn/portal/user-asset/appearance/set", map[string]any{
			"kind":         "theme",
			"resource_key": res.Key,
		}); err != nil {
		return 0, fmt.Errorf("应用主题失败: %w", err)
	}
	time.Sleep(2 * time.Second)

	if err := WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{{
		Code:   "appearance_skin_apply",
		Domain: workBuddyDomainDesktop,
		Fields: map[string]any{
			"action":   "apply",
			"source":   "settings_close",
			"id":       res.Key,
			"vipLevel": res.VIP,
			"series":   res.Series,
			"type":     "personal",
		},
	}}); err != nil {
		return 0, fmt.Errorf("上报主题事件失败: %w", err)
	}
	time.Sleep(6 * time.Second)
	return 0, nil
}

// workBuddyFetchScenes 读取真实场景列表，失败时用参考实现里的内置表。
func workBuddyFetchScenes(ctx context.Context, account *Account) [][2]string {
	fallback := [][2]string{{"0", "幻灯片"}, {"4", "深度研究"}, {"8", "数据分析"}, {"16", "设计"}, {"20", "日常开发"}}
	data, err := workBuddyActivityRequest(ctx, account, http.MethodGet,
		"/console/as/support/scenes?locale=zh-CN", nil)
	if err != nil {
		return fallback
	}
	raw, _ := data["scenes"].([]any)
	out := make([][2]string, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id := fmt.Sprintf("%v", m["id"])
		if id == "" || id == "<nil>" {
			continue
		}
		out = append(out, [2]string{id, workBuddyStringField(m, "name")})
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

// workBuddyRunTemplateTask 用真实场景 id 完成模板任务，每轮计 1/5 进度。
func workBuddyRunTemplateTask(ctx context.Context, account *Account) error {
	scenes := workBuddyFetchScenes(ctx, account)
	if len(scenes) > 5 {
		scenes = scenes[:5]
	}
	for _, s := range scenes {
		rid := fmt.Sprintf("a2a-tpl-%d-%s", time.Now().UnixMilli(), s[0])
		if err := WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{
			{Code: "agent_task_created", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"source": "CLOUD", "mode": "craft", "requestModelId": "default",
				"action": s[0], "has_template": true, "template_id": s[0],
				"template_name": s[1], "requestId": rid,
			}},
			{Code: "agent_task_created_with_template", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"templateId": s[0], "templateName": s[1], "template_id": s[0],
				"mode": "working", "isCustomModel": true, "id": s[0], "name": s[1], "requestId": rid,
			}},
			{Code: "template_used", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"template_id": s[0], "templateId": s[0], "templateName": s[1],
				"task_mode": "working", "id": s[0], "name": s[1], "source": "growth-center",
			}},
			{Code: "playbook_prompt_send", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"ext1": uuid.NewString(), "requestId": rid, "id": s[0], "name": s[1],
				"type": "other", "promptLength": 30, "isOfficial": 1, "source": "growth-center",
			}},
		}); err != nil {
			return fmt.Errorf("模板事件上报失败: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
	return nil
}

// workBuddyRunExpertTask 完成专家类任务：普通专家遥测，不强制真实会话。
func workBuddyRunExpertTask(ctx context.Context, account *Account) error {
	experts := workBuddyFetchExperts(ctx, account)
	for i := 0; i < 5; i++ {
		e := map[string]any{"id": "expert-" + uuid.NewString()[:8], "name": "Expert"}
		if len(experts) > 0 {
			e = experts[i%len(experts)]
		}
		id := workBuddyStringField(e, "id")
		name := workBuddyStringField(e, "name")
		if err := WorkBuddyReportEvents(ctx, account, []workBuddyReportEvent{
			{Code: "expert_summoned", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"id": id, "name": name, "type": "agent",
				"expertTitle": workBuddyStringField(e, "profession"), "expertType": "agent",
			}},
			{Code: "expert_actual_use", Domain: workBuddyDomainDesktop, Fields: map[string]any{
				"id": id, "name": name, "type": workBuddyStringField(e, "industryId"),
				"expertType": "agent", "source": "builtin", "version": "", "cost": 0,
				"characterCount": 12, "conversationId": "conv-" + uuid.NewString(),
				"requestId": uuid.NewString(), "messageId": "msg-" + uuid.NewString(),
				"requestModelId": "deepseek-v4-flash", "requestModelName": "DeepSeek V4 Flash",
			}},
		}); err != nil {
			return fmt.Errorf("专家事件上报失败: %w", err)
		}
		time.Sleep(3 * time.Second)
	}
	return nil
}

// workBuddyFetchExperts 读取专家市场列表。
func workBuddyFetchExperts(ctx context.Context, account *Account) []map[string]any {
	data, err := workBuddyActivityRequestWithBase(ctx, account, http.MethodGet,
		"https://www.workbuddy.cn/v2/operation-platform/market/expert/list?page=1&page_size=20", nil)
	if err != nil {
		return nil
	}
	raw, _ := data["experts"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok && workBuddyStringField(m, "id") != "" {
			out = append(out, m)
		}
	}
	return out
}

// workBuddyInNightWindow 判断当前是否在夜猫子任务的计数窗口（23:00–08:00）。
func workBuddyInNightWindow(now time.Time) bool {
	h := now.Hour()
	return h >= 23 || h < 8
}

// workBuddyRunBlackCatTask 夜猫子：窗口内做一次真实对话并上报。
func workBuddyRunBlackCatTask(ctx context.Context, account *Account) error {
	if !workBuddyInNightWindow(time.Now()) {
		return fmt.Errorf("夜猫子仅在 23:00–08:00 计数，当前不在窗口内")
	}
	convID, reply, err := WorkBuddyChatOnce(ctx, account, "今天天气怎么样？", "glm-5.2")
	if err != nil {
		return fmt.Errorf("对话失败: %w", err)
	}
	if strings.TrimSpace(reply) == "" {
		return fmt.Errorf("对话无回复")
	}
	if err := WorkBuddyReportChatEvents(ctx, account, convID, "今天天气怎么样？", reply, "glm-5.2"); err != nil {
		return fmt.Errorf("上报会话事件失败: %w", err)
	}
	time.Sleep(3 * time.Second)
	return nil
}

// workBuddyActivityRequestWithBase 与 workBuddyActivityRequest 相同，
// 但允许指定完整 URL（部分接口在 /v2/operation-platform 与 /portal 前缀下）。
func workBuddyActivityRequestWithBase(ctx context.Context, account *Account, method, url string, body any) (map[string]any, error) {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	token := account.workBuddyTokenOf()
	if token.Access == "" {
		return nil, fmt.Errorf("账号缺少 access_token")
	}

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.Access)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if uid := account.workBuddyUIDOf(); uid != "" {
		req.Header.Set("X-User-Id", uid)
	}
	req.Header.Set("X-Domain", workBuddySiteDomain(site))
	req.Header.Set("X-Product", "SaaS")
	req.Header.Set("User-Agent", "WorkBuddy/5.9.3 workbuddy/5.9.3 CLI/2.165.0")
	req.Header.Set("Origin", site.Endpoint)
	req.Header.Set("Referer", site.Endpoint+"/profile/growth-center")

	resp, err := workBuddyActivityClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("上游返回非 JSON（HTTP %d）: %s", resp.StatusCode, truncateRunes(string(raw), 160))
	}
	if env.Code != 0 {
		return nil, fmt.Errorf("上游错误 %d: %s", env.Code, env.Msg)
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	return env.Data, nil
}
