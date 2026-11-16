package main

//go:generate go run github.com/google/wire/cmd/wire

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "github.com/DeltrivX/Auto2API/ent/runtime"
	"github.com/DeltrivX/Auto2API/internal/config"
	"github.com/DeltrivX/Auto2API/internal/handler"
	"github.com/DeltrivX/Auto2API/internal/pkg/logger"
	"github.com/DeltrivX/Auto2API/internal/server/middleware"
	"github.com/DeltrivX/Auto2API/internal/setup"
	"github.com/DeltrivX/Auto2API/internal/web"

	"github.com/gin-gonic/gin"
)

//go:embed VERSION
var embeddedVersion string

// Build-time variables (can be set by ldflags)
var (
	Version   = ""
	Commit    = "unknown"
	Date      = "unknown"
	BuildType = "source" // "source" for manual builds, "release" for CI builds (set by ldflags)
)

func init() {
	// 如果 Version 已通过 ldflags 注入（例如 -X main.Version=...），则不要覆盖。
	if strings.TrimSpace(Version) != "" {
		return
	}

	// 默认从 embedded VERSION 文件读取版本号（编译期打包进二进制）。
	Version = strings.TrimSpace(embeddedVersion)
	if Version == "" {
		Version = "0.0.0-dev"
	}
}

// initLogger configures the default slog handler based on gin.Mode().
// In non-release mode, Debug level logs are enabled.
func main() {
	logger.InitBootstrap()
	defer logger.Sync()

	// Parse command line flags
	setupMode := flag.Bool("setup", false, "Run setup wizard in CLI mode")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		log.Printf("Auto2API %s (commit: %s, built: %s)\n", Version, Commit, Date)
		return
	}

	// CLI setup mode
	if *setupMode {
		if err := setup.RunCLI(); err != nil {
			log.Fatalf("Setup failed: %v", err)
		}
		return
	}

	// Check if setup is needed
	if setup.NeedsSetup() {
		// Check if auto-setup is enabled (for Docker deployment)
		if setup.AutoSetupEnabled() {
			log.Println("Auto setup mode enabled...")
			if err := setup.AutoSetupFromEnv(); err != nil {
				log.Fatalf("Auto setup failed: %v", err)
			}
			// Continue to main server after auto-setup
		} else {
			log.Println("First run detected, starting setup wizard...")
			runSetupServer()
			return
		}
	}

	// Normal server mode
	runMainServer()
}

func runSetupServer() {
	r := gin.New()
	r.Use(middleware.Recovery())
	r.Use(middleware.CORS(config.CORSConfig{}))
	r.Use(middleware.SecurityHeaders(config.CSPConfig{Enabled: true, Policy: config.DefaultCSPPolicy}, nil))

	// Register setup routes
	setup.RegisterRoutes(r)

	// Serve embedded frontend if available
	if web.HasEmbeddedFrontend() {
		r.Use(web.ServeEmbeddedFrontend())
	}

	// Get server address from config.yaml or environment variables (SERVER_HOST, SERVER_PORT)
	// This allows users to run setup on a different address if needed
	addr := config.GetServerAddress()
	log.Printf("Setup wizard available at http://%s", addr)
	log.Println("Complete the setup wizard to configure Auto2API")

	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetUnencryptedHTTP2(true)

	server := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		Protocols:         protocols,
	}

	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("Failed to start setup server: %v", err)
	}
}

func runMainServer() {
	cfg, err := config.LoadForBootstrap()
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if err := logger.Init(logger.OptionsFromConfig(cfg.Log)); err != nil {
		log.Fatalf("Failed to initialize logger: %v", err)
	}
	if cfg.RunMode == config.RunModeSimple {
		log.Println("⚠️  WARNING: Running in SIMPLE mode - billing and quota checks are DISABLED")
	}

	buildInfo := handler.BuildInfo{
		Version:   Version,
		BuildType: BuildType,
	}

	app, err := initializeApplication(buildInfo)
	if err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	// 积分任务调度：每个账号自行决定是否开启自动签到/自动领奖，
	// 未开启时这一轮直接跳过，不产生任何上游请求。
	if app.TaskScheduler != nil {
		schedCtx, cancelSched := context.WithCancel(context.Background())
		defer cancelSched()
		app.TaskScheduler.Start(schedCtx)
	}

	// 积分云端同步：账号可能在其它终端签到/做任务，积分要以上游为准，
	// 不能只统计本系统执行过的部分。
	if app.CreditSync != nil {
		syncCtx, cancelSync := context.WithCancel(context.Background())
		defer cancelSync()
		app.CreditSync.Start(syncCtx)
	}
	// Marvis 的令牌额度同样是上游权威：每天 0 点重置回 1000 万，
	// 期间随用量下降。后台定时拉取，前端读缓存。
	if app.MarvisWalletSync != nil {
		marvisSyncCtx, cancelMarvisSync := context.WithCancel(context.Background())
		defer cancelMarvisSync()
		app.MarvisWalletSync.Start(marvisSyncCtx)
	}
	// 仪表盘的 Marvis 汇总走独立快照：它跨账号求和，而 WalletSync 只刷新
	// 单账号字段。此前这里没有启动，看板一直显示首次查询时的账号数，
	// 新加的账号不会计入总额。
	if app.MarvisCreditSync != nil {
		marvisCreditCtx, cancelMarvisCredit := context.WithCancel(context.Background())
		defer cancelMarvisCredit()
		app.MarvisCreditSync.Start(marvisCreditCtx)
	}
	// 渠道监控的定时打点。
	//
	// 这里此前没有启动：监控记录建好了、界面上也有开关，但 runner 从未跑起来，
	// last_checked_at 一直停在启用那一刻（实测停在 15:00，八小时后仍未更新），
	// 界面就显示成异常。runner 内部按每个监控自己的 interval 调度。
	if app.ChannelMonitorRunner != nil {
		app.ChannelMonitorRunner.Start()
	}
	defer app.Cleanup()
	if app.PromptAudit != nil {
		if err := app.PromptAudit.Start(context.Background()); err != nil {
			// Startup continues so unrelated APIs stay up. Fail-closed (unavailable)
			// applies only when a persisted blocking policy was observed; without
			// blocking intent, Prompt Audit stays ModeOff so the gateway remains
			// usable and administrators can still disable the feature (#4560).
			log.Printf("Prompt Audit started in degraded state: %v", err)
		}
	}

	// 启动服务器
	go func() {
		if err := app.Server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("Failed to start server: %v", err)
		}
	}()

	log.Printf("Server started on %s", app.Server.Addr)

	// 等待中断信号
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := app.Server.Shutdown(ctx); err != nil {
		log.Printf("Server forced to shutdown: %v", err)
	}

	log.Println("Server exited")
}
