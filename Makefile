# Gates from docs/engineering.md. `make check` runs them all in order.

SHELL := /bin/bash

GO_TOOL := go tool -modfile=tools/go.mod
BIN ?= .bin
COUNT ?= 1
SH_FILES = $(shell find . -name '*.sh' -not -path './.git/*' -not -path './.bin/*' | sort)

ifeq ($(shell uname -s),Darwin)
OS := darwin
else
OS := linux
endif
ifeq ($(shell uname -m),x86_64)
ARCH := amd64
else
ARCH := arm64
endif
PLATFORM := $(OS)-$(ARCH)

# Binary tools, pinned by version and by the sha256 their release publishes.
GOLANGCI_VERSION := 2.15.0
GOLANGCI_SHA256_darwin-amd64 := 767b899a28285e88ded2823d5dcc4bc5ef681173eb2d80f029a7b17ee4ece3b8
GOLANGCI_SHA256_darwin-arm64 := d1e409481a8ca846c4bc97e781eb5cb1bd5c23353a0b3977a1cbda1b1b6447d5
GOLANGCI_SHA256_linux-amd64 := 9b7fe6a6bf6334b98decf8d86b11415ea7889afdabdd1959fdce23af2a9ebcea
GOLANGCI_SHA256_linux-arm64 := b4d8f17068b35ee6060cc2f69a16fb48740d706fbba81661c318649c114a6998

SHELLCHECK_VERSION := 0.11.0
SHELLCHECK_ASSET_darwin-amd64 := darwin.x86_64
SHELLCHECK_ASSET_darwin-arm64 := darwin.aarch64
SHELLCHECK_ASSET_linux-amd64 := linux.x86_64
SHELLCHECK_ASSET_linux-arm64 := linux.aarch64
SHELLCHECK_SHA256_darwin-amd64 := c2c15e08df0e8fbc374c335b230a7ee958c313fa5714817a59aa59f1aa594f51
SHELLCHECK_SHA256_darwin-arm64 := 339b930feb1ea764467013cc1f72d09cd6b869ebf1013296ba9055ab2ffbd26f
SHELLCHECK_SHA256_linux-amd64 := b7af85e41cc99489dcc21d66c6d5f3685138f06d34651e6d34b42ec6d54fe6f6
SHELLCHECK_SHA256_linux-arm64 := 68a8133197a50beb8803f8d42f9908d1af1c5540d4bb05fdfca8c1fa47decefc

ZIZMOR_VERSION := 1.31.1
ZIZMOR_ASSET_darwin-amd64 := x86_64-apple-darwin
ZIZMOR_ASSET_darwin-arm64 := aarch64-apple-darwin
ZIZMOR_ASSET_linux-amd64 := x86_64-unknown-linux-gnu
ZIZMOR_ASSET_linux-arm64 := aarch64-unknown-linux-gnu
ZIZMOR_SHA256_darwin-amd64 := 72ae63bb85b67229c06242f04195cb43927e9fab5f43803c6434dc701a517d61
ZIZMOR_SHA256_darwin-arm64 := ad5aa633cd56215b11e6c51d51f90c7e6b22904a75047d1215abbd422f61d698
ZIZMOR_SHA256_linux-amd64 := d185f726bdaf92df78273217dfdd24ebcd6b0ef48cf3ce776ff5762b6e138c99
ZIZMOR_SHA256_linux-arm64 := 37eebbd5762a44f262d94875431b14a6d2402991e90a685113ea97b8b41be833

# Stable paths that lefthook calls too; each links to the pinned version.
GOLANGCI := $(BIN)/golangci-lint
SHELLCHECK := $(BIN)/shellcheck
ZIZMOR := $(BIN)/zizmor
SHFMT := $(GO_TOOL) shfmt

# fetch downloads $(1), checks it against sha256 $(2) and extracts member $(3)
# to $@.
define fetch
	@test -n "$(2)" || { echo "no pinned sha256 for $(PLATFORM)" >&2; exit 1; }
	@mkdir -p $(@D)
	curl -fsSL -o $@.tar.gz $(1)
	echo "$(2)  $@.tar.gz" | shasum -a 256 -c -
	tar -xzOf $@.tar.gz $(3) >$@.tmp
	@chmod +x $@.tmp && mv $@.tmp $@ && rm $@.tar.gz
endef

GOLANGCI_PINNED := $(BIN)/golangci-lint-$(GOLANGCI_VERSION)/golangci-lint
$(GOLANGCI_PINNED):
	$(call fetch,https://github.com/golangci/golangci-lint/releases/download/v$(GOLANGCI_VERSION)/golangci-lint-$(GOLANGCI_VERSION)-$(PLATFORM).tar.gz,$(GOLANGCI_SHA256_$(PLATFORM)),golangci-lint-$(GOLANGCI_VERSION)-$(PLATFORM)/golangci-lint)
$(GOLANGCI): $(GOLANGCI_PINNED)
	@cd $(@D) && ln -sf $(patsubst $(@D)/%,%,$<) $(@F)

SHELLCHECK_PINNED := $(BIN)/shellcheck-$(SHELLCHECK_VERSION)/shellcheck
$(SHELLCHECK_PINNED):
	$(call fetch,https://github.com/koalaman/shellcheck/releases/download/v$(SHELLCHECK_VERSION)/shellcheck-v$(SHELLCHECK_VERSION).$(SHELLCHECK_ASSET_$(PLATFORM)).tar.gz,$(SHELLCHECK_SHA256_$(PLATFORM)),shellcheck-v$(SHELLCHECK_VERSION)/shellcheck)
$(SHELLCHECK): $(SHELLCHECK_PINNED)
	@cd $(@D) && ln -sf $(patsubst $(@D)/%,%,$<) $(@F)

ZIZMOR_PINNED := $(BIN)/zizmor-$(ZIZMOR_VERSION)/zizmor
$(ZIZMOR_PINNED):
	$(call fetch,https://github.com/zizmorcore/zizmor/releases/download/v$(ZIZMOR_VERSION)/zizmor-$(ZIZMOR_ASSET_$(PLATFORM)).tar.gz,$(ZIZMOR_SHA256_$(PLATFORM)),zizmor)
$(ZIZMOR): $(ZIZMOR_PINNED)
	@cd $(@D) && ln -sf $(patsubst $(@D)/%,%,$<) $(@F)

.PHONY: fmt lint deadcode deps vuln english test test-integration fuzz cover check acceptance build

fmt: $(GOLANGCI)
	$(GOLANGCI) fmt
	$(SHFMT) -w $(SH_FILES)

lint: $(GOLANGCI) $(SHELLCHECK) $(ZIZMOR)
	$(GOLANGCI) run
	$(GOLANGCI) config verify
	$(SHELLCHECK) $(SH_FILES)
	$(SHFMT) -d $(SH_FILES)
	$(GO_TOOL) actionlint -shellcheck=$(SHELLCHECK)
	$(ZIZMOR) --no-progress .github/workflows

deadcode:
	@out=$$($(GO_TOOL) deadcode ./cmd/burro) && if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

# acceptance: S0-10
deps:
	go mod tidy -diff
	go mod verify
	@extra=$$(go list -m all | grep -vxE 'github.com/timhartmann7/burro|github.com/BurntSushi/toml v[^ ]+' || true) && \
		if [ -n "$$extra" ]; then echo "deps: modules outside the allowlist:"; echo "$$extra"; exit 1; fi

vuln:
	$(GO_TOOL) govulncheck ./...

english:
	scripts/english.sh

test:
	$(GO_TOOL) gotestsum -- -race -shuffle=on -count=1 ./...

test-integration:
	$(GO_TOOL) gotestsum -- -tags integration -race -shuffle=on -count=$(COUNT) ./...

fuzz:
	@for pkg in $$(go list ./...); do \
		for fn in $$(go test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
			go test -run='^$$' -fuzz="^$$fn$$" -fuzztime=30s $$pkg || exit 1; \
		done; \
	done

cover:
	scripts/coverage.sh

# acceptance: S0-01
check: $(GOLANGCI)
	$(GOLANGCI) fmt --diff
	$(SHFMT) -d $(SH_FILES)
	$(MAKE) lint deadcode deps vuln english test test-integration cover

acceptance:
	scripts/acceptance.sh $(SLICE)

# The Linux binaries are built while internal/serverbin/bin/ holds only its
# README, so they don't embed each other, then copied there for the client.
build:
	rm -f internal/serverbin/bin/burro-linux-*
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -o bin/burro-linux-$$arch ./cmd/burro || exit 1; \
	done
	cp bin/burro-linux-amd64 bin/burro-linux-arm64 internal/serverbin/bin/
	CGO_ENABLED=0 go build -trimpath -o bin/burro ./cmd/burro
