# ccquota

[English](README.md) · [繁體中文](README.zh-TW.md) · **简体中文**

自托管、开源的 **Claude 账号额度监控** —— 单一 Go binary，内置 web UI。

一行命令部署，在浏览器连接你的 Claude 账号，就能一眼看到共用的 **7 天 / 5 小时 rate-limit 使用率** —— 支持**多账号**，并**自动检测 Anthropic 突发的额度重置**。

> Claude Code 的 7d / 5h 额度是**整个账号共用**的，登录同一账号的人一起算。ccquota 在服务端定时请求官方 OAuth usage endpoint，所以数字永远是新的 —— 只看额度完全不用装 client。

## 功能

- **每个账号的 7d / 5h 使用率**，含重置倒计时与历史。
- **突发重置检测** —— Anthropic 把你的 7d 额度重置时，ccquota 会发现那次下降、记录下来，并通知你（Telegram / webhook）。
- **按用户花费拆分**（可选）—— 每台机器装一行 client；ccquota 用「花费 ÷ 7d%」反推整周额度，显示每个人用掉了自己那份的百分之几。
- **多账号**、**多语言**（English / 繁體中文 / 简体中文）。

## 快速开始

```bash
docker run -d \
  -p 8080:8080 \
  -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=your-password \
  -e CCQUOTA_INGEST_TOKEN=your-ingest-token \
  ghcr.io/OWNER/ccquota
```

打开 `http://localhost:8080`，点击「**连接 Claude 账号**」，粘贴 OAuth code。

> 如果没有设置 `CCQUOTA_ADMIN_PASSWORD`，首次启动时会自动生成一个并打印在容器日志中。

## 添加用户（可选，追踪花费）

在每位成员的机器上执行（需要能连接到服务器）：

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/ccquota/main/scripts/install-client.sh | bash \
  -s -- \
  --server https://your-ccquota-host \
  --account <claude-account-id> \
  --user <显示名称> \
  --token <CCQUOTA_INGEST_TOKEN>
```

要添加另一位用户，在不同机器用不同的 `--user` 值执行一遍即可。
要移除某人的 client，在对方机器执行 `uninstall-client.sh`。

## 配置

所有配置均通过环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `CCQUOTA_DB` | `ccquota.db` | SQLite 数据库路径 |
| `CCQUOTA_ADMIN_PASSWORD` | *(自动生成)* | Web UI 与 API 的 Basic-auth 密码 |
| `CCQUOTA_INGEST_TOKEN` | *(关闭)* | 启用 `POST /v1/metrics` 花费 ingest 的 Bearer token |
| `CCQUOTA_ALERT_LANG` | `en` | 通知消息语言（`en` / `zh-TW` / `zh-CN`） |
| `CCQUOTA_TELEGRAM_TOKEN` | — | Telegram bot token（通知用） |
| `CCQUOTA_TELEGRAM_CHAT` | — | Telegram 聊天室/群组 ID |
| `CCQUOTA_WEBHOOK_URL` | — | 通用 webhook URL |
| `CCQUOTA_WEEKLY_WARN` | `75` | 7d 使用率达到此 % 触发警告 |
| `CCQUOTA_WEEKLY_CRIT` | `90` | 7d 使用率达到此 % 触发严重警告 |
| `CCQUOTA_FIVEHOUR_CRIT` | `95` | 5h 使用率达到此 % 触发严重警告 |
| `CCQUOTA_POLLER_STALE_SEC` | `1800` | Poller 超过几秒未更新视为 stale |

### CLI flags（`serve` 命令）

```
ccquota serve --addr :8080 --interval 5m
```

| Flag | 默认值 | 说明 |
|---|---|---|
| `--addr` | `:8080` | 监听地址 |
| `--interval` | `5m` | 轮询间隔 |

## 从源码构建

需要 Go 1.26+。二进制文件是纯 Go（无 CGO）。

```bash
# 快速构建（本机架构）
go build -o ccquota ./cmd/ccquota

# 或使用 make
make build

# 交叉编译 Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w" -o ccquota-linux-arm64 ./cmd/ccquota
```

## Docker Compose

```bash
cp docker-compose.yml my-compose.yml   # 编辑环境变量
docker compose -f my-compose.yml up -d
```

详见 `docker-compose.yml` 中所有环境变量示例。

## 许可

MIT —— 见 [LICENSE](LICENSE)。
