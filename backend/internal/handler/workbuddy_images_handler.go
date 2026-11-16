package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	pkghttputil "github.com/DeltrivX/Auto2API/internal/pkg/httputil"
	middleware2 "github.com/DeltrivX/Auto2API/internal/server/middleware"
	"github.com/DeltrivX/Auto2API/internal/service"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// WorkBuddyImages 处理 WorkBuddy 平台的 OpenAI Images API 请求。
//
//	POST /v1/images/generations  文生图
//	POST /v1/images/edits        图生图
//
// 与 OpenAI 的 Images 走不同实现：WorkBuddy 上游提供自己的
// /v2/images/* 端点，请求体必须明文 JSON（gzip 会 400 11101），
// 响应是 {code, msg, data} 信封。这里做协议搬移，不做模型池调度。
func (h *OpenAIGatewayHandler) WorkBuddyImages(c *gin.Context) {
	apiKey, ok := middleware2.GetAPIKeyFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusUnauthorized, "authentication_error", "Invalid API key")
		return
	}
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		h.errorResponse(c, http.StatusInternalServerError, "api_error", "User context not found")
		return
	}
	reqLog := requestLogger(
		c,
		"handler.openai_gateway.workbuddy_images",
		zap.Int64("user_id", subject.UserID),
		zap.Int64("api_key_id", apiKey.ID),
		zap.Any("group_id", apiKey.GroupID),
	)

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(c.Request)
	if err != nil {
		if handleRequestBodyReadError(c, reqLog, err) {
			return
		}
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "cannot read request body")
		return
	}
	if len(body) == 0 {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "request body is required")
		return
	}

	var req service.WorkBuddyImageRequest
	if err := json.Unmarshal(body, &req); err != nil {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		h.errorResponse(c, http.StatusBadRequest, "invalid_request_error", "prompt is required")
		return
	}

	edit := strings.HasSuffix(strings.TrimRight(c.Request.URL.Path, "/"), "/edits")

	// 账号：优先用请求里显式指定的，否则走调度。
	account, release, err := h.gatewayService.AcquireWorkBuddyImageAccount(c.Request.Context(), apiKey)
	if err != nil {
		reqLog.Warn("workbuddy_image_account_failed", zap.Error(err))
		h.errorResponse(c, http.StatusServiceUnavailable, "api_error", err.Error())
		return
	}
	if release != nil {
		defer release()
	}

	// 图像生成很慢，脱离请求取消，避免客户端提前断开导致上游浪费。
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 300*time.Second)
	defer cancel()

	start := time.Now()
	result, err := service.WorkBuddyGenerateImage(ctx, account, req, edit)
	elapsed := time.Since(start)
	if err != nil {
		reqLog.Warn("workbuddy_image_failed",
			zap.Int64("account_id", account.ID),
			zap.Bool("edit", edit),
			zap.Duration("elapsed", elapsed),
			zap.Error(err))
		h.errorResponse(c, http.StatusBadGateway, "upstream_error", err.Error())
		return
	}

	reqLog.Info("workbuddy_image_ok",
		zap.Int64("account_id", account.ID),
		zap.String("model", req.Model),
		zap.Bool("edit", edit),
		zap.Int("bytes", len(result.Body)),
		zap.Duration("elapsed", elapsed))

	c.Data(http.StatusOK, result.ContentType, result.Body)
}

// acquireWorkBuddyImageAccount 选出一个可用的 WorkBuddy 账号。
//
// 图像请求不走普通的对话转发路径，因此这里只做最小校验：
