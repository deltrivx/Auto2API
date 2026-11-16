package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// WorkBuddy 上游模型目录的动态拉取。
//
// 移植自 AutoBuddy 的 _fetch_upstream_variant_models（gateway/main.py）。
//
// 上游把可用的模型清单放在 /v3/config 里，随账号的套餐与灰度实时下发：
// 国内站与上游返回的集合不同，同一账号在不同时期也会变化。因此模型列表
// 必须每次向后端询问，不能内置固定目录——内置目录会包含已下线模型，
// 用户选中后在调用时才失败。
//
// 请求身份必须与 magpie（社区权威实现）一致：用 WorkBuddy 自己的 CLI 形态读取，
// 即 workBuddySign 写出的整套头（X-User-Id / X-Domain / X-IDE-* / WorkBuddy UA）。
// 早期这里伪装成 CodeBuddy IDE（CodeBuddyIDE UA + X-CodeBuddy-Request），
// 拿到的是 IDE 渠道的另一份目录：国际版缺模型，且请求被上游判为未授权渠道，
// 表现为 11128 / 11140「request illegal」。

const (
	// workBuddyConfigTimeout 是拉取模型目录的超时。
	workBuddyConfigTimeout = 25 * time.Second
)

// workBuddyConfigResponse 是 /v3/config 的响应外壳。
type workBuddyConfigResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		// ConfigFlag 形如 "[ide][cn] SAAS 国内"，用于确认拿到的站点分支。
		ConfigFlag string `json:"_configFlag"`
		// Models 是该账号当前可用的全部模型，带完整元数据。
		Models []workBuddyConfigModel `json:"models"`
		// Agents 按用途分配模型（craft / ask / plan / chat …）。
		Agents []workBuddyConfigAgent `json:"agents"`
	} `json:"data"`
}

// workBuddyConfigModel 是 /v3/config 里的一个模型条目。
type workBuddyConfigModel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Vendor 是模型提供方标识（f / e / j / tencent）。
	Vendor string `json:"vendor"`
	// Credits 是积分倍率，形如 "x0.29"；空表示不计费。
	Credits string `json:"credits"`
	// MaxInputTokens / MaxOutputTokens 是上下文与输出上限。
	MaxInputTokens  int `json:"maxInputTokens"`
	MaxOutputTokens int `json:"maxOutputTokens"`
	// SupportsImages / SupportsToolCall / SupportsReasoning 是能力标记。
	SupportsImages    bool `json:"supportsImages"`
	SupportsToolCall  bool `json:"supportsToolCall"`
	SupportsReasoning bool `json:"supportsReasoning"`
	// Reasoning 描述推理档位。
	Reasoning *struct {
		SupportedEfforts []string `json:"supportedEfforts"`
		DefaultEffort    string   `json:"defaultEffort"`
	} `json:"reasoning"`
	// Tags 含 UI 标记（如 text-to-image）。
	Tags []string `json:"tags"`
}

// workBuddyConfigAgent 是 /v3/config 里的 agent 分配。
type workBuddyConfigAgent struct {
	Name   string   `json:"name"`
	Models []string `json:"models"`
}

// fetchWorkBuddyUpstreamModels 拉取账号所属站点的真实可用模型。
//
// 返回按上游顺序排列的模型 ID。调用失败时返回错误，由上层决定是否回落到
// 账号凭据里的映射——不能静默返回空列表，否则前端会误以为没有模型。
// WorkBuddyModelPricing 是上游 /v3/config 给出的一个模型的官方价格。
//
// Credits 是积分倍率（"x0.15"），表示一次调用消耗的积分单价。
// 空值表示上游未标价（如 auto 路由模型，实际由被路由到的模型计费）。
type WorkBuddyModelPricing struct {
	ID      string
	Credits float64
	HasRate bool
}

// parseWorkBuddyCreditRate 把 "x0.15" 解析为 0.15。
func parseWorkBuddyCreditRate(raw string) (float64, bool) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return 0, false
	}
	text = strings.TrimPrefix(strings.TrimPrefix(text, "x"), "X")
	text = strings.TrimSpace(strings.Fields(text)[0])
	v, err := strconv.ParseFloat(text, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

// FetchWorkBuddyModelPricing 从上游 /v3/config 拉取模型及其官方积分倍率。
//
// 这是价格的唯一可信来源：面板不内置 WorkBuddy 的私有模型定价，
// 而这些模型也不在通用价格库里。
func (s *AccountTestService) FetchWorkBuddyModelPricing(ctx context.Context, account *Account) ([]WorkBuddyModelPricing, error) {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("workbuddy: unsupported platform %q", account.Platform)
	}
	req, err := s.buildWorkBuddyConfigRequest(ctx, account, site)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: workBuddyConfigTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: config request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("workbuddy: read config response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workbuddy: config returned HTTP %d", resp.StatusCode)
	}
	var parsed workBuddyConfigResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("workbuddy: config response is not valid JSON: %w", err)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("workbuddy: config error %d: %s", parsed.Code, parsed.Msg)
	}
	out := make([]WorkBuddyModelPricing, 0, len(parsed.Data.Models))
	seen := make(map[string]struct{}, len(parsed.Data.Models))
	for _, m := range parsed.Data.Models {
		id := strings.TrimSpace(m.ID)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		rate, ok := parseWorkBuddyCreditRate(m.Credits)
		out = append(out, WorkBuddyModelPricing{ID: id, Credits: rate, HasRate: ok})
	}
	return out, nil
}

func (s *AccountTestService) fetchWorkBuddyUpstreamModels(ctx context.Context, account *Account) ([]string, error) {
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("workbuddy: unsupported platform %q", account.Platform)
	}

	req, err := s.buildWorkBuddyConfigRequest(ctx, account, site)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: workBuddyConfigTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workbuddy: config request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("workbuddy: read config response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workbuddy: config returned HTTP %d", resp.StatusCode)
	}

	var parsed workBuddyConfigResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("workbuddy: config response is not valid JSON: %w", err)
	}
	if parsed.Code != 0 {
		return nil, fmt.Errorf("workbuddy: config error %d: %s", parsed.Code, parsed.Msg)
	}

	// magpie 取的是 agents[name=cli].models —— 产品自己给 CLI 分配的清单，
	// 与用户在 WorkBuddy 客户端里看到的完全一致。data.models 是站点全量目录，
	// 混着补全/NES/视频等不可经 CLI 调用的条目，直接用会让列表「多出来」，
	// 而 IDE 渠道拿到的那份又会「少一截」。
	source := make([]string, 0, len(parsed.Data.Models))
	for _, agent := range parsed.Data.Agents {
		if strings.EqualFold(strings.TrimSpace(agent.Name), "cli") && len(agent.Models) > 0 {
			source = agent.Models
			break
		}
	}
	if len(source) == 0 {
		for _, m := range parsed.Data.Models {
			source = append(source, m.ID)
		}
	}

	ids := make([]string, 0, len(source))
	seen := make(map[string]struct{}, len(source))
	for _, raw := range source {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}
		if _, dup := seen[id]; dup {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("workbuddy: config returned no models (flag=%s)", parsed.Data.ConfigFlag)
	}
	// 保持上游顺序（magpie 同款），不再排序。
	return ids, nil
}

// buildWorkBuddyConfigRequest 构造 /v3/config 请求。
//
// 请求头与 CodeBuddy IDE 客户端一致：上游按这些头判定来源，
// 缺少 X-CodeBuddy-Request 或 UA 不匹配都会被拒绝。
func (s *AccountTestService) buildWorkBuddyConfigRequest(ctx context.Context, account *Account, site *workBuddySite) (*http.Request, error) {
	token := account.workBuddyTokenOf()
	if strings.TrimSpace(token.Access) == "" {
		return nil, fmt.Errorf("workbuddy: access_token missing for account %d", account.ID)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, site.Endpoint+"/v3/config", nil)
	if err != nil {
		return nil, err
	}

	// 与 magpie 的 chinaModels / intlModels 完全一致：Accept 取宽松值，
	// 其余身份头交给 workBuddySign，不再发送 X-CodeBuddy-Request。
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("Content-Type", "application/json")
	workBuddySign(req.Header, site, token, account.workBuddyUIDOf(), account.workBuddyEnterpriseIDOf())
	return req, nil
}

// sortStrings 对字符串切片原地排序。
func sortStrings(items []string) {
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j] < items[j-1]; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
}

// SyncWorkBuddyUpstreamModels 拉取 WorkBuddy 账号的上游模型目录并落库。
//
// 结果写进 accounts.extra 的 workbuddy_models_cache：
//   - 编辑页的模型列表（GET /models）读这份缓存，避免呈现已下线模型；
//   - 测试连接的模型选择也读它，保证测的是真实可用的模型；
//   - 落库而非内存，进程重启后仍有效。
//
// 只缓存模型清单，不改 credentials.model_mapping——那是用户的选择，
// 由用户在编辑页确认后保存。
func (s *AccountTestService) SyncWorkBuddyUpstreamModels(ctx context.Context, account *Account) (*UpstreamModelCatalog, error) {
	ids, err := s.fetchWorkBuddyUpstreamModels(ctx, account)
	if err != nil {
		return nil, err
	}
	if s.accountRepo != nil && account != nil && account.ID > 0 {
		if persistErr := s.persistWorkBuddyModels(ctx, account, ids); persistErr != nil {
			slog.Warn("workbuddy_models_cache_persist_failed",
				"account_id", account.ID, "error", persistErr)
		}
	}
	return &UpstreamModelCatalog{
		Models:          ids,
		DiscoverySource: "v3_config",
	}, nil
}

// persistWorkBuddyModels 把模型清单写入 accounts.extra 并更新内存副本。
func (s *AccountTestService) persistWorkBuddyModels(ctx context.Context, account *Account, models []string) error {
	payload := WorkBuddyModelsCacheExtra(models, time.Now())

	extra := make(map[string]any, len(account.Extra)+1)
	for k, v := range account.Extra {
		extra[k] = v
	}
	extra["workbuddy_models_cache"] = payload

	if err := s.accountRepo.UpdateExtra(ctx, account.ID, extra); err != nil {
		return err
	}
	account.Extra = extra
	return nil
}

// WorkBuddy 连接测试。
//
// 移植自 sub2api 的 account_test_service_workbuddy.go。
//
// 不能用通用的 Chat Completions 测试：那条路径要求 API Key，而 WorkBuddy
// 是 OAuth 账号；也不能落到 Claude 测试，它发 /v1/messages 会被上游 403。
// 必须直接打 /v2/chat/completions，并用与转发路径相同的请求头与规范化。

const (
	// workBuddyTestDefaultPrompt 是探测用的最小提示词。
	workBuddyTestDefaultPrompt = "hi"
	// workBuddyTestPreviewLimit 是回显内容的字符上限。
	workBuddyTestPreviewLimit = 2000
)

// workBuddyTestModelPreference 是优先用于连通性测试的模型。
//
// 这些模型在两侧都有额度覆盖且响应快，代价最低。
var workBuddyTestModelPreference = []string{
	"glm-5.3-flash",
	"deepseek-v4-flash",
	"hy3",
	"auto",
}

// testWorkBuddyAccountConnection 用最小对话探测 WorkBuddy 账号。
func (s *AccountTestService) testWorkBuddyAccountConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	ctx := c.Request.Context()

	if account.Type != AccountTypeOAuth {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported account type: %s", account.Type))
	}

	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported platform: %s", account.Platform))
	}

	testModel := strings.TrimSpace(modelID)
	if testModel == "" {
		testModel = defaultWorkBuddyTestModel(account)
	}
	if testModel == "" {
		return s.sendErrorAndEnd(c, "No model available for this WorkBuddy account")
	}
	testModel = account.GetMappedModel(testModel)

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModel})

	userPrompt := strings.TrimSpace(prompt)
	if userPrompt == "" {
		userPrompt = workBuddyTestDefaultPrompt
	}

	// 与转发路径同一套规范化：FLAGGED 改写 + 首条必须为 system，
	// 否则上游会以 11128 拒绝。
	body := map[string]any{
		"model":  testModel,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "system", "content": "You are a helpful assistant."},
			map[string]any{"role": "user", "content": userPrompt},
		},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}
	if normalized := workBuddyNormalizeRequestBody(account, payload); len(normalized) > 0 {
		payload = normalized
	}

	reqCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 90 * time.Second}

	// 国际版走 console 两段式：先建会话，再把 conversationId 塞进请求体。
	// 直接打 /v2/chat/completions 会稳定收到 11128「request illegal」。
	chatURL := site.Endpoint + "/v2/chat/completions"
	if isWorkBuddyAIVariant(account.Platform) {
		convID, convErr := workBuddyCreateConversation(reqCtx, client, account, site.Endpoint)
		if convErr != nil {
			return s.sendErrorAndEnd(c, convErr.Error())
		}
		var withConv map[string]any
		if err := json.Unmarshal(payload, &withConv); err == nil {
			withConv["conversationId"] = convID
			if merged, mErr := json.Marshal(withConv); mErr == nil {
				payload = merged
			}
		}
		chatURL = workBuddyConsoleChatURL(site.Endpoint)
	}

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, chatURL, bytes.NewReader(payload))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test request")
	}
	// 复用转发路径的请求头：签名、指纹与客户端标识都在这里。
	applyWorkBuddyHeaders(account, req.Header)
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Upstream request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(raw))
		logAccountTestError(c, msg)
		s.sendEvent(c, TestEvent{Type: "error", Error: msg})
		return fmt.Errorf("%s", msg)
	}

	// 上游是 SSE：每行 "data: {json}"，可能夹杂 ": heartbeat" 注释行。
	var collected strings.Builder
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if chunk == "" || chunk == "[DONE]" {
			continue
		}
		var parsed struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(chunk), &parsed); err != nil {
			continue
		}
		for _, ch := range parsed.Choices {
			if ch.Delta.Content != "" {
				collected.WriteString(ch.Delta.Content)
			}
		}
		if collected.Len() >= workBuddyTestPreviewLimit {
			break
		}
	}

	preview := collected.String()
	if r := []rune(preview); len(r) > workBuddyTestPreviewLimit {
		preview = string(r[:workBuddyTestPreviewLimit])
	}
	if preview == "" {
		preview = "（上游返回成功，但未包含文本内容）"
	}
	s.sendEvent(c, TestEvent{Type: "content", Text: preview})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// defaultWorkBuddyTestModel 选取用于连通性测试的模型。
//
// 优先用账号可用模型里响应最快的几个；都不在时取映射里排序首项。
func defaultWorkBuddyTestModel(account *Account) string {
	available := make(map[string]struct{})
	for id := range account.GetModelMapping() {
		available[id] = struct{}{}
	}
	for _, m := range WorkBuddyAccountModels(account) {
		available[m.ID] = struct{}{}
	}
	for _, preferred := range workBuddyTestModelPreference {
		if _, ok := available[preferred]; ok {
			return preferred
		}
	}
	ids := make([]string, 0, len(available))
	for id := range available {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return DefaultWorkBuddyTestModel
	}
	sort.Strings(ids)
	return ids[0]
}

// WorkBuddy 国际版的 console 两段式请求。
//
// 移植自 sub2api 的 workbuddy_relay：国际版直接打 /v2/chat/completions 会
// 稳定收到 400 code 11128「request illegal」——渠道校验要求请求带着
// console 会话上下文。必须：
//
//	1. POST /console/webchat/conversations   创建会话，拿到 conversationId
//	2. POST /console/chat/completions        发消息，请求体带 conversationId
//
// 国内版仍走 /v2/chat/completions，不需要这一步。

const (
	workBuddyConsolePrefix      = "/console"
	workBuddyConvCreatePath     = "/webchat/conversations"
	workBuddyConsoleChatPath    = "/chat/completions"
	workBuddyDesktopIDEVersion  = "5.5.6"
	workBuddyDesktopCLIVersion  = "2.137.1"
	workBuddyOpenAISDKVersion   = "6.25.0"
	workBuddyNodeRuntimeVersion = "v22.21.1"
)

// isWorkBuddyAIVariant 判断是否国际版。
func isWorkBuddyAIVariant(platform string) bool {
	return platform == PlatformWorkBuddyIntl
}

// workBuddyConsoleChatURL 返回国际版 console chat 的地址。
func workBuddyConsoleChatURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + workBuddyConsolePrefix + workBuddyConsoleChatPath
}

// workBuddyCreateConversationURL 返回国际版建会话的地址。
func workBuddyCreateConversationURL(endpoint string) string {
	return strings.TrimRight(endpoint, "/") + workBuddyConsolePrefix + workBuddyConvCreatePath
}

// applyWorkBuddyDesktopHeaders 写入官方桌面端的完整请求头。
//
// 国际版的渠道校验逐项比对客户端特征，缺任一项都会 11128。
// 同时注意不能带 X-Agent-* 那四个头——上游判据已变更，带上反而触发
// 11140 账号级风控。
func applyWorkBuddyDesktopHeaders(account *Account, h http.Header) {
	site := account.workBuddySiteOf()
	if site == nil {
		return
	}
	token := account.workBuddyTokenOf()

	appName := "WorkBuddy"
	if isWorkBuddyAIVariant(account.Platform) {
		appName = "workbuddy-ai"
	}
	clientUA := "WorkBuddy/" + workBuddyDesktopIDEVersion + " " + appName + "/" +
		workBuddyDesktopIDEVersion + " CLI/" + workBuddyDesktopCLIVersion

	uid := strings.TrimSpace(account.workBuddyUIDOf())
	if uid == "" {
		uid = "wb"
	}
	rid := uid + fmt.Sprintf("%d", time.Now().UnixMilli()%1000000)

	host := strings.TrimPrefix(strings.TrimPrefix(site.Endpoint, "https://"), "http://")

	h.Set("Authorization", "Bearer "+token.Access)
	h.Set("X-User-Id", uid)
	h.Set("X-Domain", host)
	h.Set("X-Product", "SaaS")
	h.Set("X-IDE-Type", "WorkBuddy")
	h.Set("X-IDE-Name", "WorkBuddy")
	h.Set("X-IDE-Version", site.App)
	h.Set("User-Agent", clientUA)
	h.Set("X-Requested-With", "XMLHttpRequest")
	h.Set("X-Request-ID", rid)
	h.Set("Accept", "application/json")
	h.Set("Content-Type", "application/json;charset=UTF-8")
	h.Set("X-Client-Platform", "desktop")
	h.Set("X-Client-Product", "workbuddy-desktop")
	h.Set("X-Client-Version", site.App)
	h.Set("X-CodeBuddy-Request", "1")
	h.Set("X-Stainless-Retry-Count", "0")
	h.Set("X-Stainless-Timeout", "1200")
	h.Set("X-Stainless-Lang", "js")
	h.Set("X-Stainless-Package-Version", workBuddyOpenAISDKVersion)
	h.Set("X-Stainless-OS", "MacOS")
	h.Set("X-Stainless-Arch", "arm64")
	h.Set("X-Stainless-Runtime", "node")
	h.Set("X-Stainless-Runtime-Version", workBuddyNodeRuntimeVersion)
}

// workBuddyCreateConversation 在国际版 console 上创建会话，返回 conversationId。
func workBuddyCreateConversation(ctx context.Context, client *http.Client, account *Account, endpoint string) (string, error) {
	name := fmt.Sprintf("a2a-%d", time.Now().UnixNano()%100000000)
	payload, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, workBuddyCreateConversationURL(endpoint), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	applyWorkBuddyDesktopHeaders(account, req.Header)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("创建国际版会话失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("创建国际版会话返回 HTTP %d: %s", resp.StatusCode, string(body))
	}
	var out struct {
		Code int `json:"code"`
		Data struct {
			ConversationID string `json:"conversationId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("解析会话响应失败: %w", err)
	}
	if out.Code != 0 || strings.TrimSpace(out.Data.ConversationID) == "" {
		return "", fmt.Errorf("创建国际版会话失败 code=%d: %s", out.Code, string(body))
	}
	return out.Data.ConversationID, nil
}

// IsWorkBuddyPlatformID 判断平台 ID 是否属于 WorkBuddy。
func IsWorkBuddyPlatformID(platform string) bool {
	return isWorkBuddyPlatform(platform)
}

// fetchWorkBuddyModelsForAccount 用给定账号凭据拉取 /v3/config 的模型清单。
//
// 与 AccountTestService 的方法区分：这里不依赖 service 实例，供账号创建
// 等早期阶段调用（此时账号尚未入库）。
func fetchWorkBuddyModelsForAccount(ctx context.Context, account *Account, site *workBuddySite) ([]string, error) {
	svc := &AccountTestService{}
	return svc.fetchWorkBuddyUpstreamModels(ctx, account)
}
