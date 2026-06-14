<div align="center">

<img src=".github/assets/logo.svg" alt="ccquota" width="320">

Self-hosted quota monitor for shared Claude accounts. One Go binary, built-in web UI.

[![release](https://img.shields.io/github/v/release/homieyangg/ccquota?color=18181b)](https://github.com/homieyangg/ccquota/releases)
[![CI](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml/badge.svg)](https://github.com/homieyangg/ccquota/actions/workflows/ci.yml)
[![license](https://img.shields.io/github/license/homieyangg/ccquota?color=18181b)](LICENSE)

**English** · [繁體中文](README.zh-TW.md) · [简体中文](README.zh-CN.md)

</div>

![ccquota dashboard](.github/assets/dashboard.png)

ccquota polls Claude's official OAuth usage endpoint and shows how close a shared account is to its 7-day and 5-hour rate limits, across multiple accounts. It flags Anthropic's sudden resets, and (optionally) tracks each person's spend, reverse-calculates the weekly budget, and splits it per user.

## Features

- **Live 7d / 5h usage** straight from the OAuth usage endpoint, not guessed from logs.
- **Multiple accounts**, each polled on its own schedule.
- **Per-user cards** with cost and token-rate charts (24h / 7d), hover for exact values.
- **Reverse-calculated weekly budget**, split across the people on the account.
- **Sudden-reset detection** when Anthropic zeroes a window early.
- **Notifications** to Telegram or a webhook, with thresholds you set in the UI. Bot tokens are encrypted at rest.
- **No web OAuth needed**: paste a code in the browser, or import an existing `claude login` token.
- One static binary, embedded UI, SQLite. No external services.

## Quick start

One line on a Linux host with systemd (downloads the latest release, sets up the service):

```bash
curl -fsSL https://raw.githubusercontent.com/homieyangg/ccquota/main/scripts/install-server.sh | sudo bash
```

Or Docker:

```bash
docker run -d -p 11451:11451 -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=pick-one \
  ghcr.io/homieyangg/ccquota
```

Or from source:

```bash
git clone https://github.com/homieyangg/ccquota && cd ccquota
make build && ./ccquota serve
```

Then open `http://localhost:11451`. On first login with an auto-generated password you are asked to change it.

## Connect an account

In the dashboard, **Connect Account** walks you through the OAuth flow in the browser (no Claude CLI required). Already logged in with `claude login`? Import that token instead:

```bash
ccquota set-token --id main --label "Shared Claude"
```

## Add users (per-user cost)

Per-user cost needs `CCQUOTA_INGEST_TOKEN` set. Then click **Add User**, or use the **Copy install link** on any user card, and run it on each machine:

```bash
bash <(curl -fsSL -A ccquota-setup https://your-host/e/TOKEN)
```

One link works on all of that person's machines; usage merges under the same name. The client reports cost over Claude Code's native OpenTelemetry export.

## Configuration

| Env | Default | What it does |
| --- | --- | --- |
| `CCQUOTA_ADMIN_PASSWORD` | auto-generated | Admin password. Auto-generated value is logged once and must be changed on first login. |
| `CCQUOTA_DB` | `ccquota.db` | SQLite file path. |
| `CCQUOTA_INGEST_TOKEN` | unset | Enables per-user cost ingest and install links. |
| `CCQUOTA_PUBLIC_URL` | derived | Public URL baked into install links. |
| `CCQUOTA_SECRET_KEY` | keyfile | Base64 32-byte key for encrypting channel secrets. A keyfile is generated next to the DB if unset. |
| `CCQUOTA_ENROLL_TTL_DAYS` | `30` | How long an install link stays valid. |

Notifications (channels and alert thresholds) are configured in **Settings → Notifications**, not env.

![Notifications settings](.github/assets/settings.png)

## Development

```bash
make build      # build the binary
go test ./...   # run tests
```

The frontend is vanilla JS + Alpine.js, embedded via `go:embed`. No build step.

## License

[MIT](LICENSE)
