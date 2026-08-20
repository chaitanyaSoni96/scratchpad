# Internals

```bash
make build      # host binaries into bin/
make test       # go vet + unit tests (internal/store and internal/web)
```

## Layout

Two binaries share one store and coordinate **only through the filesystem** —
no IPC, no database. `~/.scratchpad` is the sole source of truth, so anything
that writes a folder there publishes an artifact.

- `cmd/scratchpad` — the CLI, and the only agent interface.
- `cmd/scratchpad-web` — the htmx site: folder pages, sandboxed previews,
  viewer overlay, SSE live refresh, delete endpoint.
- `internal/store` — every domain rule: scan, publish, watch, delete, name
  validation, visibility, and the review-notes sidecars (`annotations.go`;
  `report.go` renders the markdown report shared by the CLI and the HTTP
  read endpoint).
- `internal/watch` — fsnotify over the whole root (following symlinks, with
  cycle guards), debounced 250ms into a fan-out hub the `/events` SSE handler
  subscribes to.
- `internal/web` — handlers and view-model building. Templates and static
  assets (htmx, sse, idiomorph) are `go:embed`ded, so there are no runtime
  file dependencies. Responses pass through `withGzip`, which compresses text
  types only — event streams, Range replies and already-compressed formats go
  through untouched. `notes.go` is the review-notes HTTP surface
  ([notes.md](notes.md)).

## Preview weight

Card previews are eager: a lazy iframe that starts hidden may never load in
some engines, and hidden-until-loaded would then deadlock. The cost is that
every tile on a folder page fetches and runs its artifact on every visit, so
past `maxPreviewBytes` (1 MiB, measured on the entry document rather than the
whole subtree) the tile stops embedding and draws a placeholder that still
links through.

## Security model

Card previews run in `sandbox="allow-scripts"` iframes *without*
`allow-same-origin`, so auto-running artifact JS can't reach the parent page,
the delete endpoint, or the notes endpoints. Clicking a card opens the
artifact in a viewer overlay with same-origin allowed — a deliberate open
grants the same trust as the ↗ new-tab link, and is what lets the parent
inject the annotator script.

One consequence is worth knowing when an artifact misbehaves: a preview
iframe has an opaque origin and the server sends no CORS headers, so an
artifact whose JS `fetch`es a sibling data file works in the viewer and in a
direct tab but fails in its card preview. Assets pulled in by markup (`img`,
`link`, `script src`) are unaffected — inline the data a page needs at load,
or expect a blank tile.

Name validation is split by intent. `validateName` guards what the store
*creates* (publish, watch) with the strict
`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`; lookups of what already exists (URLs,
delete, unwatch) are looser, because a watched repo names its own folders,
but still traversal-safe.

Visibility is one decision function, `store.Visible` — every scan, the
watcher, and every listing helper route through it, so nothing drifts out of
sync. See [ignore-rules.md](ignore-rules.md); the one hard-coded exception,
the top-level `.annotations` directory, is explained in
[notes.md](notes.md#storage-and-visibility).

The notes API carries the same trust asymmetry as artifact delete: the
unauthenticated `PUT`/`DELETE /notes/{path...}` writes are the viewer's, and
the CLI can only `resolve` and `reply` — see [notes.md](notes.md).

## Deployment

`make web` builds the container (a `scratch` image holding two static
binaries) and runs it with `~/.scratchpad` mounted at `/data` and `$HOME`
mounted read-only, so the web process can never modify a watched source.

`make install` is the native alternative: CLI, skill, and `scratchpad-web` as
a `systemd --user` unit. No container and no sudo, but also no read-only
`$HOME` — the process runs with your own permissions; prefer `make web` if
you want that guarantee back. It prints the `systemctl --user enable --now`
line to finish, plus the `loginctl enable-linger` needed to start at boot
rather than at login.

## Environment

- `SCRATCHPAD_ROOT` — artifact root (the container sets `/data`).
- `SCRATCHPAD_URL` — advertised base URL, default `http://localhost:8737`.

Ignore rules are files, not env vars.
