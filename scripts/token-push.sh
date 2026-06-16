#!/usr/bin/env bash
# token-push.sh:讀本機「現成」Claude Code access token,POST 給 ccquota /v1/token。
# server 再用它統一輪詢 usage(每帳號每週期只打一次,不會 N 倍 429),完全不碰 token endpoint。
# 跑在「有真 Claude Code 登入此帳號」的機器(它讀 Keychain / ~/.claude/.credentials.json)。
#
# 用法:CCQUOTA_INGEST_TOKEN=xxx ./token-push.sh --server https://ccquota.ezlo.me --account main
set -euo pipefail

SERVER="${CCQUOTA_SERVER:-}"
ACCOUNT="${CCQUOTA_ACCOUNT:-main}"
TOKEN="${CCQUOTA_INGEST_TOKEN:-}"

while [ $# -gt 0 ]; do
  case "$1" in
    --server)  SERVER="$2"; shift 2 ;;
    --account) ACCOUNT="$2"; shift 2 ;;
    --token)   TOKEN="$2"; shift 2 ;;
    *) echo "unknown arg: $1" >&2; exit 2 ;;
  esac
done

[ -n "$SERVER" ] || { echo "錯誤:缺 --server(或 CCQUOTA_SERVER)" >&2; exit 2; }
[ -n "$TOKEN" ]  || { echo "錯誤:缺 --token(或 CCQUOTA_INGEST_TOKEN)" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "錯誤:需要 jq(brew install jq)" >&2; exit 2; }

# 讀現成 access token:Mac 在 Keychain,其他平台在 ~/.claude/.credentials.json
creds=""
if [ "$(uname -s)" = "Darwin" ]; then
  creds=$(security find-generic-password -s "Claude Code-credentials" -w 2>/dev/null || true)
fi
if [ -z "$creds" ] && [ -f "$HOME/.claude/.credentials.json" ]; then
  creds=$(cat "$HOME/.claude/.credentials.json")
fi
[ -n "$creds" ] || { echo "錯誤:讀不到 Claude Code 憑證" >&2; exit 1; }

access=$(printf '%s' "$creds" | jq -r '.claudeAiOauth.accessToken // .accessToken // empty')
expires=$(printf '%s' "$creds" | jq -r '(.claudeAiOauth.expiresAt // .expiresAt // 0)')
[ -n "$access" ] || { echo "錯誤:憑證裡沒有 accessToken" >&2; exit 1; }
# expiresAt 可能是毫秒,轉成秒
exp_s=$(( expires > 100000000000 ? expires / 1000 : expires ))

payload=$(jq -nc --arg a "$access" --argjson e "${exp_s:-0}" '{access_token:$a, expires_at:$e}')

# 自訂 UA:Cloudflare WAF 會擋預設 curl UA(回 403)
code=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "${SERVER%/}/v1/token?account=${ACCOUNT}" \
  -A "ccquota-token-push" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  --data "$payload")

if [ "$code" = "204" ]; then
  when=$(date -r "$exp_s" '+%m-%d %H:%M' 2>/dev/null || date -d "@$exp_s" '+%m-%d %H:%M' 2>/dev/null || echo "$exp_s")
  echo "OK token 推送 → $ACCOUNT(到期 $when)"
else
  echo "推送失敗:ccquota 回 $code" >&2
  exit 1
fi
