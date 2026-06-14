#!/usr/bin/env bash
# uninstall-client.sh — 移除 ccquota 安裝的 OTel 設定
set -euo pipefail

# ── i18n ──────────────────────────────────────────────────────────────────────
LANG_SEL="${CCQUOTA_LANG:-en}"

msg() {
  local key="$1" extra="${2:-}"
  case "$LANG_SEL" in
    zh-TW)
      case "$key" in
        need_jq)        echo "錯誤：需要 jq，請先安裝 (brew install jq)" ;;
        no_settings)    echo "找不到設定檔，略過：$extra" ;;
        backed_up)      echo "已備份設定檔至：$extra" ;;
        done)           echo "✓ 已移除 ccquota OTel 設定。請重新啟動 Claude Code。" ;;
        restart)        echo "提示：關閉並重新開啟 Claude Code（或執行 claude --restart）。" ;;
        unknown_arg)    echo "錯誤：未知參數：$extra" ;;
        *)              echo "$key $extra" ;;
      esac
      ;;
    zh-CN)
      case "$key" in
        need_jq)        echo "错误：需要 jq，请先安装 (brew install jq)" ;;
        no_settings)    echo "找不到配置文件，跳过：$extra" ;;
        backed_up)      echo "已备份配置文件至：$extra" ;;
        done)           echo "✓ 已移除 ccquota OTel 配置。请重启 Claude Code。" ;;
        restart)        echo "提示：关闭并重新打开 Claude Code（或执行 claude --restart）。" ;;
        unknown_arg)    echo "错误：未知参数：$extra" ;;
        *)              echo "$key $extra" ;;
      esac
      ;;
    *)  # en
      case "$key" in
        need_jq)        echo "Error: jq is required. Install it first (brew install jq)" ;;
        no_settings)    echo "Settings file not found, nothing to do: $extra" ;;
        backed_up)      echo "Backed up settings to: $extra" ;;
        done)           echo "✓ ccquota OTel settings removed. Restart Claude Code to apply." ;;
        restart)        echo "Hint: close and reopen Claude Code (or run: claude --restart)." ;;
        unknown_arg)    echo "Error: unknown argument: $extra" ;;
        *)              echo "$key $extra" ;;
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
while [[ $# -gt 0 ]]; do
  case "$1" in
    --lang) LANG_SEL="$2"; shift 2 ;;
    *)
      msg unknown_arg "$1" >&2
      exit 1
      ;;
  esac
done

# ── 設定檔路徑 ────────────────────────────────────────────────────────────────
if [[ -n "${CLAUDE_CONFIG_DIR:-}" ]]; then
  SETTINGS_FILE="$CLAUDE_CONFIG_DIR/settings.json"
else
  SETTINGS_FILE="$HOME/.claude/settings.json"
fi

if [[ ! -f "$SETTINGS_FILE" ]]; then
  msg no_settings "$SETTINGS_FILE"
  exit 0
fi

# 備份
BACKUP="${SETTINGS_FILE}.bak-$(date +%s)"
cp "$SETTINGS_FILE" "$BACKUP"
msg backed_up "$BACKUP"

# ── 移除 installer 寫入的 7 個 key ───────────────────────────────────────────
UPDATED=$(jq '
  if .env then
    .env |= del(
      .["CLAUDE_CODE_ENABLE_TELEMETRY"],
      .["OTEL_METRICS_EXPORTER"],
      .["OTEL_EXPORTER_OTLP_PROTOCOL"],
      .["OTEL_EXPORTER_OTLP_ENDPOINT"],
      .["OTEL_EXPORTER_OTLP_HEADERS"],
      .["OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"],
      .["OTEL_RESOURCE_ATTRIBUTES"]
    )
  else
    .
  end
' "$SETTINGS_FILE")

printf '%s\n' "$UPDATED" > "$SETTINGS_FILE"

msg done
msg restart
