package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Trae 上游调用：请求头、模型目录、额度。
//
// 头与路径取自 magpie 的 Trae 提供方（@magpie-community/opencode-trae-auth）：
// 对话走 /api/agent/v3/llm_utils_chat，模型目录走 /api/ide/v1/batch_get_detail_param，
// 额度国内站走 /trae/api/v2/pay/ide_user_ent_usage、国际站走
// /trae/api/v1/pay/user_current_entitlement_list。

// traeAPITimeout 是目录/额度请求的超时。
const traeAPITimeout = 25 * time.Second

// traeClient 是 Trae 上游用的 HTTP 客户端。
var traeClient = &http.Client{Timeout: traeAPITimeout}

// applyTraeHeaders 写入 IDE 客户端的整套请求头。
func applyTraeHeaders(h http.Header, site *traeSite, token traeToken) {
	if site == nil {
		return
	}
	device := token.Device
	deviceID, machineID := "", ""
	if device != nil {
		deviceID, machineID = device.DeviceID, device.MachineID
	}
	h.Set("Content-Type", "application/json")
	h.Set("Accept", "application/json")
	h.Set("Authorization", "Cloud-IDE-JWT "+token.Access)
	h.Set("X-Cloudide-Token", token.Access)
	h.Set("x-ide-token", token.Access)
	h.Set("x-uid", token.UID)
	h.Set("x-app-id", site.AppID)
	h.Set("x-device-id", deviceID)
	h.Set("x-machine-id", machineID)
	h.Set("x-app-version", site.ClientVersion)
	h.Set("x-app-version-code", site.ClientVersionCode)
	h.Set("x-ide-version", site.ClientVersion)
	h.Set("x-ide-version-code", site.ClientVersionCode)
	h.Set("x-ide-version-type", "stable")
	h.Set("x-device-cpu", "AMD")
	h.Set("x-device-brand", site.Brand)
	h.Set("x-device-type", "windows")
	h.Set("x-os-version", "Windows 10")
	h.Set("x-system-type", "Windows")
	h.Set("User-Agent", "TRAE SOLO CN/"+site.ClientVersion)
}

// traeDoJSON 发一次 JSON 请求并返回原始响应体。
func traeDoJSON(ctx context.Context, method, endpoint string, site *traeSite, token traeToken, body []byte) ([]byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, err
	}
	applyTraeHeaders(req.Header, site, token)
	resp, err := traeClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		code := traeUpstreamCode(raw)
		if code == 1001 || code == 20101 || resp.StatusCode == http.StatusUnauthorized {
			return nil, errTraeSignInLapsed
		}
		return nil, fmt.Errorf("trae: 上游 HTTP %d: %s", resp.StatusCode, traeTrimBody(raw))
	}
	return raw, nil
}

// errTraeSignInLapsed 表示登录态失效，需要重新授权。
var errTraeSignInLapsed = errors.New("trae: 登录态已失效，请重新授权")

// traeUpstreamCode 从响应体里取业务错误码。
func traeUpstreamCode(raw []byte) int {
	var envelope struct {
		Code  int `json:"code"`
		Error struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return 0
	}
	if envelope.Code != 0 {
		return envelope.Code
	}
	return envelope.Error.Code
}

// ---------- 模型目录 ----------

// traeModel 是目录里的一个模型。
type traeModel struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Context   int      `json:"context,omitempty"`
	MaxOutput int      `json:"max_output,omitempty"`
	Efforts   []string `json:"efforts,omitempty"`
	Function  string   `json:"function,omitempty"`
	// DevModel 是该条目 model_detail_list 里以 __dev 结尾的 model_name。
	//
	// 上游发对话时必须带这个值（payload 的 model_name 字段），
	// 只带 config_name 会得到「failed to get app config: record not found」。
	DevModel string `json:"dev_model,omitempty"`
	// MaxModel 是 model_detail_list 里以 __max 结尾的 model_name，Max 模式用。
	MaxModel string `json:"max_model,omitempty"`
}

// traeChatFunctions 是对话可用的 function，按优先级排列（与 magpie 一致）。
var traeChatFunctions = []string{"chat_v3", "solo_work_lite", "solo_agent", "solo_agent_lite", "solo_coder"}

// fetchTraeModels 拉取账号的模型目录。
//
// 先批量问一次 batch_get_detail_param；拿不到内容时退回逐个 function 的
// get_detail_param。返回的列表按上游顺序，并合并出 -max 变体。
func fetchTraeModels(ctx context.Context, site *traeSite, token traeToken) ([]traeModel, error) {
	if site == nil {
		return nil, errors.New("trae: 站点为空")
	}
	hosts := site.traeHostsFor(token.Region)
	host := strings.TrimRight(hosts.ModelHost, "/")

	// 参数与 magpie 的 batchLists 一致。只传 functions 也能拿到列表，
	// 但上游对参数缺失的容忍度不稳定，补齐更稳妥。
	body, err := json.Marshal(map[string]any{
		"functions":                 traeChatFunctions,
		"agent_type":                "",
		"current_config_info":       map[string]any{"config_name": "", "is_custom_model": false},
		"mode_type":                 0,
		"access_type":               0,
		"ab_force_vids":             "",
		"ab_autotest_advanced_mode": 0,
		"show_custom_model":         true,
	})
	if err == nil {
		if raw, callErr := traeDoJSON(ctx, http.MethodPost, host+"/api/ide/v1/batch_get_detail_param", site, token, body); callErr == nil {
			if models := parseTraeModelCatalog(raw); len(models) > 0 {
				return models, nil
			}
		}
	}
	for _, function := range traeChatFunctions {
		payload, _ := json.Marshal(map[string]any{"function": function})
		raw, callErr := traeDoJSON(ctx, http.MethodPost, host+"/api/ide/v1/get_detail_param", site, token, payload)
		if callErr != nil {
			continue
		}
		if models := parseTraeModelCatalog(raw); len(models) > 0 {
			for i := range models {
				if models[i].Function == "" {
					models[i].Function = function
				}
			}
			return models, nil
		}
	}
	return nil, errors.New("trae: 模型目录为空（上游未返回可用列表）")
}

// parseTraeModelCatalog 从目录响应里提取模型。
//
// 上游的结构在不同版本间变过，这里按「递归找带 model 名字的条目」处理：
// 只有同时出现模型 id 与可选上下文信息的对象才算一条，避免把配置项当成模型。
func parseTraeModelCatalog(raw []byte) []traeModel {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil
	}
	var models []traeModel
	seen := map[string]struct{}{}
	var walk func(node any, function string)
	walk = func(node any, function string) {
		switch typed := node.(type) {
		case map[string]any:
			if name := firstNonEmpty(
				traeStringValue(typed, "config_name"),
				traeStringValue(typed, "configName"),
			); name != "" {
				dev, maxM := traeDetailModels(typed)
				if _, dup := seen[name]; !dup {
					seen[name] = struct{}{}
					item := traeModel{ID: name, Name: firstNonEmpty(traeStringValue(typed, "name", "display_name"), name), DevModel: dev, MaxModel: maxM, Function: function}
					if ctxWindow, ok := typed["context_window_tokens"].(map[string]any); ok {
						item.Context = int(traeNumberValue(ctxWindow, "dev", "max"))
					}
					if v := int(traeNumberValue(typed, "max_tokens", "max_output_tokens")); v > 0 {
						item.MaxOutput = v
					}
					if effort, ok := typed["reasoning_effort_config"].(map[string]any); ok {
						for _, level := range traeReasoningLevels(effort) {
							item.Efforts = append(item.Efforts, level)
						}
					}
					models = append(models, item)
				}
			}
			if fn := traeStringValue(typed, "function", "function_name"); fn != "" {
				function = fn
			}
			for _, value := range typed {
				walk(value, function)
			}
		case []any:
			for _, value := range typed {
				walk(value, function)
			}
		}
	}
	walk(root, "")
	if len(models) == 0 {
		return nil
	}
	// -max 变体：条目同时给了 __max 与更大的上下文窗口时补一条。
	expanded := make([]traeModel, 0, len(models))
	byID := make(map[string]traeModel, len(models))
	for _, item := range models {
		expanded = append(expanded, item)
		byID[item.ID] = item
	}
	sort.SliceStable(expanded, func(i, j int) bool { return false })
	return expanded
}

// traeReasoningLevels 把 Trae 的档位名映射成网关通用档位。
func traeReasoningLevels(config map[string]any) []string {
	rawLevels, _ := config["options"].([]any)
	if len(rawLevels) == 0 {
		if single := traeStringValue(config, "effort"); single != "" {
			rawLevels = []any{single}
		}
	}
	out := make([]string, 0, len(rawLevels))
	for _, raw := range rawLevels {
		name, _ := raw.(string)
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "light", "low":
			out = append(out, "low")
		case "medium":
			out = append(out, "medium")
		case "high":
			out = append(out, "high")
		case "extra_high", "xhigh":
			out = append(out, "xhigh")
		case "max":
			out = append(out, "max")
		}
	}
	return out
}

// ---------- 额度 ----------

// TraeCredits 是一个账号的额度快照。
//
// NativeUnit 是上游的原生单位（国内站 credits、国际站 usd）；Limit/Used 是原生
// 数值。展示时统一折算成积分（1 美元 = 100 积分，见 TraeCreditsAsPoints），
// 原始数值一并保留。
type TraeCredits struct {
	Platform   string  `json:"platform"`
	NativeUnit string  `json:"native_unit"`
	Limit      float64 `json:"limit"`
	Used       float64 `json:"used"`
	Unlimited  bool    `json:"unlimited"`
	// Plan 是套餐名（上游给什么就显示什么）。
	Plan string `json:"plan,omitempty"`
	// UpdatedAt 是抓取时间。
	UpdatedAt time.Time `json:"updated_at"`
}

// fetchTraeCredits 读取账号额度。
func fetchTraeCredits(ctx context.Context, site *traeSite, token traeToken) (*TraeCredits, error) {
	if site == nil {
		return nil, errors.New("trae: 站点为空")
	}
	hosts := site.traeHostsFor(token.Region)
	payHost := strings.TrimRight(hosts.PayHost, "/")
	out := &TraeCredits{Platform: site.ID, NativeUnit: site.NativeUnit, UpdatedAt: time.Now()}

	if site.ID == PlatformTraeCN {
		body, _ := json.Marshal(map[string]any{"require_usage": true, "req_source": 0})
		raw, err := traeDoJSON(ctx, http.MethodPost, payHost+"/trae/api/v2/pay/ide_user_ent_usage", site, token, body)
		if err != nil {
			return nil, err
		}
		limit, used, unlimited := traeSumCredits(raw)
		out.Limit, out.Used, out.Unlimited = limit, used, unlimited
		return out, nil
	}

	body, _ := json.Marshal(map[string]any{})
	raw, err := traeDoJSON(ctx, http.MethodPost, payHost+"/trae/api/v1/pay/user_current_entitlement_list", site, token, body)
	if err != nil {
		return nil, err
	}
	limit, used, unlimited := traeSumDollars(raw)
	out.Limit, out.Used, out.Unlimited = limit, used, unlimited
	return out, nil
}

// traeSumCredits 汇总国内站的积分包。
func traeSumCredits(raw []byte) (float64, float64, bool) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return 0, 0, false
	}
	limit, used, unlimited := 0.0, 0.0, false
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			if quota, ok := typed["quota"].(map[string]any); ok {
				if l := traeNumberValue(quota, "credits_limit"); l != 0 {
					if l < 0 {
						unlimited = true
					} else {
						limit += l
					}
				}
			}
			if u := traeNumberValue(typed, "credits_amount"); u > 0 {
				used += u
			}
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(root)
	return limit, used, unlimited
}

// traeSumDollars 汇总国际站的美元包。
func traeSumDollars(raw []byte) (float64, float64, bool) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return 0, 0, false
	}
	limit, used := 0.0, 0.0
	var walk func(node any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			limit += traeNumberValue(typed, "basic_usage_limit", "bonus_usage_limit")
			used += traeNumberValue(typed, "basic_usage_amount", "bonus_usage_amount")
			for _, value := range typed {
				walk(value)
			}
		case []any:
			for _, value := range typed {
				walk(value)
			}
		}
	}
	walk(root)
	return limit, used, false
}

// traeUSDToCredits 是国际站「美元 → 积分」的展示折算率。
//
// 产品口径：Auto2API 内所有平台都按积分显示。国际站原生按美元计费，
// 展示时 1 美元 = 100 积分（仅换算，不改上游计费）。国内站本身就是积分，不折算。
const traeUSDToCredits = 100

// TraeCreditsPerUSD 暴露展示折算率给接口层，避免前端/文档再写死一份。
func TraeCreditsPerUSD() float64 { return traeUSDToCredits }

// TraeCreditsAsPoints 把额度按展示口径折算成积分。
func TraeCreditsAsPoints(c *TraeCredits) (limit, used float64) {
	if c == nil {
		return 0, 0
	}
	if c.NativeUnit == traeUnitUSD {
		return c.Limit * traeUSDToCredits, c.Used * traeUSDToCredits
	}
	return c.Limit, c.Used
}

// traeDetailModels 从模型条目里取出 __dev / __max 两个 model_name。
//
// 上游把可用模型放在 config_info_list 里，每个条目的 model_detail_list
// 才是真正发给上游的型号：
//
//	config_name:        "Doubao-Seed-2.1-Turbo"        ← 请求里的 config_name/model
//	model_detail_list:  [{"model_name": "...__dev"}]   ← 请求里的 model_name
//
// 只传 config_name 不带 model_name，上游会回
// 「failed to get app config: record not found」，表现为空回复。
func traeDetailModels(entry map[string]any) (dev string, max string) {
	raw, ok := entry["model_detail_list"].([]any)
	if !ok {
		return "", ""
	}
	for _, item := range raw {
		detail, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name := traeStringValue(detail, "model_name", "name")
		if name == "" {
			continue
		}
		switch {
		case strings.HasSuffix(name, "__dev") && dev == "":
			dev = name
		case strings.HasSuffix(name, "__max") && max == "":
			max = name
		}
	}
	return dev, max
}

// traeDevModelNameOf 从账号凭据里取某个模型的 __dev 型号。
//
// 模型目录在账号创建/刷新时写进 credentials.model_mapping（模型名 → 模型名），
// 这里额外读 model_dev_names（模型名 → __dev 型号）。老账号没有这个字段时
// 退回「模型名 + __dev」的约定——上游所有条目都是这个命名。
func traeDevModelNameOf(credentials map[string]any, model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if raw, ok := credentials["model_dev_names"].(map[string]any); ok {
		if v, ok := raw[model].(string); ok && v != "" {
			return v
		}
	}
	// 已经是 __dev 结尾就直接用
	if strings.HasSuffix(model, "__dev") {
		return model
	}
	return model + "__dev"
}
