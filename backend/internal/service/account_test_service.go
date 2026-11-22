package service

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/DeltrivX/Auto2API/internal/config"
	"github.com/DeltrivX/Auto2API/internal/pkg/claude"
	"github.com/DeltrivX/Auto2API/internal/pkg/openai"
	"github.com/DeltrivX/Auto2API/internal/pkg/openai_compat"
	"github.com/DeltrivX/Auto2API/internal/util/urlvalidator"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// sseDataPrefix matches SSE data lines with optional whitespace after colon.
// Some upstream APIs return non-standard "data:" without space (should be "data: ").
var sseDataPrefix = regexp.MustCompile(`^data:\s*`)

const (
	testClaudeAPIURL            = "https://api.anthropic.com/v1/messages?beta=true"
	chatgptCodexAPIURL          = "https://chatgpt.com/backend-api/codex/responses"
	defaultAntigravityTestModel = "claude-sonnet-4-6"
)

// TestEvent represents a SSE event for account testing
type TestEvent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Model    string `json:"model,omitempty"`
	Status   string `json:"status,omitempty"`
	Code     string `json:"code,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
	// AudioURL / VideoURL are data: or https URLs for in-browser media players.
	AudioURL string `json:"audio_url,omitempty"`
	VideoURL string `json:"video_url,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	Data     any    `json:"data,omitempty"`
	Success  bool   `json:"success,omitempty"`
	Error    string `json:"error,omitempty"`
}

// AccountTestOptions carries optional media for admin connectivity tests.
// ImageDataURL / AudioDataURL are full data URLs (data:<mime>;base64,...).
type AccountTestOptions struct {
	ImageDataURL string
	AudioDataURL string
}

func firstAccountTestOptions(opts []AccountTestOptions) AccountTestOptions {
	if len(opts) == 0 {
		return AccountTestOptions{}
	}
	return opts[0]
}

// maxAccountTestMediaBytes caps inbound data-URL payloads for admin tests (~8 MiB).
const maxAccountTestMediaBytes = 8 << 20

const (
	defaultGeminiTextTestPrompt  = "hi"
	defaultGeminiImageTestPrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
	defaultOpenAIImageTestPrompt = "Generate a cute orange cat astronaut sticker on a clean pastel background."
	defaultGrokImageTestPrompt   = "Generate a cute orange cat astronaut sticker on a clean pastel background."
	defaultGrokVideoTestPrompt   = "A red ball bouncing once on a white floor, short simple motion."
	defaultGrokSearchTestQuery   = "xAI Grok"
	defaultGrokTTSTestText       = "Hello from Auto2API account connectivity test."

	// Grok account-test modes (admin UI). Empty / default / text = Responses probe.
	// image/video may also be inferred from model_id when mode is default.
	AccountTestModeGrokText     = "text"
	AccountTestModeGrokImage    = "image"
	AccountTestModeGrokVideo    = "video"
	AccountTestModeGrokSearch   = "search"
	AccountTestModeGrokTTS      = "tts"
	AccountTestModeGrokSTT      = "stt"
	AccountTestModeGrokRealtime = "realtime"

	defaultGrokRealtimeTestModel = "grok-voice-latest"
	grokRealtimeProbeTimeout     = DefaultGrokRealtimeDialTimeout
)

// isOpenAIImageModel checks if the model is an OpenAI image generation model (e.g. gpt-image-2).
func isOpenAIImageModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "gpt-image-")
}

func isGrokVideoGenerationModel(model string) bool {
	return isGrokVideoBillingModel(model) ||
		strings.HasPrefix(strings.ToLower(strings.TrimSpace(model)), "grok-video")
}

func normalizeGrokAccountTestMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case AccountTestModeGrokText:
		return AccountTestModeGrokText
	case AccountTestModeGrokImage:
		return AccountTestModeGrokImage
	case AccountTestModeGrokVideo:
		return AccountTestModeGrokVideo
	case AccountTestModeGrokSearch:
		return AccountTestModeGrokSearch
	case AccountTestModeGrokTTS:
		return AccountTestModeGrokTTS
	case AccountTestModeGrokSTT:
		return AccountTestModeGrokSTT
	case AccountTestModeGrokRealtime:
		return AccountTestModeGrokRealtime
	default:
		return AccountTestModeDefault
	}
}

// AccountTestService handles account testing operations
type AccountTestService struct {
	accountRepo             AccountRepository
	claudeTokenProvider     *ClaudeTokenProvider
	httpUpstream            HTTPUpstream
	cfg                     *config.Config
	settingService          *SettingService
	tlsFPProfileService     *TLSFingerprintProfileService
	modelMetadataRegistryMu sync.Mutex
	modelMetadataRegistry   map[string]modelsDevProvider
	modelMetadataRegistryAt time.Time
	openaiGatewayService    *OpenAIGatewayService
	agentIdentityTaskMu     sync.Mutex
	agentIdentityWS         agentIdentityWSConnectionInvalidator
	// grokWSDialer is optional; realtime account tests use the default OpenAI-style
	// WS dialer when nil (supports proxy + coder/websocket handshake).
	grokWSDialer openAIWSClientDialer
}

func (s *AccountTestService) SetSettingService(settingService *SettingService) {
	if s != nil {
		s.settingService = settingService
	}
}

func (s *AccountTestService) SetOpenAIGatewayService(gateway *OpenAIGatewayService) {
	if s != nil {
		s.openaiGatewayService = gateway
	}
}

// FetchOpenAIAccountModels uses the shared cached discovery path for the test picker.
// It only fills picker-only gaps (local display-name fallbacks, OAuth image choices)
// on its own copy; the shared catalog and its cache stay untouched.
func (s *AccountTestService) FetchOpenAIAccountModels(ctx context.Context, account *Account) ([]openai.Model, error) {
	if s == nil || s.openaiGatewayService == nil {
		return nil, errors.New("OpenAI model discovery service is unavailable")
	}
	response, err := s.openaiGatewayService.FetchOpenAIModelsList(ctx, account)
	if err != nil {
		return nil, err
	}
	// The shared discovery response is the raw upstream catalog. Project it
	// through the account mapping before exposing it in the admin picker so
	// configured aliases remain public names and unconfigured models stay out.
	projectedBody, err := projectAccountModelsBody(response.Body, account, nil, false)
	if err != nil {
		return nil, fmt.Errorf("project OpenAI account models: %w", err)
	}
	var payload struct {
		Data []openai.Model `json:"data"`
	}
	if err := json.Unmarshal(projectedBody, &payload); err != nil {
		return nil, fmt.Errorf("decode OpenAI account models: %w", err)
	}
	// Every entry in the picker is labelled by the same rule: the upstream display
	// name when the catalog has one, otherwise the local catalog name for that model
	// ID, otherwise the raw ID. Without this the picker mixes "GPT-5.6 Sol" with
	// "gpt-5.6-sol" for the same catalog.
	for i := range payload.Data {
		model := &payload.Data[i]
		if strings.TrimSpace(model.DisplayName) == "" {
			model.DisplayName = model.ID
		}
		if strings.TrimSpace(model.Type) == "" {
			model.Type = "model"
		}
	}
	// Codex discovery lists Responses drivers, not image_generation tool models.
	// Add locally supported image choices only to the OAuth test picker; keep the
	// shared upstream catalog and API-key discovery authoritative.
	if account != nil && account.IsOpenAIOAuthLike() {
		passthrough := account.IsOpenAIPassthroughEnabled()
		seen := make(map[string]bool, len(payload.Data))
		for _, model := range payload.Data {
			seen[model.ID] = true
		}
		for _, model := range openai.DefaultModels {
			if IsGPTImageGenerationModel(model.ID) && account.IsModelSupported(model.ID) && !seen[model.ID] {
				if !passthrough && !IsGPTImageGenerationModel(account.GetMappedModel(model.ID)) {
					continue
				}
				payload.Data = append(payload.Data, model)
				seen[model.ID] = true
			}
		}
		// Image models that a configured alias points at are absent from the Codex
		// manifest, so the projection alone cannot surface them. Resolve each public
		// name to its target and keep the entry when that target is an image model.
		// Judging by the target rather than the public name keeps a lookalike name
		// (for example an alias spelled "gpt-image-*" that maps to a text model)
		// from being synthesized into the picker.
		// Passthrough keeps native image names without applying mapping targets.
		for publicID := range account.GetModelMapping() {
			if strings.Contains(publicID, "*") || seen[publicID] {
				continue
			}
			target := publicID
			if !passthrough {
				target = account.GetMappedModel(publicID)
			}
			if !IsGPTImageGenerationModel(target) {
				continue
			}
			payload.Data = append(payload.Data, openai.Model{ID: publicID, Object: "model", Type: "model", OwnedBy: "openai", DisplayName: publicID})
			seen[publicID] = true
		}
	}
	return payload.Data, nil
}

// NewAccountTestService creates a new AccountTestService
func NewAccountTestService(
	accountRepo AccountRepository,
	claudeTokenProvider *ClaudeTokenProvider,
	httpUpstream HTTPUpstream,
	cfg *config.Config,
	tlsFPProfileService *TLSFingerprintProfileService,
) *AccountTestService {
	return &AccountTestService{
		accountRepo:         accountRepo,
		claudeTokenProvider: claudeTokenProvider,
		httpUpstream:        httpUpstream,
		cfg:                 cfg,
		tlsFPProfileService: tlsFPProfileService,
	}
}

func (s *AccountTestService) validateUpstreamBaseURL(raw string) (string, error) {
	if s.cfg == nil {
		return "", errors.New("config is not available")
	}
	if !s.cfg.Security.URLAllowlist.Enabled {
		return urlvalidator.ValidateURLFormat(raw, s.cfg.Security.URLAllowlist.AllowInsecureHTTP)
	}
	normalized, err := urlvalidator.ValidateHTTPSURL(raw, urlvalidator.ValidationOptions{
		AllowedHosts:     s.cfg.Security.URLAllowlist.UpstreamHosts,
		RequireAllowlist: true,
		AllowPrivate:     s.cfg.Security.URLAllowlist.AllowPrivateHosts,
	})
	if err != nil {
		return "", err
	}
	return normalized, nil
}

// generateSessionString generates a Claude Code style session string.
// The output format is determined by the UA version in claude.DefaultHeaders(),
// ensuring consistency between the user_id format and the UA sent to upstream.
func generateSessionString() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	hex64 := hex.EncodeToString(b)
	sessionUUID := uuid.New().String()
	uaVersion := ExtractCLIVersion(claude.DefaultHeaders()["User-Agent"])
	return FormatMetadataUserID(hex64, "", sessionUUID, uaVersion), nil
}

// createTestPayload creates a Claude Code style test request payload
func createTestPayload(modelID string) (map[string]any, error) {
	sessionID, err := generateSessionString()
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "text",
						"text": "hi",
						"cache_control": map[string]string{
							"type": "ephemeral",
						},
					},
				},
			},
		},
		"system": []map[string]any{
			{
				"type": "text",
				"text": claudeCodeSystemPrompt,
				"cache_control": map[string]string{
					"type": "ephemeral",
				},
			},
		},
		"metadata": map[string]string{
			"user_id": sessionID,
		},
		"max_tokens":  1024,
		"temperature": 1,
		"stream":      true,
	}, nil
}

// TestAccountConnection tests an account's connection by sending a test request
// All account types use full Claude Code client characteristics, only auth header differs
// modelID is optional - if empty, defaults to claude.DefaultTestModel
// mode is optional - "compact" routes OpenAI accounts to the /responses/compact probe path
// opts is optional media (image/audio data URLs for real generation / STT).
func (s *AccountTestService) TestAccountConnection(c *gin.Context, accountID int64, modelID string, prompt string, mode string, opts ...AccountTestOptions) error {
	initAccountTestLogger(c, accountID, modelID, mode)
	ctx := c.Request.Context()
	_ = firstAccountTestOptions(opts)

	// Get account
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Account not found")
	}
	bindAccountTestPlatform(c, account)

	// Synthetic UI load-test accounts exercise the real SSE parsing and modal
	// interactions, but intentionally do not send their placeholder credentials
	// to an upstream provider.
	if account.IsSyntheticUITest() {
		testModelID := modelID
		if testModelID == "" {
			testModelID = claude.DefaultTestModel
		}
		s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
		s.sendEvent(c, TestEvent{Type: "content", Text: "Synthetic Anthropic OAuth account is healthy and interactive."})
		s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
		return nil
	}

	// Route to platform-specific test method
	// WorkBuddy 必须在所有分流之前拦下：它的 provider profile 声明了
	// 按入站协议分流，会先命中多协议分支并索要 API Key——而 WorkBuddy
	// 是 OAuth 账号，只有 access token，最终报 "No API key available"。
	// Trae 与 WorkBuddy 同理：OAuth 账号没有 API Key，必须走自己的对话链路。
	if IsTraePlatform(account.Platform) {
		testModelID := strings.TrimSpace(modelID)
		if testModelID == "" {
			testModelID = firstTraeModelID(account)
		}
		return s.testTraeAccountConnection(c, account, testModelID, prompt)
	}
	if IsWorkBuddyAccount(account) {
		testModelID := strings.TrimSpace(modelID)
		if testModelID == "" {
			testModelID = account.providerDefaultTestModel()
		}
		if testModelID == "" {
			testModelID = DefaultWorkBuddyTestModel
		}
		return s.testWorkBuddyAccountConnection(c, account, testModelID, prompt)
	}

	// 按入站协议分流的多协议供应商（国产厂商等）：按账号协议选测试路径。
	if account.RoutesProtocolByInbound() {
		switch account.GetAPIProtocol() {
		case APIProtocolAdaptive:
			return s.testCNProviderAdaptiveConnection(c, account, modelID, prompt)
		case APIProtocolResponses:
			return s.testOpenAIAccountConnection(c, account, modelID, prompt, normalizeAccountTestMode(mode))
		case APIProtocolChatCompletions:
			return s.testCNProviderChatCompletionsConnection(c, account, modelID, prompt)
		case APIProtocolAnthropic:
			return s.testCNProviderAnthropicConnection(c, account, modelID)
		}
	}

	// WorkBuddy 两个站点只有一种上游协议：流式 Chat Completions。
	// 必须在下面的分流之前拦下——它既不是 OpenAI OAuth，也不该走按模型
	// 分流的分支（那条路要求 API Key，WorkBuddy 只有 OAuth token）。
	if account.IsOpenAI() {
		return s.testOpenAIAccountConnection(c, account, modelID, prompt, normalizeAccountTestMode(mode))
	}

	// 按模型分流的多模型聚合平台（OpenCode、Command Code 等）。
	if account.routesByModel() {
		return s.testModelRoutedAccountConnection(c, account, modelID, prompt)
	}

	if false {
		return nil
	}

	return s.testClaudeAccountConnection(c, account, modelID)
}

// testModelRoutedAccountConnection probes the native endpoint for the selected
// model on providers that route by model (see ProviderRoutingByModel). Adaptive
// accounts (the default) follow the provider's protocol rules, e.g. OpenCode Go:
// grok/gpt/muse-spark → Responses, minimax/qwen → Anthropic, everything else
// (including deepseek-v4-flash) → Chat Completions. A pinned api_protocol
// overrides that catalog. Falling through to the generic Claude tester used
// credentials.base_url + /v1/messages?beta=true, which 404s as HTML on
// https://opencode.ai/zen/go/v1/v1/messages.
func (s *AccountTestService) testModelRoutedAccountConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = account.providerDefaultTestModel()
	}
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}
	testModelID = account.GetMappedModel(testModelID)
	// 与网关同一判定（含上游模型目录）；测试没有入站协议，取模型的首选协议。
	protocol := account.resolveModelRoutedProtocol(testModelID)
	if s.openaiGatewayService != nil {
		protocol = s.openaiGatewayService.resolveUpstreamProtocolFor(c.Request.Context(), account, "", testModelID)
	}
	// WorkBuddy 两个站点只提供 Chat Completions（流式）。
	// 若按模型首选协议判成 Responses，会走进需要 API Key 的分支并报
	// "No API key available"——WorkBuddy 是 OAuth 账号，根本没有 API Key。
	if IsWorkBuddyAccount(account) {
		protocol = APIProtocolChatCompletions
	}
	switch protocol {
	case APIProtocolAnthropic:
		return s.testCNProviderAnthropicConnection(c, account, testModelID)
	case APIProtocolResponses:
		return s.testModelRoutedResponsesConnection(c, account, testModelID)
	default:
		return s.testCNProviderChatCompletionsConnection(c, account, testModelID, prompt)
	}
}

func (s *AccountTestService) testModelRoutedResponsesConnection(c *gin.Context, account *Account, testModelID string) error {
	authToken := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	return s.testCNProviderAdaptiveResponsesConnection(c, account, testModelID, authToken)
}

func (s *AccountTestService) testCNProviderChatCompletionsConnection(c *gin.Context, account *Account, modelID string, prompt string) error {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = account.providerDefaultTestModel()
	}
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}
	testModelID = account.GetMappedModel(testModelID)

	authToken := strings.TrimSpace(account.GetOpenAIProtocolAPIKey())
	if authToken == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}

	baseURL := account.GetOpenAIBaseURL()
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
	}

	return s.testOpenAIChatCompletionsConnection(c, account, testModelID, prompt, normalizedBaseURL, authToken)
}

// testClaudeAccountConnection tests an Anthropic Claude account's connection
func (s *AccountTestService) testClaudeAccountConnection(c *gin.Context, account *Account, modelID string) error {
	ctx := c.Request.Context()

	// Determine the model to use
	testModelID := modelID
	if testModelID == "" {
		testModelID = claude.DefaultTestModel
	}

	// API Key 账号测试连接时也需要应用通配符模型映射。
	if account.Type == "apikey" {
		testModelID = account.GetMappedModel(testModelID)
	}

	if account.Type == AccountTypeServiceAccount {
		return s.testClaudeVertexServiceAccountConnection(c, ctx, account, testModelID)
	}

	// Determine authentication method and API URL
	var authToken string
	var apiURL string

	if account.IsOAuth() {
		apiURL = testClaudeAPIURL
		authToken = account.GetCredential("access_token")
		if authToken == "" {
			return s.sendErrorAndEnd(c, "No access token available")
		}
	} else if account.Type == "apikey" {
		authToken = account.GetCredential("api_key")
		if authToken == "" {
			return s.sendErrorAndEnd(c, "No API key available")
		}

		baseURL := account.GetBaseURL()
		if baseURL == "" {
			baseURL = "https://api.anthropic.com"
		}
		normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = buildAnthropicMessagesURL(normalizedBaseURL)
	} else {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported account type: %s", account.Type))
	}

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	// Create Claude Code style payload (same for all account types)
	payload, err := createTestPayload(testModelID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}
	payloadBytes, _ := json.Marshal(payload)

	// Send test_start event
	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}

	// Set common headers
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")

	// Apply Claude Code client headers
	for key, value := range claude.DefaultHeaders() {
		req.Header.Set(key, value)
	}

	// Set authentication header
	if account.IsOAuth() {
		req.Header.Set("anthropic-beta", claude.DefaultBetaHeader)
		req.Header.Set("Authorization", "Bearer "+authToken)
	} else {
		req.Header.Set("anthropic-beta", claude.APIKeyBetaHeader)
		// Ollama Cloud Anthropic 兼容端点按实际 base_url 强制 Bearer，
		// 其余保持 extra/default 行为。
		setAnthropicAPIKeyAuthHeader(req.Header, account, authToken, account.GetBaseURL())
	}

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	account.ApplyHeaderOverrides(req.Header)

	// Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))

		// 403 表示账号被上游封禁，标记为 error 状态
		if resp.StatusCode == http.StatusForbidden {
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}

		return s.sendErrorAndEnd(c, errMsg)
	}

	// Process SSE stream
	return s.processClaudeStream(c, resp.Body)
}

func (s *AccountTestService) testClaudeVertexServiceAccountConnection(c *gin.Context, ctx context.Context, account *Account, testModelID string) error {
	if mappedModel, matched := account.ResolveMappedModel(testModelID); matched {
		testModelID = mappedModel
	} else {
		testModelID = normalizeVertexAnthropicModelID(claude.NormalizeModelID(testModelID))
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	payload, err := createTestPayload(testModelID)
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create test payload")
	}
	payloadBytes, _ := json.Marshal(payload)
	vertexBody, err := buildVertexAnthropicRequestBody(payloadBytes)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to create Vertex request body: %s", err.Error()))
	}

	if s.claudeTokenProvider == nil {
		return s.sendErrorAndEnd(c, "Claude token provider not configured")
	}
	accessToken, err := s.claudeTokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to get service account access token: %s", err.Error()))
	}

	fullURL, err := buildVertexAnthropicURL(account.VertexProjectID(), account.VertexLocation(testModelID), testModelID, true)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to build Vertex URL: %s", err.Error()))
	}

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(vertexBody))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		errMsg := fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body))
		if resp.StatusCode == http.StatusForbidden {
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, errMsg)
	}

	return s.processClaudeStream(c, resp.Body)
}

// testOpenAIAccountConnection tests an OpenAI account's connection
func (s *AccountTestService) testOpenAIAccountConnection(c *gin.Context, account *Account, modelID string, prompt string, mode string) error {
	ctx := c.Request.Context()
	mode = normalizeAccountTestMode(mode)

	// Default to openai.DefaultTestModel for OpenAI testing
	testModelID := modelID
	if testModelID == "" {
		testModelID = openai.DefaultTestModel
	}

	// Align test routing with gateway behavior: OpenAI accounts apply normal
	// account model mapping. Native remote compaction v2 rides the ordinary
	// /responses wire and does NOT apply the legacy compact-only mapping
	// (post-#5641 semantics: compact_model_mapping is /responses/compact-only).
	testModelID = account.GetMappedModel(testModelID)
	if mode == AccountTestModeCompact {
		return s.testOpenAICompactConnection(c, account, testModelID)
	}

	// Route to image generation test if an image model is selected
	if isOpenAIImageModel(testModelID) {
		imagePrompt := strings.TrimSpace(prompt)
		if imagePrompt == "" {
			imagePrompt = defaultOpenAIImageTestPrompt
		}
		if account.Type == "apikey" {
			return s.testOpenAIImageAPIKey(c, ctx, account, testModelID, imagePrompt)
		}
		return s.testOpenAIImageOAuth(c, ctx, account, testModelID, imagePrompt)
	}

	credentialAccount := account
	if account.IsCredentialShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return s.sendErrorAndEnd(c, err.Error())
		}
		credentialAccount = resolved
	}

	// Determine authentication method and API URL
	var authToken string
	var apiURL string
	var isOAuth bool

	if credentialAccount.IsOAuth() {
		isOAuth = true
		// Agent Identity signs each request and does not retain the OAuth token.
		if !credentialAccount.IsOpenAIAgentIdentity() {
			authToken = credentialAccount.GetOpenAIAccessToken()
		}
		if authToken == "" && !credentialAccount.IsOpenAIAgentIdentity() {
			return s.sendErrorAndEnd(c, "No access token available")
		}

		// OAuth uses ChatGPT internal API
		apiURL = chatgptCodexAPIURL
	} else if credentialAccount.Type == "apikey" {
		// API Key - use Platform API
		authToken = credentialAccount.GetOpenAIProtocolAPIKey()
		if authToken == "" {
			return s.sendErrorAndEnd(c, "No API key available")
		}

		baseURL := credentialAccount.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		if !openai_compat.ShouldUseResponsesAPI(account.Extra) {
			return s.testOpenAIChatCompletionsConnection(c, account, testModelID, prompt, normalizedBaseURL, authToken)
		}
		apiURL = buildOpenAIResponsesURLForPlatform(credentialAccount.Platform, normalizedBaseURL)
	} else {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported account type: %s", account.Type))
	}

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	// Create OpenAI Responses API payload. OAuth accounts use ChatGPT Codex
	// upstream and must apply the same model normalization as real forwarding.
	// WorkBuddy 按模型名直传，不做 Codex 别名归一化。
	upstreamTestModelID := testModelID
	payload := createOpenAITestPayload(upstreamTestModelID, isOAuth)
	payloadBytes, _ := json.Marshal(payload)

	// Send test_start event once. A task-invalid Agent Identity response may
	// restart this probe after registering a replacement task.
	if !agentIdentityTaskRecoveryWasTried(ctx) {
		s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	// Set common headers
	req.Header.Set("Content-Type", "application/json")
	if credentialAccount.IsOpenAIAgentIdentity() {
		authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount)
		if authErr != nil {
			return s.sendErrorAndEnd(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}

	// ChatGPT 内部 API 的 OAuth 头（Codex 客户端专用）已随 Codex 移除。
	// WorkBuddy 是 OAuth 账号，但它走自己的 /v2/chat/completions，
	// 不需要 chatgpt.com 的 host 覆写与 OpenAI-Beta 头。

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	credentialAccount.ApplyHeaderOverrides(req.Header)

	// Get proxy URL
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if isOAuth && s.accountRepo != nil {
		if updates, err := extractOpenAICodexProbeUpdates(resp); err == nil && len(updates) > 0 {
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, updates)
			mergeAccountExtra(account, updates)
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		body = redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, credentialAccount, body)
		if !agentIdentityTaskRecoveryWasTried(ctx) && credentialAccount.IsOpenAIAgentIdentity() && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
			expectedTaskID := credentialAccount.GetCredential("task_id")
			if err := ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount, expectedTaskID); err != nil {
				return s.sendErrorAndEnd(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
			}
			c.Request = c.Request.WithContext(markAgentIdentityTaskRecoveryTried(ctx))
			return s.testOpenAIAccountConnection(c, account, modelID, prompt, mode)
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, account, resp.Header, body)
		}
		// 401 Unauthorized: 标记账号为永久错误
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	// Process SSE stream
	return s.processOpenAIStream(c, resp.Body)
}

// testGrokAccountConnection routes Grok admin connectivity tests by explicit mode first,
// then by selected model family for media. Standalone modes (search/tts/stt) never share
// the text Responses path; image/video never hit Responses either.
//
// Modes:
//   - default/text → Responses (optional model)
//   - image → /v1/images/generations (model optional; defaults to grok-imagine-image)
//   - video → /v1/videos/generations (model optional; defaults to grok-imagine-video)
//   - search → standalone web-search probe (gateway /v1/web_search semantics)
//   - tts → HTTP /v1/tts
//   - stt → HTTP /v1/stt (synthetic tiny wav probe)
//   - realtime → WS /v1/realtime dial + optional first server event
//

func resolveGrokImagePrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return defaultGrokImageTestPrompt
	}
	return strings.TrimSpace(prompt)
}

func resolveGrokVideoPrompt(prompt string) string {
	if strings.TrimSpace(prompt) == "" {
		return defaultGrokVideoTestPrompt
	}
	return strings.TrimSpace(prompt)
}

func resolveGrokImageTestModel(account *Account, modelID string) string {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = "grok-imagine-image"
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(testModelID)); mapped != "" {
		return mapped
	}
	return testModelID
}

func resolveGrokVideoTestModel(account *Account, modelID string) string {
	testModelID := strings.TrimSpace(modelID)
	if testModelID == "" {
		testModelID = "grok-imagine-video"
	}
	if mapped := strings.TrimSpace(account.GetMappedModel(testModelID)); mapped != "" {
		return mapped
	}
	return testModelID
}

// validateAccountTestDataURL ensures data URLs are well-formed and size-bounded.
func validateAccountTestDataURL(raw, requiredPrefix string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fmt.Errorf("media data URL is empty")
	}
	if !strings.HasPrefix(raw, "data:") {
		return fmt.Errorf("media must be a data: URL (data:<mime>;base64,...)")
	}
	// Rough size check before decode (base64 expands ~4/3).
	if len(raw) > maxAccountTestMediaBytes*2 {
		return fmt.Errorf("media data URL exceeds size limit")
	}
	_, mime, err := decodeAccountTestDataURL(raw)
	if err != nil {
		return err
	}
	if requiredPrefix != "" && !strings.HasPrefix(strings.ToLower(mime), strings.ToLower(requiredPrefix)) {
		return fmt.Errorf("expected media type prefix %q, got %q", requiredPrefix, mime)
	}
	return nil
}

// normalizeAccountTestImageDataURL validates an image data URL, enforces xAI
// minimum dimensions (8x8), and rewrites to a clean data:image/<type>;base64,... form.
func normalizeAccountTestImageDataURL(raw string) (string, error) {
	if err := validateAccountTestDataURL(raw, "image/"); err != nil {
		return "", err
	}
	data, mime, err := decodeAccountTestDataURL(raw)
	if err != nil {
		return "", err
	}
	// Soft cap decoded bytes (~4 MiB) for edit payloads to avoid upstream/proxy EOF.
	const maxDecodedImage = 4 << 20
	if len(data) > maxDecodedImage {
		return "", fmt.Errorf(
			"source image is too large (%d bytes decoded). Please use a smaller image (under ~4 MB) for admin edit tests",
			len(data),
		)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		// Keep raw data URL if decoder does not understand the codec (e.g. webp
		// without golang.org/x/image/webp); still send upstream and let xAI validate.
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
	}
	if cfg.Width < 8 || cfg.Height < 8 {
		return "", fmt.Errorf(
			"source image is too small (%dx%d). xAI requires both width and height to be at least 8 pixels",
			cfg.Width, cfg.Height,
		)
	}
	// Prefer a stable mime from config when known.
	if mime == "" || mime == "application/octet-stream" {
		mime = "image/png"
	}
	return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func isTransientGrokTransportError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unexpected eof") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "timeout awaiting response")
}

func formatGrokImageTransportError(err error, hasSourceImage bool, payloadBytes int) string {
	base := fmt.Sprintf("Grok image request failed: %s", err.Error())
	if !hasSourceImage {
		return base
	}
	return base + fmt.Sprintf(
		" (edit payload ~%d bytes). Tips: use a smaller source image (<4 MB / lower resolution), ensure the account proxy is stable, and retry. xAI /images/edits expects image as {\"url\":\"data:image/...;base64,...\",\"type\":\"image_url\"}.",
		payloadBytes,
	)
}

func formatGrokImagesAPIError(status int, body []byte, hasSourceImage bool) string {
	msg := strings.TrimSpace(string(body))
	if len(msg) > 800 {
		msg = msg[:800] + "..."
	}
	prefix := fmt.Sprintf("Grok images API returned %d: %s", status, msg)
	lower := strings.ToLower(msg)
	if hasSourceImage && (strings.Contains(lower, "too small") || strings.Contains(lower, "at least 8")) {
		return prefix + " — upload a source image with both width and height ≥ 8 px."
	}
	return prefix
}

func decodeAccountTestDataURL(raw string) (data []byte, mime string, err error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "data:") {
		return nil, "", fmt.Errorf("not a data URL")
	}
	rest := strings.TrimPrefix(raw, "data:")
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return nil, "", fmt.Errorf("invalid data URL (missing comma)")
	}
	meta := rest[:comma]
	payload := rest[comma+1:]
	mime = "application/octet-stream"
	if semi := strings.Index(meta, ";"); semi >= 0 {
		if t := strings.TrimSpace(meta[:semi]); t != "" {
			mime = t
		}
	} else if t := strings.TrimSpace(meta); t != "" {
		mime = t
	}
	if !strings.Contains(strings.ToLower(meta), ";base64") {
		return nil, "", fmt.Errorf("only base64 data URLs are supported")
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		// Some browsers emit URL-safe base64 without padding.
		decoded, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "="))
		if err != nil {
			return nil, "", fmt.Errorf("base64 decode failed: %w", err)
		}
	}
	if len(decoded) == 0 {
		return nil, "", fmt.Errorf("decoded media is empty")
	}
	if len(decoded) > maxAccountTestMediaBytes {
		return nil, "", fmt.Errorf("media exceeds %d byte limit", maxAccountTestMediaBytes)
	}
	return decoded, mime, nil
}

func sttFilenameForMIME(mime string) string {
	switch strings.ToLower(strings.TrimSpace(mime)) {
	case "audio/mpeg", "audio/mp3":
		return "upload.mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "upload.wav"
	case "audio/webm":
		return "upload.webm"
	case "audio/ogg", "audio/opus":
		return "upload.ogg"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "upload.m4a"
	default:
		return "upload.bin"
	}
}

// redactGrokRealtimeURLForLog strips query secrets while keeping model for diagnostics.
func redactGrokRealtimeURLForLog(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil {
		return raw
	}
	// Keep model query only.
	model := u.Query().Get("model")
	u.RawQuery = ""
	if model != "" {
		u.RawQuery = "model=" + url.QueryEscape(model)
	}
	// Never log bearer in fragment/userinfo.
	u.User = nil
	u.Fragment = ""
	return u.String()
}

// minimalSilentWAV returns a valid tiny mono 8kHz 16-bit PCM WAV (~0.05s silence).
func minimalSilentWAV() []byte {
	// 400 samples * 2 bytes = 800 data bytes
	const sampleRate = 8000
	const numSamples = 400
	dataSize := numSamples * 2
	buf := make([]byte, 44+dataSize)
	copy(buf[0:], []byte("RIFF"))
	binary.LittleEndian.PutUint32(buf[4:], uint32(36+dataSize))
	copy(buf[8:], []byte("WAVE"))
	copy(buf[12:], []byte("fmt "))
	binary.LittleEndian.PutUint32(buf[16:], 16) // PCM chunk size
	binary.LittleEndian.PutUint16(buf[20:], 1)  // PCM
	binary.LittleEndian.PutUint16(buf[22:], 1)  // mono
	binary.LittleEndian.PutUint32(buf[24:], sampleRate)
	binary.LittleEndian.PutUint32(buf[28:], sampleRate*2) // byte rate
	binary.LittleEndian.PutUint16(buf[32:], 2)            // block align
	binary.LittleEndian.PutUint16(buf[34:], 16)           // bits
	copy(buf[36:], []byte("data"))
	binary.LittleEndian.PutUint32(buf[40:], uint32(dataSize))
	// samples already zero (silence)
	return buf
}

// testOpenAIChatCompletionsConnection tests an OpenAI-compatible APIKey account
// through the raw /v1/chat/completions endpoint.
func (s *AccountTestService) testOpenAIChatCompletionsConnection(
	c *gin.Context,
	account *Account,
	testModelID string,
	prompt string,
	normalizedBaseURL string,
	authToken string,
) error {
	ctx := c.Request.Context()
	apiURL := buildOpenAIChatCompletionsURL(normalizedBaseURL)

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	payload := createOpenAIChatCompletionsTestPayload(testModelID, prompt)
	payloadBytes, _ := json.Marshal(payload)

	s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	s.sendEvent(c, TestEvent{Type: "status", Text: "正在通过 /v1/chat/completions 测试连接"})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create Chat Completions request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+authToken)

	// 官方 OpenCode / Command Code 上游收敛为规范客户端 UA，与真实转发路径一致。
	applyOpenCodeUpstreamUserAgent(account, apiURL, req.Header)

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	account.ApplyHeaderOverrides(req.Header)
	applyOpenCodeSessionHeader(c, account, apiURL, req.Header, payloadBytes)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, account, resp.Header, body)
		}
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			errMsg := fmt.Sprintf("Chat Completions authentication failed (401): %s", string(body))
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) returned %d: %s", resp.StatusCode, string(body)))
	}

	return s.processOpenAIChatCompletionsStream(c, resp.Body)
}

// testOpenAICompactConnection probes native remote compaction v2 (streaming
// /responses with a compaction_trigger input item) and persists the resulting
// capability state on the account. The legacy unary /responses/compact
// endpoint has been sunset upstream (404, #5598/#5624) and is no longer probed.
func (s *AccountTestService) testOpenAICompactConnection(c *gin.Context, account *Account, testModelID string) error {
	ctx := c.Request.Context()
	credentialAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return s.sendErrorAndEnd(c, "Failed to resolve account credentials")
		}
		credentialAccount = resolved
	}

	authToken := ""
	apiURL := ""
	isOAuth := false

	switch {
	case credentialAccount.IsOAuth():
		isOAuth = true
		if !credentialAccount.IsOpenAIAgentIdentity() {
			authToken = credentialAccount.GetOpenAIAccessToken()
		}
		if authToken == "" && !credentialAccount.IsOpenAIAgentIdentity() {
			return s.sendErrorAndEnd(c, "No access token available")
		}
		apiURL = chatgptCodexAPIURL
	case account.Type == AccountTypeAPIKey:
		authToken = account.GetOpenAIProtocolAPIKey()
		if authToken == "" {
			return s.sendErrorAndEnd(c, "No API key available")
		}
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			baseURL = "https://api.openai.com"
		}
		normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
		if err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
		}
		apiURL = buildOpenAIResponsesURLForPlatform(account.Platform, normalizedBaseURL)
	default:
		return s.sendErrorAndEnd(c, fmt.Sprintf("Unsupported account type: %s", account.Type))
	}

	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	// WorkBuddy 按模型名直传，不做 Codex 别名归一化。
	payloadBytes, _ := json.Marshal(createOpenAICompactProbePayload(testModelID, isOAuth))
	if !agentIdentityTaskRecoveryWasTried(ctx) {
		s.sendEvent(c, TestEvent{Type: "test_start", Model: testModelID})
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))

	req.Header.Set("Content-Type", "application/json")
	// v2 探测是流式请求；同时补注协商头，与真实 codex 出站线型一致。
	req.Header.Set("Accept", "text/event-stream")
	ensureOpenAIRemoteCompactionV2BetaFeature(req.Header)
	if credentialAccount.IsOpenAIAgentIdentity() {
		authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount)
		if authErr != nil {
			return s.sendErrorAndEnd(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	probeSessionID := "auto2api-compact-probe"
	req.Header.Set("Session_ID", probeSessionID)
	req.Header.Set("Conversation_ID", probeSessionID)

	if isOAuth {
		req.Host = "chatgpt.com"
		setOpenAIChatGPTAccountHeaders(req.Header, credentialAccount)
	}

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.doOpenAIAccountTestUpstream(req, proxyURL, account, true)
	if err != nil {
		if s.accountRepo != nil {
			updates := buildOpenAICompactProbeExtraUpdates(nil, nil, err, false, time.Now())
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, updates)
			mergeAccountExtra(account, updates)
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	body = redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, credentialAccount, body)
	if !agentIdentityTaskRecoveryWasTried(ctx) && credentialAccount.IsOpenAIAgentIdentity() && isAgentIdentityTaskInvalidHTTPResponse(resp.StatusCode, body) {
		expectedTaskID := credentialAccount.GetCredential("task_id")
		if err := ensureAgentIdentityTaskForAccount(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount, expectedTaskID); err != nil {
			return s.sendErrorAndEnd(c, fmt.Sprintf("Agent Identity task recovery failed: %s", err.Error()))
		}
		c.Request = c.Request.WithContext(markAgentIdentityTaskRecoveryTried(ctx))
		return s.testOpenAICompactConnection(c, account, testModelID)
	}

	compactionFound := openAICompactProbeFoundCompactionItem(body)
	if s.accountRepo != nil {
		updates := buildOpenAICompactProbeExtraUpdates(resp, body, nil, compactionFound, time.Now())
		if codexUpdates, err := extractOpenAICodexProbeUpdates(resp); err == nil && len(codexUpdates) > 0 {
			updates = mergeExtraUpdates(updates, codexUpdates)
		}
		if len(updates) > 0 {
			_ = s.accountRepo.UpdateExtra(ctx, account.ID, updates)
			mergeAccountExtra(account, updates)
		}
		// 探测如返回 429,主动同步限流状态,避免后续短时间内继续选中。
		if resp.StatusCode == http.StatusTooManyRequests {
			s.reconcileOpenAI429State(ctx, account, resp.Header, body)
		}
	}

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusUnauthorized && s.accountRepo != nil {
			errMsg := fmt.Sprintf("Authentication failed (401): %s", string(body))
			_ = s.accountRepo.SetError(ctx, account.ID, errMsg)
		}
		return s.sendErrorAndEnd(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	if !compactionFound {
		return s.sendErrorAndEnd(c, "Upstream returned 2xx without a compaction output item (native remote compaction v2 unsupported on this chain)")
	}

	s.sendEvent(c, TestEvent{Type: "content", Text: "Compact probe succeeded (native remote compaction v2)"})
	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) reconcileOpenAI429State(ctx context.Context, account *Account, headers http.Header, body []byte) {
	if s == nil || s.accountRepo == nil || account == nil {
		return
	}

	persistOpenAI429PlanType(ctx, s.accountRepo, account, body)

	var resetAt *time.Time
	if calculated := calculateOpenAI429ResetTime(headers); calculated != nil {
		resetAt = calculated
	} else if unixTs := parseOpenAIRateLimitResetTime(body); unixTs != nil {
		t := time.Unix(*unixTs, 0)
		resetAt = &t
	}
	if resetAt == nil {
		return
	}

	if err := s.accountRepo.SetRateLimited(ctx, account.ID, *resetAt); err != nil {
		return
	}

	now := time.Now()
	account.RateLimitedAt = &now
	account.RateLimitResetAt = resetAt

	if account.Status == StatusError {
		if err := s.accountRepo.ClearError(ctx, account.ID); err != nil {
			return
		}
		account.Status = StatusActive
		account.ErrorMessage = ""
	}
}

// createGeminiTestPayload creates a minimal test payload for Gemini API.
// Image models use the image-generation path so the frontend can preview the returned image.
func createGeminiTestPayload(modelID string, prompt string) []byte {
	if isImageGenerationModel(modelID) {
		imagePrompt := strings.TrimSpace(prompt)
		if imagePrompt == "" {
			imagePrompt = defaultGeminiImageTestPrompt
		}

		payload := map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]any{
						{"text": imagePrompt},
					},
				},
			},
			"generationConfig": map[string]any{
				"responseModalities": []string{"TEXT", "IMAGE"},
				"imageConfig": map[string]any{
					"aspectRatio": "1:1",
				},
			},
		}
		bytes, _ := json.Marshal(payload)
		return bytes
	}

	textPrompt := strings.TrimSpace(prompt)
	if textPrompt == "" {
		textPrompt = defaultGeminiTextTestPrompt
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"role": "user",
				"parts": []map[string]any{
					{"text": textPrompt},
				},
			},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{
				{"text": "You are a helpful AI assistant."},
			},
		},
	}
	bytes, _ := json.Marshal(payload)
	return bytes
}

// processGeminiStream processes SSE stream from Gemini API
func (s *AccountTestService) processGeminiStream(c *gin.Context, body io.Reader) error {
	reader := bufio.NewReader(body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
				return nil
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Stream read error: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonStr := strings.TrimPrefix(line, "data: ")
		if jsonStr == "[DONE]" {
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		// Support two Gemini response formats:
		// - AI Studio: {"candidates": [...]}
		// - Gemini CLI: {"response": {"candidates": [...]}}
		if resp, ok := data["response"].(map[string]any); ok && resp != nil {
			data = resp
		}
		if candidates, ok := data["candidates"].([]any); ok && len(candidates) > 0 {
			if candidate, ok := candidates[0].(map[string]any); ok {
				// Extract content first (before checking completion)
				if content, ok := candidate["content"].(map[string]any); ok {
					if parts, ok := content["parts"].([]any); ok {
						for _, part := range parts {
							if partMap, ok := part.(map[string]any); ok {
								if text, ok := partMap["text"].(string); ok && text != "" {
									s.sendEvent(c, TestEvent{Type: "content", Text: text})
								}
								if inlineData, ok := partMap["inlineData"].(map[string]any); ok {
									mimeType, _ := inlineData["mimeType"].(string)
									data, _ := inlineData["data"].(string)
									if strings.HasPrefix(strings.ToLower(mimeType), "image/") && data != "" {
										s.sendEvent(c, TestEvent{
											Type:     "image",
											ImageURL: fmt.Sprintf("data:%s;base64,%s", mimeType, data),
											MimeType: mimeType,
										})
									}
								}
							}
						}
					}
				}

				// Check for completion after extracting content
				if finishReason, ok := candidate["finishReason"].(string); ok && finishReason != "" {
					s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
					return nil
				}
			}
		}

		// Handle errors
		if errData, ok := data["error"].(map[string]any); ok {
			errorMsg := "Unknown error"
			if msg, ok := errData["message"].(string); ok {
				errorMsg = msg
			}
			return s.sendErrorAndEnd(c, errorMsg)
		}
	}
}

// createOpenAITestPayload creates a test payload for OpenAI Responses API
func createOpenAITestPayload(modelID string, isOAuth bool) map[string]any {
	payload := map[string]any{
		"model": modelID,
		"input": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{
						"type": "input_text",
						"text": "hi",
					},
				},
			},
		},
		"stream": true,
	}

	// OAuth accounts using ChatGPT internal API require store: false
	if isOAuth {
		payload["store"] = false
	}

	// All accounts require instructions for Responses API
	payload["instructions"] = openai.DefaultInstructions

	return payload
}

func createOpenAIChatCompletionsTestPayload(modelID string, prompt string) map[string]any {
	testPrompt := strings.TrimSpace(prompt)
	if testPrompt == "" {
		testPrompt = "hi"
	}

	return map[string]any{
		"model": modelID,
		"messages": []map[string]any{
			{
				"role":    "user",
				"content": testPrompt,
			},
		},
		"stream": true,
	}
}

// processClaudeStream processes the SSE stream from Claude API
func (s *AccountTestService) processClaudeStream(c *gin.Context, body io.Reader) error {
	reader := bufio.NewReader(body)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
				return nil
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Stream read error: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !sseDataPrefix.MatchString(line) {
			continue
		}

		jsonStr := sseDataPrefix.ReplaceAllString(line, "")
		if jsonStr == "[DONE]" {
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)

		switch eventType {
		case "content_block_delta":
			if delta, ok := data["delta"].(map[string]any); ok {
				if text, ok := delta["text"].(string); ok {
					s.sendEvent(c, TestEvent{Type: "content", Text: text})
				}
			}
		case "message_stop":
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		case "error":
			errorMsg := "Unknown error"
			if errData, ok := data["error"].(map[string]any); ok {
				if msg, ok := errData["message"].(string); ok {
					errorMsg = msg
				}
			}
			return s.sendErrorAndEnd(c, errorMsg)
		}
	}
}

// processOpenAIChatCompletionsStream processes SSE chunks from the
// OpenAI-compatible Chat Completions API.
func (s *AccountTestService) processOpenAIChatCompletionsStream(c *gin.Context, body io.Reader) error {
	reader := bufio.NewReader(body)
	seenJSON := false
	seenFinish := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if seenFinish {
					s.sendEvent(c, TestEvent{Type: "status", Text: "已通过 /v1/chat/completions 验证"})
					s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
					return nil
				}
				if seenJSON {
					return s.sendErrorAndEnd(c, "Chat Completions stream from /v1/chat/completions ended before [DONE]")
				}
				return s.sendErrorAndEnd(c, "Invalid Chat Completions response from /v1/chat/completions: expected SSE JSON data")
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Chat Completions stream read error from /v1/chat/completions: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !sseDataPrefix.MatchString(line) {
			continue
		}

		jsonStr := sseDataPrefix.ReplaceAllString(line, "")
		if jsonStr == "[DONE]" {
			s.sendEvent(c, TestEvent{Type: "status", Text: "已通过 /v1/chat/completions 验证"})
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			return s.sendErrorAndEnd(c, "Invalid Chat Completions response from /v1/chat/completions: expected JSON data")
		}
		seenJSON = true

		if errData, ok := data["error"].(map[string]any); ok {
			errorMsg := "Chat Completions API (/v1/chat/completions) returned an error"
			if msg, ok := errData["message"].(string); ok && msg != "" {
				errorMsg = msg
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Chat Completions API (/v1/chat/completions) error: %s", errorMsg))
		}

		choices, ok := data["choices"].([]any)
		if !ok {
			continue
		}
		for _, choiceValue := range choices {
			choice, ok := choiceValue.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok {
				if text, ok := delta["content"].(string); ok && text != "" {
					s.sendEvent(c, TestEvent{Type: "content", Text: text})
				}
			}
			if message, ok := choice["message"].(map[string]any); ok {
				if text, ok := message["content"].(string); ok && text != "" {
					s.sendEvent(c, TestEvent{Type: "content", Text: text})
				}
			}
			if finishReason, ok := choice["finish_reason"].(string); ok && finishReason != "" {
				seenFinish = true
			}
		}
	}
}

// processOpenAIStream processes the SSE stream from OpenAI Responses API
func (s *AccountTestService) processOpenAIStream(c *gin.Context, body io.Reader) error {
	reader := bufio.NewReader(body)
	seenCompleted := false

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				if seenCompleted {
					s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
					return nil
				}
				return s.sendErrorAndEnd(c, "Stream ended before response.completed")
			}
			return s.sendErrorAndEnd(c, fmt.Sprintf("Stream read error: %s", err.Error()))
		}

		line = strings.TrimSpace(line)
		if line == "" || !sseDataPrefix.MatchString(line) {
			continue
		}

		jsonStr := sseDataPrefix.ReplaceAllString(line, "")
		if jsonStr == "[DONE]" {
			if seenCompleted {
				s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
				return nil
			}
			return s.sendErrorAndEnd(c, "Stream ended before response.completed")
		}

		var data map[string]any
		if err := json.Unmarshal([]byte(jsonStr), &data); err != nil {
			continue
		}

		eventType, _ := data["type"].(string)

		switch eventType {
		case "response.output_text.delta":
			// OpenAI Responses API uses "delta" field for text content
			if delta, ok := data["delta"].(string); ok && delta != "" {
				s.sendEvent(c, TestEvent{Type: "content", Text: delta})
			}
		case "response.completed", "response.done":
			s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
			return nil
		case "response.failed":
			errorMsg := "OpenAI response failed"
			if responseData, ok := data["response"].(map[string]any); ok {
				if errData, ok := responseData["error"].(map[string]any); ok {
					if msg, ok := errData["message"].(string); ok && msg != "" {
						errorMsg = msg
					}
				}
			}
			return s.sendErrorAndEnd(c, errorMsg)
		case "error":
			errorMsg := "Unknown error"
			if errData, ok := data["error"].(map[string]any); ok {
				if msg, ok := errData["message"].(string); ok {
					errorMsg = msg
				}
			}
			return s.sendErrorAndEnd(c, errorMsg)
		}
	}
}

// testOpenAIImageAPIKey tests OpenAI image generation using an API Key account.
func (s *AccountTestService) testOpenAIImageAPIKey(c *gin.Context, ctx context.Context, account *Account, modelID, prompt string) error {
	authToken := account.GetOpenAIApiKey()
	if authToken == "" {
		return s.sendErrorAndEnd(c, "No API key available")
	}

	baseURL := account.GetOpenAIBaseURL()
	if baseURL == "" {
		baseURL = "https://api.openai.com"
	}
	normalizedBaseURL, err := s.validateUpstreamBaseURL(baseURL)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Invalid base URL: %s", err.Error()))
	}
	apiURL := buildOpenAIImagesURL(normalizedBaseURL, openAIImagesGenerationsEndpoint)

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})

	// GPT Image models return base64 images by default and reject response_format.
	payload := map[string]any{
		"model":  modelID,
		"prompt": prompt,
		"n":      1,
	}
	if size := account.OpenAIImageTestSize(); size != "" {
		payload["size"] = size
	}
	payloadBytes, _ := json.Marshal(payload)

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(payloadBytes))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+authToken)

	// 账号级请求头覆写：测试请求与真实转发保持一致的最终头
	account.ApplyHeaderOverrides(req.Header)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.tlsFPProfileService.ResolveTLSProfile(account))
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Request failed: %s", err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read response: %s", err.Error()))
	}

	if resp.StatusCode != http.StatusOK {
		return s.sendErrorAndEnd(c, fmt.Sprintf("API returned %d: %s", resp.StatusCode, string(body)))
	}

	// Parse {"data": [{"b64_json": "...", "revised_prompt": "..."}]}
	var result struct {
		Data []struct {
			B64JSON       string `json:"b64_json"`
			RevisedPrompt string `json:"revised_prompt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to parse response: %s", err.Error()))
	}

	if len(result.Data) == 0 {
		return s.sendErrorAndEnd(c, "No images returned from API")
	}

	for _, item := range result.Data {
		if item.RevisedPrompt != "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		if item.B64JSON != "" {
			s.sendEvent(c, TestEvent{
				Type:     "image",
				ImageURL: "data:image/png;base64," + item.B64JSON,
				MimeType: "image/png",
			})
		}
	}

	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

// OAuth 图片测试与正式转发共用 Codex Images / Responses 分流规则。
func (s *AccountTestService) testOpenAIImageOAuth(c *gin.Context, ctx context.Context, account *Account, modelID, prompt string) error {
	credentialAccount := account
	if account.IsShadow() {
		resolved, err := resolveCredentialAccount(ctx, s.accountRepo, account)
		if err != nil {
			return s.sendErrorAndEnd(c, "Failed to resolve account credentials")
		}
		credentialAccount = resolved
	}
	authToken := ""
	if !credentialAccount.IsOpenAIAgentIdentity() {
		authToken = credentialAccount.GetOpenAIAccessToken()
	}
	if authToken == "" && !credentialAccount.IsOpenAIAgentIdentity() {
		return s.sendErrorAndEnd(c, "No access token available")
	}

	// Set SSE headers
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.Header().Set("Cache-Control", "no-cache")
	c.Writer.Header().Set("Connection", "keep-alive")
	c.Writer.Header().Set("X-Accel-Buffering", "no")
	c.Writer.Flush()

	s.sendEvent(c, TestEvent{Type: "test_start", Model: modelID})

	parsed := &OpenAIImagesRequest{
		Endpoint: openAIImagesGenerationsEndpoint,
		Model:    strings.TrimSpace(modelID),
		Prompt:   prompt,
		Size:     account.OpenAIImageTestSize(),
	}
	parsed.ExplicitSize = parsed.Size != ""
	applyOpenAIImagesDefaults(parsed)

	upstreamModel := account.GetMappedModel(parsed.Model)
	responsesBody, targetURL, err := buildOpenAIImagesOAuthPayload(parsed, upstreamModel)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to build image request: %s", err.Error()))
	}

	direct := usesCodexDirectImages(upstreamModel)
	if direct {
		s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("Calling Codex /images/generations; image model: %s\n", upstreamModel)})
	} else {
		s.sendEvent(c, TestEvent{Type: "content", Text: fmt.Sprintf("Calling Codex /responses image tool; driver: %s; image model: %s\n", openAIImagesResponsesMainModelValue(), upstreamModel)})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(responsesBody))
	if err != nil {
		return s.sendErrorAndEnd(c, "Failed to create request")
	}
	req = req.WithContext(WithHTTPUpstreamProfile(req.Context(), HTTPUpstreamProfileOpenAI))
	req.Host = "chatgpt.com"
	if credentialAccount.IsOpenAIAgentIdentity() {
		authHeaders, authErr := buildAgentIdentityAuthenticationHeaders(ctx, s.accountRepo, s.agentIdentityWS, &s.agentIdentityTaskMu, credentialAccount)
		if authErr != nil {
			return s.sendErrorAndEnd(c, "Failed to build Agent Identity authentication")
		}
		for key, values := range authHeaders {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	} else {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	if direct {
		req.Header.Del("OpenAI-Beta")
		req.Header.Set("Accept", "application/json")
	}
	// Codex 客户端身份（originator / 收敛 UA）已随 Codex 移除；
	// 仅保留账号自定义 UA。
	if customUA := strings.TrimSpace(credentialAccount.GetOpenAIUserAgent()); customUA != "" {
		req.Header.Set("User-Agent", customUA)
	}
	setOpenAIChatGPTAccountHeaders(req.Header, credentialAccount)

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.doOpenAIAccountTestUpstream(req, proxyURL, account, false)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Image upstream request failed: %s", err.Error()))
	}
	defer func() {
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
	}()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		body = redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, credentialAccount, body)
		message := strings.TrimSpace(extractUpstreamErrorMessage(body))
		if message == "" {
			message = fmt.Sprintf("Image upstream returned %d", resp.StatusCode)
		}
		return s.sendErrorAndEnd(c, message)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to read image response: %s", err.Error()))
	}
	body = redactAgentIdentitySensitiveBodyForAccount(ctx, s.accountRepo, credentialAccount, body)

	var results []openAIResponsesImageResult
	if direct {
		results, err = parseCodexDirectImagesResponse(body)
	} else {
		if upstreamErr := extractOpenAIImagesUpstreamError(body); upstreamErr != nil {
			return s.sendErrorAndEnd(c, upstreamErr.clientMessage())
		}
		results, _, _, _, _, err = collectOpenAIImagesFromResponsesBody(body)
	}
	if err != nil {
		return s.sendErrorAndEnd(c, fmt.Sprintf("Failed to parse image response: %s", err.Error()))
	}
	if len(results) == 0 {
		if textErr := openAIImagesTextFallbackError(body); textErr != nil {
			return s.sendErrorAndEnd(c, textErr.clientMessage())
		}
		if direct {
			return s.sendErrorAndEnd(c, "No images returned from Codex Images API")
		}
		return s.sendErrorAndEnd(c, "No images returned from Codex Responses API")
	}

	for _, item := range results {
		if item.RevisedPrompt != "" {
			s.sendEvent(c, TestEvent{Type: "content", Text: item.RevisedPrompt})
		}
		mimeType := openAIImageOutputMIMEType(item.OutputFormat)
		s.sendEvent(c, TestEvent{
			Type:     "image",
			ImageURL: "data:" + mimeType + ";base64," + item.Result,
			MimeType: mimeType,
		})
	}

	s.sendEvent(c, TestEvent{Type: "test_complete", Success: true})
	return nil
}

func (s *AccountTestService) sendEvent(c *gin.Context, event TestEvent) {
	if event.Type == "test_complete" {
		if suppress, ok := c.Get(accountTestSuppressCompletionContextKey); ok {
			if suppressCompletion, _ := suppress.(bool); suppressCompletion {
				return
			}
		}
	}
	eventJSON, _ := json.Marshal(event)
	if _, err := fmt.Fprintf(c.Writer, "data: %s\n\n", eventJSON); err != nil {
		log.Printf("failed to write SSE event: %v", err)
		return
	}
	c.Writer.Flush()
}

// sendErrorAndEnd sends an error event and ends the stream
func (s *AccountTestService) sendErrorAndEnd(c *gin.Context, errorMsg string) error {
	logAccountTestError(c, errorMsg)
	s.sendEvent(c, TestEvent{Type: "error", Error: errorMsg})
	return fmt.Errorf("%s", errorMsg)
}

// RunTestBackground executes an account test in-memory (no real HTTP client),
// capturing SSE output via httptest.NewRecorder, then parses the result.
func (s *AccountTestService) RunTestBackground(ctx context.Context, accountID int64, modelID string) (*ScheduledTestResult, error) {
	startedAt := time.Now()

	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	ginCtx.Request = (&http.Request{}).WithContext(ctx)
	ginCtx.Set(accountTestBackgroundKey, true)

	testErr := s.TestAccountConnection(ginCtx, accountID, modelID, "", AccountTestModeDefault)

	finishedAt := time.Now()
	body := w.Body.String()
	responseText, errMsg := parseTestSSEOutput(body)

	status := "success"
	if testErr != nil || errMsg != "" {
		status = "failed"
		if errMsg == "" && testErr != nil {
			errMsg = testErr.Error()
		}
	}

	return &ScheduledTestResult{
		Status:       status,
		ResponseText: responseText,
		ErrorMessage: errMsg,
		LatencyMs:    finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
	}, nil
}

// parseTestSSEOutput extracts response text and error message from captured SSE output.
func parseTestSSEOutput(body string) (responseText, errMsg string) {
	var texts []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonStr := strings.TrimPrefix(line, "data: ")
		var event TestEvent
		if err := json.Unmarshal([]byte(jsonStr), &event); err != nil {
			continue
		}
		switch event.Type {
		case "content":
			if event.Text != "" {
				texts = append(texts, event.Text)
			}
		case "error":
			errMsg = event.Error
		}
	}
	responseText = strings.Join(texts, "")
	return
}
