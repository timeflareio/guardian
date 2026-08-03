# Go Testing Framework
# Internal testing routines - use root Makefile targets for public interface

# Run unit tests
# -checklinkname=0: github.com/bytedance/sonic/loader (pulled via
# cosmossdk.io/log) uses a go:linkname that Go 1.24's linker rejects but
# 1.25+ allows; disable the check so tests link under the pinned toolchain.
go-test-unit:
	@echo "Running unit tests..."
	@go test -mod=readonly -v -race -ldflags=-checklinkname=0 -timeout $(TEST_TIMEOUT) -coverprofile=$(COVERAGE_FILE) -covermode=atomic $(TEST_PACKAGES)
	@go tool cover -html=$(COVERAGE_FILE) -o $(COVERAGE_HTML_FILE)
	@rm -f $(COVERAGE_FILE)
	@for m in $(GO_SUBMODULE_DIRS); do \
		echo "Running unit tests in $$m..."; \
		(cd $$m && go test -mod=readonly -race -ldflags=-checklinkname=0 -timeout $(TEST_TIMEOUT) ./...) || exit 1; \
	done

# Run unit tests with benchmarking
go-bench:
	@echo "Running unit tests with benchmarking..."
	@go test -mod=readonly -v -timeout $(TEST_TIMEOUT) -bench=. $(TEST_PACKAGES)

.PHONY: go-test-unit go-bench