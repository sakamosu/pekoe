#!/usr/bin/env bash
# sandbox.sh — run a command (or a shell) against a throwaway, isolated bobad.
#
#   scripts/sandbox.sh                   # interactive subshell
#   scripts/sandbox.sh boba new feat/x   # one command
#   BOBA_BIN=/path/to/boba scripts/sandbox.sh ...   # use a prebuilt binary
#
# A temp dir holds BOBA_HOME / BOBA_SOCK / BOBA_WORKTREES, and the tmux server
# gets its own name (BOBA_TMUX_SOCKET=boba-sb-…). The real daemon
# (`tmux -L boba`, ~/.local/share/boba) is never touched. On exit — normal,
# failure or Ctrl-C — the sandbox runs `boba daemon stop --kill-agents --yes`
# and deletes the temp dir, so no tmux server is left behind.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# short path: unix socket paths must fit in ~104 bytes on macOS
dir=$(mktemp -d /tmp/boba-sb.XXXXXX)
id=${dir##*.}

cleanup() {
	trap - EXIT INT TERM HUP
	if [ -x "$dir/bin/boba" ]; then
		"$dir/bin/boba" daemon stop --kill-agents --yes >&2 || echo "sandbox: cleanup failed; check: ps ax | grep 'boba-sb-$id'" >&2
	fi
	# worktrees made in the sandbox stay registered in their repository
	# until `git worktree prune`; the files themselves go with the temp dir
	rm -rf "$dir"
	echo "sandbox: removed $dir" >&2
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
trap 'exit 129' HUP

mkdir -p "$dir/bin"
if [ -n "${BOBA_BIN:-}" ]; then
	ln -s "$(cd "$(dirname "$BOBA_BIN")" && pwd)/$(basename "$BOBA_BIN")" "$dir/bin/boba"
else
	(cd "$repo" && go build -o "$dir/bin/boba" ./cmd/boba)
fi
ln -s boba "$dir/bin/bobad"

export BOBA_HOME="$dir/home"
export BOBA_SOCK="$dir/bobad.sock"
export BOBA_WORKTREES="$dir/worktrees"
export BOBA_TMUX_SOCKET="boba-sb-$id"
export PATH="$dir/bin:$PATH"
unset TMUX # attach to the sandbox server from inside a tmux session, too

boba daemon start >&2
echo "sandbox: $dir · tmux -L $BOBA_TMUX_SOCKET" >&2

status=0
if [ $# -gt 0 ]; then
	"$@" || status=$?
else
	echo "sandbox: exit the shell to tear it down" >&2
	BOBA_SANDBOX="$dir" "${SHELL:-/bin/sh}" -i || status=$?
fi
exit "$status"
