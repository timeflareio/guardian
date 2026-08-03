# Go Build Patterns
# Internal build routines - use root Makefile targets for public interface

# Build configuration (can be overridden)
OUTPUT_DIR ?= bin
CMD_PATH ?= ./cmd/$(APPNAME)

# Verify module dependencies have not been modified
go-verify-deps:
	@go mod verify >/dev/null
	@echo "✅ Module dependencies verified"

# Install binary to GOPATH/bin
go-install: go-verify-deps
	@echo "--> installing $(APPNAME)"
	@go install $(BUILD_FLAGS) -mod=readonly $(CMD_PATH)

# Build binary without installing
go-build-binary: go-verify-deps
	@echo "--> building $(APPNAME) binary"
	@mkdir -p $(OUTPUT_DIR)
	@go build $(BUILD_FLAGS) -mod=readonly -o $(OUTPUT_DIR)/$(APPNAME) $(CMD_PATH)

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