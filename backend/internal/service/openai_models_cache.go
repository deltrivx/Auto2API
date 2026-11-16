package service

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	infraerrors "github.com/DeltrivX/Auto2API/internal/pkg/errors"
)

// 模型清单的进程内缓存与上游获取。
//
// 这段实现原先与 Codex 清单代码混在 openai_codex_models_service.go 中，
// 但它服务的是通用 /v1/models 路径（WorkBuddy 也走这里），因此摘出来独立成文件。

// openAIModelsCache 是模型清单缓存。
type openAIModelsCache struct {
	mu        sync.Mutex
	entries   map[string]openAIModelsCacheEntry
	nextOrder uint64
	refresh   singleflight.Group
}

// openAIModelsCacheEntry 是缓存条目。
type openAIModelsCacheEntry struct {
	response *OpenAIModelsResponse
	storedAt time.Time
	order    uint64
}

// openAIModelsCacheState 描述缓存命中状态。
type openAIModelsCacheState int

const (
	openAIModelsCacheMiss openAIModelsCacheState = iota
	openAIModelsCacheFresh
	openAIModelsCacheStale
)

// get 查询缓存。
func (c *openAIModelsCache) get(key string, now time.Time) (*OpenAIModelsResponse, openAIModelsCacheState) {
	if c == nil {
		return nil, openAIModelsCacheMiss
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || entry.response == nil {
		return nil, openAIModelsCacheMiss
	}
	if now.Sub(entry.storedAt) < openAIModelsCacheFreshTTL {
		return entry.response, openAIModelsCacheFresh
	}
	return entry.response, openAIModelsCacheStale
}

// set 写入缓存。
func (c *openAIModelsCache) set(key string, response *OpenAIModelsResponse, now time.Time) {
	if c == nil || response == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]openAIModelsCacheEntry)
	}
	c.nextOrder++
	c.entries[key] = openAIModelsCacheEntry{response: response, storedAt: now, order: c.nextOrder}
}

const openAIModelsCacheFreshTTL = 60 * time.Second

// fetchCachedOpenAIModels 读缓存；未命中或过期时调用 fetch 拉取。
func (s *OpenAIGatewayService) fetchCachedOpenAIModels(
	ctx context.Context,
	request openAIModelsRequest,
	fetch func(ctx context.Context, ifNoneMatch string) (*OpenAIModelsResponse, error),
	ifNoneMatch string,
) (*OpenAIModelsResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cacheKey := buildOpenAIModelsCacheKey(request)
	cached, state := s.openAIModelsCache.get(cacheKey, time.Now())
	if state == openAIModelsCacheFresh {
		return cloneOpenAIModelsResponse(cached), nil
	}
	response, err := fetch(ctx, "")
	if err != nil {
		// 拉取失败时退回旧快照，避免 /v1/models 直接 502。
		if cached != nil {
			return cloneOpenAIModelsResponse(cached), nil
		}
		return nil, err
	}
	if response != nil && !response.NotModified {
		s.openAIModelsCache.set(cacheKey, response, time.Now())
	}
	return response, nil
}

// fetchOpenAIModelsUpstream 向上游请求模型清单。
func (s *OpenAIGatewayService) fetchOpenAIModelsUpstream(ctx context.Context, request openAIModelsRequest, ifNoneMatch string) (*OpenAIModelsResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, request.url, nil)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_MODELS_REQUEST_INVALID", "cannot build models request: %v", err)
	}
	for key, values := range request.headers {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	if strings.TrimSpace(ifNoneMatch) != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_MODELS_UPSTREAM_FAILED", "models request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return &OpenAIModelsResponse{NotModified: true, ETag: resp.Header.Get("ETag")}, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_MODELS_READ_FAILED", "cannot read models response: %v", readErr)
	}
	if resp.StatusCode >= 400 {
		return nil, infraerrors.Newf(http.StatusBadGateway, "OPENAI_MODELS_UPSTREAM_ERROR", "upstream returned %d: %s", resp.StatusCode, truncateForLog(body, 200))
	}
	return &OpenAIModelsResponse{Body: body, ETag: resp.Header.Get("ETag")}, nil
}

// buildOpenAIModelsCacheKey 生成缓存键。
func buildOpenAIModelsCacheKey(request openAIModelsRequest) string {
	return fmt.Sprintf("%s|%d|%t", request.url, request.accountID, request.useAPIKeyUpstream)
}

// cloneOpenAIModelsResponse 复制响应，避免缓存被调用方改写。
func cloneOpenAIModelsResponse(in *OpenAIModelsResponse) *OpenAIModelsResponse {
	if in == nil {
		return nil
	}
	out := *in
	if in.Body != nil {
		out.Body = append([]byte(nil), in.Body...)
	}
	return &out
}
