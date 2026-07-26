IMAGE := localhost/scratchpad:latest
DATA  := $(HOME)/.scratchpad

.PHONY: build test image web stop logs register register-claude register-opencode register-goose register-pi

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

register: build
	scripts/register-mcp.sh all

register-claude:
	scripts/register-mcp.sh claude

register-opencode:
	scripts/register-mcp.sh opencode

register-goose:
	scripts/register-mcp.sh goose

register-pi: build
	scripts/register-mcp.sh pi

install-cli: build
	scripts/register-mcp.sh cli

install-skill:
	scripts/register-mcp.sh skill
