#!/bin/sh
# Release-line guard: assert go.mod still matches the line it sits on.
#
# Two halves, deliberately different severities.
#
# BLOCKING -- the `go` directive on the compat line. It is the one assertion with
# no toolchain backstop. A v1.x tag cut from support/go1.13 after the directive
# drifted to, say, `go 1.24` carries the SAME module path, so the proxy and the
# go command accept it, a Go 1.13 consumer resolves it inside their ordinary
# version range, and it does not compile for them. `retract` cannot reach them
# either: that directive shipped in Go 1.16, so their toolchain ignores it. The
# tag is permanent. This check is the only thing standing there.
#
# ADVISORY -- module path / branch consistency on a BRANCH or PR. Go rejects a
# path mismatch itself:
#
#   go: github.com/octoverse-id/octonomy-go@v1.0.0: invalid version:
#       go.mod has post-v1 module path ".../octonomy-go/v2" at revision v1.0.0
#
# so on a branch these are early signals, not gates. They warn and do not fail.
#
# BLOCKING AT TAG TIME -- the same checks stop being advisory the moment a tag is
# involved, because that is where permanence attaches. Go rejecting a bad tag
# afterwards does not help: the tag is already public and proxy-cached, and it
# cannot be withdrawn for a Go 1.13 consumer. So on a tag push this script
# asserts the tag against the module path AND against version.go, which is the
# gap that let `refs/tags/v1.0.0` pass on a tree whose version.go read 0.1.0.
#
# Usage:
#   scripts/compat-guard.sh          # infers the line from git or $GITHUB_*
#
# POSIX sh, no dependencies beyond git -- it runs on the go1.13 era as happily
# as on a current runner.

set -eu

COMPAT_MODULE="github.com/octoverse-id/octonomy-go"
COMPAT_BRANCH="support/go1.13"
COMPAT_GO="1.13"
MODERN_BRANCH="main"

failed=0

note() {
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then printf '::notice::%s\n' "$1"; else printf 'note: %s\n' "$1"; fi
}
warn() {
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then printf '::warning::%s\n' "$1"; else printf 'ADVISORY: %s\n' "$1"; fi
}
fail() {
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then printf '::error::%s\n' "$1"; else printf 'BLOCKING: %s\n' "$1"; fi
	failed=1
}

# --- Read the tree -------------------------------------------------------------

test -f go.mod || { fail "no go.mod in $(pwd)"; exit 1; }

module=$(sed -n 's/^module[[:space:]]\{1,\}\([^[:space:]]*\).*/\1/p' go.mod | head -1)
go_directive=$(sed -n 's/^go[[:space:]]\{1,\}\([0-9][0-9.]*\).*/\1/p' go.mod | head -1)
version=$(sed -n 's/^const Version = "\([^"]*\)".*/\1/p' version.go | head -1)

test -n "$module" || { fail "could not read the module path from go.mod"; exit 1; }
test -n "$go_directive" || { fail "could not read the go directive from go.mod"; exit 1; }

# The module path suffix is what makes the two lines different modules to Go.
case "$module" in
	*/v[0-9] | */v[0-9][0-9]) path_major=${module##*/v} ;;
	*) path_major=1 ;;
esac

# --- Work out which line this run belongs to -----------------------------------
#
# A PR is judged by its BASE branch, which is what puts this check on the
# release PR: a release/vX.Y.Z PR targets the line it will be tagged on. A
# tag-push-only check fires after the tag is already proxy-resolvable -- it
# would report a mistake nobody can undo.

tag=""
branch=""
case "${GITHUB_EVENT_NAME:-}" in
	pull_request | pull_request_target) branch="${GITHUB_BASE_REF:-}" ;;
	*)
		case "${GITHUB_REF:-}" in
			refs/tags/*) tag="${GITHUB_REF#refs/tags/}" ;;
			refs/heads/*) branch="${GITHUB_REF#refs/heads/}" ;;
			*) branch=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "") ;;
		esac
		;;
esac

printf 'compat-guard: module=%s go=%s version=%s branch=%s tag=%s\n' \
	"$module" "$go_directive" "${version:-?}" "${branch:-none}" "${tag:-none}"

# A tag push is the last gate before something permanent. Everything checked
# under `is_tag` below is blocking for that reason, including checks that only
# warn on a branch.
is_tag=0
[ -n "$tag" ] && is_tag=1

# --- BLOCKING: the go directive on the compat line -----------------------------
#
# Triggered by either independent signal -- the module path in the tree, or the
# branch context. Requiring both would let a change that rewrote go.mod's module
# line slip past on a technicality.

on_compat=0
[ "$module" = "$COMPAT_MODULE" ] && on_compat=1
[ "$branch" = "$COMPAT_BRANCH" ] && on_compat=1
case "$tag" in v1.*) on_compat=1 ;; esac

if [ "$on_compat" = "1" ]; then
	if [ "$go_directive" != "$COMPAT_GO" ]; then
		fail "the compat line must declare \`go $COMPAT_GO\` in go.mod, found \`go $go_directive\`. A v1.x release built on this go.mod keeps the same module path, so Go accepts it and a Go 1.13 consumer cannot compile it -- and \`retract\` (Go 1.16+) cannot reach that consumer. Fix go.mod, do not tag."
	else
		note "compat line: go.mod declares \`go $COMPAT_GO\` as required"
	fi
fi

# --- Path / branch consistency: advisory on a branch, blocking on a tag --------

if [ "$branch" = "$COMPAT_BRANCH" ] && [ "$module" != "$COMPAT_MODULE" ]; then
	warn "branch $COMPAT_BRANCH carries module path \`$module\`, expected the unsuffixed \`$COMPAT_MODULE\`. Go rejects the mismatch at resolve time, so on a branch this is a signal rather than the gate."
fi

# The converse direction, which was previously unchecked: the modern line must
# NOT be carrying the compat module or the 1.13 floor. This is how a cherry-pick
# from this branch to main -- go.mod included -- would show up.
if [ "$branch" = "$MODERN_BRANCH" ]; then
	if [ "$module" = "$COMPAT_MODULE" ]; then
		warn "branch $MODERN_BRANCH carries the unsuffixed compat module \`$module\`, expected a /vN-suffixed path. A compat-line go.mod reached the modern line, most likely via a cherry-pick that included it."
	fi
	if [ "$go_directive" = "$COMPAT_GO" ]; then
		warn "branch $MODERN_BRANCH declares \`go $COMPAT_GO\`, the compat floor. The modern line's floor is far higher; check what cherry-picked go.mod landed here."
	fi
fi

if [ "$is_tag" = "1" ]; then
	case "$tag" in
		# Full SemVer core, optional prerelease/build: v1.0.0, v2.0.0-alpha.1.
		v[0-9]*.[0-9]*.[0-9]*)
			tag_version=${tag#v}
			tag_major=$(printf '%s' "$tag_version" | sed 's/[.-].*//')

			if [ "$tag_major" != "$path_major" ]; then
				fail "tag $tag targets major v$tag_major but go.mod's path implies major v$path_major (\`$module\`). Go refuses to resolve this combination, and the tag cannot be moved once pushed -- delete it before it is fetched, then publish a corrected version."
			fi

			# The check that was missing: the tag and version.go must agree.
			# version.go is what the client reports as its User-Agent forever, and
			# `make version-check` has already tied it to the CHANGELOG heading --
			# so agreeing here makes tag == version.go == CHANGELOG.
			if [ -z "$version" ]; then
				fail "tag $tag pushed but version.go declares no Version; nothing ties this tag to the code it publishes."
			elif [ "$version" != "$tag_version" ]; then
				fail "tag $tag does not match version.go ($version). The published module would report a version it is not. Fix version.go in a release/$tag PR (docs/release.md step 3) before tagging."
			else
				note "tag $tag matches version.go ($version)"
			fi
			;;
		v*)
			fail "tag $tag looks like a version tag but is not valid SemVer (expected vMAJOR.MINOR.PATCH with an optional -prerelease). Go resolves module versions from tags; a malformed one publishes nothing and cannot be reused."
			;;
		*) note "tag $tag is not a version tag; skipping the tag checks" ;;
	esac
fi

if [ "$on_compat" = "1" ] && [ -n "$version" ]; then
	case "$version" in
		1.*) : ;;
		# 0.x is tolerated OUTSIDE a tag push: nothing has ever been released, and
		# the release PR (#29) is what sets 1.0.0 -- AGENTS.md keeps version bumps
		# out of feature and chore PRs. On a tag it is not tolerated; the tag
		# comparison above is blocking and would catch v1.0.0 against 0.x.
		0.*) note "version.go is $version; the release PR sets 1.0.0 on this line" ;;
		*) warn "version.go is $version on the compat line, which publishes v1.x only" ;;
	esac
fi

if [ "$failed" != "0" ]; then
	printf 'compat-guard: FAILED\n'
	exit 1
fi
printf 'compat-guard: OK\n'
