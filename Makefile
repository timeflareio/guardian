# Guardian Module Makefile
# Independent guardian service with its own build system

# Load common foundation
include make/common.mk

# Load shared capabilities
include make/go-testing.mk
include make/go-quality.mk
include make/go-build.mk
include make/cleanup.mk

# Project configuration
APPNAME := guardiand

# Default target - show help
.DEFAULT_GOAL := help

##################
###  Guardian  ###
##################

## show guardian configuration help
config-help:
	@echo "⚙️  Guardian Configuration"
	@echo "=========================="
	@echo ""
	@echo "Initialize configuration:"
	@echo "  $(APPNAME) config init --key-name <guardian-key>"
	@echo ""
	@echo "View configuration:"
	@echo "  $(APPNAME) config show"
	@echo ""
	@echo "Set configuration values:"
	@echo "  $(APPNAME) config set <key> <value>"
	@echo ""
	@echo "Register guardian:"
	@echo "  $(APPNAME) register --stake-amount 10000000000uveil"
	@echo ""
	@echo "Start guardian service:"
	@echo "  $(APPNAME) start"

## show guardian status and version
status:
	@echo "📊 Guardian Status"
	@echo "=================="
	@echo "Version: $(VERSION)"
	@echo "Binary: $(APPNAME)"
	@echo "Build date: $(shell date)"
	@echo ""
	@if command -v $(APPNAME) >/dev/null 2>&1; then \
		echo "✅ Guardian binary is installed and available"; \
		$(APPNAME) version; \
	else \
		echo "❌ Guardian binary not found. Run 'make build' to install."; \
	fi

###############################################################################
###                              Testing                                   ###
###############################################################################

## run all guardian tests
test: go-test-unit
	@echo "✅ All guardian tests passed!"

## run unit tests only
test-unit: go-test-unit

## run benchmarks
bench: go-bench

###############################################################################
###                              Build                                     ###
###############################################################################

## build and install guardian binary
build: go-install

## install binary to GOPATH/bin
install: go-install

## build binary without installing
build-binary: go-build-binary

###############################################################################
###                           Code Quality                                 ###
###############################################################################

## format and lint all code (with fixes)
clean: go-format go-lint go-imports go-vet

## verify all code quality standards (read-only checks)
verify: go-format-check go-lint-check go-imports-check go-vet verify-boundaries verify-pins vectors-verify

## format Go code
format: go-format

## check code format
format-check: go-format-check

## run linter with fixes
lint: go-lint

## check linter without fixes
lint-check: go-lint-check

## organize imports
imports: go-imports

## check import organization
imports-check: go-imports-check

## run go vet
vet: go-vet

###############################################################################
###                       Carried dependency pins                          ###
###############################################################################

# Go honours `replace` directives ONLY in the main module being built — a
# dependency's replaces are ignored entirely. So the chain's carried pins do
# nothing for this binary: guardiand must carry an identical block itself, or the
# two daemons silently build against different cosmos-sdk versions and diverge in
# wire behaviour. Inside the monorepo that was enforced by proximity. Here it
# spans two repositories, so it has to be checked.
#
# The check compares the actual replace DIRECTIVES, not a version stamp. A stamp
# is a claim about the block; the directives are the block, and a claim can be
# stale in either direction — a pin bumped without restamping, or a stamp moved
# without a bump. Comparing the real thing cannot be fooled.
#
# The comparison target is x/secrets/types at the version this module requires,
# because that is the module guardiand actually builds against.

## verify the carried pin block matches the chain's at the pinned types version
verify-pins:
	@set -e; \
	dep="github.com/timeflareio/chain/x/secrets/types"; \
	ver=$$(go list -m -f '{{.Version}}' "$$dep" 2>/dev/null); \
	if [ -z "$$ver" ]; then \
		echo "❌ could not determine the required version of $$dep"; exit 1; \
	fi; \
	echo "--> Comparing carried pins against $$dep@$$ver"; \
	theirs_mod=$$(go mod download -json "$$dep@$$ver" \
		| sed -n 's/.*"GoMod": *"\([^"]*\)".*/\1/p'); \
	if [ -z "$$theirs_mod" ] || [ ! -f "$$theirs_mod" ]; then \
		echo "❌ could not fetch the go.mod for $$dep@$$ver"; exit 1; \
	fi; \
	norm() { awk '/^replace \(/{b=1;next} b&&/^\)/{b=0;next} \
		{ line=$$0; sub(/\/\/.*/,"",line); gsub(/^[ \t]+|[ \t]+$$/,"",line); \
		  if (line=="") next; \
		  if (b) print line; \
		  else if (line ~ /^replace [^(]/) { sub(/^replace /,"",line); print line } }' "$$1" \
		| sort; }; \
	norm go.mod > /tmp/pins-ours.$$$$; \
	norm "$$theirs_mod" > /tmp/pins-theirs.$$$$; \
	if ! diff -u /tmp/pins-theirs.$$$$ /tmp/pins-ours.$$$$ \
		--label "chain ($$dep@$$ver)" --label "guardian (this module)"; then \
		rm -f /tmp/pins-ours.$$$$ /tmp/pins-theirs.$$$$; \
		echo ""; \
		echo "❌ carried pins DIVERGED from the chain."; \
		echo "   Go ignores a dependency's replaces, so a mismatch means guardiand and"; \
		echo "   timeflared build against different dependency versions — a wire-behaviour"; \
		echo "   divergence that no test here would catch."; \
		echo "   Fix: mirror the chain's block exactly (see docs/guides/DEPENDENCIES.md"; \
		echo "   in the chain repository), or bump the required types version."; \
		exit 1; \
	fi; \
	rm -f /tmp/pins-ours.$$$$ /tmp/pins-theirs.$$$$; \
	echo "✅ Carried pins match the chain"

###############################################################################
###                        Chain vector corpus                             ###
###############################################################################

# This daemon asserts two of the chain's CHAIN-SEMANTICS vectors —
# wallet_derivation (key path layout) and client_conventions (mnemonic
# handling). The chain owns them; this is a vendored pinned copy so the tests
# stay offline and hermetic, exactly as §5.2 of the migration plan describes.
#
# The pin is a chain TAG and the files are fetched from the tagged source tree
# rather than from a release asset. That works today, needs no release workflow
# in the chain repo, and the tag is itself the integrity guarantee — a tag is
# immutable unless someone moves it, which is the same trust assumption a
# release asset carries. If the chain later publishes a vectors tarball, this can
# switch to it, but nothing here requires that.
#
# The five PRIMITIVE vectors are a different corpus, owned and published by
# timeflareio/crypto. This daemon asserts none of them — it consumes the
# primitives as a Go module and lets that module's own suites prove them.

CHAIN_VECTORS_VERSION := $(shell cat testdata/vectors/CHAIN_VECTORS_VERSION 2>/dev/null)
CHAIN_VECTORS_FILES   := wallet_derivation client_conventions
CHAIN_RAW             := https://raw.githubusercontent.com/timeflareio/chain

## verify the vendored chain vectors match the pinned chain tag
vectors-verify:
	@set -e; \
	if [ -z "$(CHAIN_VECTORS_VERSION)" ]; then \
		echo "❌ testdata/vectors/CHAIN_VECTORS_VERSION is missing or empty"; exit 1; \
	fi; \
	echo "--> Verifying vendored chain vectors against chain@$(CHAIN_VECTORS_VERSION)"; \
	tmp=$$(mktemp -d); trap 'rm -rf "$$tmp"' EXIT; \
	fail=0; \
	for v in $(CHAIN_VECTORS_FILES); do \
		if ! curl -sSfL --retry 3 --retry-delay 2 --retry-all-errors \
			-o "$$tmp/$$v.json" \
			"$(CHAIN_RAW)/$(CHAIN_VECTORS_VERSION)/testdata/vectors/$$v.json"; then \
			echo "❌ could not fetch $$v.json from chain@$(CHAIN_VECTORS_VERSION)"; fail=1; continue; \
		fi; \
		if ! cmp -s "$$tmp/$$v.json" "testdata/vectors/$$v.json"; then \
			echo "❌ $$v.json differs from chain@$(CHAIN_VECTORS_VERSION)"; fail=1; \
		fi; \
	done; \
	if [ $$fail -ne 0 ]; then \
		echo "   Run 'make vectors-sync' — never hand-edit testdata/vectors/."; \
		echo "   The chain owns these files; a local edit means this daemon is"; \
		echo "   asserting conventions the chain does not implement."; \
		exit 1; \
	fi; \
	echo "✅ Vendored chain vectors match chain@$(CHAIN_VECTORS_VERSION)"

## refresh the vendored chain vectors from a chain tag (CHAIN_VECTORS_VERSION=vX.Y.Z)
vectors-sync:
	@set -e; \
	case "$(CHAIN_VECTORS_VERSION)" in \
		v*) ;; \
		*) echo "❌ pass a chain tag, e.g. make vectors-sync CHAIN_VECTORS_VERSION=v0.1.0"; exit 1;; \
	esac; \
	echo "--> Syncing chain vectors from chain@$(CHAIN_VECTORS_VERSION)"; \
	for v in $(CHAIN_VECTORS_FILES); do \
		curl -sSfL --retry 3 --retry-delay 2 --retry-all-errors \
			-o "testdata/vectors/$$v.json" \
			"$(CHAIN_RAW)/$(CHAIN_VECTORS_VERSION)/testdata/vectors/$$v.json"; \
	done; \
	echo "$(CHAIN_VECTORS_VERSION)" > testdata/vectors/CHAIN_VECTORS_VERSION
	@echo "✅ Synced — review the diff, then run 'make test'"

###############################################################################
###                           Cleanup                                      ###
###############################################################################

## clean temporary files and build artifacts
clean-all: clean-temps clean-coverage clean-build

## clean temporary files and logs
clean-temp: clean-temps

## clean coverage artifacts
clean-cov: clean-coverage

## clean build artifacts
clean-bin: clean-build


###############################################################################
###                           Dependencies                                 ###
###############################################################################

## update this module's dependencies to latest patch versions (T1 only)
update-deps: mod-update-patch

## clean and verify Go modules (download, tidy, verify)
tidy: mod-clean

## download Go modules
download: mod-clean

## show module information
info: mod-info

.PHONY: config-help status test test-unit bench
.PHONY: build install build-binary clean verify format format-check lint lint-check imports imports-check vet
.PHONY: verify-pins vectors-verify vectors-sync
.PHONY: clean-all clean-temp clean-cov clean-bin clean-cache update-deps tidy download info