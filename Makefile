.DEFAULT_GOAL := help
.PHONY: help tidy build fmt fmt-check vet lint test cover vuln examples check release-check version-check \
	dev-server dev-server-down dev-server-logs smoke

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

lint: ## Run golangci-lint (skipped if not installed; CI runs it)
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

vuln: ## Run govulncheck (skipped if not installed; CI runs it)
	@if command -v govulncheck >/dev/null 2>&1; then \
		govulncheck ./...; \
	else \
		echo "govulncheck not installed; skipping. Install: go install golang.org/x/vuln/cmd/govulncheck@latest"; \
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

version-check: ## Verify version.go matches the latest CHANGELOG.md release heading
	@code_ver=$$(grep -E '^const Version = ' version.go | sed -E 's/.*"([^"]+)".*/\1/'); \
	log_ver=$$(grep -m1 -E '^## \[[0-9]' CHANGELOG.md | sed -E 's/^## \[([^]]+)\].*/\1/'); \
	if [ "$$code_ver" != "$$log_ver" ]; then \
		echo "version mismatch: version.go=$$code_ver CHANGELOG.md=$$log_ver"; exit 1; \
	fi; \
	echo "version OK: $$code_ver"

release-check: fmt-check vet lint test vuln examples version-check ## Full pre-release gate
	@echo "release-check passed"
