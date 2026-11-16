package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/DeltrivX/Auto2API/internal/service"
)

func derefGroupID(groupID *int64) int64 {
	if groupID == nil {
		return 0
	}
	return *groupID
}

func googleErrorWithType(c *gin.Context, status int, errType, code, message string) {
	service.SetOpsLocalErrorType(c, errType, code)
	c.JSON(status, gin.H{
		"error": gin.H{
			"code":    status,
			"message": message,
			"status":  googleStatusForHandler(status),
		},
	})
}

// googleStatusForHandler 把 HTTP 状态码映射为 Google API 的 status 字符串。
func googleStatusForHandler(status int) string {
	switch status {
	case 400:
		return "INVALID_ARGUMENT"
	case 401:
		return "UNAUTHENTICATED"
	case 403:
		return "PERMISSION_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RESOURCE_EXHAUSTED"
	case 500:
		return "INTERNAL"
	case 503:
		return "UNAVAILABLE"
	}
	return "UNKNOWN"
}
