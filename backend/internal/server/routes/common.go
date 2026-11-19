package routes

import (
	"fmt"
	"net/http"
	"runtime"
	"time"

	"github.com/DeltrivX/Auto2API/internal/errcode"
	"github.com/DeltrivX/Auto2API/internal/metrics"
	"github.com/gin-gonic/gin"
)

// processStart 用于计算进程运行时长。
var processStart = time.Now()

// RegisterCommonRoutes 注册通用路由（健康检查、状态等）
func RegisterCommonRoutes(r *gin.Engine) {
	// 健康检查
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	// Prometheus 指标（最小集：进程与 Go 运行时，无第三方依赖）
	r.GET("/metrics", func(c *gin.Context) {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		fmt.Fprintf(c.Writer, "# HELP auto2api_uptime_seconds 进程运行时长（秒）\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_uptime_seconds gauge\nauto2api_uptime_seconds %.0f\n", time.Since(processStart).Seconds())
		fmt.Fprintf(c.Writer, "# HELP auto2api_goroutines 当前 goroutine 数\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_goroutines gauge\nauto2api_goroutines %d\n", runtime.NumGoroutine())
		fmt.Fprintf(c.Writer, "# HELP auto2api_memory_heap_alloc_bytes 堆内存已分配字节\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_memory_heap_alloc_bytes gauge\nauto2api_memory_heap_alloc_bytes %d\n", ms.HeapAlloc)
		fmt.Fprintf(c.Writer, "# HELP auto2api_memory_heap_sys_bytes 向系统申请的堆字节\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_memory_heap_sys_bytes gauge\nauto2api_memory_heap_sys_bytes %d\n", ms.HeapSys)
		fmt.Fprintf(c.Writer, "# HELP auto2api_gc_runs GC 次数\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_gc_runs counter\nauto2api_gc_runs %d\n", ms.NumGC)
		fmt.Fprintf(c.Writer, "# HELP auto2api_upstream_errors_total 按平台与类别统计的上游错误次数\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_upstream_errors_total counter\n")
		reqs, latSum, latCnt := metrics.Snapshot()
		fmt.Fprintf(c.Writer, "# HELP auto2api_http_requests_total 按方法与状态段统计的请求数\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_http_requests_total counter\n")
		for k, v := range reqs {
			var method, class string
			for i := 0; i < len(k); i++ {
				if k[i] == '|' {
					method, class = k[:i], k[i+1:]
					break
				}
			}
			fmt.Fprintf(c.Writer, "auto2api_http_requests_total{method=%q,status_class=%q} %d\n", method, class, v)
		}
		fmt.Fprintf(c.Writer, "# HELP auto2api_http_request_duration_seconds 请求累计耗时与次数\n")
		fmt.Fprintf(c.Writer, "# TYPE auto2api_http_request_duration_seconds summary\n")
		for method, sum := range latSum {
			fmt.Fprintf(c.Writer, "auto2api_http_request_duration_seconds_sum{method=%q} %g\n", method, sum)
			fmt.Fprintf(c.Writer, "auto2api_http_request_duration_seconds_count{method=%q} %d\n", method, latCnt[method])
		}
		for platform, classes := range errcode.Snapshot() {
			for class, n := range classes {
				fmt.Fprintf(c.Writer, "auto2api_upstream_errors_total{platform=%q,class=%q} %d\n", platform, class, n)
			}
		}
	})

	// Claude Code 遥测日志（忽略，直接返回200）
	r.POST("/api/event_logging/batch", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	// Setup status endpoint (always returns needs_setup: false in normal mode)
	// This is used by the frontend to detect when the service has restarted after setup
	r.GET("/setup/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": gin.H{
				"needs_setup": false,
				"step":        "completed",
			},
		})
	})
}
