#!/usr/bin/env bash
# sandbox.sh — run a command (or a shell) against a throwaway, isolated pekoed.
#
#   scripts/sandbox.sh                   # interactive subshell
#   scripts/sandbox.sh pekoe new feat/x  # one command
#   PEKOE_BIN=/path/to/pekoe scripts/sandbox.sh ...   # use a prebuilt binary
#   WEB=1 scripts/sandbox.sh ...         # build with the web UI (-tags web)
#
# A temp dir holds PEKOE_HOME / PEKOE_SOCK / PEKOE_WORKTREES, and the tmux
# server gets its own name (PEKOE_TMUX_SOCKET=pekoe-sb-…). The real daemon
# (`tmux -L pekoe`, ~/.local/share/pekoe) is never touched. On exit — normal,
# failure or Ctrl-C — the sandbox runs `pekoe daemon stop --kill-agents --yes`
# and deletes the temp dir, so no tmux server is left behind.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
# short path: unix socket paths must fit in ~104 bytes on macOS
dir=$(mktemp -d /tmp/pekoe-sb.XXXXXX)
id=${dir##*.}

cleanup() {
	trap - EXIT INT TERM HUP
	if [ -x "$dir/bin/pekoe" ]; then
		"$dir/bin/pekoe" daemon stop --kill-agents --yes >&2 || echo "sandbox: cleanup failed; check: ps ax | grep 'pekoe-sb-$id'" >&2
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
if [ -n "${PEKOE_BIN:-}" ]; then
	ln -s "$(cd "$(dirname "$PEKOE_BIN")" && pwd)/$(basename "$PEKOE_BIN")" "$dir/bin/pekoe"
else
	(cd "$repo" && go build ${WEB:+-tags web} -o "$dir/bin/pekoe" ./cmd/pekoe)
fi
ln -s pekoe "$dir/bin/pekoed"

export PEKOE_HOME="$dir/home"
export PEKOE_SOCK="$dir/pekoed.sock"
export PEKOE_WORKTREES="$dir/worktrees"
export PEKOE_TMUX_SOCKET="pekoe-sb-$id"
export PATH="$dir/bin:$PATH"
unset TMUX # attach to the sandbox server from inside a tmux session, too

pekoe daemon start >&2
echo "sandbox: $dir · tmux -L $PEKOE_TMUX_SOCKET" >&2

status=0
if [ $# -gt 0 ]; then
	"$@" || status=$?
else
	echo "sandbox: exit the shell to tear it down" >&2
	PEKOE_SANDBOX="$dir" "${SHELL:-/bin/sh}" -i || status=$?
fi
exit "$status"
