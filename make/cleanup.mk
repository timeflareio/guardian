# Cleanup and Maintenance Utilities
# Internal cleanup routines - use root Makefile targets for public interface

# Clean temporary files and logs
clean-temps:
	@echo "🧹 Cleaning temporary files and logs..."
	@find . -name "*.log" -type f -not -path './vendor/*' -not -path './third_party/*' -delete || true
	@find . -name "*.pid" -type f -delete || true
	@if [ -n "$(ADDITIONAL_CLEAN_PATHS)" ]; then \
		echo "🧹 Cleaning additional project paths..."; \
		for path in $(ADDITIONAL_CLEAN_PATHS); do \
			if [ -e "$$path" ]; then \
				echo "Removing $$path"; \
				rm -rf "$$path"; \
			fi; \
		done; \
	fi
	@echo "✅ Temporary files cleaned!"

# Clean coverage artifacts
clean-coverage:
	@echo "🧹 Cleaning coverage files..."
	@rm -f $(COVERAGE_FILE) $(COVERAGE_HTML_FILE) || true
	@echo "✅ Coverage files cleaned!"

# Clean build artifacts
clean-build:
	@echo "🧹 Cleaning build artifacts..."
	@rm -rf $(OUTPUT_DIR) || true
	@echo "✅ Build artifacts cleaned!"

# Patch-level module update for the current module. Deliberately -u=patch:
# blanket `go get -u ./...` walks consensus-stack minors (cosmos-sdk, cometbft,
# iavl) in one undifferentiated sweep — those are T2 upgrades with their own
# runbook (docs/guides/DEPENDENCIES.md), never batch-updated.
mod-update-patch:
	@echo "📦 Updating $$(go list -m) to latest patch versions..."
	@go get -u=patch ./...
	@go mod tidy
	@echo "✅ Patch-level updates applied"

# Show module information
mod-info:
	@echo "📦 Module Information"
	@echo "===================="
	@echo "Module: $$(go list -m)"
	@echo "Go version: $$(go version)"
	@echo "Dependencies:"
	@go list -m all

# Add project-specific paths to this variable in including Makefiles
ADDITIONAL_CLEAN_PATHS ?=

.PHONY: clean-temps clean-coverage clean-build mod-update-patch mod-info