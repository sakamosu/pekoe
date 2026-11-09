#!/usr/bin/env bash
# third-party-licenses.sh — the copyright notices and license texts of what a
# release binary links statically: the Go modules it depends on and Go itself
# (the standard library). MIT / BSD / ISC / Apache-2.0 ask for them to ship
# with the binary, so .goreleaser.yaml puts the output in each archive as
# THIRD_PARTY_LICENSES.
#
#   scripts/third-party-licenses.sh ""  > THIRD_PARTY_LICENSES   # default build
#   scripts/third-party-licenses.sh web > THIRD_PARTY_LICENSES   # -tags web
#   scripts/third-party-licenses.sh web out/THIRD_PARTY_LICENSES # write a file
#
# The modules come from `go list -deps` of ./cmd/pekoe with the given build
# tags, for every OS/arch the release builds (a dependency can be OS-specific).
# A module without a license file (LICENSE, LICENSE.txt, LICENSE.md, COPYING,
# NOTICE, …) is an error: look at it and decide by hand.
set -euo pipefail

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	echo "usage: $0 <tags> [<output>]   (tags: \"\" for the default build, web for -tags web)" >&2
	exit 2
fi
tags=$1
out=${2:-}

repo=$(cd "$(dirname "$0")/.." && pwd)
cd "$repo"

# the platforms of .goreleaser.yaml (builds: goos × goarch)
platforms="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64"

# "<path> <version> <dir>" of each module (a replace points at its own dir)
mods=$(
	for p in $platforms; do
		CGO_ENABLED=0 GOOS=${p%/*} GOARCH=${p#*/} go list -tags "$tags" -deps \
			-f '{{with .Module}}{{if not .Main}}{{with .Replace}}{{.Path}} {{.Version}} {{.Dir}}{{else}}{{.Path}} {{.Version}} {{.Dir}}{{end}}{{end}}{{end}}' \
			./cmd/pekoe
	done | sort -u
)

license_files() { # the license files in dir $1, one name per line
	find "$1" -maxdepth 1 -type f -exec basename {} \; | sort | grep -E -i '^(licen[cs]e|copying|notice)([.-].*)?$' | grep -v -i -E '\.(go|sh)$' || true
}

gen() {
	local goroot golicense=
	goroot=$(go env GOROOT)
	# Homebrew's Go keeps it one level up, outside GOROOT (libexec)
	for f in "$goroot/LICENSE" "$goroot/../LICENSE"; do
		if [ -f "$f" ]; then
			golicense=$f
			break
		fi
	done
	if [ -z "$golicense" ]; then
		echo "third-party-licenses: Go's LICENSE not found in $goroot" >&2
		return 1
	fi
	echo "Third-party software in this build of pekoe (build tags: ${tags:-none})."
	echo "pekoe itself is under the MIT License (LICENSE)."
	echo
	echo "================================================================================"
	echo "Go (standard library and runtime) $(go env GOVERSION)"
	echo "================================================================================"
	echo
	cat "$golicense"
	if [ -f "$goroot/PATENTS" ]; then
		echo
		cat "$goroot/PATENTS"
	fi

	local missing=0 path ver dir f files
	while read -r path ver dir; do
		[ -n "$path" ] || continue
		if [ -z "$dir" ] || [ ! -d "$dir" ]; then
			echo "third-party-licenses: $path $ver: module directory not found (run go mod download)" >&2
			missing=1
			continue
		fi
		files=$(license_files "$dir")
		if [ -z "$files" ]; then
			echo "third-party-licenses: $path $ver: no license file in $dir" >&2
			missing=1
			continue
		fi
		echo
		echo "================================================================================"
		echo "$path $ver"
		echo "================================================================================"
		while read -r f; do
			echo
			echo "--- $f"
			echo
			cat "$dir/$f"
		done <<<"$files"
	done <<<"$mods"
	return $missing
}

if [ -z "$out" ]; then
	gen
	exit
fi
# write it whole or not at all
mkdir -p "$(dirname "$out")"
tmp=$(mktemp "$out.XXXXXX")
trap 'rm -f "$tmp"' EXIT
gen >"$tmp"
mv "$tmp" "$out"
trap - EXIT
