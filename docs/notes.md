# Review notes

A **note** is one comment the user leaves in the viewer, anchored to a target
inside one document — an element of an `.html` page, a text range of a
`.md` doc. Notes are review feedback *about* an artifact, never part of it:
nothing on disk under the artifact itself is touched when one is created,
answered, or closed.

Every renderable document is its own subject: `index.html` and `notes.md`
inside one artifact hold two separate note sets, each keyed by that
document's own store-relative path.

## Lifecycle

A note is `open`, `resolved`, or (functionally) reopened — reopening just
puts it back to `open` with the thread explaining why:

- **open** — outstanding feedback. New notes start here.
- **resolved** — the agent's *claim* that it fixed the thing, made by
  replying with `resolve`. Resolve is not acceptance.
- **reopen** (user only) — the user contests the claim; the note goes back
  to open.
- **delete** (user only) — the user accepts the fix and removes the note.

Every reply — a plain comment, a resolve, or a reopen — lands in the same
ordered thread under the note, so the back-and-forth reads in one place
instead of being split across a status field and a comment log.

### Who may do what

| Verb | User (viewer) | Agent (CLI) |
|---|---|---|
| create / edit note | yes | no |
| reply | yes | yes |
| resolve | yes (own notes) | yes — the normal way a note gets closed |
| reopen | yes | no |
| delete | yes | no |

The asymmetry is the point: an agent can *claim* something is fixed but can
never make feedback disappear (no delete, no editing the user's text), and it
can never undo the user's verdict (no reopening a note it lost an appeal
on). Destruction stays the user's action — the same trust tier as artifact
delete.

**Resolve-vs-delete is the load-bearing decision here.** An agent closing
feedback it was given is what lets the review loop run unattended, but
"closed" and "accepted" have to be different states or the agent ends up
grading its own work. Resolve is the claim, reopen is the appeal, delete is
the user's final word — never collapse resolve and delete into one verb,
and never treat a resolved note as done until the user has looked at it.

## Storage and visibility

One sidecar JSON file per document, mirroring the document's own path:

```
~/.scratchpad/.annotations/<doc-path>.json
```

e.g. `.annotations/demo/q3-report/index.html.json` and
`.annotations/demo/q3-report/notes.md.json` for two documents of one
artifact. A document with no notes has no file — "has notes" is a cheap
stat — and deleting the last note on a document deletes the file. All
writers (the HTTP handler and the CLI) write atomically: temp file +
rename, bumping the `rev` counter (see Concurrency).

`.annotations` lives under the writable root, so it works for **watched**
artifacts too, even though their source trees are mounted read-only.
Deleting or unwatching an artifact removes its whole `.annotations/<path>/`
subtree, so a name freed by delete/unwatch never lets a re-published
artifact of the same name inherit old notes.

`.annotations` is hidden by a reserved-name check in `store.Visible`, not by
an entry in `defaultIgnores` — that ruleset can be overridden by a
`.scratchpadignore` `!` line, and this must not be. Because it's hidden, the
watcher never sees writes there: annotation activity fires no SSE events and
doesn't touch a document's `ModTime` (which keys preview iframe caching).

## CLI

```bash
scratchpad notes [<path>] [-all] [-json]
scratchpad notes resolve <doc-path> <id> -m "what changed"
scratchpad notes reply   <doc-path> <id> -m "text"
```

- `<path>` is a document, an artifact, or a project folder; omit it for the
  whole store. The read form reports **open** notes by default; `-all`
  includes resolved ones; `-json` prints the raw structure instead of the
  markdown report.
- `resolve` appends an agent reply and marks the note resolved — the normal
  way a note gets closed. `reply` appends a comment (e.g. a clarifying
  question) without changing status. Both take the **document** path
  (`demo/q3-report/index.html`), not the artifact path, plus the note `id`
  from the report. `-m`/`-message` is required for both — a resolve or
  reply with no summary is exactly what the user reads next.
- There is deliberately no `create`, `edit`, `delete`, or `reopen`. Typing
  one of those as the first argument after `notes` is rejected with an
  explanation rather than silently read as a path (so a typo doesn't get
  misreported as "no notes").
- The CLI never calls the HTTP API — it reads and writes the sidecar files
  directly, per the project's filesystem-only coordination rule.

## HTTP API

Same trust tier as the delete endpoint: no auth, local tool.

```
GET    /notes/{path...}           # canonical read
GET    /p/{path...}/notes         # convenience form, shadowed by real content named "notes"
PUT    /notes/{doc-path...}       # viewer-driven write: replace one document's notes
DELETE /notes/{doc-path...}       # viewer-driven write: remove all notes on one document
```

- `{path}` on a **document** returns that document's notes; on an
  **artifact or project folder** it aggregates every document's notes
  underneath, grouped by document (a read-time walk of `.annotations/`,
  nothing extra stored).
- `?status=open` (default) or `?status=all` — anything else is a 400. Agents
  normally want the default.
- Default response is `text/markdown` — the same report the CLI prints. A
  request that `Accept`s `text/html` instead gets the same styled page every
  other `.md` doc on the site gets. `?format=json` returns the raw
  `NotesFile`/`DocNotes` structure.
- `GET /p/{path...}/notes` is the convenience form off a browse URL. It only
  resolves after everything real at that path has already failed to —
  content actually named `notes` (a document, artifact, or folder) shadows
  it. `GET /notes/{path...}` is the unambiguous canonical route and always
  works.
- `PUT`/`DELETE /notes/{doc-path...}` are the viewer's writes, not the
  agent's (the CLI writes the sidecar directly, per above). `PUT` replaces
  the whole notes file and must carry the `rev` it was loaded at; a stale
  `rev` gets a 409 with the current file in the body, so the caller can
  refetch and replay in one round trip instead of a second GET. `DELETE`
  removes every note on the document.

```bash
curl http://localhost:8737/notes/demo/q3-report/index.html
curl http://localhost:8737/notes/demo/q3-report?status=all
curl http://localhost:8737/notes/demo/q3-report/index.html?format=json
```

## Anchoring

**Element (HTML).** The anchor is a CSS selector picked in the viewer:
prefer a stable `#id` on the element or a nearby ancestor, else a structural
`tag:nth-of-type` path down to it. It's resolved with `querySelector` plus a
stored text fingerprint (the first ~80 characters of the element's text) to
catch a selector that still matches but now points at different content.

**Text (Markdown).** Markdown is rendered server-side, so anchors target
the *rendered* text: a W3C-style quote selector (`exact` plus `prefix`/
`suffix` disambiguators), matched by string search over the rendered
document, **whitespace-normalized** on both sides — a selection spanning
soft-wrapped lines reads differently than the source markup's own line
breaks, so raw comparison would miss real matches.

**Unanchored.** The store keeps no content history, so every anchor
re-resolves against the current document on every view. If the selector or
quote doesn't resolve — or resolves but the fingerprint no longer matches —
the note is still shown, just flagged as **unanchored** rather than dropped
or highlighted on the wrong spot. Its stored selector/fingerprint or
quote/prefix/suffix stays in the report either way, which is what an agent
needs to find the spot even without a live highlight.

## Concurrency

Two writers exist against the same sidecar file: the viewer's `PUT` and the
CLI's `resolve`/`reply`. Every write is guarded by the file's `rev`
counter — the writer must supply the `rev` it last read, and a mismatch is
rejected rather than silently overwriting the other writer's change: 409
over HTTP, with the current file in the body so the viewer can refetch and
replay; a plain error from the CLI, which loads the file fresh immediately
before each `resolve`/`reply` write rather than retrying a stale one. This
is a single-user local tool, so the guard exists to turn a lost update into
a visible conflict, not to provide real multi-writer safety.
