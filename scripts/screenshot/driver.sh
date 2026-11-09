#!/usr/bin/env bash
# driver.sh — the README screenshot's scene. Run it through scripts/screenshot/run.sh.
#
# Runs inside scripts/sandbox.sh: builds a fictional repo "acme/shop" with four
# demo sessions driven by fake-agent.sh (no real agent runs, so no real
# session or path shows up), opens the TUI in its own tmux server
# (pekoe-shot-<id>), and captures the screen (ANSI) to $OUT.
#
#   OUT=/tmp/x/shot.ans scripts/sandbox.sh bash scripts/screenshot/driver.sh
#
# OUT (required): where the capture goes. "$OUT.sockets" gets the names of
# the tmux servers involved, so the caller can check they are gone.
# FAKE_AGENT: the fake agent (default: fake-agent.sh next to this file).
# COLS / ROWS: the TUI's size. KEYS: tmux keys to send before capturing.
set -euo pipefail
S=$(cd "$(dirname "$0")" && pwd)
: "${OUT:?set OUT to the capture file}"
: "${PEKOE_SOCK:?run inside scripts/sandbox.sh}"
FAKE_AGENT=${FAKE_AGENT:-$S/fake-agent.sh}
COLS=${COLS:-150}; ROWS=${ROWS:-40}
case "$PEKOE_TMUX_SOCKET" in
pekoe-sb-*) ;;
*) echo "driver.sh: PEKOE_TMUX_SOCKET=$PEKOE_TMUX_SOCKET is not a sandbox; run inside scripts/sandbox.sh" >&2; exit 1 ;;
esac
SHOT="pekoe-shot-${PEKOE_TMUX_SOCKET##*-}"
echo "$PEKOE_TMUX_SOCKET $SHOT" >"$OUT.sockets"

mkdir -p "$PEKOE_HOME/config/repos"
cat >"$PEKOE_HOME/config/config.toml" <<EOF
animate = false
[theme]
mode = "truecolor"
icons = "unicode"
[mascot]
shape = "block"
glyphs = "unicode"
splash = false
[tips]
enable = false
[agents.claude]   # the fake agent stands in for claude
command = "$FAKE_AGENT"
prompt = "arg"
allow = "1"
always = "2"
deny = "Escape"
hooks = true
EOF
cat >"$PEKOE_HOME/config/repos/github.com_acme_shop.toml" <<'EOF'
[worktree]
base = "main"
[hooks]
test = "echo 'Tests  14 passed (14)'"
EOF
# the first-run tutorial (internal/tui/tour.go) would cover the screenshot
mkdir -p "$PEKOE_HOME/share"
printf '{"seen_at":"2026-01-01T00:00:00Z"}\n' >"$PEKOE_HOME/share/tutorial.json"
FAKEHOME=$(cd "$(dirname "$PEKOE_HOME")" && pwd -P)   # shown as ~ in the TUI
export PEKOE_WORKTREES="$FAKEHOME/worktrees"
pekoe daemon restart >&2

R=$FAKEHOME/src/shop
mkdir -p "$R/src/api" "$R/src/orders" "$R/src/cart" "$R/src/invoices"
cd "$R"
git init -q -b main
printf 'export function listOrders() {}\n' >src/api/orders.ts
printf 'export default function OrdersPage() {}\n' >src/orders/OrdersPage.tsx
printf 'export function total() {}\n' >src/cart/total.ts
printf '# shop\n' >README.md
git add -A && git -c user.name=demo -c user.email=demo@example.com commit -qm init
git remote add origin https://github.com/acme/shop.git

for b in feat/csv-export fix/cart-rounding feat/invoice-pdf chore/bump-deps; do
	pekoe --json new "$b" >/dev/null
	sleep 1.5
done
sleep 12 # let hooks, diffstat, tests and the conflict forecast settle
pekoe ls >&2

trap 'tmux -L "$SHOT" kill-server 2>/dev/null || true' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
tmux -L "$SHOT" -f /dev/null new-session -d -x "$COLS" -y "$ROWS" -c "$R" \
	-e PEKOE_HOME="$PEKOE_HOME" -e PEKOE_SOCK="$PEKOE_SOCK" -e PEKOE_TMUX_SOCKET="$PEKOE_TMUX_SOCKET" \
	-e PEKOE_WORKTREES="$PEKOE_WORKTREES" -e PATH="$PATH" -e HOME="$FAKEHOME" -e COLORTERM=truecolor -e TERM=xterm-256color \
	"pekoe"
sleep 4
if [ -n "${KEYS:-}" ]; then
	# shellcheck disable=SC2086 # KEYS is a list of tmux key names
	tmux -L "$SHOT" send-keys $KEYS
fi
sleep 2
tmux -L "$SHOT" capture-pane -p -e >"$OUT"
echo "captured $OUT" >&2
