# boba — build, test, install
GO      ?= go
export GOTOOLCHAIN ?= local
PREFIX  ?= $(HOME)/.local
BIN     := bin/boba

.PHONY: build test vet install install-launchd uninstall-launchd nvim-test clean

build:
	$(GO) build -o $(BIN) ./cmd/boba
	ln -sf boba bin/bobad

test: vet
	$(GO) test ./...
	sh nvim/test/run.sh

vet:
	$(GO) vet ./...

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
