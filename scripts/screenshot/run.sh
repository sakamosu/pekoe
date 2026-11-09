#!/usr/bin/env bash
# run.sh — re-take the README screenshot (docs/images/screenshot.png).
#
#   scripts/screenshot/run.sh [out.png]
#
# Starts a throwaway pekoed with scripts/sandbox.sh, lets driver.sh build a
# fictional repository (acme/shop) with four sessions run by a fake agent
# (fake-agent.sh), and captures the real TUI from its own tmux server. No real
# agent runs, and none of your sessions, repositories or paths are shown.
# The capture becomes a terminal-window HTML page (ansi2html.py), which
# headless Chrome renders at 2x with a transparent background (2720×1760).
#
# Needs macOS (it was only made and checked there), Go, tmux, git, python3 and
# Google Chrome ($PEKOE_CHROME, or /Applications/Google Chrome.app).
# Intermediate files (capture, HTML, Chrome profile) live in a temp dir that
# is removed at the end. COLS / ROWS / KEYS are passed on to driver.sh.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
out=${1:-$repo/docs/images/screenshot.png}
chrome=${PEKOE_CHROME:-/Applications/Google Chrome.app/Contents/MacOS/Google Chrome}
if [ ! -x "$chrome" ]; then
	echo "screenshot: Google Chrome not found at $chrome · set PEKOE_CHROME" >&2
	exit 1
fi

tmp=$(mktemp -d /tmp/pekoe-shot.XXXXXX)
cleanup() {
	trap - EXIT INT TERM HUP
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

# tmux servers named in $tmp/shot.ans.sockets that are still running
leftovers() {
	[ -f "$tmp/shot.ans.sockets" ] || return 0
	local sb shot
	read -r sb shot <"$tmp/shot.ans.sockets"
	pgrep -fl "tmux -L ($sb|$shot)( |\$)" || true
}

status=0
OUT="$tmp/shot.ans" ROWS=${ROWS:-42} COLS=${COLS:-150} \
	"$repo/scripts/sandbox.sh" bash "$here/driver.sh" || status=$?
left=$(leftovers)
if [ -n "$left" ]; then
	printf 'screenshot: tmux servers left behind:\n%s\n' "$left" >&2
	exit 1
fi
[ "$status" -eq 0 ] || exit "$status"

python3 "$here/ansi2html.py" "$tmp/shot.ans" "$tmp/shot.html"
"$chrome" --headless=new --disable-gpu --hide-scrollbars --force-device-scale-factor=2 \
	--default-background-color=00000000 --user-data-dir="$tmp/chrome" \
	--window-size=1360,880 --screenshot="$tmp/screenshot.png" "file://$tmp/shot.html" 2>/dev/null
[ -s "$tmp/screenshot.png" ] || { echo "screenshot: Chrome wrote no image" >&2; exit 1; }
mv "$tmp/screenshot.png" "$out"
echo "screenshot: wrote $out" >&2
