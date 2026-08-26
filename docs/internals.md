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
- `internal/watch` — fsnotify over the whole root (following watch links, with
  cycle guards), debounced 250ms with a 1s maximum latency into a fan-out hub
  the `/events` SSE handler subscribes to. Initial registration is synchronous
  and fatal on failure. Each refresh reconciles missing and stale watches;
  event overflow triggers an immediate reconcile, while any unrecoverable
  backend or reconcile error stops the web server so the process supervisor
  can restart a service that would otherwise appear live but stale.
- `internal/web` — handlers and view-model building. Templates and static
  assets (htmx, sse, idiomorph) are `go:embed`ded, so there are no runtime
  file dependencies. Responses pass through `withGzip`, which compresses text
  types only — event streams, Range replies and already-compressed formats go
  through untouched. `notes.go` is the review-notes HTTP surface
  ([notes.md](notes.md)).

## Platform split

Domain policy lives in untagged files; only operating-system **mechanism** is
selected by build tag. There is deliberately no filesystem interface or VFS —
it would obscure exactly the properties the security model depends on.

```
internal/store/
  store.go, annotations.go, names.go   shared policy
  storefs_linux.go / _windows.go       handle-relative store mechanism
  annotationfs_linux.go / _windows.go  handle-relative annotation mechanism
  link_linux.go / _windows.go          link create/read/classify/remove
  names_linux.go / _windows.go         lookup-segment rules
  win32_windows.go                     handle wrapper, NTSTATUS translation
internal/watch/
  watch.go                             shared reconciliation and debounce
  identity_unix.go / identity_windows.go   directory identity
```

Both backends are handle-anchored: a mutation is issued against a directory
handle pinned *before* the check that authorised it, so renaming or
substituting an ancestor cannot redirect it. Linux uses `openat`/`O_NOFOLLOW`;
Windows uses `NtCreateFile` with `OBJECT_ATTRIBUTES.RootDirectory` plus a
`FILE_ATTRIBUTE_TAG_INFO` read from the same handle. On Windows,
`OBJ_DONT_REPARSE` alone is **not** sufficient — it is inert for
non-Microsoft reparse tags, where the refusal comes from no filter driver
claiming the tag rather than from containment. Path prefix comparison is not
used for containment anywhere: 8.3 short names alone make it unsound.

Neither platform re-derives a path from a pinned handle. Linux used to, via
`/proc/self/fd/N`; that has no Windows analogue and is gone from both.

See [windows.md](windows.md) for user-facing Windows behaviour and
`.agents/ADRs/2026-08-26-windows-rooted-store-backend.md` for the design and
its measured evidence.

## Artifact resolution

An artifact is any directory directly containing an `.html` file, and the
*shallowest* such directory on a path wins: `ResolvePath` stops at the first
one and reads the rest of the URL as a file inside it, while `List`,
`Watches` and the folder-page walks never descend past one. Everything below
an artifact is its assets — served as published, with ignore rules no longer
consulted.

Artifacts therefore cannot nest, and the store holds that line at both ends.
`Publish` and `Watch` refuse a project path whose own directory holds an
`.html`. Nesting that arrives by another route — a watched source tree, a
`cp -r`, an agent writing straight into the root — is absorbed rather than
refused, since the filesystem is the source of truth and there is no call to
intercept: the inner folder simply becomes assets of the outer one. The
visible edge case is a watched project tree that gains a top-level `.html`,
which collapses the whole tree into one artifact and drops every artifact
under it from the listing.

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
`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`, plus a portable-name rule rejecting
reserved Windows device basenames (`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`,
`LPT0`-`LPT9`, case-insensitive, checked before the first dot so an
extension doesn't hide them) and names ending in a trailing dot or space, so
a store built on one OS stays movable to the other; lookups of what already
exists (URLs, delete, unwatch) are looser, because a watched repo names its
own folders, but still traversal-safe. How much looser is a platform split
(`names_linux.go` / `names_windows.go`): on Linux the portable-name rule
deliberately does not apply to lookups, so an entry a watched repository
already named `CON` stays reachable and deletable; on Windows lookups also
refuse reserved device basenames, trailing dots or spaces, and `:` (an NTFS
alternate-data-stream selector), because the underlying open primitives
interpret those forms rather than merely failing to find them — such an
entry is listed but cannot be addressed there
([windows.md](windows.md#naming)).

Filesystem containment is likewise explicit. Store-created project ancestors
must be real directories, never links (on Windows, never any reparse point).
Resolution may cross one link rooted in the store because that is a
deliberate `watch`, but will not follow another link inside the watched
source; listing, markdown discovery, folder browsing, delete, and unwatch all
preserve that boundary. Annotation paths reject link components under
`.annotations` as well.

Visibility is one decision function, `store.Visible` — every scan, the
watcher, and every listing helper route through it, so nothing drifts out of
sync. See [ignore-rules.md](ignore-rules.md); the one hard-coded exception,
the top-level `.annotations` directory, is explained in
[notes.md](notes.md#storage-and-visibility).

Parsed ignore rules are cached briefly and revalidated by modification time
and size. Content hashing was deliberately not added: it would reread every
ignore file on each cache check, while same-size replacements that preserve an
mtime are an unlikely configuration-freshness edge case rather than a security
boundary.

The notes API carries the same trust asymmetry as artifact delete: the
unauthenticated `PUT`/`DELETE /notes/{path...}` writes are the viewer's, and
the CLI can only `resolve` and `reply` — see [notes.md](notes.md).

## Deployment

`make web` builds the container (a `scratch` image holding two static
binaries) and runs it with `~/.scratchpad` mounted at `/data` and `$HOME`
mounted read-only, so the web process can never modify a watched source. The
published port binds `127.0.0.1` by default; use `make web LAN=1` to bind all
interfaces.

`make install` is the native alternative: CLI, skill, and `scratchpad-web` as
a `systemd --user` unit. No container and no sudo, but also no read-only
`$HOME` — the process runs with your own permissions; prefer `make web` if
you want that guarantee back. It prints the `systemctl --user enable --now`
line to finish, plus the `loginctl enable-linger` needed to start at boot
rather than at login.

The native service also binds `127.0.0.1:8737` by default; `make install LAN=1`
installs it with `0.0.0.0:8737`. There is no authentication: LAN mode exposes
all hosted content and lets reachable clients invoke user-trusted mutations,
including artifact deletion and notes `PUT`/`DELETE`. Use it only on a trusted
network with an appropriate firewall.

Without Make, the equivalent explicit forms are:

```bash
scratchpad-web --addr 127.0.0.1:8737       # native loopback
scratchpad-web --addr 0.0.0.0:8737         # native LAN
podman run -p 127.0.0.1:8737:8737 \
  -v "$HOME/.scratchpad:/data:z" -v "$HOME:$HOME:ro" \
  localhost/scratchpad:latest
podman run -p 0.0.0.0:8737:8737 \
  -v "$HOME/.scratchpad:/data:z" -v "$HOME:$HOME:ro" \
  localhost/scratchpad:latest                 # LAN
```

The container command listens on `:8737` internally; host port publication is
the exposure boundary.

### Windows

No container and no WSL. `scripts/install.ps1` installs the two `.exe`s under
`%LOCALAPPDATA%\scratchpad\bin` and registers `scratchpad-web.exe --addr
127.0.0.1:8737` as a **per-user logon Scheduled Task** — not a Windows Service,
which would typically need elevation and run under a different profile than the
`%USERPROFILE%` data root. Foreground execution is equally supported. Nothing
requires administrator rights, and no installer operation touches the data
root. `make build-windows` cross-builds both architectures into `dist/`;
`make release-windows` adds zip archives and `SHA256SUMS.txt`.

## Environment

- `SCRATCHPAD_ROOT` — artifact root (the container sets `/data`; the default
  is `%USERPROFILE%\.scratchpad` on Windows).
- `SCRATCHPAD_URL` — advertised base URL, default `http://localhost:8737`.

Ignore rules are files, not env vars.
