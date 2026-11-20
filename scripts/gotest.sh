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
#   PEKOE_TEST_SHARDS=n   groups per build, a whole number of at least 1
#                         (default: a third of the CPUs, at least 2)
#
# Not more by default: other make test runs (other sessions' pekoe land)
# often share the machine, and the tests that wait for something (up to a
# deadline) only get slower with more test processes than that.
#
# Stopped (Ctrl-C, TERM), it stops every go test so that each removes its
# work directory ($TMPDIR/go-build…, the test binaries) and the binaries
# their temporary directories (internal/testenv), then prints what every
# background run had written, with the tests that were still running.
set -u
GO=${1:-go}
DAEMON=pekoe/internal/daemon

if [ -n "${PEKOE_TEST_SHARDS:-}" ]; then
	case $PEKOE_TEST_SHARDS in
	*[!0-9]* | 0*)
		echo "gotest.sh: PEKOE_TEST_SHARDS=$PEKOE_TEST_SHARDS: want a whole number of at least 1" >&2
		exit 2
		;;
	esac
	shards=$PEKOE_TEST_SHARDS
else
	ncpu=$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu 2>/dev/null || echo 2)
	shards=$((ncpu / 3))
	[ "$shards" -ge 2 ] 2>/dev/null || shards=2
fi

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pkgotest.XXXXXX") || exit 1

# stop <go test pid>…: stop go test runs of one package each. With its test
# binary running, TERM the binary: go test then prints its output (the
# binary says which tests were running) and removes its work directory, as
# when a test fails. With none (go test is compiling), stop_all. Not INT
# while a binary runs: go test would wait for the binary to finish and drop
# its output; nor TERM: go test would exit at once and leave its work
# directory ($TMPDIR/go-build…, the binary in it) behind.
stop() {
	for p in "$@"; do
		pkill -TERM -P "$p" '\.test$' 2>/dev/null || stop_all "$p"
	done
}

# stop_all <go test pid>…: stop go test runs of several packages, which would
# go on to the next package after their binary is stopped: INT go test (it
# starts nothing more, the output of the packages not printed yet included,
# and removes its work directory), TERM what it runs (the binary, or the
# compiler).
stop_all() {
	for p in "$@"; do
		kill -INT "$p" 2>/dev/null
		pkill -TERM -P "$p" 2>/dev/null
	done
}

note="ran alongside the above"
failed=

# show <name> <header>: print one background run's log, note it when it failed
show() {
	echo "==> $2 ($note)"
	cat "$tmp/$1.log" 2>/dev/null
	rc=$(cat "$tmp/$1.rc" 2>/dev/null || echo 1)
	[ "$rc" -ne 0 ] && failed="$failed
FAIL: $2 (exit $rc)"
}

# show_shards <name> <listing flags>: every group of a build, in order (the
# listing when it failed, or was stopped before there were groups)
show_shards() {
	if [ -f "$tmp/$1.list.rc" ] || ! [ -f "$tmp/$1.0.cmd" ]; then
		[ -f "$tmp/$1.list.log" ] || return
		echo "==> $GO test ${2:+$2 }-list '.*' $DAEMON ($note)"
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

show_all() {
	show_shards def ""
	show webcmd "$GO test -tags web ./cmd/..."
	show_shards web "-tags web"
}

others_pid=
webcmd_pid=
shard_pids=
stopped=
cleanup() {
	# shellcheck disable=SC2086 # one pid per word
	stop_all $others_pid
	# ./cmd/... is one package (cmd/pekoe); were there more, stop_all
	# shellcheck disable=SC2086
	stop $webcmd_pid
	# each daemon_shards subshell stops its own go tests (its TERM trap)
	# shellcheck disable=SC2086
	[ -n "$shard_pids" ] && kill $shard_pids 2>/dev/null
	# every go test has cleaned up and written its log
	wait
	if [ -n "$stopped" ]; then
		note=stopped
		show_all
	fi
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'stopped=1; exit 130' INT TERM

# daemon_shards <name> [go test flags…]: list the daemon tests of a build and
# start one background go test per group. Writes $tmp/<name>.<i>.log, the
# exit code to $tmp/<name>.<i>.rc and the header to $tmp/<name>.<i>.cmd;
# when listing fails, its output goes to $tmp/<name>.list.log instead.
daemon_shards() {
	name=$1
	shift
	spids=
	# cleanup's kill stops this subshell; take its go tests along ($! too:
	# the one just started, when the trap comes before it is in $spids; +u
	# for when none has started yet).
	# Every go test runs in the background, waited for: the shell runs the
	# trap only once a command in the foreground has returned.
	# shellcheck disable=SC2086 # one pid per word
	trap 'set +u; stop $spids $!; wait; exit 130' TERM
	"$GO" test "$@" -list '.*' "$DAEMON" >"$tmp/$name.list.log" 2>&1 &
	spids=$!
	if ! wait "$spids"; then
		echo 1 >"$tmp/$name.list.rc"
		return
	fi
	spids=
	grep -E '^(Test|Example|Fuzz)' "$tmp/$name.list.log" | sort >"$tmp/$name.names"
	total=$(wc -l <"$tmp/$name.names" | tr -d ' ')
	if [ "$total" -eq 0 ]; then
		# no names read from the listing: fail rather than run nothing
		echo "no tests in the listing" >>"$tmp/$name.list.log"
		echo 1 >"$tmp/$name.list.rc"
		return
	fi
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

# The go test runs go in the background, each in a process group of its own
# (set -m) so that Ctrl-C (SIGINT to the terminal's process group) reaches
# this script alone, which stops them (cleanup): go test interrupted by the
# terminal drops the output it has not printed yet. Without a terminal (CI)
# there is no job control, and no Ctrl-C either. Off again for what runs in
# the foreground, which would get the terminal.
set -m 2>/dev/null
# the web run's own packages besides the daemon
"$GO" test -tags web ./cmd/... >"$tmp/webcmd.log" 2>&1 &
webcmd_pid=$!
daemon_shards def &
shard_pids="$shard_pids $!"
daemon_shards web -tags web &
shard_pids="$shard_pids $!"
set +m

others=$("$GO" list ./... | grep -vx "$DAEMON")
echo "==> $GO test ./... except $DAEMON (the daemon's tests run alongside, in $shards groups per build)"
# in the background too, its output streaming as usual: stopping this
# script does not wait for it (see daemon_shards)
set -m 2>/dev/null
# shellcheck disable=SC2086 # one package per word
"$GO" test $others &
others_pid=$!
set +m
wait "$others_pid"
def=$?
others_pid=

wait "$webcmd_pid"
echo $? >"$tmp/webcmd.rc"
webcmd_pid=
# shellcheck disable=SC2086
wait $shard_pids
shard_pids=

[ "$def" -ne 0 ] && failed="$failed
FAIL: $GO test ./... except $DAEMON (exit $def)"

show_all

[ -z "$failed" ] && exit 0
echo "$failed" >&2
exit 1
