# 更新日志

本文件记录每个版本的变更。发布时由 CI 截取对应版本段落作为 Release 说明。

> 说明：v1.0.7 – v1.2.16 的条目缺失，此处从 v1.2.17 起重新按版本记录。

## v1.2.24

新增 HTTP 请求指标，清理 WorkBuddy 死代码。

- `internal/metrics` 中间件按「方法 + 状态段」计数，避免路径高基数；
  `/metrics` 输出 `auto2api_http_requests_total` 与请求耗时 summary
- 删除已无引用的 `workBuddyFingerprintPool` / `workBuddyFingerprintAllocateProfile`
  （画像实际由维度派生）

## v1.2.23

指纹展示维度跟随账号自身系统族。

此前 Trae / Marvis 的指纹弹窗按随机系统族派生主机名、字体、WebGL，
会出现 Windows 机器显示 `MacBook-Pro`、macOS 出现 Microsoft YaHei 的矛盾。
现在按账号 OS 传入 family，并修正兜底表中 Apple M4 Pro 的 GPU 与 CPU 型号不一致。
新增 `ValidateDimensionExtras` 及对应测试。

## v1.2.22

上游错误统一归类并计入指标。

新增 `internal/errcode`：按状态码与响应体归类为 `auth_expired` / `risk_blocked` /
`rate_limited` / `bad_request` / `upstream_error`，风控（4100404）优先于鉴权判断。
在 OpenAI 兼容网关的上游错误处理处统一计数，通过 `/metrics` 暴露
`auto2api_upstream_errors_total{platform,class}`。不改变请求行为。

## v1.2.21

新增真实 `/metrics` 端点。

此前 `/metrics` 命中的是前端 catch-all，返回 index.html，并不存在指标。
现在输出 uptime、goroutine、堆内存、GC 次数，Prometheus 文本格式、无第三方依赖；
同时在 embed 中间件的放行列表加入 `/metrics`，避免被 SPA 接管。

## v1.2.20

删除 service 包无法编译的历史测试文件（537 个），构建流程新增 service 包测试编译检查。

这些测试引用了仓库里已不存在的符号（`AccountTypeBedrock`、`anthropicHTTPUpstreamRecorder`、
`domain.PlatformGemini` 等），并存在 `floatPtr` 重复声明，导致整包测试无法编译。
删除后包恢复可测；需要时可按文件从 git 历史恢复。

## v1.2.19

新增硬件画像自洽性校验：`ValidateFingerprintRealism` / `FingerprintMacSpecIssue`，
由独立包 `internal/fingerprintrealism` 在 CI 中执行，不依赖 service 包的历史测试。

## v1.2.18

硬件画像改为国内环境并保证机型自洽：

- 时区/语言收敛为 `Asia/Shanghai` + `zh-CN`
- 字体集按系统拆分，macOS 不再出现 Microsoft YaHei，Windows 不再出现 PingFang SC
- WebGL 由芯片/显卡推导，M2 Pro 不会再报 Apple M4
- 主机名跟随系统，macOS 不再出现 `DESKTOP-XXXXXXX`
- macOS 屏幕/容量按真实内置面板收敛，移除 Studio Display 分辨率
- Marvis 画像修正：M2 Pro 18GB→16GB、i5-10400 8→6 核、i3-9100 6→4 核、7745HX 14→8 核

## v1.2.17

交付链修正：

- 修复镜像构建读取版本号路径错误（`VERSION` 构建参数此前为空）
- `latest` 标签策略调整
- Deploy 工作流从「推送二进制 + systemd」改为发布产物校验，与容器化部署一致

## v1.0.6

修复积分卡片不自动更新。

`CreditsOverviewCards` 只在挂载时读一次快照，之后不再刷新。后端快照其实已是
最新（四个 Marvis 账号合计 4000 万），但卡片仍显示首次加载的数字，账号恢复
后总额看起来少了 1000 万。

改为接入 `useAutoRefresh`——与频道状态页、账号列表同一套机制：
30 / 60 / 300 秒可选、默认开启、页面不可见时暂停。
`useAutoRefresh` 只管理状态，定时器需显式调用 `start()`——漏掉这一步等于没刷新。组件同时暴露 `refresh()`，
带 `sync=1` 让后端立刻重读上游后再返回。

## v1.0.5

Marvis 账号加入自动续期；授权第一步不再锁死。

**自动续期**

Marvis 的 access token 来自上游 Cookie，微信登录只有 2 小时寿命。一旦过期，
所有调用返回 401 4100403「登录态过期」，额度接口取不到 `data` 字段，账号被
判零额度并自动停用。上游有续期端点，此前只有手动刷新接口在调用。

额度同步现在会在 token 距过期 30 分钟以内先续期再读额度。续期按账号串行：
上游成对轮换 access/refresh，并发续期会让后到的请求把 refresh_token 打成
废值。续期结果整组写回原凭据。

**授权流程**

生成链接后第一步（渠道选择）不再禁用。选错渠道时可直接改选，切换会清空
链接与回调输入，第二步重新开始。仅在请求进行中锁定，避免连点。

重新授权弹窗关闭时现在也会重置 Marvis 状态——此前只重置 WorkBuddy，
导致关掉再打开仍显示上一个已过期的链接。

## v1.0.4

修复仪表盘 Marvis 总积分不随账号增加而更新。

两个服务在刷新 Marvis 额度：`MarvisWalletSync` 把单账号钱包写进
`account.extra`，`MarvisCreditSync` 跨账号求和生成看板快照。后者构造了
却没有启动——没有任何地方调用 `Start()`。

结果是看板显示的是首次按需查询时的账号数：新加账号后 `account.extra`
已更新，快照却停在旧值，四个账号各 1000 万只报出 1000 万。

现已把 `MarvisCreditSync` 接入 Application 并在钱包同步旁一并启动。
两个查询都是全量列账号、不按归属过滤，因此总额覆盖所有用户添加的账号。

## v1.0.3

清理前端对不存在接口的调用，并修正构建流程。

- 管理登录时会请求 `/admin/compliance`，该接口在后端已不存在，每次返回 401。
  合规确认对话框因此永远不会弹出。已移除整个链路：store、API 客户端、
  对话框组件、语言文案与对应拦截器测试。
- 设置加载时会请求 `/admin/payment/config`，同样不存在。支付开关改读
  系统设置里的 `payment_enabled` 字段，该字段后端有返回。
- 删除 `getConfig` 客户端方法，已无调用方。

构建：

- 打标签与推 main 曾各触发一次完整构建，同一版本产生两份产物。改为只由
  标签驱动，PR 仅做编译校验，一次发布只构建一次。
- 升级 `pnpm/action-setup` 到 v5、`softprops/action-gh-release` 到 v3，
  这两个是最后仍运行在 Node 20 上的 action。

## v1.0.2

修复 v1.0.1 未覆盖的积分精度问题。

上一版只改了仪表盘的两张卡片，其余位置的积分仍按四位小数或按量级切换精度
显示，同一屏幕上会出现 `8,475.74` 与 `0.0004` 并存。现统一为两位小数，
共 20 处：模型成本表、用量统计卡、账号统计弹窗、积分明细弹窗、用量趋势图、
模型与端点分布图、平台用量拆分、API Key 列表。

千位以上保留 K 单位，形如 `1,234.57K`。风控阈值仍为四位小数——那是百分比，
不是积分。

## v1.0.1

修复版，解决上一版遗留的三处界面细节。

- 版本号显示重复前缀，页头渲染成 `vv1.0.0`。后端返回值本身带 `v`，
  徽章又补了一次前缀；现在只在缺失时补。（`v1.0.0` 起所有构建均受影响）
- 仪表盘积分精度不统一。管理端按量级切换 2 / 3 / 4 位小数，用户端固定
  4 位，同一排卡片里数字精度各异；现统一为两位小数。
- 积分卡片图标重复。第四张「平台合计」与第二张「WorkBuddy 总量」共用
  trophy，改为 grid，四张卡片图标互不相同。

界面调整：

- 第三行第三、四张卡片（Marvis 与合计）改用 K 单位显示，
  形如 `1,234.57K`，仍保留两位小数。

构建：

- 升级 GitHub Actions 到 Node 24 运行时（checkout v5、setup-go v6、
  setup-node v5、artifact v5）。此前的 Node 20 弃用提示来自 action
  自身，与工具链无关。
- 停用 Docker 镜像构建，日常 CI 不再产出镜像。需要镜像时用仓库内的
  Dockerfile 自行构建。

## v1.0.0

首个正式版本。WorkBuddy（国内版 / 国际版）与腾讯 Marvis 双平台
OpenAI 兼容网关。

平台：

- 三个渠道各自独立的 OAuth 授权：WorkBuddy 走浏览器授权，Marvis 支持
  QQ 与微信扫码，完成后粘贴回调地址换取登录态。
- WorkBuddy 国际站走 console 两段式会话，先建会话拿 conversationId
  再发起对话。
- 模型清单从上游 `/v3/config` 动态拉取。

计费：

- 结算单位是积分。
- 模型价格从上游 `credits` 倍率实时读取，不落库，避免维护会过期的副本；
  取不到时回落到通用价格库。
- 积分归零的账号自动停止调度。

账号管理：

- 所有登录用户对全部账号拥有完整操作权限，编辑与删除按钮仅管理员可见。
- 授权流程三步：选渠道 → 生成链接 → 粘贴回调地址，按钮状态随流程推进。
- 支持测试连接、查看统计、硬件指纹与重新授权。

部署：

- 单个二进制内嵌前端产物，无外部静态文件依赖。
- 需要 PostgreSQL 与 Redis。

---

## v1.0.23

验证在线升级链路的版本。无功能变更。


## v1.0.24

修复在线升级：下载文件与解包目标同名导致二进制被写成 0 字节。解包格式改为按内容嗅探。


## v1.0.25

在线升级端到端验证版本。无功能变更。


## v1.0.26

在线升级下载加入重试，缓解 release-assets 偶发连接超时。


## v1.0.27

在线升级最终验证版本。


## v1.0.28

修复在线升级的校验文件名匹配。


## v1.0.29

在线升级端到端验证。


## v1.0.35

- 升级面板改用环形进度
- 支持自动下载与自动安装开关
- 更新日志可滚动查看


## v1.0.37

- 升级面板：环形进度、自动下载、自动安装
- 更新日志可滚动
- 修复部分账号积分显示不可用


## v1.0.42

- 弹窗居中
- 开关位置修复


## v1.0.44

- 弹窗尺寸统一
- 内容超出时滚动
- 位置恢复初始

