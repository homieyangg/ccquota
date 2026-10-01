<div align="center">

<img src=".github/assets/logo.svg" alt="ccquota" width="320">

自架的 Claude 共用帳號額度監控。單一 Go binary,內建 web UI。

[![release](https://img.shields.io/github/v/release/homieyangg/ccquota?color=18181b)](https://github.com/homieyangg/ccquota/releases)
[![CI](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml/badge.svg)](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/homieyangg/ccquota?color=18181b)](LICENSE)

[English](README.md) · **繁體中文** · [简体中文](README.zh-CN.md)

</div>

![ccquota 操作示範](.github/assets/demo.gif)

ccquota 定時打 Claude 官方 OAuth usage endpoint,顯示共用帳號離 7 天、5 小時、模型別週限還有多遠(可多帳號),偵測 Anthropic 的突發重置,並可選擇追蹤每人花費,反推週額度後平分。

## 功能

- **即時 7d / 5h 使用率**,直接讀 OAuth usage endpoint,不靠 log 猜。
- **模型別週限**,像 Fable 這種限定模型的週額度,常比整體 7 天用量更早撞到。
- **多帳號**,各自排程輪詢。
- **per-user 卡片**,每人 cost 與 token 速率圖(24h / 7d),滑上去看精確數值。
- **反推週額度**,在帳號的人之間平分。
- **突發重置偵測**,Anthropic 提早把視窗歸零時會抓到。
- **通知**到 Telegram 或 webhook,門檻在 UI 設定,bot token 加密存。
- **免網頁 OAuth**:瀏覽器貼 code,或匯入現有的 `claude login` token。
- **登入過期也有資料**,設一顆一年期 token 當後備,不用每四週重登。
- 單一 static binary,內嵌 UI,SQLite,不依賴外部服務。

## 快速開始

server 裝在一台機器上就好,成員的電腦之後各跑一行指令。

### Linux、macOS

```bash
curl -fsSL https://raw.githubusercontent.com/homieyangg/ccquota/main/scripts/install-server.sh | sudo bash
```

抓最新 release 裝到 `/usr/local/bin/ccquota`。有 systemd 的 Linux 會裝成服務並印出管理員密碼。macOS、WSL 這類沒有 systemd 的環境只裝 binary 和資料目錄,腳本最後會印出啟動指令。

### Docker(Windows、macOS、Linux)

```bash
docker run -d -p 11451:11451 -v ccquota:/data -e CCQUOTA_ADMIN_PASSWORD=change-me ghcr.io/homieyangg/ccquota
```

指令寫成一行,不靠特定 shell 的換行寫法。Windows 沒有原生 binary,用 Docker 或 WSL。

### 從原始碼

```bash
git clone https://github.com/homieyangg/ccquota && cd ccquota
make build && ./ccquota serve
```

開 `http://localhost:11451`。第一次用自動產生的密碼登入會要求你改掉。

## 連接帳號

dashboard 的 **連接帳號** 會帶你在瀏覽器走 OAuth(不用 Claude CLI)。已經 `claude login` 過?直接匯入那個 token:

```bash
ccquota set-token --id main --label "Shared Claude"
```

## 額度怎麼看

![帳號摘要列](.github/assets/summary.png)

| 欄位 | 意思 |
| --- | --- |
| 7 日用量 | 帳號整體週額度用掉的比例。 |
| 5 小時用量 | 目前這個 5 小時視窗用掉的比例。 |
| Fable 週限 | 限定某個模型的週額度,名稱跟著 Anthropic 回傳的模型走。帳號沒有這種限制就不顯示。 |
| 重置倒計時 | 7 日視窗多久後重置。 |
| 反推週額度 | 本期所有人的花費除以 7 日用量,推回整週額度值多少錢。 |
| 上週反推額度 | 上一個視窗重置當下的反推值。 |

週限告警比的是 7 日用量和模型別週限兩者較高的那個。

## 新增使用者(每人花費)

每人花費需要設 `CCQUOTA_INGEST_TOKEN`。安裝腳本會自動產生一組,Docker 要自己加 `-e CCQUOTA_INGEST_TOKEN=...`。

點 **新增使用者** 產生安裝指令,或在使用者卡片按 **複製安裝連結**。

<img src=".github/assets/enroll.png" alt="新增使用者" width="520">

在那個人的每台電腦上跑:

```bash
curl -fsSL -A ccquota-setup https://your-host/e/TOKEN | bash
```

指令只把 Claude Code 原生的 OpenTelemetry 上報設定寫進 `~/.claude/settings.json`,寫之前會先備份。一條連結可以用在那個人所有的電腦上,用量會合併到同一個名字底下。

statusline 是選配,預設不動。想在 Claude Code 的 statusline 看到 `5h:23% 7d:59% me:80%`,產生連結時勾選,或自己在指令後面加參數:

```bash
curl -fsSL -A ccquota-setup https://your-host/e/TOKEN | bash -s -- --statusline
```

原本就有 statusline 的話,ccquota 會接在後面,不會蓋掉。

指令需要 `bash`、`curl`、`jq`:

| 系統 | 在哪裡跑 | 裝 jq |
| --- | --- | --- |
| macOS | Terminal,zsh、bash、fish 都可以 | `brew install jq` |
| Linux | 任何 shell | `sudo apt install jq` 或 `sudo dnf install jq` |
| Windows,Claude Code 跑在 WSL | WSL 的 shell | `sudo apt install jq` |
| Windows,Claude Code 跑在原生環境 | Git Bash | `winget install jqlang.jq` |

原生 Windows 加 Git Bash 這個組合還沒實機驗證,遇到問題請開 issue。

## 登入過期後繼續讀額度

`claude login` 的登入約四週到期,到期後 usage endpoint 讀不到,dashboard 會標成資料過時。設一顆一年期 token 當後備,就不用每四週重登。

在任一台登入同一個 Claude 帳號的電腦上產生 token:

```bash
claude setup-token
```

把印出來的 token 設成 server 的 `CCQUOTA_PROBE_TOKEN`:

| 安裝方式 | 設在哪 |
| --- | --- |
| systemd | `/opt/ccquota/ccquota.env` 加一行 `CCQUOTA_PROBE_TOKEN=...`,然後 `sudo systemctl restart ccquota` |
| Docker | `docker run` 加 `-e CCQUOTA_PROBE_TOKEN=...` |
| 手動啟動 | 啟動前 `export CCQUOTA_PROBE_TOKEN=...` |

登入還有效時照常讀 usage endpoint,用不到這顆 token。讀不到時才改發一個 `max_tokens=1` 的請求,從 response header 讀 5 小時、7 天和模型別週限,每次約 20 幾個 token。

## 設定

| 環境變數 | 預設 | 作用 |
| --- | --- | --- |
| `CCQUOTA_ADMIN_PASSWORD` | 自動產生 | 管理員密碼。自動產生的值會 log 一次,首次登入須改掉。 |
| `CCQUOTA_DB` | `ccquota.db` | SQLite 檔路徑。 |
| `CCQUOTA_INGEST_TOKEN` | 未設 | 開啟每人花費上報與安裝連結。 |
| `CCQUOTA_PUBLIC_URL` | 自動推導 | 安裝連結用的對外網址。 |
| `CCQUOTA_SECRET_KEY` | keyfile | 加密頻道密鑰用的 base64 32-byte key。未設時會在 DB 旁產生 keyfile。 |
| `CCQUOTA_ENROLL_TTL_DAYS` | `30` | 安裝連結有效天數。 |
| `CCQUOTA_PROBE_TOKEN` | 未設 | `claude setup-token` 產生的一年期 token。帳號登入過期、usage endpoint 讀不到時,改發一個 1 token 的請求從 response header 讀額度。 |
| `CCQUOTA_PROBE_ACCOUNT` | `main` | 那顆 token 所屬的帳號 id。 |
| `CCQUOTA_PROBE_MODEL` | `claude-fable-5-1` | probe 請求打的模型。要打有模型別週限的那個,才讀得到該週限。 |

通知(頻道與告警門檻)在 **設定 → 通知** 裡設,不走環境變數。

![通知設定](.github/assets/settings.png)

## 開發

```bash
make build      # 編譯 binary
go test ./...   # 跑測試
```

前端是 vanilla JS + Alpine.js,用 `go:embed` 內嵌,沒有 build step。

## 社群

在 [LINUX DO](https://linux.do) 社群分享,歡迎回饋與 issue。

## 授權

[MIT](LICENSE)
