IMAGE := localhost/scratchpad:latest
DATA  := $(HOME)/.scratchpad

.PHONY: build test image web stop logs install-skill install-cli drop-mcp

build:
	go build -o bin/ ./cmd/...

test:
	go vet ./... && go test ./...

image:
	podman build -t $(IMAGE) .

# $(HOME) is mounted read-only at the same path so symlinks created by
# `scratchpad watch` resolve inside the container (ro also means the web
# process can never modify watched sources).
web: image
	-podman rm -f scratchpad-web
	podman run -d --name scratchpad-web -p 8737:8737 \
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
