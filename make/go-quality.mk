# Go Code Quality & Standards
# Internal code quality routines - use root Makefile targets for public interface

# Pinned so every run lints identically. CI installs this exact version as a
# prebuilt release binary (.github/workflows/ci.yml) — compiling golangci-lint
# from source via `go install` costs minutes on a cold cache, so the go-install
# below is only the local fallback. Keep the two in lockstep, and keep both in
# lockstep with .golangci.yml (v2 config, tuned for v1 lint-surface parity).
# v2 is required: v1.64.8 release binaries are built with go1.24 and refuse
# the repo's go 1.25.x language target.
GOLANGCI_LINT_VERSION ?= v2.12.2

# Format Go code automatically
go-format:
	@echo "--> Formatting Go code with gofmt"
	@find . -name '*.go' -type f -not -path './vendor/*' -not -path './third_party/*' | xargs gofmt -w -s
	@echo "✅ Code formatted successfully"

# Lint code with automatic fixes
go-lint:
	@echo "--> Running golangci-lint with automatic fixes"
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@golangci-lint run $(TEST_PACKAGES) --fix --timeout $(LINT_TIMEOUT)
	@for m in $(GO_SUBMODULE_DIRS); do \
		echo "--> Linting module $$m"; \
		(cd $$m && golangci-lint run ./... --fix --timeout $(LINT_TIMEOUT)) || exit 1; \
	done
	@echo "✅ Code linted successfully"

# Organise imports automatically
go-imports:
	@echo "--> Organising Go imports with goimports"
	@command -v goimports >/dev/null 2>&1 || go install golang.org/x/tools/cmd/goimports@latest
	@find . -name '*.go' -type f -not -path './vendor/*' -not -path './third_party/*' | xargs goimports -w
	@echo "✅ Imports organised successfully"

# Check import organisation (read-only)
go-imports-check:
	@echo "--> Checking Go import organisation..."
	@command -v goimports >/dev/null 2>&1 || go install golang.org/x/tools/cmd/goimports@latest
	@goimports_files=$$(find . -name '*.go' -type f -not -path './vendor/*' -not -path './third_party/*' | xargs goimports -l); \
	if [ -n "$$goimports_files" ]; then \
		echo "❌ The following files have unorganised imports:"; \
		echo "$$goimports_files"; \
		echo "Run 'make imports' to fix"; \
		exit 1; \
	fi
	@echo "✅ Import organisation OK"

# Static analysis with go vet
go-vet:
	@echo "--> Running go vet for static analysis"
	@go vet $(TEST_PACKAGES)
	@for m in $(GO_SUBMODULE_DIRS); do \
		(cd $$m && go vet ./...) || exit 1; \
	done
	@echo "✅ Static analysis completed"

# Security vulnerability scannin
go-govulncheck:
	@echo "--> Checking for security vulnerabilities"
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	@govulncheck $(TEST_PACKAGES)
	@echo "✅ Security check completed"

# Check code formatting (read-only)
go-format-check:
	@echo "--> Checking code formatting..."
	@gofmt_files=$$(find . -name '*.go' -type f -not -path './vendor/*' -not -path './third_party/*' | xargs gofmt -l -s); \
	if [ -n "$$gofmt_files" ]; then \
		echo "❌ The following files are not formatted:"; \
		echo "$$gofmt_files"; \
		echo "Run 'make format' to fix formatting"; \
		exit 1; \
	fi
	@echo "✅ Code formatting OK"

# Lint code without fixes (read-only)
go-lint-check:
	@echo "--> Running linter checks..."
	@command -v golangci-lint >/dev/null 2>&1 || go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
	@golangci-lint run $(TEST_PACKAGES) --timeout $(LINT_TIMEOUT)
	@for m in $(GO_SUBMODULE_DIRS); do \
		echo "--> Linting module $$m"; \
		(cd $$m && golangci-lint run ./... --timeout $(LINT_TIMEOUT)) || exit 1; \
	done
	@echo "✅ Linter checks passed"

# Module boundary enforcement (docs/planning/done/DONE_MODULE_BOUNDARIES_PLAN.md
# §3): the dependency flow is one-way. This repository consumes the wire
# contract and the crypto primitives, and must NEVER reach into chain
# internals — keeper, client, module wiring, app or cmd. That edge is the whole
# reason x/secrets/types is a separate module: it is the only part of the chain
# the guardian is allowed to know about.
#
# Crossing it here would not fail to compile — the chain repo is public and a
# require is one command away — so this check is the only thing standing in the
# way. It replaces the proximity that made the edge obvious inside the monorepo.
verify-boundaries:
	@echo "--> Checking module dependency boundaries..."
	@bad=$$(go list -deps ./... | \
		grep -E '^github.com/timeflareio/chain(/|$$)' | \
		grep -v '^github.com/timeflareio/chain/x/secrets/types$$' || true); \
	if [ -n "$$bad" ]; then \
		echo "❌ guardian imports chain internals — only x/secrets/types is permitted:"; \
		echo "$$bad"; exit 1; \
	fi
	@echo "✅ Module boundary respected"

# Combined quality checks (read-only mode)
# go-govulncheck runs separately (advisory) — see the verify target note.
go-quality-check: go-format-check go-lint-check go-vet
	@echo "🎉 All code quality checks passed!"

.PHONY: go-format go-lint go-imports go-imports-check go-vet go-govulncheck go-format-check go-lint-check go-quality-check verify-boundaries