GO      ?= go
TOOL    := $(GO) tool -modfile=tools/go.mod
GIT_DESC := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION  ?= $(if $(GIT_DESC),$(GIT_DESC)-dev,dev)
LDFLAGS := -X github.com/davitizhgenti/hostd/internal/version.Version=$(VERSION)
FUZZTIME ?= 30s
CORE_COVER_MIN := 85

.PHONY: all build test test-integration test-e2e fuzz lint cover tidy clean \
	devbox devbox-build devbox-shell devbox-check devbox-logs devbox-stop

all: lint test build

build:
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...

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
	rm -rf bin coverage.out core.out

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
		-v $(CURDIR)/test/devbox:/opt/devbox:ro \
		-v $(DEVBOX)-containers:/home/screen/.local/share/containers \
		$(DEVBOX)
	@echo "devbox started: make devbox-check, make devbox-shell, VNC to 127.0.0.1:5900"

devbox-shell:
	$(PODMAN) exec -it -u screen -w /home/screen $(DEVBOX) bash

devbox-check:
	$(DEVBOX_EXEC) bash /opt/devbox/check.sh; rc=$$?; \
	$(PODMAN) cp $(DEVBOX):/tmp/devbox-screen.png bin/devbox-screen.png 2>/dev/null && \
		echo "screenshot: bin/devbox-screen.png"; exit $$rc

devbox-logs:
	$(PODMAN) exec $(DEVBOX) journalctl -b --no-pager -n 100

devbox-stop:
	$(PODMAN) rm -f $(DEVBOX)
