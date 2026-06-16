#!/usr/bin/env bash
# ccquota statusline 包裝層。由 enroll 設成 Claude Code 的 statusLine;stdin = session JSON。
#   - 有原本的 statusLine(存在 ~/.ccquota/statusline-orig):跑它,末尾接上 ccquota 個人 share(含進度條)。
#   - 沒有原本的:直接出完整 ccquota 行(5h/7d/me)。
# 設計:ccquota 失敗就靜默略過,絕不弄壞原 statusline。
set -uo pipefail
DIR="${CCQUOTA_DIR:-$HOME/.ccquota}"
input=$(cat)
orig=$(cat "$DIR/statusline-orig" 2>/dev/null || true)

# 沒有原 statusline → ccquota 當整條。
if [ -z "$orig" ]; then
  bash "$DIR/statusline.sh"
  exit 0
fi

base=$(printf '%s' "$input" | sh -c "$orig" 2>/dev/null)

# 從 ccquota 取個人 share 百分比(整數)。
share=$(bash "$DIR/statusline.sh" 2>/dev/null | grep -oE 'me:[0-9]+' | grep -oE '[0-9]+' | head -1)

seg=""
if [ -n "$share" ]; then
  DIM=$'\033[38;5;240m'; RESET=$'\033[0m'
  # <100% 綠、>=100% 紅(跟 dashboard 的 share 配色一致)。
  if [ "$share" -ge 100 ]; then C=$'\033[38;5;203m'; else C=$'\033[38;5;155m'; fi
  # 進度條:width 5,實心 █(帶色)+ 空 ░(DIM);>100% 夾到滿。
  w=5; p=$share; [ "$p" -gt 100 ] && p=100
  filled=$(( p * w / 100 )); empty=$(( w - filled ))
  bar=""; i=0; while [ "$i" -lt "$filled" ]; do bar="${bar}█"; i=$((i+1)); done
  e="";   i=0; while [ "$i" -lt "$empty"  ]; do e="${e}░";   i=$((i+1)); done
  seg="${C}ccquota:${RESET} ${C}${bar}${RESET}${DIM}${e}${RESET} ${C}${share}%${RESET}"
fi

if [ -n "$base" ] && [ -n "$seg" ]; then
  DIM=$'\033[38;5;240m'; RESET=$'\033[0m'
  printf '%s %s│%s %s' "$base" "$DIM" "$RESET" "$seg"
elif [ -n "$seg" ]; then
  printf '%s' "$seg"
else
  printf '%s' "$base"
fi
