#!/bin/sh
# make test's two go test runs at the same time: the default build and
# -tags web (internal/daemon, the slowest package, is in both, and each run
# is sequential, so the second CPU is otherwise idle).
#
# The default build's output streams as usual; the web one goes to a file
# and is printed after it, under a header, so the two never interleave (the
# CI step that lists skipped tests parses this output). Fails when either
# fails and says which.
#
#   scripts/gotest.sh [go]
set -u
GO=${1:-go}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pkgotest.XXXXXX") || exit 1
web_log="$tmp/web.log"
web_pid=
cleanup() {
	[ -n "$web_pid" ] && kill "$web_pid" 2>/dev/null
	rm -rf "$tmp"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

"$GO" test -tags web ./internal/daemon/ ./cmd/... >"$web_log" 2>&1 &
web_pid=$!

echo "==> $GO test ./..."
"$GO" test ./...
def=$?

wait "$web_pid"
web=$?
web_pid=

echo "==> $GO test -tags web ./internal/daemon/ ./cmd/... (ran alongside the above)"
cat "$web_log"

[ "$def" -eq 0 ] && [ "$web" -eq 0 ] && exit 0
[ "$def" -ne 0 ] && echo "FAIL: $GO test ./... (exit $def)" >&2
[ "$web" -ne 0 ] && echo "FAIL: $GO test -tags web ./internal/daemon/ ./cmd/... (exit $web)" >&2
exit 1
