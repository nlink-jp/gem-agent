BINARY  := gem-agent
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -ldflags "-X main.version=$(VERSION)"
DIST_DIR := dist

# macOS Developer ID signing / notarization (see nlink-jp/.github
# CONVENTIONS.md §Code Signing). Defaults match any Developer ID
# Application cert in the keychain and the org-standard notary
# profile. Builds without these fall back to ad-hoc / un-notarized
# with a one-line warning — see scripts/codesign-darwin.sh.
CODESIGN_IDENTITY ?= Developer ID Application
NOTARY_PROFILE    ?= nlink-jp-notary

.PHONY: build build-all package test vet check clean

build:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY) .
	@scripts/codesign-darwin.sh $(DIST_DIR)/$(BINARY) "$(CODESIGN_IDENTITY)"

# gem-agent is macOS-only by design (sandbox-exec based isolation);
# darwin ships arm64 only per the org Release Archive Standard.
build-all:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-darwin-arm64 .
	@scripts/codesign-darwin.sh $(DIST_DIR)/$(BINARY)-darwin-arm64 "$(CODESIGN_IDENTITY)" "$(BINARY)"

## package: Archive the darwin build with the canonical binary name +
## README.md + LICENSE, then notarize. Asset naming follows the org
## Release Archive Standard (gem-agent-vX.Y.Z-darwin-arm64.zip).
package: build-all
	@cd $(DIST_DIR) && \
		stage=_pkg; rm -rf $$stage; mkdir -p $$stage; \
		cp "$(BINARY)-darwin-arm64" "$$stage/$(BINARY)"; \
		cp ../README.md ../LICENSE $$stage/; \
		( cd $$stage && zip -q "../$(BINARY)-$(VERSION)-darwin-arm64.zip" * ); \
		rm -rf $$stage
	@scripts/notarize-darwin.sh $(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

test:
	go test ./...

vet:
	go vet ./...

check: vet test build

clean:
	rm -rf $(DIST_DIR)

# Homebrew tap generation (see scripts/release-brew.mk). After `make package`,
# `make brew` generates this formula from the built darwin-arm64 zip into the
# local nlink-jp/homebrew-tap checkout. The package target is unchanged.
BREW_KIND := formula
BREW_DESC := Interactive CLI agent on Vertex AI Gemini (Claude Code fallback)
include scripts/release-brew.mk
