package admin

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/DeltrivX/Auto2API/internal/pkg/response"

	"github.com/gin-gonic/gin"
)

// respondSnapshot 以 ETag 方式返回快照。
//
// 三个平台快照的 JSON 有 3~30KB，而内容只在同步周期（5 分钟）内变化。
// 加 ETag 后，重复访问走 304，不再重复传输和重新渲染。
//
// syncedAt 是快照的生成时间，直接参与 ETag 计算——同一个同步周期内
// 内容不变，ETag 就不会变。
func respondSnapshot(c *gin.Context, payload any, syncedAt time.Time) {
	// 包装成与 response.Success 相同的信封，前端不需要改解析逻辑。
	envelope := gin.H{"code": 0, "message": "success", "data": payload}

	raw, err := json.Marshal(envelope)
	if err != nil {
		response.InternalError(c, "快照序列化失败")
		return
	}

	tag := sha1.Sum([]byte(fmt.Sprintf("%d|%d", syncedAt.UnixNano(), len(raw))))
	etag := `"` + hex.EncodeToString(tag[:8]) + `"`

	c.Header("ETag", etag)
	c.Header("Cache-Control", "private, max-age=0, must-revalidate")

	// 客户端的 ETag 一致就直接 304，省掉这几十 KB。
	if match := c.GetHeader("If-None-Match"); match == etag {
		c.Status(http.StatusNotModified)
		return
	}

	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
