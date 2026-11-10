# pekoe — build, test, install
GO      ?= go
export GOTOOLCHAIN ?= local
PREFIX  ?= $(HOME)/.local
BIN     := bin/pekoe
# WEB=1 builds in the phone web UI ([web], docs/web.md), which links Tailscale
# (tsnet). The default build has no Tailscale code at all.
WEB     ?=
TAGS    := $(if $(WEB),-tags web)
# the version shown by `pekoe version`; releases get it from GoReleaser
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
LDFLAGS := -X pekoe/internal/daemon.Version=$(or $(VERSION),dev)

.PHONY: build test vet vulncheck install install-launchd uninstall-launchd nvim-test clean

build:
	$(GO) build $(TAGS) -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/pekoe
	ln -sf pekoe bin/pekoed

# both builds: the default (no Tailscale) and -tags web. In an agent's
# sandbox the tests that need a real socket, port or tmux are skipped; the
# last line says so (internal/testenv/probe)
test: vet
	$(GO) test ./...
	$(GO) test -tags web ./internal/daemon/ ./cmd/...
	sh nvim/test/run.sh
	@$(GO) run ./internal/testenv/probe

vet:
	$(GO) vet ./...
	$(GO) vet -tags web ./...

# known vulnerabilities reachable from both builds, checked with the Go that
# builds them (GOTOOLCHAIN=local: auto would switch Go and hide stdlib ones)
GOVULNCHECK_VERSION ?= v1.9.0
vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) -tags web ./...

nvim-test:
	sh nvim/test/run.sh

# installs the binary and links the nvim plugin + extension next to it
install: build
	install -d $(PREFIX)/bin $(PREFIX)/share/pekoe
	install -m 0755 $(BIN) $(PREFIX)/bin/pekoe
	ln -sf pekoe $(PREFIX)/bin/pekoed
	ln -sfn $(CURDIR)/nvim $(PREFIX)/share/pekoe/nvim
	ln -sfn $(CURDIR)/extension $(PREFIX)/share/pekoe/extension
	@echo "installed $(PREFIX)/bin/pekoe — next: pekoe hooks install"

install-launchd: install
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.local/share/pekoe
	sed -e 's|PEKOE_BIN|$(PREFIX)/bin/pekoe|' -e 's|HOME|$(HOME)|g' contrib/launchd/dev.pekoe.pekoed.plist > $(HOME)/Library/LaunchAgents/dev.pekoe.pekoed.plist
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/dev.pekoe.pekoed.plist || launchctl kickstart -k gui/$$(id -u)/dev.pekoe.pekoed

uninstall-launchd:
	-launchctl bootout gui/$$(id -u)/dev.pekoe.pekoed
	rm -f $(HOME)/Library/LaunchAgents/dev.pekoe.pekoed.plist

clean:
	rm -f bin/pekoe bin/pekoed
	rm -rf dist
