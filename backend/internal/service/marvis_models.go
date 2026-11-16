package service

// Marvis 的模型目录与请求改写。
//
// 上游只认**槽位名**（main-auto、hy3-preview-agent 之类），不认真实模型名。
// 把真实名当 model 传过去会回 400 无效模型 id。而槽位名对使用者毫无意义
// （hy3-preview-agent 实际跑的是 deepseek-v4-flash），所以对外显示真实名，
// 转发前再映射回槽位名。
//
// 槽位与真实模型的对应关系来自 magpie-marvis 的逐槽位实测，见其 context.mjs。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// marvisModelSpec 是一个可用的槽位。
type marvisModelSpec struct {
	// Slot 是上游要求的槽位名，也是请求体里 model 字段该填的值。
	Slot string
	// Display 是对外显示的真实模型名。
	Display string
	// Context 是上下文窗口。
	Context int
	// Vision 表示该槽位支持图片输入。
	Vision bool
}

// marvisModels 是已实测可用的槽位表。
//
// 只列真实存在且验证过能出结果的槽位。已移除的（上游 400 / 500 / 空响应）：
//
//	glm-5v-turbo-thinking、claude-opus-4-6-thinking、memory-compress、
//	kimi-k2.5（500 RPC）、hunyuan-2.0-instruct（空响应）、venus-vision-llm
// marvisModels 的第一个条目是**默认推荐模型**：客户端通常取列表首项，
// 所以放在最前的是通用对话模型（支持工具调用），视觉模型排最后。
var marvisModels = []marvisModelSpec{
	// 下列槽位均已在 2026-11-15 逐个实测通过（真实账号直连上游）。
	{Slot: "main-auto", Display: "deepseek-v4-1-flash-261017", Context: 1_000_000},
	// 名字里有 pro，实际回的也是 flash。留着是因为 main-auto 是服务端自动
	// 路由，这个槽位给了一个映射固定的替代选择。
	{Slot: "deepseek-v4-pro-external", Display: "deepseek-v4-1-flash-261017-pro", Context: 1_000_000},
	{Slot: "hy3-preview-agent", Display: "deepseek-v4-flash-ga-260731", Context: 1_000_000},
	{Slot: "hy3-preview-agent-real", Display: "hy3", Context: 1_000_000},
	// 视觉模型：支持图片输入，但**不支持工具调用**（实测），排最后。
	{Slot: "hunyuan-turbos-vision", Display: "HY-Vision-2.0-instruct", Context: 32_000, Vision: true},
}

// marvisRetiredSlots 是曾经出现在插件里的槽位，上游已经不再接受。
//
// 实测（2026-11-15，真实账号）：
//
//	claude-sonnet-4-6   → 400 4130001 无效模型id
//	claude-opus-4-6     → 400 4130001 无效模型id
//	gpt-5.4             → 400 4130001 无效模型id
//
// 这些槽位 magpie 的槽位表里有，但上游已经下线。留在目录里只会让用户
// 选到一个必然 400 的模型，所以整条移到这里的弃用名单，用于给出可读提示。
var marvisRetiredSlots = []string{
	"claude-sonnet-4-6",
	"claude-opus-4-6",
	"gpt-5.4",
	"gpt-5.4-2026-03-05",
}

// marvisUnavailableModels 是已确认持续失败、必须在本地拦下的模型。
var marvisUnavailableModels = append([]string{"kimi-k2.5"}, marvisRetiredSlots...)

// marvisModelDisplayToSlot 是「显示名 → 槽位名」。
var marvisModelDisplayToSlot = func() map[string]string {
	out := make(map[string]string, len(marvisModels))
	for _, m := range marvisModels {
		out[strings.ToLower(m.Display)] = m.Slot
	}
	return out
}()

// marvisModelSlotToSpec 是「槽位名 → 规格」。
var marvisModelSlotToSpec = func() map[string]marvisModelSpec {
	out := make(map[string]marvisModelSpec, len(marvisModels))
	for _, m := range marvisModels {
		out[strings.ToLower(m.Slot)] = m
	}
	return out
}()

// MarvisModelIDs 返回对外的模型 ID 列表（真实模型名）。
func MarvisModelIDs() []string {
	out := make([]string, 0, len(marvisModels))
	for _, m := range marvisModels {
		out = append(out, m.Display)
	}
	return out
}

// marvisAccountModel 是账号连接测试里展示的模型条目。
//
// 形状与 workBuddyAccountModel 保持一致，前端同一个对话框能直接渲染。
type marvisAccountModel struct {
	ID          string `json:"id"`
	Object      string `json:"object"`
	Created     int64  `json:"created"`
	OwnedBy     string `json:"owned_by"`
	Type        string `json:"type"`
	DisplayName string `json:"display_name"`
}

// MarvisAccountModels 返回 Marvis 账号可用的模型。
//
// 与 WorkBuddy 不同，Marvis 的模型集合是固定的槽位表（上游只认槽位名），
// 没有「按账号下发」的概念，所以这里直接返回整张表。
// 展示用真实模型名，转发时才换成槽位名。
func MarvisAccountModels() []marvisAccountModel {
	out := make([]marvisAccountModel, 0, len(marvisModels))
	for _, m := range marvisModels {
		out = append(out, marvisAccountModel{
			ID: m.Display, Object: "model", Type: "model", OwnedBy: "tencent",
			DisplayName: m.Display,
		})
	}
	return out
}

// MarvisModels 返回模型目录，供 /v1/models 使用。
func MarvisModels() []map[string]any {
	out := make([]map[string]any, 0, len(marvisModels))
	for _, m := range marvisModels {
		out = append(out, map[string]any{
			"id":       m.Display,
			"object":   "model",
			"owned_by": "tencent",
			"created":  0,
		})
	}
	return out
}

// marvisResolveSlot 把外部传进来的模型名映射成上游槽位名。
//
// 接受：显示名、槽位名本身、大小写不同的写法。未知值原样返回 ——
// 上游会给出明确的「无效模型 id」，比在这里猜更有用。
func marvisResolveSlot(model string) string {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return "main-auto"
	}
	if slot, ok := marvisModelDisplayToSlot[key]; ok {
		return slot
	}
	if _, ok := marvisModelSlotToSpec[key]; ok {
		return model
	}
	return model
}

// IsMarvisUnavailableModel 判定模型是否在已知失败名单里。
// IsUnavailableModelForPlatform 报告模型在指定平台是否已下线。
//
// ⚠ 必须带平台：同名的 claude-opus-4-6 在 Marvis 已下线，
// 但在 WorkBuddy 国际站是正常可用的。早先只用模型名判断，
// 把国际版的两个合法模型也拦下了。
func IsUnavailableModelForPlatform(platform, model string) bool {
	if platform != PlatformMarvis {
		return false
	}
	return IsMarvisUnavailableModel(model)
}

func IsMarvisUnavailableModel(model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return false
	}
	key = strings.ToLower(marvisResolveSlot(key))
	for _, id := range marvisUnavailableModels {
		if key == id {
			return true
		}
	}
	return false
}

// marvisRewriteModel 把请求体里的 model 换成槽位名。
//
// 只在确实需要时改写（槽位名与传入值不同才动 body），避免无谓的重编码。
func marvisRewriteModel(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	raw, ok := payload["model"].(string)
	if !ok {
		return body
	}
	slot := marvisResolveSlot(raw)
	if slot == raw {
		return body
	}
	payload["model"] = slot
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// marvisNormalizeRequestBody 做 Marvis 上游需要的最小归一化。
//
// 上游是标准 OpenAI 形状，与 WorkBuddy 不同，绝大部分字段它都认识。
// 这里只处理两件事：
//   - model 换槽位名
//   - 去掉上游明确不认的字段（目前没有确证清单，先保持空）
func marvisNormalizeRequestBody(body []byte) []byte {
	return marvisRewriteModel(body)
}

// marvisPrepareUpstreamBody 做出站前的完整归一化。
//
// 比 marvisNormalizeRequestBody 多两步，都来自实测：
//   - tools[*].function.parameters 必须显式带 type:"object"，缺一个就 400
//   - 已下线的槽位在本地拦掉，不发出去
//
// 只在确实改了内容时才重新序列化，避免无谓地改变请求字节。
func marvisPrepareUpstreamBody(body []byte) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body
	}
	changed := false
	if raw, ok := payload["model"].(string); ok {
		if slot := marvisResolveSlot(raw); slot != raw {
			payload["model"] = slot
			changed = true
		}
	}
	if tools, ok := payload["tools"]; ok {
		if next, toolsChanged := marvisNormalizeTools(tools); toolsChanged {
			payload["tools"] = next
			changed = true
		}
	}
	if !changed {
		return body
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return out
}

// DefaultMarvisModelMapping 返回 Marvis 账号的默认模型映射。
//
// 键是用户请求时填的模型名，值是上游认的槽位名。调度器靠这张表判断
// 「这个账号能不能服务这个模型」，因此每个 Marvis 账号都必须带上它。
func DefaultMarvisModelMapping() map[string]any {
	out := make(map[string]any, len(marvisModels))
	for _, m := range marvisModels {
		out[m.Display] = m.Slot
	}
	return out
}

// ensureMarvisModelMappingCredentials 在凭据里补齐 model_mapping。
//
// 已有映射时保留用户的设置，只在其为空时写入默认表。
func ensureMarvisModelMappingCredentials(creds map[string]any) map[string]any {
	if creds == nil {
		creds = map[string]any{}
	}
	if existing, ok := creds["model_mapping"].(map[string]any); ok && len(existing) > 0 {
		return creds
	}
	creds["model_mapping"] = DefaultMarvisModelMapping()
	return creds
}

// marvisWithObjectType 递归补齐 JSON Schema 里缺失的 type:"object"。
//
// Marvis 要求每个对象型 schema 显式声明 type —— 只给 properties 不给 type
// 会被回 400 invalid parameter。客户端（尤其各类 Agent）常常省略它，
// 所以出站前统一补上。
//
// 与 magpie 的 tool-schema.mjs 保持同一套规则：只处理 properties 存在而
// type 缺失的情形，不猜别的类型（数组/字符串的语义无法从字段名推断）。
func marvisWithObjectType(schema any) (any, bool) {
	switch v := schema.(type) {
	case []any:
		changed := false
		out := make([]any, len(v))
		for i, item := range v {
			next, itemChanged := marvisWithObjectType(item)
			out[i] = next
			if itemChanged {
				changed = true
			}
		}
		if !changed {
			return schema, false
		}
		return out, true
	case map[string]any:
		changed := false
		out := make(map[string]any, len(v))
		for k, val := range v {
			out[k] = val
		}
		if _, hasType := out["type"]; !hasType {
			if props, ok := out["properties"].(map[string]any); ok && len(props) > 0 {
				out["type"] = "object"
				changed = true
			}
		}
		for _, key := range []string{"properties", "patternProperties"} {
			raw, ok := out[key].(map[string]any)
			if !ok {
				continue
			}
			next := make(map[string]any, len(raw))
			mapChanged := false
			for k, val := range raw {
				nv, ch := marvisWithObjectType(val)
				next[k] = nv
				if ch {
					mapChanged = true
				}
			}
			if mapChanged {
				out[key] = next
				changed = true
			}
		}
		for _, key := range []string{"items", "additionalProperties", "anyOf", "oneOf", "allOf"} {
			if val, ok := out[key]; ok {
				if nv, ch := marvisWithObjectType(val); ch {
					out[key] = nv
					changed = true
				}
			}
		}
		if !changed {
			return schema, false
		}
		return out, true
	}
	return schema, false
}

// marvisNormalizeTools 给每个工具的 parameters 补齐 type。
func marvisNormalizeTools(tools any) (any, bool) {
	list, ok := tools.([]any)
	if !ok {
		return tools, false
	}
	changed := false
	out := make([]any, len(list))
	for i, item := range list {
		tool, ok := item.(map[string]any)
		if !ok {
			out[i] = item
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			out[i] = item
			continue
		}
		params, hasParams := fn["parameters"]
		if !hasParams || params == nil {
			out[i] = item
			continue
		}
		nextParams, pChanged := marvisWithObjectType(params)
		if !pChanged {
			out[i] = item
			continue
		}
		nextFn := make(map[string]any, len(fn))
		for k, v := range fn {
			nextFn[k] = v
		}
		nextFn["parameters"] = nextParams
		nextTool := make(map[string]any, len(tool))
		for k, v := range tool {
			nextTool[k] = v
		}
		nextTool["function"] = nextFn
		out[i] = nextTool
		changed = true
	}
	if !changed {
		return tools, false
	}
	return out, true
}

// MarvisSlotProbe 是一次槽位探测的结果。
type MarvisSlotProbe struct {
	Slot string `json:"slot"`
	// ActualModel 是上游在响应体 model 字段里回的真实模型名。
	// 槽位名只是入参，真正跑的是这个。
	ActualModel string `json:"actual_model"`
	OK          bool   `json:"ok"`
	Error       string `json:"error,omitempty"`
}

// MarvisDiscoverSlots 逐个探测槽位，拿回上游实际返回的模型名。
//
// 上游没有「列出模型」的接口 —— 模型集合是靠槽位名点名的。但每个槽位
// 背后实际跑哪个模型只有上游知道（hy3-preview-agent 实际跑 deepseek），
// 而这个真名会出现在响应的 model 字段里。所以这里用一次最小请求把映射
// **问出来**，而不是把 magpie 表里那份快照当永久事实。
//
// 探测失败（400/超时）的槽位不进结果，避免把已下线的槽位写进账号映射。
func MarvisDiscoverSlots(ctx context.Context, account *Account) []MarvisSlotProbe {
	if account == nil {
		return nil
	}
	creds := marvisCredentialsOf(account)
	if creds.AccessToken == "" || creds.OpenID == "" {
		return nil
	}
	out := make([]MarvisSlotProbe, 0, len(marvisSlotsToProbe))
	for _, slot := range marvisSlotsToProbe {
		probe := MarvisSlotProbe{Slot: slot}
		actual, err := marvisProbeSlot(ctx, creds, slot)
		if err != nil {
			probe.Error = err.Error()
			out = append(out, probe)
			continue
		}
		probe.OK = true
		probe.ActualModel = actual
		out = append(out, probe)
	}
	return out
}

// marvisProbeSlot 发一次最小请求，返回响应体里的真实模型名。
func marvisProbeSlot(ctx context.Context, creds MarvisCredentials, slot string) (string, error) {
	body := map[string]any{
		"model":  slot,
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "1"},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, MarvisBaseURL+MarvisChatPath, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range marvisLLMHeaders(creds) {
		req.Header.Set(k, v)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		txt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(txt)))
	}

	// 只要第一行带 model 的 data chunk 就够，不读完整条流。
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 16*1024), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		chunk := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if chunk == "" || chunk == "[DONE]" {
			continue
		}
		var head struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal([]byte(chunk), &head); err != nil {
			continue
		}
		if strings.TrimSpace(head.Model) != "" {
			return strings.TrimSpace(head.Model), nil
		}
	}
	return "", fmt.Errorf("响应里没有 model 字段")
}

// marvisSlotsToProbe 是探测用的槽位清单。
//
// 这是**入参名**而不是模型清单：上游按这个名字路由，名字本身固定；
// 它背后跑什么模型由 DiscoverSlots 问出来。
var marvisSlotsToProbe = []string{
	"main-auto",
	"deepseek-v4-pro-external",
	"hy3-preview-agent",
	"hy3-preview-agent-real",
	"hunyuan-turbos-vision",
}

// marvisDiscoverModelMapping 现场探测各槽位，把「真实模型名 → 槽位名」
// 写进凭据。
//
// 探测逐个槽位发一次最小请求，拿到上游响应里的 model 字段。失败的槽位
// 不进映射；如果一个都没成功，退回静态表 —— 建号流程不能因为一次探测
// 失败而产出没有映射的账号（那会让账号完全调度不到）。
func marvisDiscoverModelMapping(ctx context.Context, creds map[string]any) map[string]any {
	if creds == nil {
		return creds
	}
	probeAccount := &Account{
		Platform:    PlatformMarvis,
		Type:        AccountTypeOAuth,
		Credentials: creds,
	}
	probes := MarvisDiscoverSlots(ctx, probeAccount)
	discovered := make(map[string]any, len(probes))
	for _, probe := range probes {
		if probe.OK && strings.TrimSpace(probe.ActualModel) != "" {
			discovered[probe.ActualModel] = probe.Slot
		}
	}
	if len(discovered) == 0 {
		// 全部失败：保留 ensureMarvisModelMappingCredentials 写入的静态表。
		return creds
	}
	creds["model_mapping"] = discovered
	// 记下这份映射是探测出来的，以及探测时间，便于运维判断是否过期。
	creds["model_mapping_source"] = "discovered"
	creds["model_mapping_probed_at"] = time.Now().UTC().Format(time.RFC3339)
	return creds
}

// startMarvisModelDiscovery 在后台探测账号的模型映射并落库。
//
// 独立 goroutine + 独立超时：建号请求可能已经被取消，探测不能跟着断。
// 失败只记日志 —— 账号此时已有静态表，不影响调度。
func (s *adminServiceImpl) startMarvisModelDiscovery(accountID int64) {
	if s == nil || s.accountRepo == nil || accountID <= 0 {
		return
	}
	go func() {
		// 给建号事务/缓存一点时间落定，避免读到旧快照。
		time.Sleep(2 * time.Second)

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()

		account, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil || account == nil {
			slog.Warn("marvis_discovery_load_failed", "account_id", accountID, "error", err)
			return
		}
		creds := account.Credentials
		if creds == nil {
			return
		}
		if src, _ := creds["model_mapping_source"].(string); src == "discovered" {
			// 已经探测过（例如幂等重放），不重复打上游。
			return
		}

		have := creds
		next := make(map[string]any, len(creds))
		for k, v := range creds {
			next[k] = v
		}
		next = marvisDiscoverModelMapping(ctx, next)
		if src, _ := next["model_mapping_source"].(string); src != "discovered" {
			slog.Info("marvis_discovery_skipped", "account_id", accountID,
				"reason", "no slot answered")
			return
		}

		// 只改映射相关的键，其余凭据字段原样保留 —— 异步执行期间
		// token 可能已被刷新，不能整份覆盖。
		merged := make(map[string]any, len(have)+3)
		for k, v := range have {
			merged[k] = v
		}
		merged["model_mapping"] = next["model_mapping"]
		merged["model_mapping_source"] = next["model_mapping_source"]
		merged["model_mapping_probed_at"] = next["model_mapping_probed_at"]

		fresh, err := s.accountRepo.GetByID(ctx, accountID)
		if err != nil || fresh == nil {
			return
		}
		fresh.Credentials = merged
		if err := s.accountRepo.Update(ctx, fresh); err != nil {
			slog.Warn("marvis_discovery_persist_failed", "account_id", accountID, "error", err)
			return
		}
		slog.Info("marvis_discovery_applied", "account_id", accountID,
			"models", len(next["model_mapping"].(map[string]any)))
	}()
}
