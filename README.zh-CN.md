<div align="center">

<img src=".github/assets/logo.svg" alt="ccquota" width="320">

自架的 Claude 共享账号额度监控。单一 Go binary,内建 web UI。

[![release](https://img.shields.io/github/v/release/homieyangg/ccquota?color=18181b)](https://github.com/homieyangg/ccquota/releases)
[![CI](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml/badge.svg)](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/homieyangg/ccquota?color=18181b)](LICENSE)

[English](README.md) · [繁體中文](README.zh-TW.md) · **简体中文**

</div>

![ccquota 操作演示](.github/assets/demo.gif)

ccquota 定时调用 Claude 官方 OAuth usage endpoint,显示共用账号离 7 天、5 小时、模型级周限还有多远(可多账号),检测 Anthropic 的突发重置,并可选择追踪每人花费,反推周额度后平分。

## 功能

- **实时 7d / 5h 使用率**,直接读 OAuth usage endpoint,不靠 log 猜。
- **模型级周限**,像 Fable 这种限定模型的周额度,常比整体 7 天用量更早撞到。
- **多账号**,各自排程轮询。
- **per-user 卡片**,每人 cost 与 token 速率图(24h / 7d),鼠标移上去看精确数值。
- **反推周额度**,在账号的人之间平分。
- **突发重置侦测**,Anthropic 提早把窗口归零时会抓到。
- **通知**到 Telegram 或 webhook,门槛在 UI 设定,bot token 加密存。
- **免网页 OAuth**:浏览器贴 code,或导入现有的 `claude login` token。
- **登录过期也有数据**,设一个一年期 token 当后备,不用每四周重新登录。
- 单一 static binary,内嵌 UI,SQLite,不依赖外部服务。

## 快速开始

server 装在一台机器上就好,成员的电脑之后各跑一行命令。

### Linux、macOS

```bash
curl -fsSL https://raw.githubusercontent.com/homieyangg/ccquota/main/scripts/install-server.sh | sudo bash
```

抓最新 release 装到 `/usr/local/bin/ccquota`。有 systemd 的 Linux 会装成服务并打印管理员密码。macOS、WSL 这类没有 systemd 的环境只装 binary 和数据目录,脚本最后会打印启动命令。

### Docker(Windows、macOS、Linux)

```bash
docker run -d -p 11451:11451 -v ccquota:/data -e CCQUOTA_ADMIN_PASSWORD=change-me ghcr.io/homieyangg/ccquota
```

命令写成一行,不依赖特定 shell 的换行写法。Windows 没有原生 binary,用 Docker 或 WSL。

### 从源码

```bash
git clone https://github.com/homieyangg/ccquota && cd ccquota
make build && ./ccquota serve
```

打开 `http://localhost:11451`。第一次用自动生成的密码登录会要求你改掉。

## 连接账号

dashboard 的 **连接账号** 会带你在浏览器走 OAuth(不用 Claude CLI)。已经 `claude login` 过?直接导入那个 token:

```bash
ccquota set-token --id main --label "Shared Claude"
```

## 额度怎么看

![账号摘要栏](.github/assets/summary.png)

| 字段 | 意思 |
| --- | --- |
| 7 日用量 | 账号整体周额度用掉的比例。 |
| 5 小时用量 | 当前这个 5 小时窗口用掉的比例。 |
| Fable 周限 | 限定某个模型的周额度,名称跟着 Anthropic 返回的模型走。账号没有这种限制就不显示。 |
| 重置倒计时 | 7 日窗口多久后重置。 |
| 反推周额度 | 本期所有人的花费除以 7 日用量,推回整周额度值多少钱。 |
| 上周反推额度 | 上一个窗口重置当下的反推值。 |

周限告警比的是 7 日用量和模型级周限两者较高的那个。

## 添加用户(每人花费)

每人花费需要设 `CCQUOTA_INGEST_TOKEN`。安装脚本会自动生成一组,Docker 要自己加 `-e CCQUOTA_INGEST_TOKEN=...`。

点 **添加用户** 生成安装命令,或在用户卡片按 **复制安装链接**。

![添加用户](.github/assets/enroll.png)

在那个人的每台电脑上跑:

```bash
curl -fsSL -A ccquota-setup https://your-host/e/TOKEN | bash
```

命令只把 Claude Code 原生的 OpenTelemetry 上报配置写进 `~/.claude/settings.json`,写之前会先备份。一条链接可以用在那个人所有的电脑上,用量会合并到同一个名字底下。

statusline 是选配,默认不动。想在 Claude Code 的 statusline 看到 `5h:23% 7d:59% me:80%`,生成链接时勾选,或自己在命令后面加参数:

```bash
curl -fsSL -A ccquota-setup https://your-host/e/TOKEN | bash -s -- --statusline
```

原本就有 statusline 的话,ccquota 会接在后面,不会覆盖。

命令需要 `bash`、`curl`、`jq`:

| 系统 | 在哪里跑 | 装 jq |
| --- | --- | --- |
| macOS | Terminal,zsh、bash、fish 都可以 | `brew install jq` |
| Linux | 任何 shell | `sudo apt install jq` 或 `sudo dnf install jq` |
| Windows,Claude Code 跑在 WSL | WSL 的 shell | `sudo apt install jq` |
| Windows,Claude Code 跑在原生环境 | Git Bash | `winget install jqlang.jq` |

原生 Windows 加 Git Bash 这个组合还没实机验证,遇到问题请开 issue。

## 登录过期后继续读额度

`claude login` 的登录约四周到期,到期后 usage endpoint 读不到,dashboard 会标成数据过时。设一个一年期 token 当后备,就不用每四周重新登录。

在任一台登录同一个 Claude 账号的电脑上生成 token:

```bash
claude setup-token
```

把打印出来的 token 设成 server 的 `CCQUOTA_PROBE_TOKEN`:

| 安装方式 | 设在哪 |
| --- | --- |
| systemd | `/opt/ccquota/ccquota.env` 加一行 `CCQUOTA_PROBE_TOKEN=...`,然后 `sudo systemctl restart ccquota` |
| Docker | `docker run` 加 `-e CCQUOTA_PROBE_TOKEN=...` |
| 手动启动 | 启动前 `export CCQUOTA_PROBE_TOKEN=...` |

登录还有效时照常读 usage endpoint,用不到这个 token。读不到时才改发一个 `max_tokens=1` 的请求,从 response header 读 5 小时、7 天和模型级周限,每次约 20 几个 token。

## 配置

| 环境变量 | 默认 | 作用 |
| --- | --- | --- |
| `CCQUOTA_ADMIN_PASSWORD` | 自动生成 | 管理员密码。自动生成的值会 log 一次,首次登入须改掉。 |
| `CCQUOTA_DB` | `ccquota.db` | SQLite 文件路径。 |
| `CCQUOTA_INGEST_TOKEN` | 未设 | 开启每人花费上报与安装链接。 |
| `CCQUOTA_PUBLIC_URL` | 自动推导 | 安装链接用的对外网址。 |
| `CCQUOTA_SECRET_KEY` | keyfile | 加密频道密钥用的 base64 32-byte key。未设时会在 DB 旁生成 keyfile。 |
| `CCQUOTA_ENROLL_TTL_DAYS` | `30` | 安装链接有效天数。 |
| `CCQUOTA_PROBE_TOKEN` | 未设 | `claude setup-token` 生成的一年期 token。账号登录过期、usage endpoint 读不到时,改发一个 1 token 的请求从 response header 读额度。 |
| `CCQUOTA_PROBE_ACCOUNT` | `main` | 那个 token 所属的账号 id。 |
| `CCQUOTA_PROBE_MODEL` | `claude-fable-5-1` | probe 请求打的模型。要打有模型级周限的那个,才读得到该周限。 |

通知(频道与告警门槛)在 **设定 → 通知** 里设,不走环境变量。

![通知设定](.github/assets/settings.png)

## 开发

```bash
make build      # 编译 binary
go test ./...   # 跑测试
```

前端是 vanilla JS + Alpine.js,用 `go:embed` 内嵌,没有 build step。

## 社区

在 [LINUX DO](https://linux.do) 社区分享,欢迎反馈与 issue。

## 授权

[MIT](LICENSE)
