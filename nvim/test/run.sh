#!/bin/sh
# 偽デーモンを立ててヘッドレス nvim でスモークテストを走らせる。
set -eu
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(dirname "$HERE")
TMPD=${PEKOE_TEST_TMP:-${TMPDIR:-/tmp}}
SOCK="$TMPD/pekoe-test-$$.sock"

# 構文チェック
for f in "$ROOT"/plugin/*.lua "$ROOT"/lua/pekoe/*.lua "$ROOT"/test/*.lua; do
  luac -p "$f"
done
echo "syntax ok"

# 偽デーモンは本物の unix ソケットで待つ。エージェントの sandbox の中では bind
# できないので、そのときだけ理由を出して飛ばす(ほかの失敗は隠さない)
if ! why=$(python3 -c '
import os, socket, sys
p = sys.argv[1]
s = socket.socket(socket.AF_UNIX)
try:
    s.bind(p)
except PermissionError as e:
    print(e); sys.exit(1)
finally:
    s.close()
    if os.path.exists(p): os.unlink(p)
' "$SOCK"); then
  echo "SKIP nvim headless tests: needs a real unix socket, which cannot be bound here ($why); run make test outside the agent's sandbox to cover it"
  exit 0
fi

PID=
trap 'kill $PID 2>/dev/null || true; rm -f "$SOCK"' EXIT
start_daemon() {
  python3 "$HERE/fake_daemon.py" "$SOCK" &
  PID=$!
  i=0
  while [ ! -S "$SOCK" ] && [ $i -lt 50 ]; do sleep 0.1; i=$((i+1)); done
}

echo "== smoke"
# 人間用トークン: session.answer に付けて送られることを smoke.lua が確かめる
HOMED="$TMPD/pekoe-test-home-$$"
mkdir -p "$HOMED/share"
printf 'test-human-token\n' >"$HOMED/share/human-token"
trap 'kill $PID 2>/dev/null || true; rm -f "$SOCK" "$HOMED/share/human-token"; rmdir "$HOMED/share" "$HOMED" 2>/dev/null || true' EXIT
start_daemon
PEKOE_HOME="$HOMED" PEKOE_SOCK="$SOCK" nvim --headless -u NONE -i NONE -n \
  --cmd "set rtp+=$ROOT" \
  -c "luafile $HERE/smoke.lua"
kill $PID; wait $PID 2>/dev/null || true; rm -f "$SOCK"

echo "== auto review (PEKOE_REVIEW=1, daemon starts 1s later)"
(sleep 1; exec python3 "$HERE/fake_daemon.py" "$SOCK") &
PID=$!
PEKOE_SOCK="$SOCK" PEKOE_REVIEW=1 PEKOE_SESSION=feat-x nvim --headless -u NONE -i NONE -n \
  --cmd "set rtp+=$ROOT" --cmd "runtime plugin/pekoe.lua" \
  -c "luafile $HERE/auto_review.lua"
