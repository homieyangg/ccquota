# ccquota

**English** · [繁體中文](README.zh-TW.md) · [简体中文](README.zh-CN.md)

Self-hosted quota monitor for Claude accounts. One Go binary with a built-in web UI.

It polls the official OAuth usage endpoint, so your shared 7-day and 5-hour rate-limit usage stays live across every account, and it flags Anthropic's sudden resets. You can also track per-user spend and split a weekly budget.

## Run

```bash
docker run -d -p 8080:8080 -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=pick-one \
  ghcr.io/homieyangg/ccquota
```

Open `http://localhost:8080` and connect your Claude account.

## Add a user (optional)

Type a name in the dashboard to get an install link. They run it once:

```bash
bash <(curl -fsSL https://your-host/e/TOKEN)
```

## Config

Env vars and alerts (Telegram or webhook) live in [docker-compose.yml](docker-compose.yml). Build from source with `make build`.

## License

MIT
