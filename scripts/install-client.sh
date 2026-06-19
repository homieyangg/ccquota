#!/usr/bin/env bash
# install-client.sh: 設定 Claude Code OTel telemetry 指向 ccquota server
set -euo pipefail

# ── i18n ──────────────────────────────────────────────────────────────────────
LANG_SEL="${CCQUOTA_LANG:-en}"

msg() {
  # msg <key> [extra]
  local key="$1" extra="${2:-}"
  case "$LANG_SEL" in
    zh-TW)
      case "$key" in
        need_jq)         echo "錯誤：需要 jq，請先安裝 (brew install jq)" ;;
        missing_server)  echo "錯誤：缺少 --server 參數" ;;
        missing_account) echo "錯誤：缺少 --account 參數" ;;
        missing_user)    echo "錯誤：缺少 --user 參數" ;;
        missing_token)   echo "錯誤：缺少 --token 參數" ;;
        unknown_arg)     echo "錯誤：未知參數：$extra" ;;
        backed_up)       echo "已備份設定檔至：$extra" ;;
        created)         echo "已建立新設定檔：$extra" ;;
        done)            echo "✓ 安裝完成！請重新啟動 Claude Code 以套用設定。" ;;
        restart)         echo "提示：關閉並重新開啟 Claude Code。" ;;
        *)               echo "$key $extra" ;;
      esac
      ;;
    zh-CN)
      case "$key" in
        need_jq)         echo "错误：需要 jq，请先安装 (brew install jq)" ;;
        missing_server)  echo "错误：缺少 --server 参数" ;;
        missing_account) echo "错误：缺少 --account 参数" ;;
        missing_user)    echo "错误：缺少 --user 参数" ;;
        missing_token)   echo "错误：缺少 --token 参数" ;;
        unknown_arg)     echo "错误：未知参数：$extra" ;;
        backed_up)       echo "已备份配置文件至：$extra" ;;
        created)         echo "已创建新配置文件：$extra" ;;
        done)            echo "✓ 安装完成！请重启 Claude Code 以应用配置。" ;;
        restart)         echo "提示：关闭并重新打开 Claude Code。" ;;
        *)               echo "$key $extra" ;;
      esac
      ;;
    *)  # en (default)
      case "$key" in
        need_jq)         echo "Error: jq is required. Install it first (brew install jq)" ;;
        missing_server)  echo "Error: --server is required" ;;
        missing_account) echo "Error: --account is required" ;;
        missing_user)    echo "Error: --user is required" ;;
        missing_token)   echo "Error: --token is required" ;;
        unknown_arg)     echo "Error: unknown argument: $extra" ;;
        backed_up)       echo "Backed up settings to: $extra" ;;
        created)         echo "Created settings file: $extra" ;;
        done)            echo "✓ Installation complete! Restart Claude Code to apply settings." ;;
        restart)         echo "Hint: close and reopen Claude Code." ;;
        *)               echo "$key $extra" ;;
      esac
      ;;
  esac
}

# ── 依賴檢查 ──────────────────────────────────────────────────────────────────
if ! command -v jq &>/dev/null; then
  msg need_jq >&2
  exit 1
fi

# ── 解析參數 ──────────────────────────────────────────────────────────────────
SERVER=""
ACCOUNT=""
USER_NAME=""
TOKEN=""

while [[ $# -gt 0 ]]; do
  case "$1" in
    --server)   SERVER="$2";    shift 2 ;;
    --account)  ACCOUNT="$2";   shift 2 ;;
    --user)     USER_NAME="$2"; shift 2 ;;
    --token)    TOKEN="$2";     shift 2 ;;
    --lang)     LANG_SEL="$2";  shift 2 ;;
    *)
      msg unknown_arg "$1" >&2
      exit 1
      ;;
  esac
done

[[ -z "$SERVER"    ]] && { msg missing_server    >&2; exit 1; }
[[ -z "$ACCOUNT"   ]] && { msg missing_account   >&2; exit 1; }
[[ -z "$USER_NAME" ]] && { msg missing_user      >&2; exit 1; }
[[ -z "$TOKEN"     ]] && { msg missing_token     >&2; exit 1; }

# strip trailing slash
SERVER="${SERVER%/}"

# ── 設定檔路徑 ────────────────────────────────────────────────────────────────
if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]]; then
  SETTINGS_FILE="$CLAUDE_CONFIG_DIR/settings.json"
else
  SETTINGS_FILE="$HOME/.claude/settings.json"
fi
mkdir -p "$(dirname "$SETTINGS_FILE")"

# 備份 / 建立
BACKUP="${SETTINGS_FILE}.bak-$(date +%s)"
if [[ -f "$SETTINGS_FILE" ]]; then
  cp "$SETTINGS_FILE" "$BACKUP"
  msg backed_up "$BACKUP"
else
  echo "{}" > "$SETTINGS_FILE"
  msg created "$SETTINGS_FILE"
  # 建立時也需要備份路徑存在（smoke test 要驗證 backup 存在）
  cp "$SETTINGS_FILE" "$BACKUP"
fi

# ── merge OTel 設定（不覆蓋其他 key） ────────────────────────────────────────
UPDATED=$(jq \
  --arg server  "$SERVER" \
  --arg account "$ACCOUNT" \
  --arg user    "$USER_NAME" \
  --arg token   "$TOKEN" \
  '
  .env //= {} |
  .env["CLAUDE_CODE_ENABLE_TELEMETRY"]                       = "1" |
  .env["OTEL_METRICS_EXPORTER"]                              = "otlp" |
  .env["OTEL_EXPORTER_OTLP_PROTOCOL"]                       = "http/protobuf" |
  .env["OTEL_EXPORTER_OTLP_ENDPOINT"]                       = $server |
  .env["OTEL_EXPORTER_OTLP_HEADERS"]                        = ("Authorization=Bearer " + $token) |
  .env["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"] = "delta" |
  .env["OTEL_RESOURCE_ATTRIBUTES"]                          = ("ccquota.account=" + $account + ",ccquota.user=" + $user)
  ' "$SETTINGS_FILE")

printf '%s\n' "$UPDATED" > "$SETTINGS_FILE"

msg done
msg restart

# ── 冷啟動回填 ────────────────────────────────────────────────────────────────
# 把本機過去 7 天的用量(token + 用維護中的價格表算的 $)推給 server,讓全新安裝 Day 1
# 就能反推週額度,免等累積。抓不到價格表就只送 token,dashboard 退回 token 估算。
# 全程 best-effort:任何一步失敗就安靜略過,不影響安裝。
LITELLM_PRICES_URL="https://raw.githubusercontent.com/BerriAI/litellm/main/model_prices_and_context_window.json"
backfill_history() {
  local projects resets now ws pricefile result tokens cost
  projects="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/projects"
  [[ -d "$projects" ]] || return 0
  command -v curl >/dev/null 2>&1 || return 0
  # 對齊帳號 7d 視窗:跟 server 要 seven_day_resets_at(= 下次重置,減 7 天即上次重置=視窗起點)
  resets=$(curl -fsS -H "Authorization: Bearer $TOKEN" \
    "$SERVER/v1/quota?account=$ACCOUNT&user=$USER_NAME" 2>/dev/null | jq -r '.seven_day_resets_at // empty' 2>/dev/null) || return 0
  [[ "$resets" =~ ^[0-9]+$ ]] || return 0
  now=$(date +%s)
  ws=$((resets - 7 * 24 * 3600))
  # 抓維護中的價格表(LiteLLM,ccusage 同源)算 $;抓不到就留空表,$ 會是 0,只送 token。
  pricefile=$(mktemp)
  curl -fsSL "$LITELLM_PRICES_URL" -o "$pricefile" 2>/dev/null || echo '{}' > "$pricefile"
  # 掃 projects/**/*.jsonl,視窗內 assistant 訊息:加總 token(全 type,對齊 live ingest)+ 用 model 查表算 $。
  # timestamp 去掉小數秒再給 fromdateiso8601;非 UTC/解析失敗或不在表內的單筆,token 仍算、$ 記 0。
  result=$(cat "$projects"/*/*.jsonl 2>/dev/null | jq -rn \
    --slurpfile prices "$pricefile" --argjson ws "$ws" --argjson cut "$now" '
    ($prices[0] // {}) as $p
    | reduce (inputs
        | select((.message.usage // null) != null and (.timestamp // null) != null)
        | (.timestamp | sub("\\.[0-9]+";"")) as $ts
        | select(($ts | fromdateiso8601) >= $ws and ($ts | fromdateiso8601) < $cut)
        | .message.usage as $u | ($p[.message.model] // {}) as $pr
        | { tok: (($u.input_tokens//0)+($u.output_tokens//0)+($u.cache_creation_input_tokens//0)+($u.cache_read_input_tokens//0)),
            cost: (($u.input_tokens//0)*($pr.input_cost_per_token//0)
                 + ($u.output_tokens//0)*($pr.output_cost_per_token//0)
                 + ($u.cache_creation_input_tokens//0)*($pr.cache_creation_input_token_cost//0)
                 + ($u.cache_read_input_tokens//0)*($pr.cache_read_input_token_cost//0)) }
      ) as $m ({tok:0,cost:0}; {tok:(.tok+$m.tok), cost:(.cost+$m.cost)})
    | "\(.tok) \(.cost)"' 2>/dev/null) || { rm -f "$pricefile"; return 0; }
  rm -f "$pricefile"
  read -r tokens cost <<< "$result"
  [[ "$tokens" =~ ^[0-9]+$ && "$tokens" -gt 0 ]] || return 0
  [[ "$cost" =~ ^[0-9.]+$ ]] || cost=0
  curl -fsS -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
    -d "{\"account\":\"$ACCOUNT\",\"user\":\"$USER_NAME\",\"cost_usd\":$cost,\"tokens\":$tokens,\"window_start\":$ws,\"cutoff\":$now}" \
    "$SERVER/v1/backfill" >/dev/null 2>&1 || return 0
  if awk "BEGIN{exit !($cost>0)}" 2>/dev/null; then
    case "$LANG_SEL" in
      zh-*) printf '✓ 已回填本機歷史用量(約 $%.2f),冷啟動估算用\n' "$cost" ;;
      *)    printf '✓ Backfilled local history (~$%.2f) for cold-start estimate\n' "$cost" ;;
    esac
  else
    case "$LANG_SEL" in
      zh-*) echo "✓ 已回填本機歷史 token($tokens,無價格表),冷啟動估算用" ;;
      *)    echo "✓ Backfilled $tokens local tokens (no price table) for cold-start estimate" ;;
    esac
  fi
}
backfill_history || true
