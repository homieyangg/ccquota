#!/usr/bin/env bash
# usage-push.sh: 在「有真 Claude Code 登入此帳號」的機器上跑。
# 讀本機現成的 access token → 打 /api/oauth/usage 拿 7d/5h → POST 回 ccquota server。
# 重點:完全不 refresh、不碰被限流的 token endpoint,只借真 CC 維護好的 token。
#
# 用法:
#   CCQUOTA_INGEST_TOKEN=xxx ./usage-push.sh --server https://ccquota.ezlo.me --account main
# 或全部用旗標:
#   ./usage-push.sh --server <url> --account <id> --token <ingest-token>
set -euo pipefail

SERVER="${CCQUOTA_SERVER:-}"
ACCOUNT="${CCQUOTA_ACCOUNT:-main}"
TOKEN="${CCQUOTA_INGEST_TOKEN:-}"
UA="claude-code/2.1.177" # 少了這個 UA 會掉進更兇的限流桶

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
[ -n "$access" ] || { echo "錯誤:憑證裡沒有 accessToken" >&2; exit 1; }

# 打 usage endpoint(只讀,不 refresh)
resp=$(curl -sS -w $'\n%{http_code}' "https://api.anthropic.com/api/oauth/usage" \
  -H "Authorization: Bearer $access" \
  -H "anthropic-beta: oauth-2025-04-20" \
  -H "Content-Type: application/json" \
  -H "User-Agent: $UA")
code=$(printf '%s' "$resp" | tail -n1)
body=$(printf '%s' "$resp" | sed '$d')

if [ "$code" != "200" ]; then
  echo "usage endpoint 回 $code,跳過(它自己可能在限流): $(printf '%s' "$body" | head -c 160)" >&2
  exit 1
fi

# POST 原始 usage JSON 回 ccquota(server 端用 usage.Parse 解析後寫 reading)
pcode=$(curl -sS -o /dev/null -w '%{http_code}' -X POST \
  "${SERVER%/}/v1/usage?account=${ACCOUNT}" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  --data "$body")

if [ "$pcode" = "204" ]; then
  echo "OK 7d=$(printf '%s' "$body" | jq -r '.seven_day.utilization // "?"')% 5h=$(printf '%s' "$body" | jq -r '.five_hour.utilization // "?"')% → $ACCOUNT"
else
  echo "推送失敗:ccquota 回 $pcode" >&2
  exit 1
fi
