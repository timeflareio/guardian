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
CTLNAME := guardianctl

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
	@echo "  $(CTLNAME) config init --key-name <guardian-key>"
	@echo ""
	@echo "View configuration:"
	@echo "  $(CTLNAME) config show"
	@echo ""
	@echo "Set configuration values:"
	@echo "  $(CTLNAME) config set <key> <value>"
	@echo ""
	@echo "Register guardian:"
	@echo "  $(CTLNAME) register --stake-amount 10000000000uveil"
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
verify: go-format-check go-lint-check go-imports-check go-vet verify-boundaries verify-pins

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
###                        Vulnerability gating                            ###
###############################################################################

# `go-govulncheck` (make/go-quality.mk) reports everything and exits non-zero on
# any reachable finding. That is the right behaviour for a human reading output,
# but useless as a gate here: this module carries a permanently-unfixable
# advisory (see .govulncheck-accepted), so the raw target can never pass.
#
# This target is the gate. It fails only on reachable findings that are NOT
# accepted, and prints a reminder for each accepted one that is still triggering —
# so an accepted advisory stays visible rather than becoming invisible. Without
# it, .govulncheck-accepted would be a file nothing reads: a documented gate that
# does not exist, which is worse than no gate at all.

## fail only on reachable vulnerabilities that are not in .govulncheck-accepted
govulncheck-gated:
	@command -v govulncheck >/dev/null 2>&1 || go install golang.org/x/vuln/cmd/govulncheck@latest
	@command -v jq >/dev/null 2>&1 || { echo "❌ jq is required for the gated scan"; exit 1; }
	@found=$$(govulncheck -format json ./... | \
		jq -r 'select(.finding != null and .finding.trace[0].function != null) | .finding.osv' | sort -u); \
	accepted=$$(grep -E '^GO-' .govulncheck-accepted 2>/dev/null || true); \
	remaining=""; \
	for id in $$found; do \
		echo "$$accepted" | grep -qxF "$$id" || remaining="$$remaining $$id"; \
	done; \
	if [ -n "$$remaining" ]; then \
		echo "❌ Reachable vulnerabilities not accepted:$$remaining"; \
		echo "   Fix by bumping the affected module. Only if genuinely unfixable, add"; \
		echo "   to .govulncheck-accepted with a reason, the reachability path, and an"; \
		echo "   exit condition — and check the path really is this module's, not one"; \
		echo "   copied from another repository."; \
		echo "   Details:"; \
		govulncheck ./... || true; \
		exit 1; \
	fi; \
	for id in $$found; do \
		echo "  ⚠️  accepted advisory triggered: $$id (see .govulncheck-accepted)"; \
	done; \
	echo "✅ No unaccepted reachable vulnerabilities"

###############################################################################
###                        Chain vector corpus                             ###
###############################################################################

# This daemon asserts two of the chain's CHAIN-SEMANTICS vectors —
# wallet_derivation (key path layout) and client_conventions (mnemonic
# handling). The chain owns them; this is a vendored pinned copy so the tests
# stay offline and hermetic, exactly as §5.2 of the migration plan describes.
#
# The pin is a chain TAG, and the files come from that release's vectors tarball
# and its per-file SHA-256 manifest — the same mechanism the SDK uses for both of
# its corpora, so there is one way to consume vectors across the whole graph
# rather than two.
#
# Fetching from the tagged source tree instead would work and needs no release,
# but it couples this repository to the chain's internal directory layout
# (`testdata/vectors/…`), which nothing promises to keep stable. A release asset
# is a published contract; a path inside someone else's tree is not. The manifest
# also states the expected digest independently of the file, so a truncated or
# substituted download fails loudly rather than comparing equal to itself.
#
# The cost is real and worth naming: a chain tag is only pinnable here if it
# carries a release. chain v0.0.1 does not, which is why the floor is v0.0.2.
#
# The chain-owned vectors this daemon asserts — wallet_derivation and
# client_conventions — travel inside the x/secrets/types module it already
# requires, so there is nothing to sync and nothing to verify here. go.mod is
# the pin, and the Go checksum database is the integrity check.
#
# The primitive vectors are a different corpus, owned by timeflareio/crypto.
# This daemon asserts none of them — it consumes the primitives as a Go module
# and lets that module's own suites prove them.

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
.PHONY: verify-pins govulncheck-gated
.PHONY: clean-all clean-temp clean-cov clean-bin clean-cache update-deps tidy download info