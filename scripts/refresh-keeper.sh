#!/usr/bin/env bash
# refresh-keeper.sh:讓「與 ccquota 同機的真 Claude Code CLI」的 OAuth token 持續新鮮。
#
# 用途(colocated 部署):server 上有真 `claude` 登入,ccquota 直接用它的 token 打 usage,
# 不依賴外部機器、不碰被限流的 token endpoint。但真 CLI 只在「快到期」時才會 refresh,
# 而它的 refresh 是免費的(不叫 model、不吃 6/15 後的 Agent SDK credit)——
# `claude doctor` 在 token 進入 refresh buffer(實測 < ~10 分)時會觸發那次 refresh。
#
# 策略:每次跑檢查剩餘壽命,接近到期(預設 < 9 分)就跑一次 `claude doctor` 觸發 refresh。
# 用 cron 每 1~2 分鐘跑;平常只讀檔(秒回),只有快到期那幾分鐘才真的拉起 doctor。
# refresh 用的是長壽 refresh token,所以即使 access token 剛過期,doctor 一樣換得回來。
set -euo pipefail

CRED="${CLAUDE_CREDS:-$HOME/.claude/.credentials.json}"
THRESHOLD="${REFRESH_AHEAD_SEC:-540}" # 剩 < 9 分就嘗試 refresh

[ -f "$CRED" ] || exit 0
command -v jq    >/dev/null 2>&1 || exit 0
command -v claude >/dev/null 2>&1 || exit 0

exp=$(jq -r '.claudeAiOauth.expiresAt // 0' "$CRED")
case "$exp" in ''|*[!0-9]*) exit 0 ;; esac
[ "$exp" -gt 100000000000 ] && exp=$((exp / 1000)) # 毫秒 → 秒
remaining=$((exp - $(date +%s)))

if [ "$remaining" -lt "$THRESHOLD" ]; then
  # doctor 是 TUI 會 hang,但 refresh 在啟動時就完成,timeout 砍掉即可。
  timeout 45 claude doctor >/dev/null 2>&1 || true
fi
