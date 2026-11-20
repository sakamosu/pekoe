#!/bin/sh
# make test's go test runs, side by side: the default build and -tags web.
#
# internal/daemon is most of the time (its tests are sequential, no
# t.Parallel), and both builds run all of it. So each build lists its daemon
# tests (go test -list), deals them out into $PEKOE_TEST_SHARDS groups and
# runs each group as its own go test -run '^(A|B|…)$' process, all at once.
# A process is its own test environment (internal/testenv: own temporary
# home, socket and tmux server), so the groups do not meet. Subtests run with
# their top-level test.
#
# Only the default build's other packages stream as usual; every daemon
# group and the web run go to files and are printed after it, one at a time
# under a header, so the outputs never interleave (the CI step that lists
# skipped tests parses this output). Fails when any of them fails and says
# which.
#
#   scripts/gotest.sh [go]
#   PEKOE_TEST_SHARDS=n   groups per build (default: a third of the CPUs, at
#                         least 2)
#
# Not more by default: some daemon tests time themselves (the PR poller must
# return within 1.5s while a hook sleeps), and with as many test processes as
# CPUs they failed locally under the load.
set -u
GO=${1:-go}
DAEMON=pekoe/internal/daemon

ncpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 2)
shards=${PEKOE_TEST_SHARDS:-$((ncpu / 3))}
[ "$shards" -ge 2 ] 2>/dev/null || shards=2

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pkgotest.XXXXXX") || exit 1

# stop <go test pid>…: go test leaves its test binary running when killed, so
# stop the binary first
stop() {
	for p in "$@"; do
		pkill -TERM -P "$p" 2>/dev/null
		kill "$p" 2>/dev/null
	done
}

webcmd_pid=
shard_pids=
cleanup() {
	[ -n "$webcmd_pid" ] && stop "$webcmd_pid"
	# each daemon_shards subshell stops its own go tests (its TERM trap)
	[ -n "$shard_pids" ] && kill $shard_pids 2>/dev/null
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# daemon_shards <name> [go test flags…]: list the daemon tests of a build and
# start one background go test per group. Writes $tmp/<name>.<i>.log, the
# exit code to $tmp/<name>.<i>.rc and the header to $tmp/<name>.<i>.cmd;
# when listing fails, its output goes to $tmp/<name>.list.log instead.
daemon_shards() {
	name=$1
	shift
	spids=
	# cleanup's kill stops this subshell; take its go tests along
	# shellcheck disable=SC2086 # one pid per word
	trap 'stop $spids; exit 130' TERM
	if ! "$GO" test "$@" -list '.*' "$DAEMON" >"$tmp/$name.list.log" 2>&1; then
		echo 1 >"$tmp/$name.list.rc"
		return
	fi
	grep -E '^(Test|Example|Fuzz)' "$tmp/$name.list.log" | sort >"$tmp/$name.names"
	total=$(wc -l <"$tmp/$name.names" | tr -d ' ')
	i=0
	while [ "$i" -lt "$shards" ]; do
		awk -v n="$shards" -v i="$i" '(NR - 1) % n == i' "$tmp/$name.names" >"$tmp/$name.$i.names"
		if [ -s "$tmp/$name.$i.names" ]; then
			re="^($(paste -sd'|' - <"$tmp/$name.$i.names"))\$"
			echo "$GO test ${*:+$* }$DAEMON (group $((i + 1))/$shards: $(wc -l <"$tmp/$name.$i.names" | tr -d ' ') of $total tests)" >"$tmp/$name.$i.cmd"
			"$GO" test "$@" -run "$re" "$DAEMON" >"$tmp/$name.$i.log" 2>&1 &
			echo $! >"$tmp/$name.$i.pid"
			spids="$spids $!"
		fi
		i=$((i + 1))
	done
	i=0
	while [ "$i" -lt "$shards" ]; do
		if [ -f "$tmp/$name.$i.pid" ]; then
			wait "$(cat "$tmp/$name.$i.pid")"
			echo $? >"$tmp/$name.$i.rc"
		fi
		i=$((i + 1))
	done
}

# the web run's own packages besides the daemon
"$GO" test -tags web ./cmd/... >"$tmp/webcmd.log" 2>&1 &
webcmd_pid=$!
daemon_shards def &
shard_pids="$shard_pids $!"
daemon_shards web -tags web &
shard_pids="$shard_pids $!"

others=$("$GO" list ./... | grep -vx "$DAEMON")
echo "==> $GO test ./... except $DAEMON (the daemon's tests run alongside, in $shards groups per build)"
# shellcheck disable=SC2086 # one package per word
"$GO" test $others
def=$?

wait "$webcmd_pid"
echo $? >"$tmp/webcmd.rc"
webcmd_pid=
wait $shard_pids
shard_pids=

failed=
[ "$def" -ne 0 ] && failed="$failed
FAIL: $GO test ./... except $DAEMON (exit $def)"

# show <name> <header>: print one background run's log, note it when it failed
show() {
	echo "==> $2 (ran alongside the above)"
	cat "$tmp/$1.log"
	rc=$(cat "$tmp/$1.rc" 2>/dev/null || echo 1)
	[ "$rc" -ne 0 ] && failed="$failed
FAIL: $2 (exit $rc)"
}

# show_shards <name> <listing flags>: every group of a build, in order
show_shards() {
	if [ -f "$tmp/$1.list.rc" ]; then
		echo "==> $GO test ${2:+$2 }-list '.*' $DAEMON (ran alongside the above)"
		cat "$tmp/$1.list.log"
		failed="$failed
FAIL: $GO test ${2:+$2 }-list '.*' $DAEMON: could not list the tests"
		return
	fi
	i=0
	while [ "$i" -lt "$shards" ]; do
		[ -f "$tmp/$1.$i.cmd" ] && show "$1.$i" "$(cat "$tmp/$1.$i.cmd")"
		i=$((i + 1))
	done
}

show_shards def ""
show webcmd "$GO test -tags web ./cmd/..."
show_shards web "-tags web"

[ -z "$failed" ] && exit 0
echo "$failed" >&2
exit 1
