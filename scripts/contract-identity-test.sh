#!/bin/sh
# Fixture tests for scripts/contract-identity.sh (#98).
#
# The identity check is what makes divergence between this line's copy of the
# contract gate and main's impossible rather than merely discouraged, and a check
# that has never been shown to fail cannot be told apart from one that cannot.
# So each case builds a throwaway pair of repositories -- an "upstream" with a
# main branch carrying tools/contractdrift, and a clone of it standing in for this
# line -- breaks one thing, runs the check, and asserts both the exit status and
# that the message names the thing that broke. The status alone would pass a
# check that fails for the wrong reason.
#
# Usage: scripts/contract-identity-test.sh   (or `make contract-identity-test`)
#
# POSIX sh plus mktemp, like scripts/compat-guard-test.sh.

set -eu

CHECK=$(cd "$(dirname "$0")" && pwd)/contract-identity.sh
test -x "$CHECK" || { echo "not executable: $CHECK"; exit 1; }

WORK=$(mktemp -d "${TMPDIR:-/tmp}/contract-identity-test-XXXXXX")
trap 'rm -rf "$WORK"' EXIT INT TERM

# Every git call here is about the fixtures, never the caller's repository or
# configuration: a hook, a global template or a GIT_DIR leaking in would make the
# fixtures something other than what each case says they are.
unset GIT_DIR GIT_WORK_TREE GIT_INDEX_FILE GIT_OBJECT_DIRECTORY GIT_ALTERNATE_OBJECT_DIRECTORIES 2>/dev/null || true
GIT_CONFIG_NOSYSTEM=1
HOME="$WORK/home"
mkdir -p "$HOME"
export GIT_CONFIG_NOSYSTEM HOME
git config --global user.email test@example.test
git config --global user.name test
git config --global init.defaultBranch main
git config --global protocol.file.allow always
unset GITHUB_ACTIONS 2>/dev/null || true

passed=0
failed=0

commit_all() { git add -A && git commit -q -m "$1"; }

# upstream: main with three gate files, then one more commit so the pin is not
# simply main's tip.
mkdir -p "$WORK/upstream/tools/contractdrift"
(
	cd "$WORK/upstream"
	git init -q .
	# checks.go imports no SDK package, so it must be identical; drivers.go does,
	# so this line may keep its own -- as on the real tree.
	printf 'package main\n// shared; github.com/octoverse-id/octonomy-go is named, not imported\n' >tools/contractdrift/checks.go
	printf 'package main\n\nimport octonomy "github.com/octoverse-id/octonomy-go/v2"\n\n// drives\n' >tools/contractdrift/drivers.go
	printf 'module x\n' >tools/contractdrift/go.mod
	commit_all "gate"
	printf 'readme\n' >README
	commit_all "later"
)
PIN=$(git -C "$WORK/upstream" rev-parse HEAD~1)

# fixture builds $WORK/line: a clone of upstream, on a support branch, with the
# compat line's copy of the gate (checks.go identical, drivers.go differing,
# main.pin own) committed. A case then breaks one thing.
fixture() {
	rm -rf "$WORK/line"
	git clone -q "file://$WORK/upstream" "$WORK/line"
	(
		cd "$WORK/line"
		git checkout -q -b support/line
		printf 'package main\n\nimport (\n\toctonomy "github.com/octoverse-id/octonomy-go"\n)\n\n// drives, the compat way\n' >tools/contractdrift/drivers.go
		write_pin "$PIN"
		commit_all "line"
	)
}

write_pin() {
	cat >"$WORK/line/tools/contractdrift/main.pin" <<EOF
# a comment
pin $1

identical checks.go
advisory drivers.go
advisory go.mod
own main.pin
EOF
}

# expect <name> <want-exit> <want-substring> [mode]
expect() {
	name=$1 want=$2 needle=$3 mode=${4:-check}
	set +e
	out=$(cd "$WORK/line" && "$CHECK" "$mode" 2>&1)
	got=$?
	set -e
	if [ "$got" -ne "$want" ]; then
		printf 'FAIL %s: exit %s, want %s\n%s\n' "$name" "$got" "$want" "$out"
		failed=$((failed + 1))
		return
	fi
	case "$out" in
	*"$needle"*) ;;
	*)
		printf 'FAIL %s: output does not mention %s\n%s\n' "$name" "$needle" "$out"
		failed=$((failed + 1))
		return
		;;
	esac
	printf 'ok   %s\n' "$name"
	passed=$((passed + 1))
}

# --- the clean case, which every other case breaks one thing of ---------------------
fixture
expect "identical copy at a pin on main passes" 0 "1 identical file(s) match main"

# --- divergence ------------------------------------------------------------------------
fixture
printf '\n' >>"$WORK/line/tools/contractdrift/checks.go"
expect "one byte appended to an identical file fails" 1 "checks.go differs from main's at the pin"

fixture
rm "$WORK/line/tools/contractdrift/checks.go"
expect "an identical file deleted from the work tree fails" 1 "checks.go is tracked and missing"

fixture
(cd "$WORK/line" && git rm -q tools/contractdrift/checks.go && commit_all "drop")
expect "an identical file this line no longer has fails" 1 "checks.go is marked identical and this line does not have it"

# --- the pin -----------------------------------------------------------------------------
fixture
(
	cd "$WORK/line"
	printf 'package main\n// edited on the line\n' >tools/contractdrift/checks.go
	commit_all "edit the copy"
)
LINEPIN=$(git -C "$WORK/line" rev-parse HEAD)
write_pin "$LINEPIN"
expect "a pin into this line's own history fails, even though the copy matches it" 1 "is not on origin/main"

fixture
write_pin 0123456789abcdef0123456789abcdef01234567
expect "a pin no repository has cannot be checked" 2 "could not be fetched"

# A commit the remote has, off its main: fetched by id, then refused for ancestry.
fixture
(
	cd "$WORK/upstream"
	git checkout -q -b elsewhere HEAD~1
	printf 'package main\n// shared, edited off main\n' >tools/contractdrift/checks.go
	commit_all "not on main"
	git checkout -q main
)
OFFMAIN=$(git -C "$WORK/upstream" rev-parse elsewhere)
git -C "$WORK/upstream" show "$OFFMAIN:tools/contractdrift/checks.go" >"$WORK/line/tools/contractdrift/checks.go"
write_pin "$OFFMAIN"
expect "a pin fetched from the remote but not on its main fails" 1 "is not on origin/main"
git -C "$WORK/upstream" branch -q -D elsewhere

# A pin on main that this clone has never seen: fetched, then compared -- and main
# fetched too, since the clone's origin/main predates it.
fixture
(
	cd "$WORK/upstream"
	printf 'package main\n// shared, newer\n' >tools/contractdrift/checks.go
	commit_all "newer"
)
NEWPIN=$(git -C "$WORK/upstream" rev-parse HEAD)
if git -C "$WORK/line" cat-file -e "$NEWPIN^{commit}" 2>/dev/null; then
	echo "FAIL fixture: the clone already has the new pin, so the fetch path is not exercised"
	failed=$((failed + 1))
fi
git -C "$WORK/upstream" show "$NEWPIN:tools/contractdrift/checks.go" >"$WORK/line/tools/contractdrift/checks.go"
write_pin "$NEWPIN"
expect "a pin on main this clone lacks is fetched and compared" 0 "match main at $NEWPIN"
git -C "$WORK/upstream" reset -q --hard HEAD~1

# --- the derived floor ---------------------------------------------------------------------
# One word in main.pin must not be able to exempt a shared file from the comparison.
fixture
sed 's/^identical checks.go$/advisory checks.go/' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
printf '\n' >>"$WORK/line/tools/contractdrift/checks.go"
expect "a shared file relabelled advisory, then changed, fails" 1 "checks.go imports no SDK package at the pin"

fixture
sed 's/^identical checks.go$/advisory checks.go/' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
expect "a shared file relabelled advisory fails even unchanged" 1 "must be marked identical, not advisory"

# An SDK-importing file may be identical too, if it happens to match.
fixture
sed 's/^advisory drivers.go$/identical drivers.go/' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
git -C "$WORK/line" show "$PIN:tools/contractdrift/drivers.go" >"$WORK/line/tools/contractdrift/drivers.go"
expect "a file that imports the SDK may still be marked identical when it matches" 0 "2 identical file(s) match"

# --- classification ----------------------------------------------------------------------
fixture
printf 'package main\n' >"$WORK/line/tools/contractdrift/extra.go"
expect "an untracked file nothing classifies fails" 1 "extra.go is not classified"

fixture
(cd "$WORK/line" && printf 'package main\n' >tools/contractdrift/extra.go && commit_all "extra")
expect "a tracked file nothing classifies fails" 1 "extra.go is not classified"

fixture
(cd "$WORK/line" && mkdir -p tools/contractdrift/sub && printf 'x\n' >tools/contractdrift/sub/f.go)
expect "a file in a subdirectory fails" 1 "sub/f.go is in a subdirectory"

fixture
grep -v '^advisory go.mod$' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
(cd "$WORK/line" && git rm -q tools/contractdrift/go.mod && commit_all "drop go.mod")
expect "a file main has at the pin that nothing classifies fails" 1 "main has tools/contractdrift/go.mod at the pin"

fixture
sed 's/^advisory drivers.go$/own drivers.go/' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
expect "an own file main has fails" 1 "drivers.go is marked own, and main has it"

fixture
printf 'advisory local.go\n' >>"$WORK/line/tools/contractdrift/main.pin"
printf 'package main\n' >"$WORK/line/tools/contractdrift/local.go"
expect "an advisory file main lacks fails" 1 "local.go is marked advisory, and main has no"

fixture
printf 'own gone.go\n' >>"$WORK/line/tools/contractdrift/main.pin"
expect "an own file that is not here fails" 1 "gone.go is marked own and this line does not have it"

# Names are compared whole, never as patterns: neither a name that differs only
# where a classified one has a `.`, nor one that is a fragment of a classified
# one, borrows that file's classification.
fixture
printf 'package main\n' >"$WORK/line/tools/contractdrift/checksXgo"
expect "a name differing only where the other has a dot is still unclassified" 1 "checksXgo is not classified"

fixture
printf 'package main\n' >"$WORK/line/tools/contractdrift/s.go"
expect "a name that is part of a classified one is still unclassified" 1 "s.go is not classified"

# --- the pin file itself -------------------------------------------------------------------
for case in \
	"second-pin|pin $PIN" \
	"unknown-kind|identicl checks.go" \
	"duplicate|advisory checks.go" \
	"two-spaces|advisory  drivers.go" \
	"path|advisory sub/drivers.go"; do
	label=${case%%|*}
	extra=${case#*|}
	fixture
	printf '%s\n' "$extra" >>"$WORK/line/tools/contractdrift/main.pin"
	expect "pin file refuses: $label" 2 "main.pin:"
done

fixture
printf 'pin %s\n' "${PIN%?}" >"$WORK/pin" && sed "s/^pin .*/$(cat "$WORK/pin")/" "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin2" && cp "$WORK/pin2" "$WORK/line/tools/contractdrift/main.pin"
expect "pin file refuses: a short commit id" 2 "forty-hex-digit"

fixture
sed '/^pin /d' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
expect "pin file refuses: no pin" 2 "has no pin line"

fixture
sed '/^own main.pin$/d' "$WORK/line/tools/contractdrift/main.pin" >"$WORK/pin" && cp "$WORK/pin" "$WORK/line/tools/contractdrift/main.pin"
expect "pin file refuses: not classifying itself" 2 "must classify itself"

fixture
rm "$WORK/line/tools/contractdrift/main.pin"
expect "a missing pin file cannot be checked" 2 "main.pin does not exist"

# --- the clone -----------------------------------------------------------------------------
fixture
rm -rf "$WORK/shallow"
git clone -q --depth 1 --branch support/line "file://$WORK/line" "$WORK/shallow"
set +e
out=$(cd "$WORK/shallow" && "$CHECK" check 2>&1)
got=$?
set -e
case "$got:$out" in
2:*shallow*)
	printf 'ok   %s\n' "a shallow clone cannot decide ancestry and says so"
	passed=$((passed + 1))
	;;
*)
	printf 'FAIL shallow: exit %s\n%s\n' "$got" "$out"
	failed=$((failed + 1))
	;;
esac

# --- report ----------------------------------------------------------------------------------
fixture
printf '\n' >>"$WORK/line/tools/contractdrift/checks.go"
expect "report shows an advisory file's diff" 0 "+// drives, the compat way" report
expect "report does not fail on an identical file's divergence -- check does" 0 "## drivers.go" report

fixture
(
	cd "$WORK/line"
	printf 'package main\n// shared; github.com/octoverse-id/octonomy-go is named, not imported\n' >tools/contractdrift/checks.go
	printf 'local\n' >NOTE
	commit_all "a commit on this line"
)
write_pin "$(git -C "$WORK/line" rev-parse HEAD)"
expect "report refuses a pin that is not on main, rather than label it main's" 2 "no 'main at the pin'" report
set +e
out=$(cd "$WORK/line" && "$CHECK" report 2>&1)
set -e
case "$out" in
*"## checks.go"*)
	printf 'FAIL report: it printed an identical file as if it were advisory\n%s\n' "$out"
	failed=$((failed + 1))
	;;
*)
	printf 'ok   %s\n' "report leaves identical files to check"
	passed=$((passed + 1))
	;;
esac

printf '\n%d passed, %d failed\n' "$passed" "$failed"
[ "$failed" -eq 0 ]
