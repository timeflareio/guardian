# Common Makefile Infrastructure
# Shared variables, shell setup, colors, and utilities used across all Makefiles

# Git and version information
BRANCH := $(shell git rev-parse --abbrev-ref HEAD)
COMMIT := $(shell git log -1 --format='%H')

# Determine version from git tags or branch/commit
ifeq (,$(VERSION))
  VERSION := $(shell git describe --exact-match 2>/dev/null)
  # if VERSION is empty, then populate it with branch name and raw commit hash
  ifeq (,$(VERSION))
    VERSION := $(BRANCH)-$(COMMIT)
  endif
endif

# Build flags for Go applications
# Note: APPNAME should be set by the including Makefile
ldflags = -X github.com/cosmos/cosmos-sdk/version.Name=$(APPNAME) \
	-X github.com/cosmos/cosmos-sdk/version.AppName=$(APPNAME) \
	-X github.com/cosmos/cosmos-sdk/version.Version=$(VERSION) \
	-X github.com/cosmos/cosmos-sdk/version.Commit=$(COMMIT)

BUILD_FLAGS := -ldflags '$(ldflags)'

# Shell configuration
SHELL := /bin/bash

# Terminal colors for consistent output
GREEN  := $(shell tput -Txterm setaf 2)
YELLOW := $(shell tput -Txterm setaf 3)
BLUE   := $(shell tput -Txterm setaf 4)
RED    := $(shell tput -Txterm setaf 1)
WHITE  := $(shell tput -Txterm bold)$(shell tput -Txterm setaf 7)
RESET  := $(shell tput -Txterm sgr0)

# Help system configuration
TARGET_MAX_CHAR_NUM=22

# Grouped help: a '##@ Section' comment assigns every following '## described'
# target (in that file) to the named section; targets in the same section merge
# across files. HELP_SECTION_ORDER ('|'-separated) fixes the section order;
# unlisted sections print afterwards in first-seen order, and targets with no
# section land under 'Targets' — so Makefiles without markers keep a flat list.
HELP_SECTION_ORDER ?=

# Universal help target that works with any Makefile
##@ Misc

## show available targets and their descriptions
help:
	@echo ''
	@echo 'Usage:'
	@echo '  ${YELLOW}make${RESET} ${GREEN}<target>${RESET}'
	@awk 'FNR == 1 { section = "" } \
	/^##@ / { section = substr($$0, 5) } \
	/^[a-zA-Z\-\_0-9%]+:/ { \
		if (match(lastLine, /^## (.*)/)) { \
			helpCommand = substr($$1, 1, index($$1, ":")-1); \
			helpMessage = substr(lastLine, RSTART + 3, RLENGTH); \
			s = (section == "" ? "Targets" : section); \
			if (!(s in bodies)) { seen[++n] = s } \
			bodies[s] = bodies[s] sprintf("  ${YELLOW}%-$(TARGET_MAX_CHAR_NUM)s${RESET} ${GREEN}%s${RESET}\n", helpCommand, helpMessage); \
		} \
	} \
	{ lastLine = $$0 } \
	END { \
		nPref = split("$(HELP_SECTION_ORDER)", pref, "|"); \
		for (i = 1; i <= nPref; i++) emit(pref[i]); \
		for (i = 1; i <= n; i++) emit(seen[i]); \
	} \
	function emit(s) { \
		if (s in bodies) { \
			printf "\n${WHITE}%s${RESET}\n%s", s, bodies[s]; \
			delete bodies[s]; \
		} \
	}' $(MAKEFILE_LIST)

# Common file path patterns for exclusions
VENDOR_PATHS := -not -path './vendor/*' -not -path './third_party/*'
VENDOR_PATHS_GUARDIAN := -not -path './vendor/*'

# Default timeout configurations (can be overridden)
TEST_TIMEOUT ?= 5m
LINT_TIMEOUT ?= 15m

# Default test packages (can be overridden)
TEST_PACKAGES ?= ./...

# Nested leaf library modules owned by the root Makefile — set there, empty
# for other includers (guardian's Makefile shares these .mk files but has no
# nested modules). `go <cmd> ./...` from the repo root does NOT descend into
# these — every quality/test/deps target must iterate them explicitly
# (docs/planning/done/DONE_MODULE_BOUNDARIES_PLAN.md §4 Phase 3).
GO_SUBMODULE_DIRS ?=

# Coverage file configuration
COVERAGE_FILE ?= coverage.out
COVERAGE_HTML_FILE ?= coverage.html

.PHONY: help