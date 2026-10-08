#!/bin/sh
# The contract gate's pinned identity check (#98).
#
# tools/contractdrift on this line is main's gate, ported. Six of its files never
# name the SDK, so they can be main's byte for byte -- and this makes sure they
# are, at the commit tools/contractdrift/main.pin names. The rest differ by
# design, and `report` prints how, for a human to read before a release.
#
#   scripts/contract-identity.sh check    fail on any divergence (CI, release-check)
#   scripts/contract-identity.sh report   print each advisory file's diff; exit 0
#
# What `check` refuses, each with a message naming the file:
#
#   - a pin file this script cannot read exactly: one `pin` line of forty hex
#     digits, `identical` / `advisory` / `own` lines naming plain file names,
#     nothing listed twice, and nothing else;
#   - a pin that is not a commit on main. A pin into this branch's own history
#     would compare the copies with themselves and pass forever;
#   - an `identical` file that differs from main's at the pin by a single byte;
#   - a Go file main has at the pin that imports no SDK package, classified as
#     anything but `identical`. Which files must match is DERIVED, not read off
#     the pin file: otherwise turning `identical checks.go` into `advisory
#     checks.go` would exempt checks.go from the comparison in one word;
#   - a file in this directory, or in main's at the pin, that the pin file does
#     not classify -- a new file on either side has to be decided, not inherited
#     by default -- and a classification the trees contradict (an `own` file
#     main has, an `identical` or `advisory` one it lacks).
#
# THE PIN IS FETCHED, NEVER MAIN'S TIP. Only the pinned commit's content is
# compared, so a merge on main cannot turn an unchanged commit here red. Main's
# branch is consulted for one thing, ancestry, and that is monotonic: once the pin
# is on main it stays there. Both are read locally first and fetched from the
# remote only when missing, so a clone that already has them runs offline.
#
# Environment:
#   CONTRACT_IDENTITY_REMOTE   remote to fetch from        (default: origin)
#   CONTRACT_IDENTITY_MAIN     main's branch on that remote (default: main)
#
# Exit codes: 0 clean, 1 divergence found, 2 the check could not be made. As
# with tools/contractdrift, the third is separate because a check that cannot
# read its inputs must never be mistaken for one that found nothing.
#
# POSIX sh, like the other scripts here.

set -eu

REMOTE="${CONTRACT_IDENTITY_REMOTE:-origin}"
MAIN="${CONTRACT_IDENTITY_MAIN:-main}"
DIR=tools/contractdrift
PINFILE="$DIR/main.pin"

cannot() {
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		printf '::error::contract-identity: %s\n' "$*" >&2
	else
		printf 'contract-identity: ERROR: %s\n' "$*" >&2
	fi
	exit 2
}

mode="${1:-check}"
case "$mode" in
check | report) ;;
*) cannot "usage: scripts/contract-identity.sh [check|report]" ;;
esac

top=$(git rev-parse --show-toplevel 2>/dev/null) || cannot "not inside a git work tree"
cd "$top"
test -f "$PINFILE" || cannot "$PINFILE does not exist"

WORK=$(mktemp -d "${TMPDIR:-/tmp}/contract-identity-XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM

# --- the pin file -----------------------------------------------------------------
#
# Read strictly. A line this does not understand is an error rather than a skip:
# a misspelled `identicl checks.go` that read as a comment would leave checks.go
# unclassified -- caught below -- but `identical  checks.go` with a stray tab, read
# loosely, could classify a file nobody meant to.
pin=""
: >"$WORK/classes"
# The kind a file is classified as, or nothing. Compared as a string, not a
# pattern: a `.` in a file name is not a wildcard here.
class_of() { awk -v n="$1" '$2 == n { print $1 }' "$WORK/classes"; }

# Whether a Go file imports an SDK package. An import is the quoted path on a
# top-level `import` line, or on a line of a top-level `import ( ... )` block --
# where gofmt puts every import, at column 0. A line anywhere else that starts with
# the quoted path -- inside a raw string, a block comment, a table of fixtures -- is
# NOT one, so it cannot make a file look SDK-bound and exempt it from the floor
# below. Lexical, not a parser: an `import (` at column 0 inside a raw string or
# block comment would still open a block, and nothing in main's files has one.
imports_sdk() {
	awk '
		/^import[ \t]*\($/ { inblock = 1; next }
		inblock && /^\)/ { inblock = 0; next }
		inblock && /^[ \t]*([A-Za-z_.][A-Za-z0-9_]*[ \t]+)?"github\.com\/octoverse-id\/octonomy-go(\/v[0-9]+)?"/ { found = 1 }
		/^import[ \t]+([A-Za-z_.][A-Za-z0-9_]*[ \t]+)?"github\.com\/octoverse-id\/octonomy-go(\/v[0-9]+)?"/ { found = 1 }
		END { exit !found }
	' "$1"
}
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
	lineno=$((lineno + 1))
	case "$line" in
	'' | '#'*) continue ;;
	esac
	kind=${line%% *}
	name=${line#* }
	if [ "$kind" = "$line" ] || [ -z "$name" ]; then
		cannot "$PINFILE:$lineno: expected '<kind> <value>', got: $line"
	fi
	case "$kind" in
	pin)
		[ -z "$pin" ] || cannot "$PINFILE:$lineno: a second pin line"
		printf '%s\n' "$name" | grep -Eq '^[0-9a-f]{40}$' ||
			cannot "$PINFILE:$lineno: the pin must be a full forty-hex-digit commit id, got: $name"
		pin=$name
		;;
	identical | advisory | own)
		printf '%s\n' "$name" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$' ||
			cannot "$PINFILE:$lineno: '$name' is not a plain file name in $DIR"
		if [ -n "$(class_of "$name")" ]; then
			cannot "$PINFILE:$lineno: $name is classified twice"
		fi
		printf '%s %s\n' "$kind" "$name" >>"$WORK/classes"
		;;
	*) cannot "$PINFILE:$lineno: unknown kind '$kind' (expected pin, identical, advisory or own)" ;;
	esac
done <"$PINFILE"
[ -n "$pin" ] || cannot "$PINFILE has no pin line"
grep -q '^own main.pin$' "$WORK/classes" || cannot "$PINFILE must classify itself as 'own main.pin'"

# --- the pin, and main's ancestry ----------------------------------------------------
if [ "$(git rev-parse --is-shallow-repository)" = "true" ]; then
	cannot "this clone is shallow, so whether the pin is on $REMOTE/$MAIN cannot be decided -- fetch full history (actions/checkout: fetch-depth: 0)"
fi
if ! git cat-file -e "$pin^{commit}" 2>/dev/null; then
	git fetch --no-tags --quiet "$REMOTE" "$pin" 2>/dev/null ||
		cannot "the pinned commit $pin is not in this clone and could not be fetched from $REMOTE"
	git cat-file -e "$pin^{commit}" 2>/dev/null || cannot "fetched $pin from $REMOTE and it is not a commit"
fi
mainref="refs/remotes/$REMOTE/$MAIN"
if ! git rev-parse -q --verify "$mainref^{commit}" >/dev/null || ! git merge-base --is-ancestor "$pin" "$mainref"; then
	git fetch --no-tags --quiet "$REMOTE" "+refs/heads/$MAIN:$mainref" ||
		cannot "could not fetch $MAIN from $REMOTE to decide whether the pin is on it"
fi

# Whether the pin is one of main's commits. `check` reports it as a finding; `report`
# refuses to print at all without it, since every diff it prints is headed "main at
# the pin" -- and a pin off main would put another commit's content under that name.
onmain=no
git merge-base --is-ancestor "$pin" "$mainref" && onmain=yes

# --- the two file sets ---------------------------------------------------------------
#
# Here: what git tracks, plus untracked files it would not ignore -- a new file has
# to be classified before it is committed, not after. A subdirectory is refused,
# since the pin file names plain files in one directory.
git ls-files --cached --others --exclude-standard -- "$DIR" | sed "s#^$DIR/##" | sort -u >"$WORK/here"
git ls-tree -r --name-only "$pin" -- "$DIR/" | sed "s#^$DIR/##" | sort -u >"$WORK/there"
[ -s "$WORK/there" ] || cannot "main has no $DIR at $pin -- the pin names the wrong commit"

if [ "$mode" = report ]; then
	# Report mode never fails on a difference; it fails only when it cannot say.
	[ "$onmain" = yes ] || cannot "the pin $pin is not on $REMOTE/$MAIN, so there is no 'main at the pin' to report against"
	printf '# Contract gate: advisory diff against main at %s\n\n' "$pin"
	printf 'Files marked `identical` in %s are checked byte for byte by `make contract-identity` and are not shown.\n' "$PINFILE"
	printf 'Each diff below is main at the pin (---) against this line (+++). Read each for a change main made\n'
	printf 'that this line should take, or a change here that main should; neither side is automatically right.\n'
	sed -n 's/^advisory //p' "$WORK/classes" >"$WORK/advisory"
	while IFS= read -r f; do
		printf '\n## %s\n\n' "$f"
		git show "$pin:$DIR/$f" >"$WORK/theirs" 2>/dev/null || { printf '(not at the pin)\n'; continue; }
		[ -f "$DIR/$f" ] || { printf '(missing here)\n'; continue; }
		if diff -u --label "main@${pin%"${pin#???????}"}:$DIR/$f" --label "$DIR/$f" "$WORK/theirs" "$DIR/$f"; then
			printf '(no difference -- consider marking it identical)\n'
		fi
	done <"$WORK/advisory"
	exit 0
fi

# --- check ----------------------------------------------------------------------------
problems=0
problem() {
	problems=$((problems + 1))
	if [ -n "${GITHUB_ACTIONS:-}" ]; then
		printf '::error::contract-identity: %s\n' "$*"
	else
		printf 'contract-identity: %s\n' "$*"
	fi
}

if [ "$onmain" = no ]; then
	problem "the pin $pin is not on $REMOTE/$MAIN -- it must name one of main's commits, or the copies would be compared with themselves"
fi

# The derived floor. A Go file that imports no SDK package names nothing this line's
# dialect could make differ -- the module path, Optional, List[T] all arrive through
# that import -- so a difference in it is divergence and nothing else. Read off
# main's own copy at the pin, so a file cannot escape by changing here; what counts
# as an import is imports_sdk's business, above.
#
# Every loop over file names reads them a LINE at a time. `for f in $(cat ...)`
# split them at spaces, and an untracked file named `checks.go main.go` read as two
# classified names and passed.
while IFS= read -r f; do
	case "$f" in
	*.go) ;;
	*) continue ;;
	esac
	git show "$pin:$DIR/$f" >"$WORK/theirs"
	if ! imports_sdk "$WORK/theirs"; then
		kind=$(class_of "$f")
		[ "$kind" = identical ] ||
			problem "$f imports no SDK package at the pin, so nothing about this line can justify a difference in it: it must be marked identical, not ${kind:-unclassified}"
	fi
done <"$WORK/there"

while IFS= read -r f; do
	case "$f" in
	*/*)
		problem "$DIR/$f is in a subdirectory; the pin file classifies the files of $DIR itself"
		continue
		;;
	esac
	[ -n "$(class_of "$f")" ] || problem "$DIR/$f is not classified in $PINFILE -- mark it identical, advisory or own"
done <"$WORK/here"
while IFS= read -r f; do
	[ -n "$(class_of "$f")" ] || problem "main has $DIR/$f at the pin and $PINFILE does not classify it -- port it and mark it, or say why not"
done <"$WORK/there"

while read -r kind f; do
	inhere=no
	inthere=no
	grep -Fqx -- "$f" "$WORK/here" && inhere=yes
	grep -Fqx -- "$f" "$WORK/there" && inthere=yes
	case "$kind" in
	own)
		[ "$inthere" = no ] || problem "$f is marked own, and main has it at the pin -- it is identical or advisory"
		[ "$inhere" = yes ] || problem "$f is marked own and this line does not have it -- drop the line"
		;;
	identical | advisory)
		[ "$inthere" = yes ] || problem "$f is marked $kind, and main has no $DIR/$f at the pin"
		[ "$inhere" = yes ] || problem "$f is marked $kind and this line does not have it"
		;;
	esac
	if [ "$kind" = identical ] && [ "$inhere" = yes ] && [ "$inthere" = yes ]; then
		if [ ! -f "$DIR/$f" ]; then
			problem "$DIR/$f is tracked and missing from the work tree"
			continue
		fi
		git show "$pin:$DIR/$f" >"$WORK/theirs"
		cmp -s "$WORK/theirs" "$DIR/$f" ||
			problem "$DIR/$f differs from main's at the pin. It is marked identical: fix it on main and advance the pin, never here"
	fi
done <"$WORK/classes"

if [ "$problems" -ne 0 ]; then
	printf 'contract-identity: %d problem(s); see %s for what each kind of line means\n' "$problems" "$PINFILE"
	exit 1
fi
printf 'contract-identity: %d identical file(s) match main at %s\n' \
	"$(grep -c '^identical ' "$WORK/classes")" "$pin"
