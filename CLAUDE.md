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
make install-skill  # CLI on PATH + skill into ~/.claude and ~/.pi (scripts/install.sh)
make install-cli    # just the bin/scratchpad symlink into ~/.local/bin
make drop-mcp       # clear MCP registrations left by older installs
```

The container is built with podman from `Containerfile` (a `scratch` image holding two static binaries). `make web` mounts `~/.scratchpad` at `/data` and `$HOME` read-only (so `watch` symlinks resolve but the web process can never modify watched sources).

## Architecture

Two binaries in `cmd/` share one store and coordinate **only through the filesystem** — there is no IPC or database. `~/.scratchpad` (override: `SCRATCHPAD_ROOT`; the container sets `/data`) is the sole source of truth: anything that writes a folder there publishes an artifact.

- `cmd/scratchpad` — CLI (publish/watch/unwatch/watches/list/delete). The **only** agent interface: agents drive it through bash, taught by `skill/SKILL.md`. There is deliberately no MCP server (one existed and was removed — it duplicated the CLI and split the guardrails across two places); don't reintroduce one without a reason the CLI can't cover.
- `cmd/scratchpad-web` — the htmx site: folder pages, sandboxed iframe previews, viewer overlay, SSE-driven live refresh, delete endpoint.

Internal packages:

- `internal/store` — all domain rules. An **artifact** is any directory directly containing a `.html` file; its whole subtree is assets, every directory above it is project path (any depth). Names must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`; `artifactDir`/`ValidateFilePath` reject traversal. Publish is **create-only**: `os.Mkdir` (not MkdirAll) atomically claims the name, so races and existing names both surface as EEXIST — deletion is a human action in the web UI, never an agent's. `Watch` symlinks an external folder in (same atomic create via `os.Symlink`); `Delete` unlinks watched entries without touching the source and refuses anything whose path crosses a symlink. `Unwatch` is the deliberate counterpart to `Watch` — it removes *only* a symlink (refusing real directories) and so also reaches watched project trees, which are not artifacts and therefore invisible to `Delete`; `WatchLinkFor` maps any path to the link governing it, which is how the web layer offers unwatch on cards that merely live inside a watched tree. Artifacts cannot nest. `ModTime` is the newest mtime in the tree (not the directory's) because the UI keys preview iframes on it.
- `internal/watch` — fsnotify over the whole root (following symlinks, with cycle guards), debounced 250ms into a fan-out `Hub` that the web server's `/events` SSE handler subscribes to. A single Create event must hook an entire new subtree (dirs can arrive populated via `mv`/`cp -r`/`ln -s`).
- `internal/web` — handlers plus view-model building (`buildCards` handles folder tiles, single-artifact-folder collapse, multi-page collections, loose-markdown docs). Templates and static assets (htmx, sse, idiomorph) are `go:embed`ded — no runtime file dependencies. `internal/web/markdown.go` renders any `.md` via goldmark as a styled page. `internal/web/gzip.go` wraps the whole mux: it decides at first write (when Content-Type is settled) and compresses text types only — `text/event-stream` must pass through or SSE stalls, the wrapper must keep `http.Flusher` or the SSE handler 500s, and Range requests are skipped because encoding renumbers the offsets.

Security model worth preserving: card previews run in `sandbox="allow-scripts"` iframes **without** `allow-same-origin` so auto-running artifact JS can't reach the delete endpoint; the viewer overlay (a deliberate user click) grants same-origin.

Visibility is one decision function, `store.Visible(dir, name, isDir)` (`internal/store/ignore.go`) — every scan, the watcher, and every listing helper in `internal/web` route through it, so nothing drifts out of sync. **Default is visible**, dot-folders included; the `defaultIgnores` const is a built-in ruleset written in `.scratchpadignore` syntax and parsed by the same code, so it behaves like a file one level above the root and any `!line` overrides it. Keep it short and justified — an entry belongs there only if it is ruinously expensive to walk/watch (`.git`, `node_modules`, `.venv`) or holds credentials (`.env*`, `.netrc`, `*.pem`, `.ssh/`); ordinary dot-folders (`.agents`, `.github`) are content. Real `.scratchpadignore` files (gitignore subset + `include <file>`) layer on top: deepest file wins, last matching line wins. Hidden means unreachable, not just unlisted: `ResolvePath`/`ResolveDoc`/`VisiblePath` refuse hidden paths, so a hidden folder 404s. Parsed files are cached with a 1s TTL revalidated by mtime+size (`resetIgnoreCache` in tests).

Name validation is split by intent: `validateName`/`artifactDir` (strict `^[a-zA-Z0-9]…`) guard what the store **creates** (publish, watch); `validateSegment`/`existingDir`/`visibleSegments` guard what it merely **looks up or removes** (URLs, delete, unwatch) — looser, because watched repos name their own folders, but still traversal-safe. Visibility checks stop at an artifact directory: files inside an artifact are assets and are served as published.

Env vars: `SCRATCHPAD_ROOT` (store root), `SCRATCHPAD_URL` (advertised base URL, default `http://localhost:8737`). Ignore rules are files, not env: see `.scratchpadignore` above.

`skill/SKILL.md` (Agent Skills format) restates the store's rules for agents and is the single agent-facing contract — if publish/name/create-only semantics or CLI flags change, update the skill and `README.md` together, and re-run `make install-skill` to push the copies in `~/.claude/skills` and `~/.pi/agent/skills` (they are copies, not symlinks — an edit in the repo does nothing until you re-run it).
