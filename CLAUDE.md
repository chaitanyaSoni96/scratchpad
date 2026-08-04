# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```bash
make build      # host binaries into bin/ (go build ./cmd/...)
make test       # go vet ./... && go test ./...
go test ./internal/store -run TestName   # single test (store is the only tested package)
make web        # podman-build the image and run the site at http://localhost:8737
make stop       # stop the container
make logs       # follow container logs
make register   # register MCP + CLI + skill with local agent CLIs (scripts/register-mcp.sh)
```

The container is built with podman from `Containerfile` (a `scratch` image holding three static binaries). `make web` mounts `~/.scratchpad` at `/data` and `$HOME` read-only (so `watch` symlinks resolve but the web process can never modify watched sources).

## Architecture

Three binaries in `cmd/` share one store and coordinate **only through the filesystem** — there is no IPC or database. `~/.scratchpad` (override: `SCRATCHPAD_ROOT`; the container sets `/data`) is the sole source of truth: anything that writes a folder there publishes an artifact.

- `cmd/scratchpad` — CLI (publish/watch/unwatch/watches/list/delete) for humans and bash-driven agents.
- `cmd/scratchpad-mcp` — stdio MCP server, deliberately minimal: `publish_artifact` (create-only) + `list_artifacts`; guardrails live in the server instructions string. stdout is the MCP transport, logs go to stderr.
- `cmd/scratchpad-web` — the htmx site: folder pages, sandboxed iframe previews, viewer overlay, SSE-driven live refresh, delete endpoint.

Internal packages:

- `internal/store` — all domain rules. An **artifact** is any directory directly containing a `.html` file; its whole subtree is assets, every directory above it is project path (any depth). Names must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`; `artifactDir`/`ValidateFilePath` reject traversal. Publish is **create-only**: `os.Mkdir` (not MkdirAll) atomically claims the name, so races and existing names both surface as EEXIST — deletion is a human action in the web UI, never an agent's. `Watch` symlinks an external folder in (same atomic create via `os.Symlink`); `Delete` unlinks watched entries without touching the source and refuses anything whose path crosses a symlink. `Unwatch` is the deliberate counterpart to `Watch` — it removes *only* a symlink (refusing real directories) and so also reaches watched project trees, which are not artifacts and therefore invisible to `Delete`; `WatchLinkFor` maps any path to the link governing it, which is how the web layer offers unwatch on cards that merely live inside a watched tree. Artifacts cannot nest. `ModTime` is the newest mtime in the tree (not the directory's) because the UI keys preview iframes on it.
- `internal/watch` — fsnotify over the whole root (following symlinks, with cycle guards), debounced 250ms into a fan-out `Hub` that the web server's `/events` SSE handler subscribes to. A single Create event must hook an entire new subtree (dirs can arrive populated via `mv`/`cp -r`/`ln -s`).
- `internal/web` — handlers plus view-model building (`buildCards` handles folder tiles, single-artifact-folder collapse, multi-page collections, loose-markdown docs). Templates and static assets (htmx, sse, idiomorph) are `go:embed`ded — no runtime file dependencies. `internal/web/markdown.go` renders any `.md` via goldmark as a styled page.

Security model worth preserving: card previews run in `sandbox="allow-scripts"` iframes **without** `allow-same-origin` so auto-running artifact JS can't reach the delete endpoint; the viewer overlay (a deliberate user click) grants same-origin. Ignored dirs (`node_modules`, `vendor`, `bin`, … + `SCRATCHPAD_IGNORE`) are invisible to both scanning and the watcher — keep the two in sync via `store.Ignored`.

Env vars: `SCRATCHPAD_ROOT` (store root), `SCRATCHPAD_URL` (advertised base URL, default `http://localhost:8737`), `SCRATCHPAD_IGNORE` (extra comma-separated ignore dirs).

`skill/SKILL.md` (Agent Skills format) and the MCP server instructions both restate the store's rules for agents — if publish/name/create-only semantics change, update `README.md`, the skill, and the `instructions` const in `cmd/scratchpad-mcp/main.go` together.
