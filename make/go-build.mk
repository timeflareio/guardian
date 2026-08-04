# Go Build Patterns
# Internal build routines - use root Makefile targets for public interface

# Build configuration (can be overridden)
OUTPUT_DIR ?= bin
CMD_PATH ?= ./cmd/$(APPNAME)

# Every binary this module ships. APPNAME stays the daemon, because it is what
# `make status` and the help text talk about; BINARIES is what actually gets
# built, so a new binary is added in one place.
BINARIES ?= guardiand guardianctl

# Verify module dependencies have not been modified
go-verify-deps:
	@go mod verify >/dev/null
	@echo "✅ Module dependencies verified"

# Install binary to GOPATH/bin
go-install: go-verify-deps
	@for b in $(BINARIES); do \
		echo "--> installing $$b"; \
		go install $(BUILD_FLAGS) -mod=readonly ./cmd/$$b || exit 1; \
	done

# Build binary without installing
go-build-binary: go-verify-deps
	@mkdir -p $(OUTPUT_DIR)
	@for b in $(BINARIES); do \
		echo "--> building $$b"; \
		go build $(BUILD_FLAGS) -mod=readonly -o $(OUTPUT_DIR)/$$b ./cmd/$$b || exit 1; \
	done

# Module cleanup and verification
mod-clean:
	@echo "--> downloading Go modules"
	@go mod download
	@echo "--> tidying Go modules"
	@go mod tidy
	@echo "--> ensure dependencies have not been modified"
	@go mod verify

# Build information
build-info:
	@echo "📦 Build Information"
	@echo "===================="
	@echo "App Name: $(APPNAME)"
	@echo "Version:  $(VERSION)"
	@echo "Branch:   $(BRANCH)"
	@echo "Commit:   $(COMMIT)"
	@echo "Go Version: $$(go version)"
	@echo "Build Flags: $(BUILD_FLAGS)"

# Check if binary exists and show its info
binary-info:
	@if command -v $(APPNAME) >/dev/null 2>&1; then \
		echo "✅ $(APPNAME) binary is installed and available"; \
		which $(APPNAME); \
		$(APPNAME) version 2>/dev/null || echo "Version command not available"; \
	else \
		echo "❌ $(APPNAME) binary not found. Run 'make build' to install."; \
	fi

# Show binary size if built locally
binary-size:
	@if [ -f "$(OUTPUT_DIR)/$(APPNAME)" ]; then \
		echo "📏 $(APPNAME) binary size: $$(du -h $(OUTPUT_DIR)/$(APPNAME) | cut -f1)"; \
	else \
		echo "❌ $(APPNAME) binary not found in $(OUTPUT_DIR)/. Run 'make build-binary' first."; \
	fi

# Build artifact cleanup is handled by cleanup.mk

.PHONY: go-install go-build-binary go-verify-deps mod-clean
.PHONY: build-info binary-info binary-size