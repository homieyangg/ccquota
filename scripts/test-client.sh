#!/usr/bin/env bash
# test-client.sh — smoke test for install-client.sh / uninstall-client.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL="$SCRIPT_DIR/install-client.sh"
UNINSTALL="$SCRIPT_DIR/uninstall-client.sh"

PASS=0
FAIL=0

pass() { echo "[PASS] $1"; PASS=$((PASS+1)); }
fail() { echo "[FAIL] $1"; FAIL=$((FAIL+1)); }

assert_eq() {
  local label="$1" got="$2" want="$3"
  if [[ "$got" == "$want" ]]; then
    pass "$label"
  else
    fail "$label (got: '$got', want: '$want')"
  fi
}

assert_file_exists() {
  local label="$1" path="$2"
  if [[ -f "$path" ]]; then
    pass "$label"
  else
    fail "$label (file not found: $path)"
  fi
}

assert_key_absent() {
  local label="$1" file="$2" key="$3"
  local val
  val=$(jq -r --arg k "$key" '.env[$k] // "ABSENT"' "$file")
  if [[ "$val" == "ABSENT" ]]; then
    pass "$label"
  else
    fail "$label (key still present with value: '$val')"
  fi
}

# ── 建立臨時目錄 ──────────────────────────────────────────────────────────────
TMPDIR_TEST=$(mktemp -d)
trap 'rm -rf "$TMPDIR_TEST"' EXIT
export CLAUDE_CONFIG_DIR="$TMPDIR_TEST"

SETTINGS="$TMPDIR_TEST/settings.json"

# 預先寫入一個不相關的 key（驗證不被覆蓋）
echo '{"env":{"MY_EXISTING_KEY":"keep-me"},"otherSection":{"foo":"bar"}}' > "$SETTINGS"

echo "=== [1] Install (fresh run) ==="
bash "$INSTALL" \
  --server "https://ccquota.example.com/" \
  --account "acct-42" \
  --user "alice" \
  --token "tok-secret" \
  --lang en

# 確認 backup 存在
BACKUP_FILE=$(ls "$TMPDIR_TEST"/settings.json.bak-* 2>/dev/null | head -1)
assert_file_exists "backup file exists" "$BACKUP_FILE"

# 確認 7 個 key 都正確
assert_eq "CLAUDE_CODE_ENABLE_TELEMETRY" \
  "$(jq -r '.env.CLAUDE_CODE_ENABLE_TELEMETRY' "$SETTINGS")" "1"

assert_eq "OTEL_METRICS_EXPORTER" \
  "$(jq -r '.env.OTEL_METRICS_EXPORTER' "$SETTINGS")" "otlp"

assert_eq "OTEL_EXPORTER_OTLP_PROTOCOL" \
  "$(jq -r '.env.OTEL_EXPORTER_OTLP_PROTOCOL' "$SETTINGS")" "http/protobuf"

assert_eq "OTEL_EXPORTER_OTLP_ENDPOINT (trailing slash stripped)" \
  "$(jq -r '.env.OTEL_EXPORTER_OTLP_ENDPOINT' "$SETTINGS")" "https://ccquota.example.com"

assert_eq "OTEL_EXPORTER_OTLP_HEADERS (bearer token)" \
  "$(jq -r '.env.OTEL_EXPORTER_OTLP_HEADERS' "$SETTINGS")" "Authorization=Bearer tok-secret"

assert_eq "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE" \
  "$(jq -r '.env.OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE' "$SETTINGS")" "delta"

assert_eq "OTEL_RESOURCE_ATTRIBUTES (account+user)" \
  "$(jq -r '.env.OTEL_RESOURCE_ATTRIBUTES' "$SETTINGS")" "ccquota.account=acct-42,ccquota.user=alice"

# 確認不相關的 key 被保留
assert_eq "pre-existing MY_EXISTING_KEY preserved" \
  "$(jq -r '.env.MY_EXISTING_KEY' "$SETTINGS")" "keep-me"

assert_eq "pre-existing otherSection preserved" \
  "$(jq -r '.otherSection.foo' "$SETTINGS")" "bar"

echo ""
echo "=== [2] Install again (idempotent run) ==="
# 換成新 user，驗證 update 正確
bash "$INSTALL" \
  --server "https://ccquota.example.com" \
  --account "acct-42" \
  --user "bob" \
  --token "tok-secret2" \
  --lang en

assert_eq "idempotent: OTEL_RESOURCE_ATTRIBUTES updated to bob" \
  "$(jq -r '.env.OTEL_RESOURCE_ATTRIBUTES' "$SETTINGS")" "ccquota.account=acct-42,ccquota.user=bob"

assert_eq "idempotent: OTEL_EXPORTER_OTLP_HEADERS updated" \
  "$(jq -r '.env.OTEL_EXPORTER_OTLP_HEADERS' "$SETTINGS")" "Authorization=Bearer tok-secret2"

assert_eq "idempotent: MY_EXISTING_KEY still preserved" \
  "$(jq -r '.env.MY_EXISTING_KEY' "$SETTINGS")" "keep-me"

# 確認沒有重複欄位（jq 讀回來 key count 正確）
KEY_COUNT=$(jq '.env | keys | length' "$SETTINGS")
# 8 keys: 7 OTel + MY_EXISTING_KEY
assert_eq "no duplicate keys (8 total)" "$KEY_COUNT" "8"

echo ""
echo "=== [3] Uninstall ==="
bash "$UNINSTALL" --lang en

# 確認 7 個 key 被移除
assert_key_absent "CLAUDE_CODE_ENABLE_TELEMETRY removed"                       "$SETTINGS" "CLAUDE_CODE_ENABLE_TELEMETRY"
assert_key_absent "OTEL_METRICS_EXPORTER removed"                              "$SETTINGS" "OTEL_METRICS_EXPORTER"
assert_key_absent "OTEL_EXPORTER_OTLP_PROTOCOL removed"                        "$SETTINGS" "OTEL_EXPORTER_OTLP_PROTOCOL"
assert_key_absent "OTEL_EXPORTER_OTLP_ENDPOINT removed"                        "$SETTINGS" "OTEL_EXPORTER_OTLP_ENDPOINT"
assert_key_absent "OTEL_EXPORTER_OTLP_HEADERS removed"                         "$SETTINGS" "OTEL_EXPORTER_OTLP_HEADERS"
assert_key_absent "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE removed"  "$SETTINGS" "OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE"
assert_key_absent "OTEL_RESOURCE_ATTRIBUTES removed"                            "$SETTINGS" "OTEL_RESOURCE_ATTRIBUTES"

# 確認不相關的 key 還在
assert_eq "MY_EXISTING_KEY remains after uninstall" \
  "$(jq -r '.env.MY_EXISTING_KEY' "$SETTINGS")" "keep-me"

assert_eq "otherSection remains after uninstall" \
  "$(jq -r '.otherSection.foo' "$SETTINGS")" "bar"

echo ""
echo "=== [4] zh-TW language test ==="
echo '{}' > "$SETTINGS"
OUTPUT=$(bash "$INSTALL" \
  --server "https://example.com" \
  --account "a1" \
  --user "測試者" \
  --token "t1" \
  --lang zh-TW 2>&1)
if echo "$OUTPUT" | grep -q "安裝完成"; then
  pass "zh-TW success message shown"
else
  fail "zh-TW success message not found in output: $OUTPUT"
fi

echo ""
echo "================================================"
echo "Results: $PASS passed, $FAIL failed"
echo "================================================"

[[ "$FAIL" -eq 0 ]]
