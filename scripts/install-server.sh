#!/usr/bin/env bash
# ccquota server installer. 下載最新 release binary,在有 systemd 的機器上裝成服務。
# 用法:curl -fsSL https://raw.githubusercontent.com/homieyangg/ccquota/main/scripts/install-server.sh | sudo bash
# 可用環境變數覆寫:CCQUOTA_PORT、CCQUOTA_ADDR、CCQUOTA_DATA_DIR、CCQUOTA_BIN_DIR、CCQUOTA_USER。
set -euo pipefail

REPO="homieyangg/ccquota"
BIN_DIR="${CCQUOTA_BIN_DIR:-/usr/local/bin}"
DATA_DIR="${CCQUOTA_DATA_DIR:-/opt/ccquota/data}"
PORT="${CCQUOTA_PORT:-11451}"
ADDR="${CCQUOTA_ADDR:-127.0.0.1:${PORT}}"
RUN_USER="${CCQUOTA_USER:-${SUDO_USER:-$(id -un)}}"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac
case "$os" in linux|darwin) ;; *) echo "unsupported os: $os" >&2; exit 1 ;; esac
asset="ccquota-${os}-${arch}"

sudo_cmd=""
if [ "$(id -u)" -ne 0 ] && command -v sudo >/dev/null 2>&1; then
  sudo_cmd="sudo"
fi

echo "Looking up the latest ccquota release..."
tag=$(curl -fsSL "https://api.github.com/repos/${REPO}/releases/latest" \
  | grep -oE '"tag_name"[[:space:]]*:[[:space:]]*"[^"]+"' | head -1 | sed -E 's/.*"([^"]+)"$/\1/')
[ -n "${tag:-}" ] || { echo "no release found for ${REPO}" >&2; exit 1; }
base="https://github.com/${REPO}/releases/download/${tag}"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
echo "Downloading ${asset} (${tag})..."
curl -fsSL -o "$tmp/$asset" "$base/$asset"
if curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS" 2>/dev/null; then
  # Linux 用 sha256sum、macOS 用 shasum,擇一(預設都沒裝就跳過驗證)。
  if command -v sha256sum >/dev/null 2>&1; then
    sha_cmd="sha256sum"
  elif command -v shasum >/dev/null 2>&1; then
    sha_cmd="shasum -a 256"
  else
    sha_cmd=""
    echo "warning: 找不到 sha256sum/shasum,略過 checksum 驗證" >&2
  fi
  if [ -n "$sha_cmd" ]; then
    ( cd "$tmp" && grep " ${asset}\$" SHA256SUMS | $sha_cmd -c - ) \
      || { echo "checksum mismatch" >&2; exit 1; }
  fi
fi
chmod +x "$tmp/$asset"
$sudo_cmd install -m 755 "$tmp/$asset" "$BIN_DIR/ccquota"
echo "Installed ccquota ${tag} to ${BIN_DIR}/ccquota"

if ! command -v systemctl >/dev/null 2>&1; then
  echo
  echo "No systemd here. Start it manually:"
  echo "  CCQUOTA_DB=${DATA_DIR}/ccquota.db ${BIN_DIR}/ccquota serve --addr ${ADDR}"
  exit 0
fi

$sudo_cmd mkdir -p "$DATA_DIR"
$sudo_cmd chown -R "$RUN_USER" "$(dirname "$DATA_DIR")" 2>/dev/null || true

envfile="$(dirname "$DATA_DIR")/ccquota.env"
if [ ! -f "$envfile" ]; then
  ingest=$(openssl rand -hex 24 2>/dev/null || head -c 24 /dev/urandom | od -An -tx1 | tr -d ' \n')
  printf 'CCQUOTA_DB=%s/ccquota.db\nCCQUOTA_INGEST_TOKEN=%s\n' "$DATA_DIR" "$ingest" | $sudo_cmd tee "$envfile" >/dev/null
  $sudo_cmd chmod 600 "$envfile"
fi

$sudo_cmd tee /etc/systemd/system/ccquota.service >/dev/null <<EOF
[Unit]
Description=ccquota quota dashboard
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${RUN_USER}
EnvironmentFile=${envfile}
ExecStart=${BIN_DIR}/ccquota serve --addr ${ADDR}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

$sudo_cmd systemctl daemon-reload
$sudo_cmd systemctl enable --now ccquota.service
sleep 2

echo
echo "ccquota ${tag} is running on ${ADDR}."

# 只抓「這次啟動」的 log,避免重裝(舊 db 還在)時撈到上一輪的舊密碼。
inv=$($sudo_cmd systemctl show -p InvocationID --value ccquota.service 2>/dev/null || true)
if [ -n "$inv" ]; then
  jlog=$($sudo_cmd journalctl _SYSTEMD_INVOCATION_ID="$inv" --no-pager 2>/dev/null || true)
else
  jlog=$($sudo_cmd journalctl -u ccquota.service --no-pager 2>/dev/null || true)
fi
pw=$(printf '%s\n' "$jlog" | sed -nE 's/.*auto-generated admin password.*: ([a-f0-9]+).*/\1/p' | tail -1 || true)

if [ -n "$pw" ]; then
  if [ -t 1 ]; then hi="\033[1;32m"; off="\033[0m"; else hi=""; off=""; fi
  echo
  printf "Admin login: user admin / password ${hi}%s${off}\n" "$pw"
  echo "You will be asked to change it on first login."
else
  echo "Set CCQUOTA_ADMIN_PASSWORD in ${envfile} (then: systemctl restart ccquota) if you need a known password."
fi
