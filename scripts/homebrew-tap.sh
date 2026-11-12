#!/bin/sh
# homebrew-tap.sh — put the cask GoReleaser generated (dist/homebrew/Casks/pekoe.rb)
# into the Homebrew tap, so that `brew install sakamosu/tap/pekoe` gets this release.
# release.yml runs it after publishing the draft release: written any earlier,
# the tap would point at archives nobody can download yet (or ever, if attest fails).
#
#   scripts/homebrew-tap.sh <cask.rb> <tag>
#
#   HOMEBREW_TAP_TOKEN  a fine-grained PAT that can write the tap's contents
#   HOMEBREW_TAP        the tap repository (default sakamosu/homebrew-tap)
#
# It writes nothing and exits 0 (the release goes on) with a ::warning:: when
#   - HOMEBREW_TAP_TOKEN is empty (the secret isn't set up yet, or a fork)
#   - the tap answers HTTP 404 (it doesn't exist yet, or the token can't read it)
# Any other failure (no write permission, the API is down) exits 1. Either way the
# cask goes into the step summary, to be put into the tap by hand: by then the
# release is published, and re-running the workflow would try to make it again.
set -eu

if [ $# -ne 2 ]; then
	echo "usage: $0 <cask.rb> <tag>" >&2
	exit 2
fi
cask=$1
ver=${2#v}
tap=${HOMEBREW_TAP:-sakamosu/homebrew-tap}
dest="Casks/$(basename "$cask")"

if [ ! -f "$cask" ]; then
	echo "::error::homebrew-tap: $cask not found (see homebrew_casks in .goreleaser.yaml)" >&2
	exit 1
fi

# leave the cask in the step summary when it wasn't written
leave_in_summary() {
	[ -n "${GITHUB_STEP_SUMMARY:-}" ] || return 0
	{
		echo "### Homebrew: $dest was not written to $tap"
		echo
		echo "$1"
		echo
		echo "To put it there by hand, this is $dest:"
		echo
		echo '```ruby'
		cat "$cask"
		echo '```'
	} >>"$GITHUB_STEP_SUMMARY"
}

skip() {
	echo "::warning::homebrew-tap: skipped: $1"
	leave_in_summary "Skipped: $1"
	exit 0
}

fail() {
	cat "$err" >&2
	leave_in_summary "Failed: $1: $(cat "$err")"
	echo "::error::homebrew-tap: $1" >&2
	exit 1
}

if [ -z "${HOMEBREW_TAP_TOKEN:-}" ]; then
	skip "HOMEBREW_TAP_TOKEN is not set (CONTRIBUTING.md: Homebrew tap)"
fi
GH_TOKEN=$HOMEBREW_TAP_TOKEN
export GH_TOKEN
unset GITHUB_TOKEN

err=$(mktemp "${TMPDIR:-/tmp}/homebrew-tap.XXXXXX")
trap 'rm -f "$err"' EXIT

if ! gh api "repos/$tap" --silent 2>"$err"; then
	if grep -q 'HTTP 404' "$err"; then
		skip "$tap not found (it doesn't exist yet, or HOMEBREW_TAP_TOKEN can't read it)"
	fi
	fail "could not read $tap"
fi

# replace the file if it is there (the API wants its blob sha), create it if not
sha=""
if ! sha=$(gh api "repos/$tap/contents/$dest" --jq .sha 2>"$err"); then
	grep -q 'HTTP 404' "$err" || fail "could not read $dest in $tap"
	sha=""
fi

content=$(base64 <"$cask" | tr -d '\n')
if [ -n "$sha" ]; then
	set -- -f "sha=$sha"
else
	set --
fi
gh api -X PUT "repos/$tap/contents/$dest" -f "message=pekoe $ver" -f "content=$content" "$@" --silent 2>"$err" ||
	fail "could not write $dest to $tap (HOMEBREW_TAP_TOKEN needs Contents: Read and write on it)"

echo "homebrew-tap: wrote $dest to $tap (pekoe $ver)"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	echo "Homebrew: $dest in $tap is now pekoe $ver" >>"$GITHUB_STEP_SUMMARY"
fi
