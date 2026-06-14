# ccquota

[English](README.md) · **繁體中文** · [简体中文](README.zh-CN.md)

自架的 Claude 帳號額度監控，單一 Go binary，內建 web UI。

它定時打官方 OAuth usage endpoint，所以共用的 7 天、5 小時 rate-limit 使用率永遠是即時的，跨多個帳號都看得到，也會標出 Anthropic 的突發重置。也可以追蹤每個人的花費、平分一週的額度。

## 跑起來

```bash
docker run -d -p 11451:11451 -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=pick-one \
  ghcr.io/homieyangg/ccquota
```

開 `http://localhost:11451`，連結你的 Claude 帳號。

## 加人（選用）

在儀表板輸入名字，拿到一條安裝連結，對方跑一次：

```bash
bash <(curl -fsSL -A ccquota-setup https://your-host/e/TOKEN)
```

## 設定

環境變數與通知（Telegram 或 webhook）寫在 [docker-compose.yml](docker-compose.yml)。要從原始碼建置就 `make build`。

## 授權

MIT
