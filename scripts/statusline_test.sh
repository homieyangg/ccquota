#!/usr/bin/env bash
# statusline.sh 的格式/上色測試(用 CCQUOTA_QUOTA_JSON 餵假資料,不打網路)。
set -uo pipefail
HERE="$(cd "$(dirname "$0")" && pwd)"
SL="$HERE/statusline.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0
chk() { # 名稱 期望子字串 實際
  if printf '%s' "$3" | grep -q -- "$2"; then echo "ok  - $1"; else echo "FAIL- $1: 期望含 '$2',得到 '$3'"; fail=1; fi
}

# 綠色:5h/7d/share 都低於門檻
out=$(CCQUOTA_DIR="$TMP" CCQUOTA_QUOTA_JSON='{"five_hour":23,"seven_day":59,"share_pct":80,"thresholds":{"five_hour_crit":95,"seven_day_warn":75,"seven_day_crit":90,"user_share_warn":150,"user_share_crit":250}}' bash "$SL")
chk "格式 5h"  "5h:23%" "$out"
chk "格式 7d"  "7d:59%" "$out"
chk "格式 me"  "me:80%" "$out"
chk "綠色"     $'\033[32m' "$out"

# 7d 超 crit → 紅
rm -f "$TMP/quota.cache"
out=$(CCQUOTA_DIR="$TMP" CCQUOTA_QUOTA_JSON='{"five_hour":10,"seven_day":92,"share_pct":10,"thresholds":{"five_hour_crit":95,"seven_day_warn":75,"seven_day_crit":90,"user_share_warn":150,"user_share_crit":250}}' bash "$SL")
chk "7d 紅"   $'\033[31m' "$out"

# 無 reading(5h/7d null) → 不應出現 5h:
rm -f "$TMP/quota.cache"
out=$(CCQUOTA_DIR="$TMP" CCQUOTA_QUOTA_JSON='{"five_hour":null,"seven_day":null,"share_pct":null,"thresholds":{"five_hour_crit":95,"seven_day_warn":75,"seven_day_crit":90,"user_share_warn":150,"user_share_crit":250}}' bash "$SL")
if printf '%s' "$out" | grep -q "5h:"; then echo "FAIL- null 不該有 5h: '$out'"; fail=1; else echo "ok  - null 無 5h"; fi

# 壞 JSON → 佔位
rm -f "$TMP/quota.cache"
out=$(CCQUOTA_DIR="$TMP" CCQUOTA_QUOTA_JSON='not json' bash "$SL")
chk "壞 JSON 佔位" "5h:–" "$out"

exit $fail
