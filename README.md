# Auto2API

多账号统一管理面板与 OpenAI 兼容 API 网关，面向 NAS 与服务器部署。

把 WorkBuddy 与腾讯 Marvis 的账号管理、积分计量与模型调用能力收进一个容器，
对外提供标准 OpenAI 接口。三个渠道共用一套调度与计费，切换平台只需换一个
API Key。

<p>
  <a href="https://github.com/deltrivx/Auto2API/releases"><img src="https://img.shields.io/github/v/release/deltrivx/Auto2API?display_name=tag&sort=semver&label=Release" alt="Release" /></a>
  <img src="https://img.shields.io/badge/Docker-Ready-2496ED?logo=docker&logoColor=white" alt="Docker" />
  <img src="https://img.shields.io/badge/Go-Gateway-00ADD8?logo=go&logoColor=white" alt="Go" />
  <img src="https://img.shields.io/badge/Vue_3-Panel-4FC08D?logo=vuedotjs&logoColor=white" alt="Vue" />
  <img src="https://img.shields.io/badge/OpenAI_API-Compatible-412991?logo=openai&logoColor=white" alt="OpenAI API" />
  <a href="LICENSE"><img src="https://img.shields.io/badge/License-MIT-green.svg" alt="License" /></a>
</p>

[功能](#功能) · [快速开始](#快速开始) · [配置](#配置) · [接入](#接入) ·
[运维](#运维) · [实现说明](#实现说明) · [许可](#许可)

---

## 功能

### 三渠道接入

| 渠道 | 授权方式 | 说明 |
| :--- | :--- | :--- |
| WorkBuddy 国内版 | 浏览器 OAuth | 生成授权链接，登录后回填回调地址 |
| WorkBuddy 国际版 | 浏览器 OAuth | 同上，走 console 两段式会话 |
| 腾讯 Marvis | QQ / 微信扫码 | 扫码授权后粘贴回调地址换取登录态 |

三个渠道各自维护账号池，但共用一套 API Key、分组与计费口径。

### 账号池

- **请求级分摊**：每次请求独立分配账号，上下文与计费互不串扰。
- **并发优先空闲**：按「在飞请求数最少」挑选账号，避免慢请求集中。
- **账号级控制**：启用 / 停用、调度开关、临时停用、重新授权。
- **积分归零自动停用**：上游返回余额 0 时自动置为不可调度，充值后手动恢复。

### 计费

结算单位是**积分**，不是美元。

- **价格实时读取**：WorkBuddy 的模型价格来自上游 `/v3/config` 的 `credits`
  倍率，每次组装数据时现取并缓存五分钟。不落库，避免维护一份会过期的副本。
- **回落链**：渠道配置价 → 上游挂牌价 → 通用价格库 → 未标价。
- **分组倍率**：按分组设置计费倍率，支持分时段倍率。
- **用量留痕**：每次调用记录 token 数与实际扣费，可在仪表盘按模型 / 账号聚合。

### 模型广场

- 按分组展示可用模型、计费方式与实付价。
- 支持文本、图片、视频、按次四种计费模式。
- 模型图标按平台着色，价格单位与后台一致。

### 面板

- **仪表盘**：今日请求、积分消耗、四张积分卡片（WorkBuddy 今日 / 总量 /
  Marvis / 合计）。
- **账号管理**：渠道筛选、批量测试、硬件指纹查看、重新授权。
- **API Key**：按分组签发，可设过期时间与额度。
- **用户与分组**：角色权限、分组倍率、并发上限。

---

## 快速开始

### Docker

```bash
docker run -d --name auto2api \
  -p 8080:8080 \
  -v /path/to/data:/app/data \
  -e DATABASE_URL="postgres://user:pass@host:5432/auto2api?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  ghcr.io/deltrivx/auto2api:latest
```

打开 `http://YOUR_SERVER_IP:8080`，按向导完成初始化。

### 从 Release 安装

```bash
# 下载对应架构的二进制
curl -L -o auto2api \
  https://github.com/deltrivx/Auto2API/releases/latest/download/auto2api
chmod +x auto2api
./auto2api
```

### 从源码构建

```bash
git clone https://github.com/deltrivx/Auto2API.git
cd Auto2API

# 前端产物内嵌进后端二进制
cd frontend && pnpm install && pnpm run build && cd ..
cd backend && CGO_ENABLED=0 go build -tags embed -o ../auto2api ./cmd/server
```

---

## 配置

主要环境变量：

| 变量 | 默认值 | 说明 |
| :--- | :--- | :--- |
| `SERVER_PORT` | `8080` | HTTP 监听端口 |
| `DATABASE_URL` | — | PostgreSQL 连接串，必填 |
| `REDIS_ADDR` | — | Redis 地址，必填 |
| `JWT_SECRET` | 自动生成 | 会话签名密钥 |
| `LOG_LEVEL` | `info` | 日志级别 |

其余配置可在面板「系统设置」中修改，写入数据库后立即生效。

---

## 接入

网关兼容 OpenAI 协议，任何支持自定义 base_url 的客户端都能接入。

### Python

```python
from openai import OpenAI

client = OpenAI(
    base_url="http://YOUR_SERVER_IP:8080/v1",
    api_key="sk-...",           # 面板中签发的 API Key
)

resp = client.chat.completions.create(
    model="auto",               # 或 balanced-model / deep-model
    messages=[{"role": "user", "content": "你好"}],
)
print(resp.choices[0].message.content)
```

### curl

```bash
curl http://YOUR_SERVER_IP:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-..." \
  -H "Content-Type: application/json" \
  -d '{
    "model": "auto",
    "messages": [{"role": "user", "content": "你好"}],
    "stream": false
  }'
```

### 端点

| 端点 | 说明 |
| :--- | :--- |
| `POST /v1/chat/completions` | 对话补全，支持流式与工具调用 |
| `POST /v1/messages` | Anthropic 协议入口，供 Claude Code 等客户端使用 |
| `GET /v1/models` | 当前分组可用的模型清单 |
| `GET /health` | 健康检查 |

模型名以「模型广场」中该分组实际可用的为准。WorkBuddy 的 `auto` 由上游
自行路由，实际计费按被路由到的模型计算。

---

## 运维

### 账号保活

- 积分归零的账号自动置为不可调度，不会持续失败。
- 凭据过期的账号在列表中标记为错误状态，重新授权即可恢复。
- 授权流程：选择渠道 → 生成链接 → 粘贴回调地址，按钮状态随流程推进。

### 日志

```bash
docker logs -f auto2api
```

请求日志含 `request_id`、`account_id`、`platform`、`model` 与状态码，
可按 `request_id` 串联完整链路。

### 备份

需要备份的只有数据库。Redis 用作缓存与并发计数，重启后自动重建。

```bash
pg_dump -Fc "$DATABASE_URL" > auto2api-$(date +%F).dump
```

---

## 实现说明

### 请求链路

```
客户端 ──▶ /v1/chat/completions
           │
           ├─ 鉴权与分组解析
           ├─ 选号：并发优先空闲
           ├─ 协议适配：OpenAI ⇄ 上游协议
           ├─ 上游转发：WorkBuddy / Marvis
           └─ 计费落库 + SSE 回写
```

三个渠道的上游都是 OpenAI 协议，因此网关内部只需一套转发实现；
Marvis 与 WorkBuddy 国际站在会话建立、鉴权头上各有差异，由各自的适配层处理。

### 价格来源

WorkBuddy 的私有模型（`auto`、`balanced-model`、`codewise-*` 等）不在任何
公开价格库里，唯一权威来源是上游 `/v3/config` 响应中每个模型的 `credits`
字段（形如 `x0.15`，即每次调用消耗 0.15 积分）。

网关在组装模型广场时向上游读取该字段，缓存五分钟。取不到就回落到通用价格库，
再取不到则显示未标价——不会编造数字。

### 数据表

| 表 | 用途 |
| :--- | :--- |
| `accounts` | 各渠道账号与凭据 |
| `groups` | 分组、倍率、并发上限 |
| `api_keys` | 签发的密钥与额度 |
| `channels` | 渠道模型映射与定价 |
| `usage_logs` | 调用明细与扣费记录 |

---

## 许可

[MIT](LICENSE)

---

## 致谢

- [LiteLLM](https://github.com/BerriAI/litellm) — 通用模型价格数据
- [Simple Icons](https://simpleicons.org/) — 平台标识
