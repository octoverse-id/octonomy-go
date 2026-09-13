.DEFAULT_GOAL := help
.PHONY: help tidy build fmt fmt-check vet lint test cover vuln examples check release-check require-tools \
	version-check dev-server dev-server-env dev-server-down dev-server-logs smoke test-integration \
	contract-check contract-drift contract-test

help: ## List available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

tidy: ## Tidy go.mod / go.sum
	go mod tidy

build: ## Compile the module
	go build ./...

fmt: ## Format the code with gofmt
	gofmt -w .

fmt-check: ## Fail if any file is not gofmt-clean
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then \
		echo "gofmt needs to run on:"; echo "$$out"; exit 1; \
	fi

vet: ## Run go vet
	go vet ./...

# Both modules, and both statuses, for the reasons spelled out over `vuln` below.
# CI lints tools/contractdrift as its own step; this target did not, so
# release-check could pass on a tree CI would reject.
lint: ## Run golangci-lint on the SDK and on the contract gate's module
	@if command -v golangci-lint >/dev/null 2>&1; then \
		status=0; \
		golangci-lint run || status=$$?; \
		(cd tools/contractdrift && golangci-lint run) || status=$$?; \
		exit $$status; \
	else \
		echo "golangci-lint not installed; skipping. Install: https://golangci-lint.run/welcome/install/"; \
	fi

test: ## Run tests with the race detector and coverage
	go test -race -cover ./...

# Redirected, not piped: make runs recipes under /bin/sh, which has no
# `pipefail`, so `go test ... | tee log; status=$$?` would capture TEE's exit
# status and silently drop a `go test` failure that still printed a PASS line
# for this test (a panic after it, or another package failing).
#
# `|| status=$$?` rather than a bare command, because `set -e` would otherwise
# exit the recipe the instant `go test` failed -- before `cat` ran and while the
# EXIT trap deleted the log. The job would go red carrying no test output at all,
# which is the one thing a failing integration test must not do. `cat || true`
# for the same reason: a cat failure must not overwrite go test's status.
#
# -count=1 because `go test` caches a passing result and will replay it without
# contacting the server -- a cached green from a run against a container that is
# no longer up is exactly the vacuous pass this target exists to prevent.
#
# Two further vacuous-green holes, closed by two distinct mechanisms, because
# neither covers the other:
#   - the test SKIPS when no harness is up      -> OCTONOMY_SMOKE_REQUIRED=1 (CI sets it)
#   - the test is not SELECTED by -run at all   -> the guard below
# `go test -run` exits 0 when its pattern matches nothing, so a renamed or
# deleted test reports success having run no assertions. Accepting SKIP here is
# deliberate: an absent harness is a developer's normal case and is the other
# mechanism's business, not this one's.
smoke: ## Run the integration smoke test against a booted harness (see dev-server)
	@set -e; \
	if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	log=$$(mktemp); trap 'rm -f "$$log"' EXIT; \
	status=0; \
	go test -tags=integration -count=1 -run '^TestSmoke_RealServer$$' -v ./... >"$$log" 2>&1 || status=$$?; \
	cat "$$log" || true; \
	[ $$status -eq 0 ] || exit $$status; \
	grep -qE -- '^--- (PASS|SKIP): TestSmoke_RealServer \(' "$$log" || { \
		echo "smoke: TestSmoke_RealServer was not selected by -run."; \
		echo "smoke: \`go test -run\` exits 0 when its pattern matches nothing, so this would"; \
		echo "smoke: otherwise report success having asserted nothing. Was the test renamed?"; \
		exit 1; }

# The FULL integration suite (#17), not just the smoke test.
#
# Everything the `smoke` target's header says about redirection, `|| status=$$?`,
# `cat || true`, and -count=1 applies here unchanged and is not repeated; read it
# there. Three things are different:
#
#   * No -run. `smoke` narrows to one test because it is the blocking check and
#     wants to stay fast and minimal; this target is the whole tagged package,
#     which is exactly the acceptance criterion -- `go test -tags=integration`
#     boots against a real server and passes. TestSmoke_RealServer runs here too,
#     and that overlap is deliberate: a developer running one command should not
#     have to know which file an assertion lives in.
#   * -race, which `smoke` omits. AGENTS.md asks for it on every suite, and while
#     nothing here runs concurrently, the cost is one compile against a container
#     that already took a minute to boot.
#   * The not-selected guard keys on the suite's own prefix. `go test` with no
#     -run cannot select nothing, but a package whose TestIntegration_* functions
#     were all renamed or removed still exits 0 having asserted none of this --
#     the same vacuous green in a different shape. SKIP is accepted for the same
#     reason it is there: no harness is a developer's normal case, and
#     OCTONOMY_SMOKE_REQUIRED (which CI sets) is the mechanism that closes it.
test-integration: ## Run the full integration suite against a booted harness (see dev-server)
	@set -e; \
	if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	log=$$(mktemp); trap 'rm -f "$$log"' EXIT; \
	status=0; \
	go test -tags=integration -race -count=1 -v ./... >"$$log" 2>&1 || status=$$?; \
	cat "$$log" || true; \
	[ $$status -eq 0 ] || exit $$status; \
	grep -qE -- '^--- (PASS|SKIP): TestIntegration_' "$$log" || { \
		echo "test-integration: no TestIntegration_* test ran."; \
		echo "test-integration: the package compiled and exited 0 having asserted none of the"; \
		echo "test-integration: suite. Were the tests renamed, or is the build tag missing?"; \
		exit 1; }

# Examples are excluded, and the reason is arithmetic rather than taste. They are
# main packages with no tests, so every statement in them lands in the profile
# uncovered: adding the ten examples on #19 moved this number from 96.7% to 54.0%
# without one line of the library becoming less tested. A figure that reads
# "coverage collapsed" when nothing collapsed is worse than no figure, and this
# is the number AGENTS.md's "keep new code covered" is read off.
#
# Nothing is lost by the exclusion. `make test` still compiles and runs every
# package, `make examples` is what proves the examples build, and no claim
# anywhere says an example is covered by a test.
cover: ## Run tests and print total library coverage (examples excluded)
	go test -race -coverprofile=coverage.out $$(go list ./... | grep -v '/examples/')
	go tool cover -func=coverage.out | tail -1

# Both modules. The SDK module has no dependencies, so its scan covers the
# standard library it builds against; the drift gate's nested module has one, and
# `govulncheck ./...` at the root stops at that go.mod and never sees it. Nothing
# there ships to a consumer -- it is CI tooling -- but an unscanned directory is
# an unscanned directory, and this is the target that says otherwise.
#
# BOTH statuses, not just the last one. `sh` gives an `if` body the exit status of
# the command that ended it, so adding the second scan under the first silently
# discarded the first's: a root scan that found a vulnerability left `make vuln`
# exiting 0, and release-check runs this target. Caught in review on #56.
#
# Captured rather than chained with `&&`, because a scanner should say everything
# it found in one run -- `&&` would mean a root failure hides the nested module
# entirely, and you would fix one and then discover the other.
vuln: ## Run govulncheck on the SDK and on the contract gate's module
	@if command -v govulncheck >/dev/null 2>&1; then \
		status=0; \
		govulncheck ./... || status=$$?; \
		(cd tools/contractdrift && govulncheck ./...) || status=$$?; \
		exit $$status; \
	else \
		echo "govulncheck not installed; skipping. Install: GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi

# One `go build` per directory with -o /dev/null, rather than the single
# `go build ./examples/...` this used to be documented as. That form means two
# different things depending on how many examples exist: with several main
# packages the go command discards the results, but with exactly ONE it writes
# that binary into the working directory. A compile check must not leave an
# artifact behind on the day someone deletes the second-to-last example.
#
# The emptiness guard is the vacuous-green rule the smoke target states at
# length, in its third shape: `find` matching nothing makes this target exit 0
# having compiled nothing, and a renamed or moved examples/ directory looks
# exactly like that. The count is echoed so a run that quietly stopped covering
# half the tree is visible rather than merely non-zero.
examples: ## Compile-check the runnable examples (no binaries emitted)
	@set -e; \
	dirs=$$(find examples -name main.go -exec dirname {} \; | sort -u); \
	[ -n "$$dirs" ] || { \
		echo "examples: no main.go found under examples/."; \
		echo "examples: this target would otherwise report success having compiled nothing."; \
		exit 1; }; \
	count=0; \
	for dir in $$dirs; do \
		echo "build ./$$dir"; go build -o /dev/null "./$$dir"; \
		count=$$((count + 1)); \
	done; \
	echo "examples: $$count compiled"

check: fmt-check vet build ## Fast pre-push gate (format, vet, build)

dev-server: ## Boot a real Octonomy (Postgres + GHCR container) and print the examples' env
	@scripts/octonomy-harness.sh up
	@$(MAKE) --no-print-directory dev-server-env

# The bridge between the harness and the examples, and the reason it exists is
# that the two use different variable names on purpose. The harness writes
# OCTONOMY_TEST_* because the integration suites GATE on those names -- an empty
# OCTONOMY_TEST_BASE_URL is what makes them skip instead of fail -- while an
# example is a program a reader copies, and a consumer's program reads
# OCTONOMY_*. Renaming either set to match the other would break one of those
# two properties.
#
# It is a target rather than a paragraph in the README because the value that
# matters is a freshly minted token: something to copy, never to retype. It is
# also separate from `dev-server` so the block can be reprinted into a second
# terminal without rebooting the container.
#
# Values are single-quoted so a copied line survives a space, and the env file is
# sourced through an explicit ./ prefix when it is relative -- POSIX `.` searches
# PATH for a bare name, which would source something else entirely.
#
# Every value is checked for emptiness before anything is printed, which is the
# vacuous-green rule the smoke target states at length, in this target's shape: a
# file that exists proves nothing, and an interrupted boot leaves a stale or
# partial one behind. Printing OCTONOMY_TOKEN='' and exiting 0 would say the
# examples can run while handing over credentials that cannot authenticate, and
# the failure would surface three commands later as a blanket 401.
dev-server-env: ## Print the export block the examples read (needs a booted dev-server)
	@set -e; \
	env_file=$$(scripts/octonomy-harness.sh env); \
	case "$$env_file" in /*) ;; *) env_file="./$$env_file" ;; esac; \
	[ -f "$$env_file" ] || { \
		echo "dev-server-env: $$env_file does not exist -- run \`make dev-server\` first."; \
		exit 1; }; \
	set -a; . "$$env_file"; set +a; \
	missing=""; \
	for var in OCTONOMY_TEST_BASE_URL OCTONOMY_TEST_TOKEN OCTONOMY_TEST_TENANT_ID \
		OCTONOMY_TEST_APPLICATION_ID OCTONOMY_TEST_NAMESPACE_TYPE OCTONOMY_TEST_NAMESPACE_ID; do \
		eval "value=\$$$$var"; \
		[ -n "$$value" ] || missing="$$missing $$var"; \
	done; \
	[ -z "$$missing" ] || { \
		echo "dev-server-env: $$env_file is missing a value for:$$missing"; \
		echo "dev-server-env: printing the block anyway would hand you blank credentials and a"; \
		echo "dev-server-env: green exit -- the examples would then fail somewhere else. The file"; \
		echo "dev-server-env: is written whole at the end of a successful boot, so a partial one"; \
		echo "dev-server-env: means an interrupted or stale run: \`make dev-server\` again."; \
		exit 1; }; \
	echo; \
	echo "Run any example against this server:"; \
	echo; \
	echo "  export OCTONOMY_BASE_URL='$$OCTONOMY_TEST_BASE_URL'"; \
	echo "  export OCTONOMY_TOKEN='$$OCTONOMY_TEST_TOKEN'"; \
	echo "  export OCTONOMY_TENANT_ID='$$OCTONOMY_TEST_TENANT_ID'"; \
	echo "  export OCTONOMY_APPLICATION_ID='$$OCTONOMY_TEST_APPLICATION_ID'"; \
	echo "  export OCTONOMY_NAMESPACE_TYPE='$$OCTONOMY_TEST_NAMESPACE_TYPE'"; \
	echo "  export OCTONOMY_NAMESPACE_ID='$$OCTONOMY_TEST_NAMESPACE_ID'"; \
	echo; \
	echo "  go run ./examples/quickstart"; \
	echo

dev-server-down: ## Tear down the Octonomy container harness
	@scripts/octonomy-harness.sh down

dev-server-logs: ## Dump container logs from the Octonomy container harness
	@scripts/octonomy-harness.sh logs

# --- Contract drift (#18) ------------------------------------------------------
#
# Three targets, and the split between the first two is the whole design. The
# offline half compares the VENDORED contracts against this repository and is a
# pull-request gate; the cross-repository half reaches into octoverse-id/octonomy
# and runs on a schedule only, because a check that can fail for network reasons
# must never stand between a correct change and its merge.
#
# The tool lives in its own module (tools/contractdrift) so its dependencies are
# not the SDK's: a YAML parser, and the SDK itself, which it imports through a
# replace in order to CALL the client and read the request off the wire. The
# module boundary is what keeps both out of `go build ./...`, out of `go.sum`, and
# out of anything a consumer resolves -- nothing flows back the other way.

# `go build` then run, never `go run`. The tool exits 0 clean, 1 drift found, 2
# comparison could not be made, and `go run` collapses that 2 into a shell exit of
# 1 while printing "exit status 2" -- so a caller that reads the code learns the
# opposite of what happened.
#
# Note what this does and does not buy. Make flattens ANY failed recipe to its own
# exit 2, so these targets cannot pass the distinction on; it survives for anyone
# invoking the binary directly, which is the case worth protecting. In CI the two
# outcomes are told apart by shape instead: a fetch that could not complete
# annotates `::error::contract-fetch:` and leaves the job summary empty, while
# drift leaves the whole report in it.
contract-check: ## Offline contract gate: vendored contracts vs this repository
	@set -e; \
	dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	(cd tools/contractdrift && go build -o "$$dir/contractdrift" .); \
	"$$dir/contractdrift" -repo . -local

# `-summary` always gets a path so CI and a laptop run the same command: in CI it
# is the job summary, locally it is /dev/null. A conditional flag here would mean
# the two diverge in the one place nobody re-reads.
contract-drift: ## Full contract gate: fetch the server's contract and report drift (network)
	@set -e; \
	dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	(cd tools/contractdrift && go build -o "$$dir/contractdrift" .); \
	scripts/contract-fetch.sh "$$dir/upstream"; \
	"$$dir/contractdrift" -repo . -upstream "$$dir/upstream" \
		-source "$$(cat "$$dir/upstream/source.txt")" \
		-summary "$${GITHUB_STEP_SUMMARY:-/dev/null}"

# -race like every other suite here (AGENTS.md), and vet because the root
# `go vet ./...` stops at the nested module's go.mod and never sees this
# directory.
contract-test: ## Run the contract gate's own tests (proves the gate can still fail)
	@cd tools/contractdrift && go vet ./... && go test -race ./...

version-check: ## Verify version.go matches the latest CHANGELOG.md release heading
	@code_ver=$$(grep -E '^const Version = ' version.go | sed -E 's/.*"([^"]+)".*/\1/'); \
	log_ver=$$(grep -m1 -E '^## \[[0-9]' CHANGELOG.md | sed -E 's/^## \[([^]]+)\].*/\1/'); \
	if [ "$$code_ver" != "$$log_ver" ]; then \
		echo "version mismatch: version.go=$$code_ver CHANGELOG.md=$$log_ver"; exit 1; \
	fi; \
	echo "version OK: $$code_ver"

# The release gate's tool check, deliberately NOT inside `lint` and `vuln`.
#
# Those two skip with a notice when their binary is missing, which is right for a
# fresh clone: `make lint` should not hard-fail a contributor over a dev tool CI
# installs anyway. What was wrong is that `release-check` INHERITED that skip.
# Neither `else` branch exits non-zero, so on a machine without the tools a green
# gate proved that neither ran -- in front of a release that cannot be recalled,
# since proxy.golang.org keeps a version permanently and `retract` is inert for
# the Go 1.13 consumers the compat line exists to serve (#53).
#
# It is a live case rather than a hypothetical: golangci-lint installs under the
# active toolchain's GOPATH, so it is off PATH by default in this project's own
# dev setup and `make lint` printing "skipping" is the normal local experience.
#
# Both binaries are reported in one run rather than failing at the first, so you
# install them once instead of finding the second after fixing the first.
require-tools: ## Verify the tools release-check needs are on PATH
	@ok=1; \
	command -v golangci-lint >/dev/null 2>&1 || { ok=0; \
		echo "missing: golangci-lint  -- install: https://golangci-lint.run/welcome/install/"; }; \
	command -v govulncheck >/dev/null 2>&1 || { ok=0; \
		echo "missing: govulncheck    -- install: GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest"; }; \
	if [ "$$ok" -eq 0 ]; then \
		echo "release-check runs lint and vuln for real and cannot do so without these."; \
		echo "A tool installed with \`go install\` lands in \$$(go env GOPATH)/bin -- check that it is on PATH."; \
		exit 1; \
	fi; \
	echo "release tools OK: golangci-lint, govulncheck"

# Staged through a sub-make rather than listed as peer prerequisites, so that
# require-tools genuinely runs FIRST. Make orders prerequisites only in a serial
# run: under `make -j release-check` they may all start at once, which would
# leave the tool check racing the full race-enabled test suite it exists to run
# ahead of. As a prerequisite of a recipe that then invokes the rest, the
# ordering is a property of the target rather than of how it was invoked.
# contract-check, not contract-drift: the release gate asserts that this tree is
# internally consistent -- the inventory, the methods it names, the parameters the
# client sends, the recorded contract version. Whether the SERVER has moved since
# is a different question, it is the scheduled job's, and a release must not be
# blocked by a network round trip to another repository.
release-check: require-tools ## Full pre-release gate
	@$(MAKE) --no-print-directory fmt-check vet lint test vuln examples version-check contract-check
	@echo "release-check passed: all eight checks ran"
