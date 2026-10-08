GO      ?= go
TOOL    := $(GO) tool -modfile=tools/go.mod
GIT_DESC := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION  ?= $(if $(GIT_DESC),$(GIT_DESC)-dev,dev)
LDFLAGS := -X github.com/davitizhgenti/hostd/internal/version.Version=$(VERSION)
FUZZTIME ?= 30s
CORE_COVER_MIN := 85

.PHONY: all build dist test test-integration test-e2e fuzz lint lint-scripts cover tidy clean \
	devbox devbox-build devbox-shell devbox-check devbox-logs devbox-stop devbox-hostd \
	check-server test-install m1-gate m1-gate-server

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

# Release binaries for every architecture, with a checksum file. CI
# publishes these as the "edge" release that deploy/install.sh downloads.
DIST_ARCHES ?= amd64 arm64
dist:
	rm -rf dist && mkdir -p dist
	for arch in $(DIST_ARCHES); do for cmd in hostd hostctl; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/$$cmd-linux-$$arch ./cmd/$$cmd || exit 1; \
	done; done
	cd dist && sha256sum hostd-* hostctl-* > SHA256SUMS

test:
	$(GO) test -race -shuffle=on ./...

# Integration tests run against real Sway, PipeWire, Podman and systemd in
# containers (test/integration, build tag "integration").
test-integration:
	@if [ -d test/integration ]; then \
		$(GO) test -race -tags integration ./test/integration/...; \
	else echo "no integration tests yet"; fi

# End-to-end tests boot a VM and run the milestone gates (test/e2e).
test-e2e:
	@if [ -d test/e2e ]; then \
		$(GO) test -tags e2e -timeout 60m ./test/e2e/...; \
	else echo "no e2e tests yet"; fi

# Runs every Fuzz* target for FUZZTIME each.
fuzz:
	@for pkg in $$($(GO) list ./...); do \
		for fn in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz'); do \
			echo "== $$pkg $$fn"; \
			$(GO) test -run '^$$' -fuzz "^$$fn$$" -fuzztime $(FUZZTIME) $$pkg || exit 1; \
		done; \
	done

lint:
	$(TOOL) golangci-lint run ./...
	$(TOOL) govulncheck ./...

# The code that is not Go: shell scripts (shellcheck) and the on-screen
# menu (Python: ruff and a compile check). CI runs this before publishing.
SCRIPTS := deploy/install.sh deploy/check.sh deploy/m1-gate.sh deploy/files/rollback.sh \
	test/install/*.sh test/devbox/setup-hostd.sh
lint-scripts:
	shellcheck -S warning $(SCRIPTS)
	python3 -m py_compile deploy/files/menu/hostd-menu
	ruff check --no-cache --select E,F,W,B --line-length 130 deploy/files/menu/hostd-menu
	@rm -rf deploy/files/menu/__pycache__

# Coverage report; fails if core/ is below CORE_COVER_MIN percent once it has
# code with tests.
cover:
	$(GO) test -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | tail -1
	@if ls core/*_test.go >/dev/null 2>&1; then \
		$(GO) test -coverprofile=core.out ./core/... >/dev/null && \
		pct=$$($(GO) tool cover -func=core.out | awk '/^total:/ {sub("%","",$$3); print $$3}') && \
		echo "core coverage: $$pct%" && \
		awk -v p=$$pct -v m=$(CORE_COVER_MIN) 'BEGIN { exit (p < m) }' || \
		{ echo "core coverage below $(CORE_COVER_MIN)%"; exit 1; }; \
	fi

tidy:
	$(GO) mod tidy
	$(GO) mod tidy -modfile=tools/go.mod

clean:
	rm -rf bin dist coverage.out core.out

# --- devbox: a stand-in for the mini PC in one Podman container -------------
# See test/devbox/README.md. VNC on 127.0.0.1:5900, API on 127.0.0.1:7300.
DEVBOX       := hostd-devbox
PODMAN       ?= podman
DEVBOX_EXEC  := $(PODMAN) exec -u screen -w /home/screen $(DEVBOX)

devbox-build:
	$(PODMAN) build -t $(DEVBOX) test/devbox

devbox: devbox-build build
	-@$(PODMAN) rm -f $(DEVBOX) >/dev/null 2>&1
	$(PODMAN) run -d --name $(DEVBOX) --hostname devbox \
		--systemd=always --privileged \
		-p 127.0.0.1:5900:5900 -p 127.0.0.1:7300:7300 \
		-v $(CURDIR)/bin:/opt/hostd/bin:ro \
		-v $(CURDIR)/deploy:/opt/hostd-deploy:ro \
		-v $(DEVBOX)-containers:/home/screen/.local/share/containers \
		$(DEVBOX)
	@echo "devbox started: make devbox-check, make devbox-shell, VNC to 127.0.0.1:5900"

# Runs hostd in the devbox as on the real machine (service, menu, sample
# apps). Again after `make build` to restart it on the new binary.
devbox-hostd: build
	$(PODMAN) exec -i -u screen -w /home/screen $(DEVBOX) bash -s < test/devbox/setup-hostd.sh

devbox-shell:
	$(PODMAN) exec -it -u screen -w /home/screen $(DEVBOX) bash

devbox-check:
	$(DEVBOX_EXEC) bash /opt/hostd-deploy/check.sh; rc=$$?; \
	$(PODMAN) cp $(DEVBOX):/tmp/hostd-check.png bin/devbox-screen.png 2>/dev/null && \
		echo "screenshot: bin/devbox-screen.png"; exit $$rc

devbox-logs:
	$(PODMAN) exec $(DEVBOX) journalctl -b --no-pager -n 100

devbox-stop:
	$(PODMAN) rm -f $(DEVBOX)

# --- the real machine ---------------------------------------------------------
# SERVER is an SSH destination that logs in as the screen user.
SERVER ?= core-screen

# Runs deploy/check.sh on the server and copies its screenshot back.
check-server:
	ssh $(SERVER) 'bash -s' < deploy/check.sh; rc=$$?; \
	scp -q $(SERVER):/tmp/hostd-check.png bin/server-screen.png 2>/dev/null && \
		echo "screenshot: bin/server-screen.png"; exit $$rc

# Runs deploy/install.sh twice on a fresh Debian 13 container and checks the
# result; the second run must change nothing. Covers everything except the
# NVIDIA driver, greetd on a real console and the reboot.
INSTALL_TEST := hostd-install-test

test-install: dist
	@mkdir -p bin/e2e
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -trimpath \
		-ldflags '-X github.com/davitizhgenti/hostd/internal/version.Version=e2e-next' -o bin/e2e/hostd-next ./cmd/hostd
	printf '#!/bin/sh\n# Passes the version check, then cannot start.\n[ "$$1" = -version ] && { echo "hostd e2e-bad" >&2; exit 0; }\nexit 1\n' > bin/e2e/hostd-bad
	chmod +x bin/e2e/hostd-bad
	$(PODMAN) build -q -t $(INSTALL_TEST) test/install >/dev/null
	-@$(PODMAN) rm -f $(INSTALL_TEST) >/dev/null 2>&1
	$(PODMAN) run -d --name $(INSTALL_TEST) --systemd=always --privileged \
		-v $(CURDIR)/deploy:/opt/hostd-deploy:ro -v $(CURDIR)/test/install:/opt/hostd-test:ro \
		-v $(CURDIR)/dist:/opt/hostd-dist:ro -v $(CURDIR)/bin/e2e:/opt/hostd-test-bins:ro \
		-v $(CURDIR):/opt/hostd-src:ro \
		$(INSTALL_TEST) >/dev/null
	@$(PODMAN) exec $(INSTALL_TEST) systemctl is-system-running --wait >/dev/null || true
	@mkdir -p bin
	$(PODMAN) exec -u admin $(INSTALL_TEST) sudo /opt/hostd-deploy/install.sh --gpu other --from /opt/hostd-dist
	$(PODMAN) exec -u admin $(INSTALL_TEST) sudo /opt/hostd-deploy/install.sh --gpu other --from /opt/hostd-dist | tee bin/install-second-run.log
	grep -q "No changes needed" bin/install-second-run.log
	$(PODMAN) exec $(INSTALL_TEST) bash /opt/hostd-test/verify.sh
	$(PODMAN) exec -u screen -w /home/screen $(INSTALL_TEST) bash /opt/hostd-test/update.sh
	$(PODMAN) exec -u admin -w /home/admin $(INSTALL_TEST) bash /opt/hostd-test/lifecycle.sh
	$(PODMAN) rm -f $(INSTALL_TEST) >/dev/null
	@echo "install test passed"

# The M1 gate (deploy/m1-gate.sh): on the devbox, and on the real machine.
m1-gate:
	$(DEVBOX_EXEC) env PATH=/opt/hostd/bin:/usr/local/bin:/usr/bin:/bin bash /opt/hostd-deploy/m1-gate.sh

m1-gate-server:
	ssh $(SERVER) 'bash -s' < deploy/m1-gate.sh
