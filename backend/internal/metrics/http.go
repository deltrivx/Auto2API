// Package metrics 提供轻量的 HTTP 请求指标，无第三方依赖。
//
// 指标按「方法 + 状态段」聚合，避免按路径拆分导致的高基数。
package metrics

import (
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

var (
	mu         sync.Mutex
	requests   = map[string]int64{}   // "METHOD|2xx" -> 次数
	latencySum = map[string]float64{} // METHOD -> 累计秒
	latencyCnt = map[string]int64{}   // METHOD -> 次数
)

// Middleware 统计请求数与累计耗时。
func Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		elapsed := time.Since(start).Seconds()
		method := c.Request.Method
		class := statusClass(c.Writer.Status())

		mu.Lock()
		requests[method+"|"+class]++
		latencySum[method] += elapsed
		latencyCnt[method]++
		mu.Unlock()
	}
}

func statusClass(code int) string {
	switch {
	case code >= 500:
		return "5xx"
	case code >= 400:
		return "4xx"
	case code >= 300:
		return "3xx"
	case code >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}

// Snapshot 返回当前计数的副本，供 /metrics 输出。
func Snapshot() (map[string]int64, map[string]float64, map[string]int64) {
	mu.Lock()
	defer mu.Unlock()

	req := make(map[string]int64, len(requests))
	for k, v := range requests {
		req[k] = v
	}
	sum := make(map[string]float64, len(latencySum))
	for k, v := range latencySum {
		sum[k] = v
	}
	cnt := make(map[string]int64, len(latencyCnt))
	for k, v := range latencyCnt {
		cnt[k] = v
	}
	return req, sum, cnt
}
