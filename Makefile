# boba — build, test, install
GO      ?= go
export GOTOOLCHAIN ?= local
PREFIX  ?= $(HOME)/.local
BIN     := bin/boba
# WEB=1 builds in the phone web UI ([web], docs/web.md), which links Tailscale
# (tsnet). The default build has no Tailscale code at all.
WEB     ?=
TAGS    := $(if $(WEB),-tags web)

.PHONY: build test vet install install-launchd uninstall-launchd nvim-test clean

build:
	$(GO) build $(TAGS) -o $(BIN) ./cmd/boba
	ln -sf boba bin/bobad

# both builds: the default (no Tailscale) and -tags web
test: vet
	$(GO) test ./...
	$(GO) test -tags web ./internal/daemon/ ./cmd/...
	sh nvim/test/run.sh

vet:
	$(GO) vet ./...
	$(GO) vet -tags web ./...

nvim-test:
	sh nvim/test/run.sh

# installs the binary and links the nvim plugin + extension next to it
install: build
	install -d $(PREFIX)/bin $(PREFIX)/share/boba
	install -m 0755 $(BIN) $(PREFIX)/bin/boba
	ln -sf boba $(PREFIX)/bin/bobad
	ln -sfn $(CURDIR)/nvim $(PREFIX)/share/boba/nvim
	ln -sfn $(CURDIR)/extension $(PREFIX)/share/boba/extension
	@echo "installed $(PREFIX)/bin/boba — next: boba hooks install"

install-launchd: install
	mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.local/share/boba
	sed -e 's|BOBA_BIN|$(PREFIX)/bin/boba|' -e 's|HOME|$(HOME)|g' contrib/launchd/dev.boba.bobad.plist > $(HOME)/Library/LaunchAgents/dev.boba.bobad.plist
	launchctl bootstrap gui/$$(id -u) $(HOME)/Library/LaunchAgents/dev.boba.bobad.plist || launchctl kickstart -k gui/$$(id -u)/dev.boba.bobad

uninstall-launchd:
	-launchctl bootout gui/$$(id -u)/dev.boba.bobad
	rm -f $(HOME)/Library/LaunchAgents/dev.boba.bobad.plist

clean:
	rm -f bin/boba bin/bobad
