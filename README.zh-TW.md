<div align="center">

<img src=".github/assets/logo.svg" alt="ccquota" width="320">

自架的 Claude 共用帳號額度監控。單一 Go binary,內建 web UI。

[![release](https://img.shields.io/github/v/release/homieyangg/ccquota?color=18181b)](https://github.com/homieyangg/ccquota/releases)
[![CI](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml/badge.svg)](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/homieyangg/ccquota?color=18181b)](LICENSE)

[English](README.md) · **繁體中文** · [简体中文](README.zh-CN.md)

</div>

![ccquota dashboard](.github/assets/dashboard.png)

ccquota 定時打 Claude 官方 OAuth usage endpoint,顯示共用帳號離 7 天 / 5 小時限流還有多遠(可多帳號),偵測 Anthropic 的突發重置,並可選擇追蹤每人花費,反推週額度後平分。

## 功能

- **即時 7d / 5h 使用率**,直接讀 OAuth usage endpoint,不靠 log 猜。
- **多帳號**,各自排程輪詢。
- **per-user 卡片**,每人 cost 與 token 速率圖(24h / 7d),滑上去看精確數值。
- **反推週額度**,在帳號的人之間平分。
- **突發重置偵測**,Anthropic 提早把視窗歸零時會抓到。
- **通知**到 Telegram 或 webhook,門檻在 UI 設定,bot token 加密存。
- **免網頁 OAuth**:瀏覽器貼 code,或匯入現有的 `claude login` token。
- 單一 static binary,內嵌 UI,SQLite,不依賴外部服務。

## 快速開始

Linux + systemd 一行裝(抓最新 release、設好服務):

```bash
curl -fsSL https://raw.githubusercontent.com/homieyangg/ccquota/main/scripts/install-server.sh | sudo bash
```

或用 Docker:

```bash
docker run -d -p 11451:11451 -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=自己取一個 \
  ghcr.io/homieyangg/ccquota
```

或從原始碼:

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

## 新增使用者(每人花費)

每人花費需要設 `CCQUOTA_INGEST_TOKEN`。之後點 **新增使用者**,或在任一使用者卡片按 **複製安裝連結**,在每台機器上跑:

```bash
bash <(curl -fsSL -A ccquota-setup https://your-host/e/TOKEN)
```

一條連結可以用在那個人所有的電腦上,用量會合併到同一個名字底下。Client 透過 Claude Code 原生的 OpenTelemetry 上報花費。

## 設定

| 環境變數 | 預設 | 作用 |
| --- | --- | --- |
| `CCQUOTA_ADMIN_PASSWORD` | 自動產生 | 管理員密碼。自動產生的值會 log 一次,首次登入須改掉。 |
| `CCQUOTA_DB` | `ccquota.db` | SQLite 檔路徑。 |
| `CCQUOTA_INGEST_TOKEN` | 未設 | 開啟每人花費上報與安裝連結。 |
| `CCQUOTA_PUBLIC_URL` | 自動推導 | 安裝連結用的對外網址。 |
| `CCQUOTA_SECRET_KEY` | keyfile | 加密頻道密鑰用的 base64 32-byte key。未設時會在 DB 旁產生 keyfile。 |
| `CCQUOTA_ENROLL_TTL_DAYS` | `30` | 安裝連結有效天數。 |

通知(頻道與告警門檻)在 **設定 → 通知** 裡設,不走環境變數。

![通知設定](.github/assets/settings.png)

## 開發

```bash
make build      # 編譯 binary
go test ./...   # 跑測試
```

前端是 vanilla JS + Alpine.js,用 `go:embed` 內嵌,沒有 build step。

## 授權

[MIT](LICENSE)
