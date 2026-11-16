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
)

// WorkBuddy 图像生成的网关实现。
//
// 上游提供两个端点：
//
//	POST {site}/v2/images/generations  文生图
//	POST {site}/v2/images/edits        图生图（image 数组带 data URL）
//
// 请求体是**明文 JSON**，不能 gzip——上游把 body 直接按 JSON 反序列化，
// gzip 会撞上 0x1f 首字节并返回
// `400 11101 Unmarshal create image params failed`。
// 参考 magpie 的 images.test.mjs（iwinux 在 Discord 报的 0.1.43 回归）。
//
// 成功响应：{code: 0, data: {created, data: [{b64_json}]}}。
// 上游也可能直接返回 OpenAI 形状的 {created, data: [...]}，两种都接受。

const (
	// workBuddyImagesGenerationsPath 是文生图端点。
	workBuddyImagesGenerationsPath = "/v2/images/generations"
	// workBuddyImagesEditsPath 是图生图端点。
	workBuddyImagesEditsPath = "/v2/images/edits"
	// workBuddyImageRequestTimeout 是图像生成的超时。出图比对话慢得多，
	// 且这里不是流式，必须给足时间。
	workBuddyImageRequestTimeout = 300 * time.Second
)

// workBuddyImagesClient 是图像生成专用的 HTTP 客户端。
//
// 与活动接口的 30 秒客户端区分开：一次出图经常超过 30 秒。
func workBuddyImagesClient() *http.Client {
	return &http.Client{Timeout: workBuddyImageRequestTimeout}
}

// WorkBuddyImageRequest 是网关侧的图像请求（OpenAI Images API 形状）。
//
// 只保留 WorkBuddy 上游认识或能安全转发的字段。size / quality / style
// 之类 OpenAI 专属参数不转发——上游按自己的默认值出图。
type WorkBuddyImageRequest struct {
	Model          string   `json:"model"`
	Prompt         string   `json:"prompt"`
	N              int      `json:"n,omitempty"`
	Size           string   `json:"size,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	Image          []string `json:"image,omitempty"`
	Mask           string   `json:"mask,omitempty"`
}

// WorkBuddyImageResult 是转发结果：原样带回上游的 JSON 体。
type WorkBuddyImageResult struct {
	Body        []byte
	ContentType string
}

// WorkBuddyGenerateImage 调用上游图像端点。
//
// edit 为 true 时走 /v2/images/edits。调用方负责鉴权与并发控制。
func WorkBuddyGenerateImage(ctx context.Context, account *Account, req WorkBuddyImageRequest, edit bool) (*WorkBuddyImageResult, error) {
	if account == nil {
		return nil, fmt.Errorf("账号不能为空")
	}
	site := workBuddySiteForPlatform(account.Platform)
	if site == nil {
		return nil, fmt.Errorf("unsupported platform: %s", account.Platform)
	}
	token := account.workBuddyTokenOf()
	if token.Access == "" {
		return nil, fmt.Errorf("账号缺少 access_token")
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, fmt.Errorf("prompt 不能为空")
	}

	// 只带上游认识的字段：model / prompt / n / size / response_format / image。
	payload := map[string]any{
		"prompt": prompt,
	}
	if m := strings.TrimSpace(req.Model); m != "" {
		payload["model"] = m
	}
	if req.N > 0 {
		payload["n"] = req.N
	}
	if s := strings.TrimSpace(req.Size); s != "" {
		payload["size"] = s
	}
	// response_format 按客户端要求传；不指定则跟随上游默认（返回 url）。
	// 注意：上游实测会把 b64_json 也回成 url，因此这里不强求 b64。
	if rf := strings.TrimSpace(req.ResponseFormat); rf != "" {
		payload["response_format"] = rf
	}
	if len(req.Image) > 0 {
		payload["image"] = req.Image
	}
	if strings.TrimSpace(req.Mask) != "" {
		payload["mask"] = req.Mask
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	path := workBuddyImagesGenerationsPath
	if edit {
		path = workBuddyImagesEditsPath
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, site.Endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+token.Access)
	// 明文 JSON：不要设 Content-Encoding。
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")
	if uid := account.workBuddyUIDOf(); uid != "" {
		httpReq.Header.Set("X-User-Id", uid)
	}
	httpReq.Header.Set("X-Domain", workBuddySiteDomain(site))
	httpReq.Header.Set("X-Product", "SaaS")
	httpReq.Header.Set("User-Agent", "WorkBuddy/5.9.3 workbuddy/5.9.3 CLI/2.165.0")
	httpReq.Header.Set("Origin", site.Endpoint)
	httpReq.Header.Set("Referer", site.Endpoint+"/bot/chat")

	resp, err := workBuddyImagesClient().Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// 图片是 base64，单张就可能是几 MB；放宽到 64 MB。
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d: %s", resp.StatusCode, workBuddyImageErrorText(body))
	}
	normalized, err := normalizeWorkBuddyImageResponse(body)
	if err != nil {
		return nil, err
	}
	return &WorkBuddyImageResult{Body: normalized, ContentType: "application/json"}, nil
}

// normalizeWorkBuddyImageResponse 把上游响应统一成 OpenAI Images API 形状。
//
// 上游返回 {code, msg, data:{created, data:[...]}}；OpenAI 客户端要的是
// {created, data:[...]}。已经是对应形状时原样返回。
func normalizeWorkBuddyImageResponse(body []byte) ([]byte, error) {
	var envelope struct {
		Code *int            `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("无法解析上游响应: %w", err)
	}
	if envelope.Code != nil {
		if *envelope.Code != 0 {
			return nil, fmt.Errorf("上游业务错误 %d: %s", *envelope.Code, strings.TrimSpace(envelope.Msg))
		}
		if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
			return envelope.Data, nil
		}
		return nil, fmt.Errorf("上游返回空结果")
	}
	// 已经是 OpenAI 形状。
	return body, nil
}

// workBuddyImageErrorText 从错误响应里取出可读信息。
func workBuddyImageErrorText(body []byte) string {
	var env struct {
		Code       int    `json:"code"`
		Msg        string `json:"msg"`
		DisplayMsg struct {
			Zh string `json:"zh"`
			En string `json:"en"`
		} `json:"displayMsg"`
	}
	if err := json.Unmarshal(body, &env); err == nil {
		if msg := strings.TrimSpace(env.Msg); msg != "" {
			if env.Code != 0 {
				return fmt.Sprintf("%d %s", env.Code, msg)
			}
			return msg
		}
		if zh := strings.TrimSpace(env.DisplayMsg.Zh); zh != "" {
			return zh
		}
		if en := strings.TrimSpace(env.DisplayMsg.En); en != "" {
			return en
		}
	}
	text := strings.TrimSpace(string(body))
	if len(text) > 300 {
		text = text[:300] + "..."
	}
	if text == "" {
		text = "(empty body)"
	}
	return text
}

// WorkBuddyImageModels 返回可用于图像生成的模型 ID。
//
// 上游模型目录里 hunyuan-image-alpha 是图像模型，不能当对话模型用
// （对话请求会得到 11103 Backend [hunyuan-stream] is not supported）。
func WorkBuddyImageModels() []string {
	return []string{"hunyuan-image-alpha", "gpt-image-2.5-sunburst"}
}

// IsWorkBuddyImageModel 判定模型是否属于图像生成模型。
func IsWorkBuddyImageModel(model string) bool {
	m := strings.ToLower(strings.TrimSpace(model))
	if m == "" {
		return false
	}
	for _, id := range WorkBuddyImageModels() {
		if m == id {
			return true
		}
	}
	return false
}

// AcquireWorkBuddyImageAccount 为一次图像请求挑一个可用账号。
//
// 图像请求不走对话的调度器（那条路径带模型评分、粘性会话等，
// 对出图没意义），这里只做必要的可用性筛选：
// 分组内、平台是 WorkBuddy、账号 active、有 access_token。
//
// 返回值 release 恒为 nil，保留给将来接入并发槽位。
func (s *OpenAIGatewayService) AcquireWorkBuddyImageAccount(ctx context.Context, apiKey *APIKey) (*Account, func(), error) {
	if s == nil || s.accountRepo == nil {
		return nil, nil, fmt.Errorf("账号仓储不可用")
	}
	if apiKey == nil || apiKey.GroupID == nil {
		return nil, nil, fmt.Errorf("API Key 未绑定分组，无法选择账号")
	}

	accounts, err := s.accountRepo.ListSchedulableByGroupID(ctx, *apiKey.GroupID)
	if err != nil {
		return nil, nil, fmt.Errorf("读取可用账号失败: %w", err)
	}
	var lastErr error
	for i := range accounts {
		acc := &accounts[i]
		if !isWorkBuddyPlatform(acc.Platform) {
			continue
		}
		if acc.Status != StatusActive {
			continue
		}
		if acc.workBuddyTokenOf().Access == "" {
			lastErr = fmt.Errorf("账号 %d 缺少 access_token", acc.ID)
			continue
		}
		return acc, nil, nil
	}
	if lastErr != nil {
		return nil, nil, lastErr
	}
	return nil, nil, fmt.Errorf("分组内没有可用的 WorkBuddy 图像账号")
}
