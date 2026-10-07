.DEFAULT_GOAL := help
.PHONY: help tidy build fmt fmt-check vet lint test cover vuln examples check release-check version-check \
	dev-server dev-server-down dev-server-logs compat-guard compat-guard-test smoke test-integration test-go113 \
	tools-check contract-check contract-test contract-identity contract-identity-test contract-report

# A real go1.13 toolchain, for the one gate a modern toolchain cannot provide.
# Override with the path to any go1.13.x binary:
#   GO113=/tmp/go1.13.15/bin/go make test-go113
# See docs/development.md for how to fetch one.
GO113 ?= go1.13.15

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
# The SDK is one module and the contract gate (tools/contractdrift, #98) another,
# and `golangci-lint run` stops at the nested go.mod exactly as `go vet ./...`
# does -- so a target that linted only the root would let release-check pass on a
# gate CI rejects.
lint: ## Run golangci-lint on the SDK and on the contract gate's module (skipped if not installed; CI runs it)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		status=0; \
		golangci-lint run || status=$$?; \
		(cd tools/contractdrift && golangci-lint run --config ../../.golangci.yml) || status=$$?; \
		exit $$status; \
	else \
		echo "golangci-lint not installed; skipping. Install: https://golangci-lint.run/welcome/install/"; \
	fi

test: ## Run tests with the race detector and coverage
	go test -race -cover ./...

cover: ## Run tests and print total coverage
	go test -race -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

# Two modules, two scans, and BOTH statuses. main's first version of this put the
# second scan under the first inside the `if` body, and `sh` gives an `if` body
# the status of the command that ended it -- so a root scan that found a
# vulnerability left `make vuln` exiting 0 (main's #56). Captured rather than
# chained with `&&`, so one run reports everything both scans found.
# TestBothModuleScansPropagateFailure (tools/contractdrift) drives both targets
# with a failing stub in each module in turn.
vuln: ## Run govulncheck on the SDK and on the contract gate's module (skipped if not installed; CI runs it)
	@if command -v govulncheck >/dev/null 2>&1; then \
		status=0; \
		govulncheck ./... || status=$$?; \
		(cd tools/contractdrift && govulncheck ./...) || status=$$?; \
		exit $$status; \
	else \
		echo "govulncheck not installed; skipping. Install: GOTOOLCHAIN=auto go install golang.org/x/vuln/cmd/govulncheck@latest"; \
	fi

examples: ## Compile-check the runnable examples (no binaries emitted)
	@find examples -name main.go -exec dirname {} \; | sort -u | while read -r dir; do \
		echo "build ./$$dir"; go build -o /dev/null "./$$dir" || exit 1; \
	done

tools-check: ## Fail unless the optional gate tools are actually installed
	@missing=""; \
	command -v golangci-lint >/dev/null 2>&1 || missing="$$missing golangci-lint"; \
	command -v govulncheck   >/dev/null 2>&1 || missing="$$missing govulncheck"; \
	if [ -n "$$missing" ]; then \
		echo "release gate tools missing:$$missing"; \
		echo "\`make lint\` and \`make vuln\` SKIP when their tool is absent, which is fine"; \
		echo "day to day and wrong for a release: release-check would print 'passed'"; \
		echo "having run neither. Install them (see docs/development.md) and re-run."; \
		exit 1; \
	fi; \
	echo "release gate tools present"

compat-guard: ## Assert go.mod still matches this release line (blocking on the go directive)
	@scripts/compat-guard.sh

compat-guard-test: ## Run the compat-guard fixture tests (release-PR and tag paths)
	@scripts/compat-guard-test.sh

smoke: ## Run the integration smoke test against a booted harness (see dev-server)
	@if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	go test -tags=integration -count=1 -run '^TestSmoke_' -v ./...

# Pinned, like smoke: isolationRecipePin in readprobes_test.go is this rule's
# text, and the comment above it lists what a change must re-check.
test-integration: ## Run the namespace isolation suite against a booted harness (see dev-server)
	@if [ -f .octonomy-harness.env ]; then set -a; . ./.octonomy-harness.env; set +a; fi; \
	go test -tags=integration -count=1 -run '^TestIntegration_' -v ./...

test-go113: ## Build, vet and test with a REAL go1.13 toolchain (override GO113=<path>)
	@command -v $(GO113) >/dev/null 2>&1 || { \
		echo "$(GO113) not found. Fetch a real go1.13 toolchain -- see docs/development.md"; exit 1; }
	@echo "using $$($(GO113) version)"
	GO111MODULE=on $(GO113) build ./...
	GO111MODULE=on $(GO113) vet ./...
	GO111MODULE=on $(GO113) test -race ./...

check: fmt-check vet build compat-guard compat-guard-test ## Fast pre-push gate (format, vet, build, line guard)

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

# --- The contract gate (#98) ----------------------------------------------------
#
# main's tools/contractdrift, ported: a nested Go 1.24 module that CALLS every
# method the inventory names against a stub built from the vendored schema, and
# compares what went on the wire and what came back with the contract. The
# LIBRARY is Go 1.13; the gate is not, and need not be -- only CI and a
# contributor ever compile it. See docs/development.md#contract-drift.
#
# Only the offline half is here. main at 5e40964 also has `contract-drift`, which
# fetches the server's contract from another repository and runs there on a
# schedule; GitHub fires schedules on the default branch only, so this line carries
# neither the target nor its fetch script.

# `go build` then run, never `go run`. The tool exits 0 clean, 1 drift found, 2
# comparison could not be made, and `go run` collapses that 2 into a shell exit of
# 1 while printing "exit status 2". Make flattens any failed recipe to its own exit
# 2 regardless, so the distinction survives only for someone running the binary
# directly -- which is the case worth protecting.
contract-check: ## Offline contract gate: vendored contracts vs what this client sends and decodes
	@set -e; \
	dir=$$(mktemp -d); trap 'rm -rf "$$dir"' EXIT; \
	(cd tools/contractdrift && go build -o "$$dir/contractdrift" .); \
	"$$dir/contractdrift" -repo . -local

# -race like every other suite here (AGENTS.md), and vet because the root
# `go vet ./...` stops at the nested module's go.mod and never sees this
# directory. These tests are what prove the gate can still FAIL -- a drift gate
# never shown to fail cannot be told apart from one that cannot.
contract-test: ## Run the contract gate's own tests (proves the gate can still fail)
	@cd tools/contractdrift && go vet ./... && go test -race ./...

# The pinned identity check. Six of the gate's files are main's, byte for byte,
# at the commit tools/contractdrift/main.pin names; this fails on any difference,
# on a pin that is not on main, and on a file in either tree the pin file does not
# classify. Fetches the pin from origin when it is not already local.
contract-identity: ## Fail unless the gate's shared files are byte-identical to main at the pinned commit
	@scripts/contract-identity.sh check

contract-identity-test: ## Run the identity check's fixture tests
	@scripts/contract-identity-test.sh

# Advisory, never failing on a difference: the files the pin file marks
# `advisory` legitimately differ from main's, and what a human must read at
# release time is HOW. docs/release.md says when.
contract-report: ## Print the diff of the gate's compat-only files against main at the pin (advisory)
	@scripts/contract-identity.sh report

release-check: tools-check fmt-check vet lint test vuln examples version-check compat-guard compat-guard-test contract-test contract-check contract-identity ## Full pre-release gate
	@echo "release-check passed"
