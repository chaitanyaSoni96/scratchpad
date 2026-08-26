IMAGE := localhost/scratchpad:latest
DATA  := $(HOME)/.scratchpad
LAN ?= 0

ifeq ($(LAN),1)
HOST_BIND := 0.0.0.0
else ifeq ($(LAN),0)
HOST_BIND := 127.0.0.1
else
$(error LAN must be 0 or 1)
endif

.PHONY: build test image web stop logs install install-skill install-cli drop-mcp \
	build-windows release-windows clean-dist

build:
	go build -o bin/ ./cmd/...

# ---- Windows cross-builds and release packaging (P5.3 / P5.4) ----
# Everything lands in dist/, never bin/: `make build` stays host-only and the
# two outputs cannot collide. -trimpath plus CGO_ENABLED=0 makes the binaries
# reproducible for a given toolchain+commit; the default VCS stamping records
# the commit inside the binary. There is no -X version variable to stamp, so
# VERSION names the archives only (tag name on releases, `git describe`
# fallback otherwise).
DIST := dist
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
WINDOWS_LDFLAGS := -s -w

build-windows:
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="$(WINDOWS_LDFLAGS)" -o $(DIST)/windows-amd64/ ./cmd/...
	GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags="$(WINDOWS_LDFLAGS)" -o $(DIST)/windows-arm64/ ./cmd/...

# Zip archives + SHA256SUMS.txt via the Go helper (no dependency on a host
# `zip` binary). RELEASE_FLAGS=-require-installer makes a missing
# scripts/install.ps1 fatal — the tagged-release CI job sets it.
release-windows: build-windows
	go run scripts/mkrelease.go -dist $(DIST) -version $(VERSION) $(RELEASE_FLAGS)

clean-dist:
	rm -rf $(DIST)

test:
	go vet ./... && go test ./... && scripts/check-make.sh

image:
	podman build -t $(IMAGE) .

# $(HOME) is mounted read-only at the same path so symlinks created by
# `scratchpad watch` resolve inside the container (ro also means the web
# process can never modify watched sources).
web: image
	-podman rm -f scratchpad-web
	podman run -d --name scratchpad-web -p $(HOST_BIND):8737:8737 \
		-v $(DATA):/data:z -v $(HOME):$(HOME):ro \
		--restart unless-stopped $(IMAGE)
	@echo "scratchpad-web up at http://localhost:8737"

stop:
	podman rm -f scratchpad-web

logs:
	podman logs -f scratchpad-web

# The skill is how agents learn the CLI, and it is useless without the CLI on
# PATH — so this installs both: bin/scratchpad -> ~/.local/bin, SKILL.md ->
# ~/.claude/skills and ~/.pi/agent/skills. Also clears MCP registrations left
# by older installs. Re-run after editing skill/SKILL.md.
install-skill: build
	scripts/install.sh all

# just the CLI symlink, no skill
install-cli: build
	scripts/install.sh cli

# clean up MCP registrations left by older installs
drop-mcp:
	scripts/install.sh drop-mcp

# Full native install: CLI + skill (see install-skill), plus scratchpad-web
# running under systemd --user instead of podman — no container, no sudo, no
# read-only $HOME guarantee (see the unit file). Installs the web binary to
# ~/.local/bin and the unit to ~/.config/systemd/user/.
install: build install-skill
	install -m 0755 bin/scratchpad-web $(HOME)/.local/bin/scratchpad-web
	mkdir -p $(HOME)/.config/systemd/user
	sed 's/--addr 127\.0\.0\.1:8737/--addr $(HOST_BIND):8737/' systemd/scratchpad-web.service > $(HOME)/.config/systemd/user/scratchpad-web.service
	chmod 0644 $(HOME)/.config/systemd/user/scratchpad-web.service
	systemctl --user daemon-reload
# daemon-reload only re-reads unit files — an already-running service keeps
# serving the old binary until it is restarted. try-restart is a no-op when
# the service is not running, so a first install still just prints the hint.
	systemctl --user try-restart scratchpad-web
	@echo "Start at login and run now with:"
	@echo "  systemctl --user enable --now scratchpad-web"
	@echo "To also start at boot without logging in first:"
	@echo "  sudo loginctl enable-linger $(USER)"
