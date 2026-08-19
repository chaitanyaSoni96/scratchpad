---
title: Artifact Annotations
status: completed
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

- [x] `internal/store/annotations.go`: types mirroring the spec's JSON — `NotesFile{Rev, Annotations}`, `Annotation{ID, Created, Updated, Status, Body, Target, Replies}`, `Target{Type, Selector, Fingerprint, Quote{Exact, Prefix, Suffix}}`, `Reply{By, Created, Action, Body}`
- [x] Sidecar path mapping + validation: `notesPath(docRel)` reusing `validateSegment`/traversal guards; doc must resolve via `ResolveDoc`/`ResolvePath` before any write
- [x] `LoadNotes(doc)` (missing file → empty, rev 0), `SaveNotes(doc, file, expectRev)` (temp+rename, rev bump, `ErrRevMismatch`), delete-file-when-empty, `DeleteNotes(doc)`
- [x] Agent verbs as store functions: `ResolveNote(doc, id, msg)` and `ReplyNote(doc, id, msg)` — read-modify-write, append `Reply{By: "agent", Action: …}`, flip status on resolve; error on unknown id
- [x] Reserved-name check in `store.Visible` for top-level `.annotations`; confirm watcher and every listing helper inherit it (they all route through `Visible`)
- [x] `Delete`/`Unwatch`: remove `.annotations/<artifact-path>/` and any `.annotations/<artifact-path>.json`-style doc files under it; prune empty dirs like `pruneEmpty` does for content
- [x] Tests (`internal/store/annotations_test.go`): path traversal rejection, rev conflict, empty-file lifecycle, resolve/reply verbs, `.annotations` invisible to `List`/`ResolvePath`/`VisiblePath`, delete/unwatch cleanup, name-reuse inherits nothing

### Phase 2 — HTTP surface (`internal/web/server.go` + new `notes.go`)

- [x] `GET /notes/{path...}` (canonical): doc path → that doc's notes; artifact/folder path → read-time walk of `.annotations/<path>/` grouped by document. Default `?status=open`, `?status=all`
- [x] Response formats: `?format=json` → raw `NotesFile`(s); default → markdown report (doc, status, body, thread, anchor quote/fingerprint, timestamps); render via the existing goldmark page (`markdown.go`) when the request `Accept`s HTML so a browser gets a styled page and curl gets text
- [x] `/p/{path...}/notes` convenience: in `handleFolderPage`, when the path doesn't resolve and the last segment is `notes`, delegate to the notes handler (real content named `notes` keeps winning)
- [x] `PUT /notes/{doc-path...}`: whole-file replace, body carries expected rev → 409 on mismatch; `DELETE /notes/{doc-path...}`; both same trust tier as the delete endpoint
- [x] Sanity-check the gzip wrapper (`gzip.go`) passes the new text/markdown + JSON responses through its existing text-type logic
- [x] Handler tests for the rev-409 path and the `/p/…/notes` shadowing rule

### Phase 3 — CLI (`cmd/scratchpad/main.go`)

- [x] `scratchpad notes <path>` (`--all`, `--json`) — same report as the HTTP default, built from a shared formatter (put the report rendering in the store package or a small `internal/notes` helper so web + CLI can't drift)
- [x] `scratchpad notes resolve <doc-path> <id> -m "what changed"` and `scratchpad notes reply <doc-path> <id> -m "…"` — direct store calls, no HTTP, matching the filesystem-only coordination rule
- [x] No `create`/`edit`/`delete`/`reopen` verbs — enforced by simply not existing; usage text says why
- [x] Update `printWatches`-style help/usage text and command dispatch in `main.go`

### Phase 4 — viewer UI

- [x] Port the mockup's anchoring core (`buildSelector`, `fingerprint`, `textMap`, `normMap`, `findQuote`, `wrapRange`, `flatOffset`, `quoteFromSelection`) into an injectable annotator script under `internal/web/assets/`; parent (`viewer.js` + `viewer.tmpl`) injects it into the viewer iframe after load, keeps card previews untouched
- [x] Marker layer in content coordinates inside the iframe (numbered chips for open, ✓ for resolved, overlay boxes for element anchors — the mockup's WebKit-safe approach), debounced `MutationObserver` re-resolve
- [x] Bubbles: thread rendering, delete/reopen verbs, contenteditable body as composer+editor, ✓-when-dirty save chip, draft lifecycle (no delete until saved, close discards), margin-first placement with in-column fallbacks — all as proven in the mockup's `app.js`
- [x] Panel (collapsed by default, persistent edge tab), annotate toggle, unanchored group; doc-switcher chips as general viewer chrome for multi-doc artifacts, each carrying its doc's open count
- [x] Wire persistence: load on viewer open via `GET /notes/…?format=json`, save via `PUT` with rev, refetch-and-replay on 409; delete/reopen/save all go through the same coarse PUT
- [x] Card badge ("3 open") in `buildCards` from a cheap sidecar stat — no iframe involvement, previews stay sandboxed
- [x] XSS: note bodies and replies rendered with `textContent` (or through goldmark later) — never `innerHTML`

### Phase 5 — agent contract + docs

- [x] `skill/SKILL.md`: new section — after a human review pass, run `scratchpad notes <artifact>`, fix, `resolve` each note with a one-line summary, `reply` when a note needs clarification instead of a fix; never expect create/delete
- [x] `README.md` + `docs/`: notes feature, HTTP endpoints, CLI verbs, trust-tier table from the spec
- [x] Re-run `make install-skill` (copies, not symlinks) and note it in the tracker
- [x] Retire or annotate the mockup artifact (`~/.scratchpad/scratchpad/annotations`) once the real viewer ships

## Verification

- `make test` green throughout; store tests carry the domain rules (Phase 1 list above)
- Manual loop once Phases 1–4 land: annotate a real artifact in the viewer → `scratchpad notes` shows it → `resolve -m` → viewer shows the thread + reopen → delete clears the sidecar file
- `curl http://192.168.1.79:8737/p/<artifact>/notes` returns the readable report; `?format=json` round-trips
- Safari click-through of the viewer UI (mockup was Chromium-verified; contenteditable and selection code are the risk areas)

## Outcome (2026-08-19)

All five phases shipped. `make test` green; `gofmt` and `node --check` clean.

Verified end to end in a real browser (headless Chromium driven by
playwright-core) against a live `scratchpad-web`: annotator injection, element
pick → draft bubble → contenteditable → ✓ save → `PUT` persisted; panel groups
(open / resolved / unanchored) with the agent's reply inline; numbered and ✓
markers in the gutter; reopen writes the thread event; panel delete removes the
note; doc-switch re-injects in `text` mode; a selection spanning a soft-wrapped
line resolves to a whitespace-normalized quote and wraps in `<mark>`; the card
badge aggregates open notes across an artifact's documents; a forced `409`
reconciles the human's edit onto the agent's resolve without losing either.
`scratchpad notes demo/q3` and `GET /p/demo/q3/notes` are byte-identical.
Deleting an artifact removes its `.annotations` subtree, so a re-created name
inherits nothing.

Four bugs the unit tests could not have caught, all found in the real browser
and fixed:

- **Injection into a doomed document.** A freshly inserted iframe already holds
  an `about:blank` document reporting `readyState: "complete"`, and a morph swap
  reuses the element with the *previous* artifact still in it. URL comparison
  can't settle it either — `ServeFile` 301s `/a/x/index.html` to `/a/x/`. Replaced
  the timing heuristic with idempotent re-injection keyed on the document object.
- **MutationObserver eating the composer.** The observer watched `document.body`,
  which contains the annotator's own layer, so typing in a bubble re-rendered it
  and discarded the text. Records originating inside the layer are now ignored.
- **Double-bound controls.** Idiomorph preserves `#annobtn` across a doc switch,
  so re-wiring stacked a second listener and the toggle cancelled itself out.
  Control wiring is bound once per node.
- **Panel unclickable with a mouse.** Hovering a row called `setActive`, which
  re-rendered the whole panel and destroyed the row before its click landed.
  The panel now redraws only when its content changes; selection restyles in place.

Also fixed: text markers were pinned to the mark's own left edge, dropping the
number on top of the words; they now hang off the enclosing block (the prose
column), as the mockup did.

**Not done, deliberately:** the mockup artifact at `~/.scratchpad/scratchpad/annotations`
is left in place. Deleting artifacts is a human action in the web UI, never an
agent's — that rule is the feature's own premise.

**Not verified:** Safari/WebKit. The cached WebKit build in this environment is
missing system libraries (`libgtk-4`, `libgstreamer`, `libicu*`) that can't be
installed without root, so the click-through in the plan's Verification section
was run on Chromium only. `contenteditable`, `getSelection`/`Range`, and
`document.execCommand("insertText")` in the bubble composer are the parts most
likely to differ and are still worth a manual pass in Safari.
