# OpenAI 代码分类（双平台四渠道安全区分析）

生成时间：2026-11-16
判定方法：是否有生产路由注册 + 是否被网关调度/转发引用

## A 类：平台专属，无生产路由 —— 可安全删除

| 文件 | 行数 | 判定依据 |
|---|---|---|
| `handler/admin/openai_oauth_handler.go` | 563 | 无路由注册，仅在 wire_gen 残留实例化 |
| `service/openai_oauth_service.go` | 448 | 仅被上一行引用 |
| `handler/admin/openai_referral_handler.go` | 113 | 无路由 |
| `service/openai_referral_service.go` | 200 | 无路由 |
| `service/openai_referral_client.go` | 19 | 无路由 |
| `service/openai_quota_reset_workflow.go` | 86 | 仅被 OAuth 引用 |
| `service/openai_chatgpt_headers.go` | ~50 | 无外部引用 |
| `handler/openai_page_context.go` | 123 | 无路由，1 处引用 |
| `handler/openai_cyber_allowlist.go` | 18 | 无路由，0 处引用 |

**合计约 1620 行**

## B 类：协议层 —— 必须保留

- `internal/pkg/openai/`（含 codex_gpt61_*.json 等）
- `internal/pkg/openai_compat/`

**理由**：网关本体。WorkBuddy 与 Marvis 都是 OpenAI 协议上游，
对外提供 /v1/chat/completions 靠这层收发。AutoBuddy 同样保留。

## C 类：调度 / 转发 —— 必须保留

约 100+ 文件，代表：
- `openai_account_scheduler.go` — 账号调度
- `openai_client_transport.go` — 传输层
- `openai_chat_roles.go` — 消息角色处理
- `openai_403_counter.go` — 错误计数
- `openai_live.go` — **754 处引用**，核心

**理由**：双平台账号的负载均衡、失败切换、限流都依赖这些。

## D 类：网关入口 handler —— 必须保留

- `openai_chat_completions.go` — 主端点
- `openai_models_handler.go` — /v1/models
- `openai_embeddings.go`
- `openai_images.go`

**理由**：对外 API 面。

## 删除 A 类的影响评估

- 网关转发：无影响
- 四渠道调用：无影响
- 模型广场：无影响
- 管理端 OpenAI OAuth 入口：**已不存在**，删除不影响
- 需同步修改：`wire.go`、`wire_gen.go` 移除 A 类的装配
