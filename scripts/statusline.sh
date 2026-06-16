#!/usr/bin/env bash
# ccquota statusline: 顯示帳號 5h/7d 使用率 + 個人平分額度佔比(門檻變色)。
# 由 Claude Code 的 statusLine 呼叫。self-caching:cache 新鮮就直接印,過期才打一次 server。
# config(CCQUOTA_SERVER/CCQUOTA_ACCOUNT/CCQUOTA_USER/CCQUOTA_TOKEN)由 enroll 寫進 ~/.ccquota/config。
# 測試/除錯:設 CCQUOTA_QUOTA_JSON 可跳過 curl 直接餵 JSON。
set -uo pipefail

DIR="${CCQUOTA_DIR:-$HOME/.ccquota}"
CACHE="$DIR/quota.cache"
TTL="${CCQUOTA_STATUSLINE_TTL:-60}"
[ -f "$DIR/config" ] && . "$DIR/config"

mtime() { stat -f %m "$1" 2>/dev/null || stat -c %Y "$1" 2>/dev/null || echo 0; }

# cache 新鮮就直接用(且非測試模式)。
if [ -z "${CCQUOTA_QUOTA_JSON:-}" ] && [ -f "$CACHE" ]; then
  age=$(( $(date +%s) - $(mtime "$CACHE") ))
  if [ "$age" -lt "$TTL" ]; then
    cat "$CACHE"
    exit 0
  fi
fi

# 取 JSON:測試模式直接用 env,否則 curl(短 timeout)。
if [ -n "${CCQUOTA_QUOTA_JSON:-}" ]; then
  json="$CCQUOTA_QUOTA_JSON"
else
  json=$(curl -fsS --max-time 2 -H "Authorization: Bearer ${CCQUOTA_TOKEN:-}" \
    "${CCQUOTA_SERVER:-}/v1/quota?account=${CCQUOTA_ACCOUNT:-}&user=${CCQUOTA_USER:-}" 2>/dev/null)
fi

# server 掛掉/沒裝/壞 JSON:有舊 cache 用舊的,否則印佔位。
if [ -z "$json" ] || ! printf '%s' "$json" | jq -e . >/dev/null 2>&1; then
  if [ -f "$CACHE" ]; then cat "$CACHE"; else printf '5h:– 7d:–'; fi
  exit 0
fi

read -r fh sd share fhc sdw sdc usw usc < <(printf '%s' "$json" | jq -r '
  [ (.five_hour // -1), (.seven_day // -1), (.share_pct // -1),
    .thresholds.five_hour_crit, .thresholds.seven_day_warn, .thresholds.seven_day_crit,
    .thresholds.user_share_warn, .thresholds.user_share_crit ] | @tsv')

RESET=$'\033[0m'
# col VALUE WARN CRIT → 印對應 ANSI 色碼(warn 給空字串代表只有 crit)。
col() {
  awk -v v="$1" -v warn="$2" -v crit="$3" 'BEGIN{
    if (v < 0) exit;
    if (crit != "" && v+0 >= crit+0) { printf "\033[31m"; exit }
    if (warn != "" && v+0 >= warn+0) { printf "\033[33m"; exit }
    printf "\033[32m"
  }'
}
seg() { printf '%.0f' "$1"; }

out=""
[ "$fh" != "-1" ] && out="${out}$(col "$fh" "" "$fhc")5h:$(seg "$fh")%${RESET} "
[ "$sd" != "-1" ] && out="${out}$(col "$sd" "$sdw" "$sdc")7d:$(seg "$sd")%${RESET} "
[ "$share" != "-1" ] && out="${out}$(col "$share" "$usw" "$usc")me:$(seg "$share")%${RESET}"

out="${out% }"
[ -z "$out" ] && out='5h:– 7d:–'
mkdir -p "$DIR"
printf '%s' "$out" > "$CACHE"
printf '%s' "$out"
