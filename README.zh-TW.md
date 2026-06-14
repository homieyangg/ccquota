# ccquota

[English](README.md) · **繁體中文** · [简体中文](README.zh-CN.md)

自架、開源的 **Claude 帳號額度監控** —— 單一 Go binary,內建 web UI。

一行指令部署,在瀏覽器連結你的 Claude 帳號,就能一眼看到共用的 **7 天 / 5 小時 rate-limit 使用率** —— 支援**多帳號**,並**自動偵測 Anthropic 突發的額度重置**。

> Claude Code 的 7d / 5h 額度是**整個帳號共用**的,登入同一帳號的人一起算。ccquota 在 server 端定時打官方 OAuth usage endpoint,所以數字永遠是新的 —— 光看額度完全不用裝 client。

## 功能

- **每個帳號的 7d / 5h 使用率**,含重置倒數與歷史。
- **突發重置偵測** —— Anthropic 把你的 7d 額度重置時,ccquota 會發現那個下掉、記錄下來,並通知你(Telegram / webhook)。
- **每人花費拆分**(選用)—— 每台機器裝一行 client;ccquota 用「花費 ÷ 7d%」反推整週額度,顯示每個人用掉了自己那份的幾 %。
- **多帳號**、**多語言**(English / 繁體中文 / 简体中文)。

## 快速開始

```bash
docker run -d \
  -p 8080:8080 \
  -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=your-password \
  -e CCQUOTA_INGEST_TOKEN=your-ingest-token \
  ghcr.io/OWNER/ccquota
```

開 `http://localhost:8080`,點「**連結 Claude 帳號**」,貼上 OAuth code。

> 如果沒設定 `CCQUOTA_ADMIN_PASSWORD`,第一次啟動時會自動產生一組並印在 container log 裡。

## 新增使用者（選用，追蹤花費）

### 一鍵 Enrollment（推薦）

1. 在 ccquota 管理介面點「**新增使用者**」，選擇帳號、輸入顯示名稱，按「產生安裝連結」。
2. 把產生的連結**私訊**傳給成員（連結內含 ingest token，請勿公開）。
3. 成員在自己機器上執行一行指令：

```bash
bash <(curl -fsSL https://your-ccquota-host/e/<token>)
```

完成，不需要任何參數。連結 24 小時後失效。

> 如果 ccquota server 在 reverse proxy 後面、無法從 request 自動推導外部 URL，請設定 `CCQUOTA_PUBLIC_URL=https://your-host`。

### 手動 / 進階方式

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/ccquota/main/scripts/install-client.sh | bash \
  -s -- \
  --server https://your-ccquota-host \
  --account <claude-account-id> \
  --user <顯示名稱> \
  --token <CCQUOTA_INGEST_TOKEN>
```

要移除某人的 client，在對方機器執行 `uninstall-client.sh`。

## 設定

所有設定都透過環境變數：

| 變數 | 預設值 | 說明 |
|---|---|---|
| `CCQUOTA_DB` | `ccquota.db` | SQLite 資料庫路徑 |
| `CCQUOTA_ADMIN_PASSWORD` | *(自動產生)* | Web UI 與 API 的 Basic-auth 密碼 |
| `CCQUOTA_INGEST_TOKEN` | *(關閉)* | 啟用 `POST /v1/metrics` 花費 ingest 與 enrollment 連結功能的 Bearer token |
| `CCQUOTA_PUBLIC_URL` | *(自動推導)* | 對外 base URL（如 `https://ccquota.example.com`）；reverse proxy 後面無法自動推導時設定 |
| `CCQUOTA_ALERT_LANG` | `en` | 通知訊息語言（`en` / `zh-TW` / `zh-CN`） |
| `CCQUOTA_TELEGRAM_TOKEN` | — | Telegram bot token（通知用） |
| `CCQUOTA_TELEGRAM_CHAT` | — | Telegram 聊天室/群組 ID |
| `CCQUOTA_WEBHOOK_URL` | — | 通用 webhook URL |
| `CCQUOTA_WEEKLY_WARN` | `75` | 7d 使用率達此 % 觸發警告 |
| `CCQUOTA_WEEKLY_CRIT` | `90` | 7d 使用率達此 % 觸發嚴重警告 |
| `CCQUOTA_FIVEHOUR_CRIT` | `95` | 5h 使用率達此 % 觸發嚴重警告 |
| `CCQUOTA_POLLER_STALE_SEC` | `1800` | Poller 超過幾秒沒更新視為 stale |

### CLI flags（`serve` 指令）

```
ccquota serve --addr :8080 --interval 5m
```

| Flag | 預設值 | 說明 |
|---|---|---|
| `--addr` | `:8080` | 監聽地址 |
| `--interval` | `5m` | 輪詢間隔 |

## 從原始碼建置

需要 Go 1.26+。Binary 是純 Go（無 CGO）。

```bash
# 快速建置（本機架構）
go build -o ccquota ./cmd/ccquota

# 或用 make
make build

# 交叉編譯 Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w" -o ccquota-linux-arm64 ./cmd/ccquota
```

## Docker Compose

```bash
cp docker-compose.yml my-compose.yml   # 編輯環境變數
docker compose -f my-compose.yml up -d
```

詳見 `docker-compose.yml` 裡所有環境變數範例。

## 授權

MIT —— 見 [LICENSE](LICENSE)。
