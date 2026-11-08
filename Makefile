# pekoe — build, test, install
GO      ?= go
export GOTOOLCHAIN ?= local
PREFIX  ?= $(HOME)/.local
BIN     := bin/pekoe
# WEB=1 builds in the phone web UI ([web], docs/web.md), which links Tailscale
# (tsnet). The default build has no Tailscale code at all.
WEB     ?=
TAGS    := $(if $(WEB),-tags web)

.PHONY: build test vet install install-launchd uninstall-launchd nvim-test clean

build:
	$(GO) build $(TAGS) -o $(BIN) ./cmd/pekoe
	ln -sf pekoe bin/pekoed

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

# also drops dev.boba.bobad, the agent of pekoe's old name, when it is there
uninstall-launchd:
	-launchctl bootout gui/$$(id -u)/dev.pekoe.pekoed
	rm -f $(HOME)/Library/LaunchAgents/dev.pekoe.pekoed.plist
	@if [ -f $(HOME)/Library/LaunchAgents/dev.boba.bobad.plist ] || launchctl print gui/$$(id -u)/dev.boba.bobad >/dev/null 2>&1; then \
		launchctl bootout gui/$$(id -u)/dev.boba.bobad || true; \
		rm -f $(HOME)/Library/LaunchAgents/dev.boba.bobad.plist; \
		echo "removed the old launchd agent dev.boba.bobad"; \
	fi

clean:
	rm -f bin/pekoe bin/pekoed
