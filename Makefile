.DEFAULT_GOAL := help
.PHONY: help tidy build fmt fmt-check vet lint test cover vuln examples check release-check require-tools \
	version-check dev-server dev-server-down dev-server-logs smoke \
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

lint: ## Run golangci-lint (skipped if not installed; release-check requires it)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
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

cover: ## Run tests and print total coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Both modules. The SDK module has no dependencies, so its scan covers the
# standard library it builds against; the drift gate's nested module has one, and
# `govulncheck ./...` at the root stops at that go.mod and never sees it. Nothing
# there ships to a consumer -- it is CI tooling -- but an unscanned directory is
# an unscanned directory, and this is the target that says otherwise.
vuln: ## Run govulncheck on the SDK and on the contract gate's module
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
		(cd tools/contractdrift && govulncheck ./...); \
	else \
		echo "govulncheck not installed; skipping. Install: GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi

examples: ## Compile-check the runnable examples (no binaries emitted)
	@find examples -name main.go -exec dirname {} \; | sort -u | while read -r dir; do \
		echo "build ./$$dir"; go build -o /dev/null "./$$dir" || exit 1; \
	done

check: fmt-check vet build ## Fast pre-push gate (format, vet, build)

dev-server: ## Boot a real Octonomy (Postgres + GHCR container) and write .octonomy-harness.env
	@scripts/octonomy-harness.sh up

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
