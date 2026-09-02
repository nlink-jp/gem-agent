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

.PHONY: build build-all package verify-release test vet lint docs-check check clean

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

# verify-release inspects the packaged zip from INSIDE the repo: the
# ad-hoc "cd scratch && unzip && gh release ..." chain stranded gh in a
# non-repo cwd three releases in a row. Run this, then gh from here.
# The notarization marker is the gate, written by notarize-darwin.sh
# only on "status: Accepted". spctl is shown for information but NOT
# gated on: piped through head it cannot fail the chain anyway (the
# pipeline's exit status is head's), and its online ticket lookup can
# lag a fresh submission. An un-notarised zip once shipped with this
# target green — the probe failed on an updated Apple agreement, the
# script failed open by design, and nothing here checked.
verify-release:
	@test -f "$(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" || { \
		echo "verify-release: FAIL — $(BINARY)-$(VERSION)-darwin-arm64.zip has no notarization marker."; \
		echo "  make package must end with '[notarize] ...: Accepted'. Do not upload this zip."; \
		exit 1; }
	@test "$(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" -nt "$(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip" || { \
		echo "verify-release: FAIL — the zip was rebuilt after its marker (re-run make package)."; \
		exit 1; }
	@tmp=$$(mktemp -d) && \
		unzip -oq "$(DIST_DIR)/$(BINARY)-$(VERSION)-darwin-arm64.zip" -d "$$tmp" && \
		"$$tmp/$(BINARY)" --version && \
		spctl -a -vv -t install "$$tmp/$(BINARY)" 2>&1 | head -2 || true; \
		rm -rf "$$tmp"
	@echo "verify-release: OK ($(VERSION), notarization marker present)"

test:
	go test ./...

vet:
	go vet ./...

## lint: golangci-lint with the org config (.golangci.yml). errcheck is
## on everywhere except writes to the CLI's own streams, so an ignored
## error has to be written as one — an unchecked Close on a file this
## tool wrote is a real defect class, not noise.
lint:
	golangci-lint run ./...

## docs-check: docs/en and docs/ja must be full structural mirrors. A
## missing translation is invisible in review — it looks exactly like a
## document nobody has written yet — so it is checked mechanically.
docs-check:
	@scripts/docs-mirror-check.sh

check: vet lint test docs-check build

clean:
	rm -rf $(DIST_DIR)

# Homebrew tap generation (see scripts/release-brew.mk). After `make package`,
# `make brew` generates this formula from the built darwin-arm64 zip into the
# local nlink-jp/homebrew-tap checkout. The package target is unchanged.
BREW_KIND := formula
BREW_DESC := Interactive CLI agent runtime on Vertex AI Gemini
include scripts/release-brew.mk
