# Auto2API Docker Image

Auto2API is an AI API Gateway Platform for distributing and managing AI product subscription API quotas.

## Quick Start

```bash
docker run -d \
  --name auto2api \
  -p 8080:8080 \
  -e DATABASE_URL="postgres://user:pass@host:5432/auto2api" \
  -e REDIS_URL="redis://host:6379" \
  weishaw/auto2api:latest
```

## Docker Compose

```yaml
version: '3.8'

services:
  auto2api:
    image: weishaw/auto2api:latest
    ports:
      - "8080:8080"
    environment:
      - DATABASE_URL=postgres://postgres:postgres@db:5432/auto2api?sslmode=disable
      - REDIS_URL=redis://redis:6379
    depends_on:
      - db
      - redis

  db:
    image: postgres:15-alpine
    environment:
      - POSTGRES_USER=postgres
      - POSTGRES_PASSWORD=postgres
      - POSTGRES_DB=auto2api
    volumes:
      - postgres_data:/var/lib/postgresql/data

  redis:
    image: redis:7-alpine
    volumes:
      - redis_data:/data

volumes:
  postgres_data:
  redis_data:
```

## Startup and Database Recovery

Auto2API runs database migrations while starting. PostgreSQL may still be
recovering briefly after a host or Docker daemon restart. The application
retries transient PostgreSQL startup and connection errors with bounded
exponential backoff, then continues startup when the database is ready.
Permanent errors such as invalid credentials, migration checksum mismatches,
SQL errors, and incompatible data fail immediately.

The Compose deployment also checks PostgreSQL readiness with both `pg_isready`
and a simple SQL query. `depends_on: condition: service_healthy` helps order a
fresh Compose start, but application-level retries are still required when
Docker restores existing containers after a host restart.

## Environment Variables

| Variable | Description | Required | Default |
|----------|-------------|----------|---------|
| `DATABASE_URL` | PostgreSQL connection string | Yes | - |
| `REDIS_URL` | Redis connection string | Yes | - |
| `PORT` | Server port | No | `8080` |
| `GIN_MODE` | Gin framework mode (`debug`/`release`) | No | `release` |

## Supported Architectures

- `linux/amd64`
- `linux/arm64`

## Tags

- `latest` - Latest stable release
- `x.y.z` - Specific version
- `x.y` - Latest patch of minor version
- `x` - Latest minor of major version

## Links

- [GitHub Repository](https://github.com/weishaw/auto2api)
- [Documentation](https://github.com/weishaw/auto2api#readme)

## 生产运维约定

以下规则适用于本仓库的生产部署（宝塔面板 + Docker Compose），新增部署也应按此执行。

### 更新流程

```bash
cd /www/dk_project/dk_app/auto2api
docker compose pull
docker compose up -d
```

也可以在宝塔「Docker → 容器编排 → auto2api」里点「拉取镜像 → 重建」。
`latest` 标签只跟随 `v*` 发布标签生成，main 分支构建不会覆盖它。

### 镜像清理（必须）

镜像更新后旧镜像会变成悬空镜像（`<none>:<none>`），必须清理：

```bash
docker image prune -f
```

- **只用默认的 dangling 过滤，禁止 `-a`** —— `-a` 会删掉未被容器引用但仍带标签的镜像，回滚就没了
- 生产环境已配置为宝塔计划任务「Auto2API 镜像清理」，每天 04:10 执行
- 脚本：`/usr/local/bin/auto2api-image-cleanup.sh`，日志 `/var/log/auto2api-image-cleanup.log`

### 存储：只用直接路径挂载

compose 里不允许出现命名卷（`volumes:` 顶层段）。所有数据都绑定到宿主目录，
便于备份、迁移和排查：

```
auto2api  -> ${AUTO2API_DATA_DIR:-./data/auto2api}:/app/data
postgres  -> ${POSTGRES_DATA_DIR:-./data/postgres}:/var/lib/postgresql/data
             ${POSTGRES_ROOT_DIR:-./data/pgroot}:/var/lib/postgresql
redis     -> ${REDIS_DATA_DIR:-./data/redis}:/data
```

第二条是必须的：`postgres` 镜像声明了 `VOLUME /var/lib/postgresql`，
只覆盖子目录会让 Docker 在宿主上自动创建匿名卷。

### 数据库备份

- 格式：`pg_dump -Fc`（自定义压缩格式，用 `pg_restore` 恢复）
- 命名：`auto2api-YYYYMMDD-HHMM.dump`
- 位置：`/www/dk_project/dk_app/postgres/backups/`
- 保留：最近 3 份，超出自动删除
- 调度：宝塔计划任务「Auto2API 数据库备份」，每天 03:20
- 脚本：`/usr/local/bin/auto2api-backup.sh`
- 恢复演练：定期用 `pg_restore` 恢复到临时库验证，确认表数量与关键表行数正常后再删库

### 监控与告警

- `/metrics` 只对本机开放，公网由 nginx 返回 403（`vhost/nginx/extension/<site>/deny-debug.conf`）
- 指标：`auto2api_uptime_seconds`、`auto2api_goroutines`、`auto2api_memory_*`、
  `auto2api_http_requests_total{method,status_class}`、`auto2api_upstream_errors_total{platform,class}`
- 巡检：宝塔计划任务「Auto2API 巡检告警」每小时执行，检查容器健康、`/health`、
  磁盘占用、备份新鲜度、HTTP 5xx 比例；异常通过宝塔微信公众号推送
- 脚本：`/usr/local/bin/auto2api-alert.py`
