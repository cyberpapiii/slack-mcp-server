# If you update this file, please follow
# https://suva.sh/posts/well-documented-makefiles

.DEFAULT_GOAL := help

TAG ?=
RELEASE_REMOTE ?= personal
GO=go
PACKAGE = $(shell go list -m)
GIT_COMMIT_HASH = $(shell git rev-parse HEAD)
GIT_VERSION = $(shell git describe --tags --always --dirty)
BUILD_TIME = $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
BINARY_NAME = slack-mcp-server
AUTH_BINARY_NAME = slack-mcp-auth
LOCAL_SIGNING_IDENTITY ?= Plug Local Signing
LOCAL_SIGNING_IDENTIFIER ?= com.robdezendorf.slack-mcp.oauth
LD_FLAGS = -s -w \
	-X '$(PACKAGE)/pkg/version.CommitHash=$(GIT_COMMIT_HASH)' \
	-X '$(PACKAGE)/pkg/version.Version=$(GIT_VERSION)' \
	-X '$(PACKAGE)/pkg/version.BuildTime=$(BUILD_TIME)' \
	-X '$(PACKAGE)/pkg/version.BinaryName=$(BINARY_NAME)'
COMMON_BUILD_ARGS = -ldflags "$(LD_FLAGS)"

NPM_VERSION = $(shell git describe --tags --always | sed 's/^v//' | cut -d- -f1)
OSES = darwin linux windows
ARCHS = amd64 arm64

CLEAN_TARGETS :=
CLEAN_TARGETS += '$(BINARY_NAME)'
CLEAN_TARGETS += './build/$(AUTH_BINARY_NAME)'
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,)))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./build/extension.dxt/server/$(BINARY_NAME)-$(os)-$(arch)))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(BINARY_NAME)-$(os)-$(arch)/bin/))
CLEAN_TARGETS += $(foreach os,$(OSES),$(foreach arch,$(ARCHS),./npm/$(BINARY_NAME)-$(os)-$(arch)/.npmrc))
# Note: ./npm/slack-mcp-server/{LICENSE,README.md} are deliberately NOT cleaned.
# They are copied in by `npm-publish` with a plain `cp`, which overwrites them on
# the next run, and `clean` should not rm -rf paths that look like source files.
CLEAN_TARGETS += ./npm/slack-mcp-server/.npmrc build/extension.dxt/manifest.json build/extension.dxt/icon.png
CLEAN_TARGETS += ./build/slack-mcp-server.dxt ./build/slack-mcp-server-$(NPM_VERSION).dxt

# help: targets with `##` descriptions; categories via `##@`. Recipe names may include / . -
.PHONY: help
help: ## Display this help
	@awk 'BEGIN {FS = ":.*##"; printf "\nUsage:\n  make \033[36m<target>\033[0m\n"} /^[a-zA-Z_0-9\/\.-]+:.*?##/ { printf "  \033[36m%-21s\033[0m %s\n", $$1, $$2 } /^##@/ { printf "\n\033[1m%s\033[0m\n", substr($$0, 5) } ' $(MAKEFILE_LIST)

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(CLEAN_TARGETS)

.PHONY: build
build: clean ## Build the project (read-only: run `make prepare` for tidy+format)
	go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME) ./cmd/slack-mcp-server
	go build -o ./build/$(AUTH_BINARY_NAME) ./cmd/slack-mcp-auth

.PHONY: build-auth
build-auth: ## Build the local Slack OAuth helper
	go build -o ./build/$(AUTH_BINARY_NAME) ./cmd/slack-mcp-auth

.PHONY: build-all-platforms
build-all-platforms: clean ## Build the project for all platforms (read-only: run `make prepare` for tidy+format)
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		GOOS=$(os) GOARCH=$(arch) go build $(COMMON_BUILD_ARGS) -o ./build/$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,) ./cmd/slack-mcp-server; \
	))

.PHONY: build-dxt
build-dxt: ## Build DXT extension
	mkdir -p ./build/extension.dxt/server
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		EXECUTABLE=$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,); \
		DIRNAME=$(BINARY_NAME)-$(os)-$(arch); \
		cp ./build/$$EXECUTABLE ./build/extension.dxt/server/; \
	))
	cp npm/slack-mcp-server/bin/index.js ./build/extension.dxt/server/
	cp images/icon.png ./build/extension.dxt/
	jq '.version = "$(NPM_VERSION)"' ./manifest-dxt.json > tmp.json && mv tmp.json ./build/extension.dxt/manifest.json;
	chmod +x build/extension.dxt/server/slack-mcp-server-*
	dxt pack build/extension.dxt/ build/slack-mcp-server-${NPM_VERSION}.dxt
	cp build/slack-mcp-server-${NPM_VERSION}.dxt build/slack-mcp-server.dxt

.PHONY: npm-copy-binaries
npm-copy-binaries: build-all-platforms ## Copy the binaries to each npm package
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		EXECUTABLE=$(BINARY_NAME)-$(os)-$(arch)$(if $(findstring windows,$(os)),.exe,); \
		DIRNAME=$(BINARY_NAME)-$(os)-$(arch); \
		mkdir -p ./npm/$$DIRNAME/bin; \
		cp ./build/$$EXECUTABLE ./npm/$$DIRNAME/bin/; \
	))

.PHONY: npm-publish
npm-publish: npm-copy-binaries ## Publish the npm packages
	$(foreach os,$(OSES),$(foreach arch,$(ARCHS), \
		DIRNAME="$(BINARY_NAME)-$(os)-$(arch)"; \
		cd npm/$$DIRNAME; \
		echo '//registry.npmjs.org/:_authToken=$(NPM_TOKEN)' >> .npmrc; \
		jq '.version = "$(NPM_VERSION)"' package.json > tmp.json && mv tmp.json package.json; \
		npm publish; \
		cd ../..; \
	))
	cp README.md LICENSE ./npm/slack-mcp-server/
	echo '//registry.npmjs.org/:_authToken=$(NPM_TOKEN)' >> ./npm/slack-mcp-server/.npmrc
	jq '.version = "$(NPM_VERSION)"' ./npm/slack-mcp-server/package.json > tmp.json && mv tmp.json ./npm/slack-mcp-server/package.json; \
	jq '.optionalDependencies |= with_entries(.value = "$(NPM_VERSION)")' ./npm/slack-mcp-server/package.json > tmp.json && mv tmp.json ./npm/slack-mcp-server/package.json; \
	cd npm/slack-mcp-server && npm publish

.PHONY: deps
deps: ## Download dependencies
	$(GO) mod download

.PHONY: test
test: ## Run the tests (with the race detector)
	$(GO) test -count=1 -race -v -skip="Integration" ./...

.PHONY: test-integration
test-integration: ## Run integration tests
	$(GO) test -count=1 -v -run=".*Integration.*" ./...

.PHONY: lint
lint: ## Vet, check formatting and check go.mod tidiness (read-only)
	$(GO) vet ./...
	@fmt_out=$$(gofmt -l pkg cmd); if [ -n "$$fmt_out" ]; then echo "gofmt needed:"; echo "$$fmt_out"; exit 1; fi
	$(GO) mod tidy -diff

.PHONY: deploy-local
deploy-local: ## Build bin/slack-mcp-server and restart Plug's slack server (plug reload alone leaves the old process running)
	go build $(COMMON_BUILD_ARGS) -o ./bin/slack-mcp-server ./cmd/slack-mcp-server
	go build -o ./bin/slack-mcp-auth ./cmd/slack-mcp-auth
	@if security find-identity -p codesigning -v | grep -Fq '"$(LOCAL_SIGNING_IDENTITY)"'; then \
		codesign --force --sign "$(LOCAL_SIGNING_IDENTITY)" --identifier "$(LOCAL_SIGNING_IDENTIFIER)" ./bin/slack-mcp-server; \
		codesign --force --sign "$(LOCAL_SIGNING_IDENTITY)" --identifier "$(LOCAL_SIGNING_IDENTIFIER)" ./bin/slack-mcp-auth; \
		echo "Signed local binaries with $(LOCAL_SIGNING_IDENTITY)"; \
	else \
		echo "WARNING: $(LOCAL_SIGNING_IDENTITY) unavailable; Keychain may prompt after rebuild"; \
	fi
	@echo "Built ./bin/slack-mcp-server"
	@echo "Built ./bin/slack-mcp-auth"
	@strings ./bin/slack-mcp-server 2>/dev/null | grep -m1 'github.com/korotovsky/slack-mcp-server/pkg/version.CommitHash=' || true
	@if command -v plug >/dev/null 2>&1; then \
		plug server disable slack && sleep 2 && plug server enable slack \
			&& echo "Plug slack server restarted with new binary"; \
	else \
		echo "plug not in PATH: restart Plug manually"; \
	fi
	@sleep 3; NEW_PID=$$(pgrep -f 'bin/slack-mcp-server --transport' | head -1); \
	if [ -n "$$NEW_PID" ]; then \
		echo "slack-mcp-server running as PID $$NEW_PID (started $$(ps -o lstart= -p $$NEW_PID))"; \
	else \
		echo "WARNING: no slack-mcp-server process found, check plug status"; \
	fi

.PHONY: format
format: ## Format the code
	$(GO) fmt ./...

.PHONY: tidy
tidy: ## Tidy the go modules
	$(GO) mod tidy

.PHONY: prepare
prepare: tidy format ## Tidy modules and format the code (the mutating half of the old `build`)

.PHONY: release
release: ## Create release tag. Usage: make release TAG=v1.2.3 [RELEASE_REMOTE=personal]
	@if [ -z "$(TAG)" ]; then \
	  echo "Usage: make release TAG=vX.Y.Z [RELEASE_REMOTE=<remote>]"; exit 1; \
	fi
	@git remote get-url "$(RELEASE_REMOTE)" >/dev/null 2>&1 || { echo "unknown remote $(RELEASE_REMOTE); origin is upstream, set RELEASE_REMOTE to your fork"; exit 1; }
	git tag -a "$(TAG)" -m "Release $(TAG)"
	git push "$(RELEASE_REMOTE)" "$(TAG)"
