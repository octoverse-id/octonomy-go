#!/bin/sh
# Fixture tests for scripts/compat-guard.sh.
#
# The guard is the release gate for a line whose releases cannot be recalled, and
# it is now the most logic-heavy file on the branch -- so it gets tests of its
# own. Without them CI only ever exercises the ordinary-PR path: every run on
# this branch has GITHUB_HEAD_REF=chore/..., so the entire release-PR block was
# unexecuted code until this file existed.
#
# Each case builds a throwaway tree (go.mod, version.go, CHANGELOG.md), runs the
# guard against a synthetic GitHub event environment, and asserts both the exit
# status and that the reason names the right thing. Asserting the exit status
# alone would pass a guard that fails for the wrong reason.
#
# Usage: scripts/compat-guard-test.sh   (or `make compat-guard-test`)
#
# POSIX sh plus mktemp, which is not in POSIX but is present everywhere this runs
# (GNU coreutils, BSD, busybox) and is the only safe way to get a temp directory.

set -eu

GUARD=$(cd "$(dirname "$0")" && pwd)/compat-guard.sh
test -x "$GUARD" || { echo "not executable: $GUARD"; exit 1; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/compat-guard-test-XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM

passed=0
failed=0

# fixture <module> <go-directive> <version|-> <changelog-version|-> [grouped]
# Writes a tree into $WORK/tree and echoes nothing. "-" omits the file.
fixture() {
	rm -rf "$WORK/tree"
	mkdir -p "$WORK/tree"
	printf 'module %s\n\ngo %s\n' "$1" "$2" > "$WORK/tree/go.mod"
	if [ "$3" != "-" ]; then
		if [ "${5:-}" = "grouped" ]; then
			printf 'package octonomy\n\nconst (\n\tVersion = "%s"\n)\n' "$3" > "$WORK/tree/version.go"
		else
			printf 'package octonomy\n\nconst Version = "%s"\n' "$3" > "$WORK/tree/version.go"
		fi
	fi
	if [ "$4" != "-" ]; then
		printf '# Changelog\n\n## [Unreleased]\n\n## [%s] - 2026-08-21\n\n[%s]: https://example.test\n' "$4" "$4" > "$WORK/tree/CHANGELOG.md"
	fi
}

# git_fixture <branch> turns the current fixture into a git repo checked out on
# <branch>, so the guard's no-event-context path (which asks git for the branch)
# can be exercised. A commit is required: `git rev-parse --abbrev-ref HEAD` has
# nothing to resolve in a repo with no commits.
git_fixture() {
	(
		cd "$WORK/tree" || exit 1
		git init -q .
		git symbolic-ref HEAD "refs/heads/$1"
		git add -A
		git -c user.email=test@example.test -c user.name=test commit -q -m fixture
	)
}

# check <description> <expected-rc> <expected-substring|-> <env>...
check() {
	desc=$1
	want_rc=$2
	want_text=$3
	shift 3

	out=$(cd "$WORK/tree" && env -i PATH="$PATH" HOME="${HOME:-/tmp}" "$@" "$GUARD" 2>&1) && rc=0 || rc=$?

	if [ "$rc" != "$want_rc" ]; then
		printf 'FAIL  %s\n      expected rc=%s, got rc=%s\n' "$desc" "$want_rc" "$rc"
		printf '%s\n' "$out" | sed 's/^/        /'
		failed=$((failed + 1))
		return 0
	fi
	if [ "$want_text" != "-" ]; then
		case "$out" in
		*"$want_text"*) ;;
		*)
			printf 'FAIL  %s\n      rc=%s as expected, but the output never says %s\n' "$desc" "$rc" "\"$want_text\""
			printf '%s\n' "$out" | sed 's/^/        /'
			failed=$((failed + 1))
			return 0
			;;
		esac
	fi
	printf 'ok    %s\n' "$desc"
	passed=$((passed + 1))
}

COMPAT=github.com/octoverse-id/octonomy-go
MODERN=github.com/octoverse-id/octonomy-go/v2

echo "--- ordinary branch and PR flows ---"
fixture "$COMPAT" 1.13 0.1.0 0.1.0
check "PR into the compat line" 0 "declares \`go 1.13\` as required" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=chore/4-x
check "push to the compat line" 0 "branch=support/go1.13" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/support/go1.13
check "no event context (local run)" 0 "declares \`go 1.13\` as required" \
	GITHUB_EVENT_NAME= GITHUB_REF=

fixture "$COMPAT" 1.24 0.1.0 0.1.0
check "BLOCK: go directive drifted off 1.13" 1 "must declare \`go 1.13\`" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=chore/4-x

fixture "$MODERN" 1.24 2.0.0-alpha.1 2.0.0-alpha.1
# Asserts the parsed context rather than only rc=0: a clean exit here also
# happens when the guard misreads go.mod and checks nothing.
check "PR into main, correct modern tree" 0 "module=$MODERN go=1.24" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=feature/x

fixture "$COMPAT" 1.13 0.1.0 0.1.0
check "WARN: main carrying the compat go.mod" 0 "expected a /vN-suffixed path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=feature/x

echo "--- release PRs: the gate that can still be acted on ---"
fixture "$COMPAT" 1.13 1.0.0 1.0.0
check "release/v1.0.0 into the compat line" 0 "base branch, version.go, module path, and CHANGELOG all agree" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0
check "BLOCK: v1 release PR retargeted at main" 1 "v1 releases are cut from" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v1.0.0
# Both of these assert the reason, not just rc=0: a bare exit-status check would
# also pass if the release block never ran, which is the exact bug being guarded
# against.
check "release/1.0.0 without the v prefix" 0 "release PR for v1.0.0" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/1.0.0
check "pull_request_target is treated the same" 0 "release PR for v1.0.0" \
	GITHUB_EVENT_NAME=pull_request_target GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0

fixture "$COMPAT" 1.13 0.1.0 0.1.0
check "BLOCK: release PR, version.go not bumped" 1 "version.go says 0.1.0" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0

fixture "$COMPAT" 1.13 1.0.0 0.1.0
check "BLOCK: release PR, CHANGELOG heading stale" 1 "latest CHANGELOG.md heading is [0.1.0]" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0

fixture "$COMPAT" 1.13 1.0.0 -
check "BLOCK: release PR deleted the CHANGELOG" 1 "there is no CHANGELOG.md" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0

fixture "$COMPAT" 1.13 - 1.0.0
check "BLOCK: release PR, version.go unreadable" 1 "declares no Version" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0

fixture "$COMPAT" 1.13 1.0.0 1.0.0
check "BLOCK: release branch names a bad version" 1 "does not name a valid version" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0
check "BLOCK: release branch names nothing" 1 "does not name a valid version" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/foo

fixture "$MODERN" 1.24 2.0.0-alpha.1 2.0.0-alpha.1
check "release/v2.0.0-alpha.1 into main" 0 "all agree" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v2.0.0-alpha.1
check "BLOCK: v2 release PR retargeted at the compat line" 1 "v2 releases are cut from" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v2.0.0-alpha.1

fixture "$COMPAT" 1.13 2.0.0 2.0.0
check "BLOCK: v2 release cut from the compat module path" 1 "must carry module path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v2.0.0

echo "--- same major, wrong module path ---"
# Majors matching is not enough: these all carry a v1-shaped major and a path no
# consumer of this line can require.
fixture example.test/wrong 1.13 1.0.0 1.0.0
check "BLOCK: release PR, foreign module path" 1 "must carry module path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0
check "BLOCK: tag on a foreign module path" 1 "needs module path" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.0

fixture "$COMPAT/v1" 1.13 1.0.0 1.0.0
check "BLOCK: release PR with an illegal /v1 suffix" 1 "must carry module path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.0
check "BLOCK: tag with an illegal /v1 suffix" 1 "needs module path" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.0

fixture github.com/octoverse-id/octonomy-go/v3 1.24 2.0.0 2.0.0
check "BLOCK: v2 release PR on a /v3 path" 1 "must carry module path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v2.0.0

echo "--- v0: no line publishes it, and /v0 is not a legal path ---"
fixture "$COMPAT" 1.13 0.2.0 0.2.0
check "BLOCK: v0 release PR" 1 "No line publishes v0" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v0.2.0
check "BLOCK: v0 tag" 1 "publishes a v0 version" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v0.2.0

fixture github.com/octoverse-id/octonomy-go/v0 1.24 0.2.0 0.2.0
# Go forbids a /v0 suffix outright, so the expected path for major 0 is the
# unsuffixed one -- this must be rejected on the path too, not accepted as "/v0
# matches major 0".
check "BLOCK: v0 release PR on an illegal /v0 path" 1 "must carry module path" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v0.2.0
check "BLOCK: v0 tag on an illegal /v0 path" 1 "needs module path" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v0.2.0

echo "--- missing or partial event context ---"
fixture "$COMPAT" 1.13 1.0.0 1.0.0
# A release PR with no base in context must still run every other release check
# and say plainly that the target-branch comparison was skipped, rather than
# quietly skipping the lot.
check "release PR with no base ref: says what it skipped" 0 "target-branch check is skipped" \
	GITHUB_EVENT_NAME=pull_request GITHUB_HEAD_REF=release/v1.0.0
fixture "$COMPAT" 1.13 0.1.0 0.1.0
check "release PR with no base ref still checks version.go" 1 "version.go says 0.1.0" \
	GITHUB_EVENT_NAME=pull_request GITHUB_HEAD_REF=release/v1.0.0

echo "--- compat line publishes v1.0.x only ---"
fixture "$COMPAT" 1.13 1.1.0 1.1.0
check "BLOCK: v1 minor release PR" 1 "publishes v1.0.x only" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.1.0
check "BLOCK: v1 minor tag" 1 "publishes v1.0.x only" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.1.0

fixture "$COMPAT" 1.13 1.0.7 1.0.7
check "a v1.0.x patch release is fine" 0 "all agree" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=support/go1.13 GITHUB_HEAD_REF=release/v1.0.7

fixture "$MODERN" 1.24 2.1.0 2.1.0
check "a v2 minor is fine (modern line takes them)" 0 "all agree" \
	GITHUB_EVENT_NAME=pull_request GITHUB_BASE_REF=main GITHUB_HEAD_REF=release/v2.1.0

echo "--- no event context: the checked-out branch is the HEAD, not the base ---"
# Regression: assigning the checked-out branch to BOTH made a local run on a
# release branch report that the release "targets release/v1.0.1, but v1 releases
# are cut from support/go1.13" -- so runbook step 5 could not pass locally.
fixture "$COMPAT" 1.13 1.0.1 1.0.1
git_fixture release/v1.0.1
check "local run on a release branch: no bogus base error" 0 "target-branch check is skipped" \
	GITHUB_EVENT_NAME= GITHUB_REF= GITHUB_BASE_REF= GITHUB_HEAD_REF=
fixture "$COMPAT" 1.13 0.1.0 0.1.0
git_fixture release/v1.0.1
check "local run on a release branch still checks version.go" 1 "version.go says 0.1.0" \
	GITHUB_EVENT_NAME= GITHUB_REF= GITHUB_BASE_REF= GITHUB_HEAD_REF=
fixture "$COMPAT" 1.24 0.1.0 0.1.0
git_fixture support/go1.13
check "local run on the line still sees the drifted directive" 1 "must declare \`go 1.13\`" \
	GITHUB_EVENT_NAME= GITHUB_REF= GITHUB_BASE_REF= GITHUB_HEAD_REF=

echo "--- tag pushes: detection after the ref exists ---"
fixture "$COMPAT" 1.13 1.0.0 1.0.0
check "tag v1.0.0 matching version.go" 0 "matches version.go" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.0
check "BLOCK: tag v1.0.1 against version.go 1.0.0" 1 "does not match version.go" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.1
check "BLOCK: tag v2.0.0 on the compat module path" 1 "needs module path" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v2.0.0
check "non-version tag is skipped" 0 "is not a version tag" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/release-candidate

for bad in v1.2.3foo v1.02.3 v1.2.3- v1.2.3.4 v1.2.3+build.7 v1.0 v1.0.0-01 v1.0.0-alpha.01; do
	check "BLOCK: malformed tag $bad" 1 "not a valid module version" \
		GITHUB_EVENT_NAME=push GITHUB_REF="refs/tags/$bad"
done

echo "--- parsing edge cases ---"
fixture github.com/octoverse-id/octonomy-go/v100 1.24 100.0.0 100.0.0
check "multi-digit module major (/v100)" 0 "matches version.go" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v100.0.0

fixture "$COMPAT" 1.13 1.0.0 1.0.0 grouped
check "grouped const ( Version = ... ) is parsed" 0 "matches version.go" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.0

rm -rf "$WORK/tree" && mkdir -p "$WORK/tree"
printf '  module %s // trailing comment\n\n  go 1.13 // the floor\ntoolchain go1.25.4\n' "$COMPAT" > "$WORK/tree/go.mod"
printf 'package octonomy\n\nconst Version = "1.0.0" // release\n' > "$WORK/tree/version.go"
printf '# Changelog\n\n## [1.0.0] - 2026-08-21\n' > "$WORK/tree/CHANGELOG.md"
check "leading whitespace, comments, toolchain line" 0 "matches version.go" \
	GITHUB_EVENT_NAME=push GITHUB_REF=refs/tags/v1.0.0

rm -rf "$WORK/tree" && mkdir -p "$WORK/tree"
printf 'package octonomy\n' > "$WORK/tree/version.go"
check "BLOCK: no go.mod at all" 1 "no go.mod" GITHUB_EVENT_NAME=push GITHUB_REF=refs/heads/support/go1.13

echo "--- is_semver grammar, exercised directly ---"
# Sourced from the guard rather than re-declared, so this cannot drift from the
# implementation. It also reaches the multi-line case, which no git ref can carry
# but version.go could.
_NL='
'
eval "$(sed -n '/^is_semver()/,/^}/p' "$GUARD")"

semver_case() {
	want=$1
	value=$2
	if is_semver "$value"; then got=accept; else got=reject; fi
	if [ "$got" = "$want" ]; then
		printf 'ok    is_semver %-18s -> %s
' "$(printf '%s' "$value" | tr '\n' '~')" "$got"
		passed=$((passed + 1))
	else
		printf 'FAIL  is_semver %-18s -> %s, wanted %s
' "$(printf '%s' "$value" | tr '\n' '~')" "$got" "$want"
		failed=$((failed + 1))
	fi
}

for good in 1.0.0 0.0.0 10.20.30 1.0.0-0 1.0.0-alpha 1.0.0-alpha.1 1.0.0-alpha.beta 2.0.0-rc.1 1.0.0-0a 100.0.0; do
	semver_case accept "$good"
done
for bad in 1.0.0-01 1.0.0-alpha.01 01.0.0 1.02.3 1.0.0- 1.0.0.1 1.0 1 1.a.3 1.0.0+meta v1.0.0 "" " 1.0.0" "1.0.0 "; do
	semver_case reject "$bad"
done
semver_case reject "1.0.0${_NL}junk"

printf '\n%s passed, %s failed\n' "$passed" "$failed"
[ "$failed" = "0" ] || exit 1
