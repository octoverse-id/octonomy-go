.DEFAULT_GOAL := help
.PHONY: help tidy build fmt fmt-check vet lint test cover vuln examples check release-check version-check \
	dev-server dev-server-down dev-server-logs compat-guard compat-guard-test smoke test-go113 tools-check

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

lint: ## Run golangci-lint (skipped if not installed; CI runs it)
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint not installed; skipping. Install: https://golangci-lint.run/welcome/install/"; \
	fi

test: ## Run tests with the race detector and coverage
	go test -race -cover ./...

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
	go test -tags=integration -run TestSmoke_RealServer -v ./...

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

release-check: tools-check fmt-check vet lint test vuln examples version-check compat-guard compat-guard-test ## Full pre-release gate
	@echo "release-check passed"
