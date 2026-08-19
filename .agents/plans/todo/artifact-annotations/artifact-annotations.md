---
title: Artifact Annotations
status: todo
created: 2026-08-19
links: [../../spec/artifact-annotations.md]
---

# Artifact Annotations — Implementation Plan

## Scope

Implement the annotation system specced in `spec/artifact-annotations.md`: per-document notes with open/resolved lifecycle and reply threads, stored as sidecar JSON under `~/.scratchpad/.annotations/`, written by humans in the viewer, read/replied/resolved by agents over HTTP (`GET /p/<path>/notes`) and the CLI (`scratchpad notes …`). The interaction design (marker gutter, margin-placed bubbles that are also the composer/editor, ✓-when-dirty save, doc switcher chips, collapsed-by-default panel) is already proven in the mockup at `~/.scratchpad/scratchpad/annotations` — this plan ports it into the real viewer.

Out of scope (per spec): version streams, resolved-flag audit trail beyond delete, multi-user attribution, annotating non-HTML/markdown assets, agent-created notes, SSE live-refresh of open note panels.

## Approach

Five phases, each shippable on its own. Storage first (it defines the types everything shares), then the two read/write surfaces (HTTP, CLI) which are thin over it, then the viewer UI (the bulk of the JS moves over from the mockup), then the agent-facing contract (skill/docs). The store package stays the domain-rule owner and the only heavily-tested package; web handlers stay thin.

Key implementation decisions carried from the spec:

- Sidecar path is `<root>/.annotations/<doc-path>.json` — one file per document, no file when a doc has no notes, atomic temp+rename writes, `rev` counter for optimistic concurrency (viewer PUT sends its loaded rev, mismatch → 409; CLI re-reads on mismatch).
- `.annotations` is hidden by a **reserved-name check in `store.Visible`** (`internal/store/ignore.go`), not `defaultIgnores` — not overridable by `!` lines, invisible to the watcher, unreachable through `ResolvePath`.
- `Delete`/`Unwatch` (`internal/store/store.go`) remove the matching `.annotations/<path>` subtree so a re-published name never inherits old notes.
- The annotator script is injected by the parent page into the **viewer** iframe only (already same-origin by user click); card previews keep their sandbox exactly as-is.
- Anchor matching is whitespace-normalized on both sides (quote and document), with a normalized→raw offset map for `<mark>` wrapping — required for multiline selections and goldmark output; the algorithm exists in the mockup's `app.js` (`normMap`/`findQuote`/`wrapRange`).

## Task Breakdown

### Phase 1 — store: annotations domain

- [ ] `internal/store/annotations.go`: types mirroring the spec's JSON — `NotesFile{Rev, Annotations}`, `Annotation{ID, Created, Updated, Status, Body, Target, Replies}`, `Target{Type, Selector, Fingerprint, Quote{Exact, Prefix, Suffix}}`, `Reply{By, Created, Action, Body}`
- [ ] Sidecar path mapping + validation: `notesPath(docRel)` reusing `validateSegment`/traversal guards; doc must resolve via `ResolveDoc`/`ResolvePath` before any write
- [ ] `LoadNotes(doc)` (missing file → empty, rev 0), `SaveNotes(doc, file, expectRev)` (temp+rename, rev bump, `ErrRevMismatch`), delete-file-when-empty, `DeleteNotes(doc)`
- [ ] Agent verbs as store functions: `ResolveNote(doc, id, msg)` and `ReplyNote(doc, id, msg)` — read-modify-write, append `Reply{By: "agent", Action: …}`, flip status on resolve; error on unknown id
- [ ] Reserved-name check in `store.Visible` for top-level `.annotations`; confirm watcher and every listing helper inherit it (they all route through `Visible`)
- [ ] `Delete`/`Unwatch`: remove `.annotations/<artifact-path>/` and any `.annotations/<artifact-path>.json`-style doc files under it; prune empty dirs like `pruneEmpty` does for content
- [ ] Tests (`internal/store/annotations_test.go`): path traversal rejection, rev conflict, empty-file lifecycle, resolve/reply verbs, `.annotations` invisible to `List`/`ResolvePath`/`VisiblePath`, delete/unwatch cleanup, name-reuse inherits nothing

### Phase 2 — HTTP surface (`internal/web/server.go` + new `notes.go`)

- [ ] `GET /notes/{path...}` (canonical): doc path → that doc's notes; artifact/folder path → read-time walk of `.annotations/<path>/` grouped by document. Default `?status=open`, `?status=all`
- [ ] Response formats: `?format=json` → raw `NotesFile`(s); default → markdown report (doc, status, body, thread, anchor quote/fingerprint, timestamps); render via the existing goldmark page (`markdown.go`) when the request `Accept`s HTML so a browser gets a styled page and curl gets text
- [ ] `/p/{path...}/notes` convenience: in `handleFolderPage`, when the path doesn't resolve and the last segment is `notes`, delegate to the notes handler (real content named `notes` keeps winning)
- [ ] `PUT /notes/{doc-path...}`: whole-file replace, body carries expected rev → 409 on mismatch; `DELETE /notes/{doc-path...}`; both same trust tier as the delete endpoint
- [ ] Sanity-check the gzip wrapper (`gzip.go`) passes the new text/markdown + JSON responses through its existing text-type logic
- [ ] Handler tests for the rev-409 path and the `/p/…/notes` shadowing rule

### Phase 3 — CLI (`cmd/scratchpad/main.go`)

- [ ] `scratchpad notes <path>` (`--all`, `--json`) — same report as the HTTP default, built from a shared formatter (put the report rendering in the store package or a small `internal/notes` helper so web + CLI can't drift)
- [ ] `scratchpad notes resolve <doc-path> <id> -m "what changed"` and `scratchpad notes reply <doc-path> <id> -m "…"` — direct store calls, no HTTP, matching the filesystem-only coordination rule
- [ ] No `create`/`edit`/`delete`/`reopen` verbs — enforced by simply not existing; usage text says why
- [ ] Update `printWatches`-style help/usage text and command dispatch in `main.go`

### Phase 4 — viewer UI

- [ ] Port the mockup's anchoring core (`buildSelector`, `fingerprint`, `textMap`, `normMap`, `findQuote`, `wrapRange`, `flatOffset`, `quoteFromSelection`) into an injectable annotator script under `internal/web/assets/`; parent (`viewer.js` + `viewer.tmpl`) injects it into the viewer iframe after load, keeps card previews untouched
- [ ] Marker layer in content coordinates inside the iframe (numbered chips for open, ✓ for resolved, overlay boxes for element anchors — the mockup's WebKit-safe approach), debounced `MutationObserver` re-resolve
- [ ] Bubbles: thread rendering, delete/reopen verbs, contenteditable body as composer+editor, ✓-when-dirty save chip, draft lifecycle (no delete until saved, close discards), margin-first placement with in-column fallbacks — all as proven in the mockup's `app.js`
- [ ] Panel (collapsed by default, persistent edge tab), annotate toggle, unanchored group; doc-switcher chips as general viewer chrome for multi-doc artifacts, each carrying its doc's open count
- [ ] Wire persistence: load on viewer open via `GET /notes/…?format=json`, save via `PUT` with rev, refetch-and-replay on 409; delete/reopen/save all go through the same coarse PUT
- [ ] Card badge ("3 open") in `buildCards` from a cheap sidecar stat — no iframe involvement, previews stay sandboxed
- [ ] XSS: note bodies and replies rendered with `textContent` (or through goldmark later) — never `innerHTML`

### Phase 5 — agent contract + docs

- [ ] `skill/SKILL.md`: new section — after a human review pass, run `scratchpad notes <artifact>`, fix, `resolve` each note with a one-line summary, `reply` when a note needs clarification instead of a fix; never expect create/delete
- [ ] `README.md` + `docs/`: notes feature, HTTP endpoints, CLI verbs, trust-tier table from the spec
- [ ] Re-run `make install-skill` (copies, not symlinks) and note it in the tracker
- [ ] Retire or annotate the mockup artifact (`~/.scratchpad/scratchpad/annotations`) once the real viewer ships

## Verification

- `make test` green throughout; store tests carry the domain rules (Phase 1 list above)
- Manual loop once Phases 1–4 land: annotate a real artifact in the viewer → `scratchpad notes` shows it → `resolve -m` → viewer shows the thread + reopen → delete clears the sidecar file
- `curl http://192.168.1.79:8737/p/<artifact>/notes` returns the readable report; `?format=json` round-trips
- Safari click-through of the viewer UI (mockup was Chromium-verified; contenteditable and selection code are the risk areas)
