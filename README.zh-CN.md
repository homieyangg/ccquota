# ccquota

[English](README.md) · [繁體中文](README.zh-TW.md) · **简体中文**

自托管的 Claude 账号额度监控，单一 Go binary，内置 web UI。

它定时请求官方 OAuth usage endpoint，所以共用的 7 天、5 小时 rate-limit 使用率永远是即时的，跨多个账号都看得到，也会标出 Anthropic 的突发重置。也可以追踪每个人的花费、平分一周的额度。

## 跑起来

```bash
docker run -d -p 8080:8080 -v ccquota:/data \
  -e CCQUOTA_ADMIN_PASSWORD=pick-one \
  ghcr.io/homieyangg/ccquota
```

打开 `http://localhost:8080`，连接你的 Claude 账号。

## 加人（可选）

在仪表板输入名字，拿到一条安装链接，对方跑一次：

```bash
bash <(curl -fsSL https://your-host/e/TOKEN)
```

## 配置

环境变量与通知（Telegram 或 webhook）写在 [docker-compose.yml](docker-compose.yml)。要从源码构建就 `make build`。

## 许可

MIT
