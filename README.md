# ccquota

**English** · [繁體中文](README.zh-TW.md) · [简体中文](README.zh-CN.md)

Self-hosted, open-source **Claude account quota monitor** — a single Go binary with a built-in web UI.

Deploy in one command, connect your Claude account in the browser, and watch your shared **7-day / 5-hour rate-limit usage** at a glance — across **multiple accounts**, with **automatic detection of Anthropic's sudden quota resets**.

> Claude Code's 7d / 5h limits are **shared per account** across everyone logged into it. ccquota polls the official OAuth usage endpoint server-side, so the number stays fresh — no client needed just to watch quota.

## Features

- **7d / 5h usage** per account, with reset countdown and history.
- **Sudden-reset detection** — when Anthropic resets your 7d quota, ccquota notices the drop, records it, and notifies you (Telegram / webhook).
- **Per-user cost split** (optional) — install a one-line client per machine; ccquota reverse-calculates a weekly budget (spend ÷ 7d%) and shows how much of their fair share each person has used.
- **Multi-account** and **multi-language** (English / 繁體中文 / 简体中文).

## Quickstart

```bash
docker run -d \
  -p 8080:8080 \
  -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=your-password \
  -e CCQUOTA_INGEST_TOKEN=your-ingest-token \
  ghcr.io/OWNER/ccquota
```

Open `http://localhost:8080`, click **"Connect Claude account"**, and paste the OAuth code.

> If `CCQUOTA_ADMIN_PASSWORD` is not set, one is auto-generated and printed to the container logs on first start.

## Add a user (optional cost tracking)

### One-line enrollment (recommended)

1. In the ccquota web UI, go to **"Add User"**, pick an account, type the display name, and click **"Generate install link"**.
2. Send the resulting link **privately** to the teammate (it carries the ingest token — treat it as a secret).
3. The teammate runs one command on their machine:

```bash
bash <(curl -fsSL https://your-ccquota-host/e/<token>)
```

That's it — zero params needed. The link expires in 24 hours.

> If your ccquota server sits behind a reverse proxy and can't infer its own external URL, set `CCQUOTA_PUBLIC_URL=https://your-host` so the generated link is correct.

### Manual / advanced method

```bash
curl -fsSL https://raw.githubusercontent.com/OWNER/ccquota/main/scripts/install-client.sh | bash \
  -s -- \
  --server https://your-ccquota-host \
  --account <claude-account-id> \
  --user <display-name> \
  --token <CCQUOTA_INGEST_TOKEN>
```

To remove a user's client, run `uninstall-client.sh` on their machine.

## Configuration

All configuration is via environment variables:

| Variable | Default | Description |
|---|---|---|
| `CCQUOTA_DB` | `ccquota.db` | Path to the SQLite database |
| `CCQUOTA_ADMIN_PASSWORD` | *(auto-generated)* | Basic-auth password for the web UI and API |
| `CCQUOTA_INGEST_TOKEN` | *(disabled)* | Bearer token that enables `POST /v1/metrics` for cost ingest and the enrollment link feature |
| `CCQUOTA_PUBLIC_URL` | *(auto)* | External base URL (e.g. `https://ccquota.example.com`); required if the server can't infer its own URL from request headers |
| `CCQUOTA_ALERT_LANG` | `en` | Alert message language (`en` / `zh-TW` / `zh-CN`) |
| `CCQUOTA_TELEGRAM_TOKEN` | — | Telegram bot token for alerts |
| `CCQUOTA_TELEGRAM_CHAT` | — | Telegram chat/group ID for alerts |
| `CCQUOTA_WEBHOOK_URL` | — | Generic webhook URL for alerts |
| `CCQUOTA_WEEKLY_WARN` | `75` | 7d usage % that triggers a warning alert |
| `CCQUOTA_WEEKLY_CRIT` | `90` | 7d usage % that triggers a critical alert |
| `CCQUOTA_FIVEHOUR_CRIT` | `95` | 5h usage % that triggers a critical alert |
| `CCQUOTA_POLLER_STALE_SEC` | `1800` | Seconds before a poller is considered stale |

### CLI flags (`serve` command)

```
ccquota serve --addr :8080 --interval 5m
```

| Flag | Default | Description |
|---|---|---|
| `--addr` | `:8080` | Listen address |
| `--interval` | `5m` | Poll interval |

## Build from source

Requires Go 1.26+. The binary is pure-Go (no CGO).

```bash
# Quick build (native)
go build -o ccquota ./cmd/ccquota

# Or with make
make build

# Cross-compile for Linux arm64
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
  go build -ldflags "-s -w" -o ccquota-linux-arm64 ./cmd/ccquota
```

## Docker Compose

```bash
cp docker-compose.yml my-compose.yml   # edit env vars
docker compose -f my-compose.yml up -d
```

See `docker-compose.yml` for all available environment variable examples.

## License

MIT — see [LICENSE](LICENSE).
