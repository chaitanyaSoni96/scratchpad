# Review notes

A **note** is one comment the user leaves in the viewer, anchored to a target
inside one document — an element of an `.html` page, a text range of a `.md`
doc. Notes are review feedback *about* an artifact, never part of it: nothing
under the artifact itself is touched when one is created, answered, or
closed. Every renderable document is its own subject: `index.html` and
`notes.md` inside one artifact hold two separate note sets, each keyed by the
document's store-relative path.

## Lifecycle

A note is `open` (outstanding feedback — new notes start here) or `resolved`;
reopening puts it back to `open` with the thread explaining why. Every
reply — a plain comment, a resolve, or a reopen — lands in one ordered thread
under the note, so the back-and-forth reads in one place.

| Verb | User (viewer) | Agent (CLI) |
|---|---|---|
| create / edit note | yes | no |
| reply | yes | yes |
| resolve | yes (own notes) | yes — the normal way a note gets closed |
| reopen | yes | no |
| delete | yes | no |

The asymmetry is the point. An agent closing feedback it was given is what
lets the review loop run unattended, but "closed" and "accepted" must stay
different states or the agent ends up grading its own work: **resolve is the
agent's claim, reopen is the user's appeal, delete is the user's final
word.** An agent can never make feedback disappear or undo the user's
verdict — destruction stays the user's action, the same trust tier as
artifact delete. Never collapse resolve and delete into one verb, and never
treat a resolved note as done until the user has looked at it.

## Storage and visibility

One sidecar JSON file per document, mirroring the document's own path:

```
~/.scratchpad/.annotations/<doc-path>.json
```

e.g. `.annotations/demo/q3-report/index.html.json` and
`.annotations/demo/q3-report/notes.md.json` for two documents of one
artifact. A document with no notes has no file — "has notes" is a cheap
stat — until its first write. Deleting the last note then leaves an empty
revision tombstone that normal note listings omit. This preserves the latest
`rev`, so a stale writer cannot recreate deleted notes. All writers (the HTTP handler and the CLI)
serialize per document, write atomically by temp file + rename, and bump the
`rev` counter (see Concurrency).

`.annotations` lives under the writable root, so it works for **watched**
artifacts too, even though their source trees are mounted read-only. Deleting
or unwatching an artifact removes its whole `.annotations/<path>/` subtree,
so a name freed by delete/unwatch never lets a re-published artifact of the
same name inherit old notes.

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

Arguments and flags: [cli.md](cli.md#notes). The CLI never calls the HTTP
API — it reads and writes the sidecar files directly, per the project's
filesystem-only coordination rule.

## HTTP API

Same trust tier as the delete endpoint: no auth, local tool. The writes are
the viewer's, not the agent's.

```
GET    /notes/{path...}           # canonical read
GET    /p/{path...}/notes         # convenience alias off a browse URL
PUT    /notes/{doc-path...}       # replace one document's notes
DELETE /notes/{doc-path...}       # remove all notes on one document
```

- `{path}` on a **document** returns that document's notes; on an artifact or
  project folder it aggregates every document's notes underneath, grouped by
  document (a read-time walk of `.annotations/`, nothing extra stored).
- `?status=open` (default) or `?status=all` — anything else is a 400.
- Default response is `text/markdown`, the same report the CLI prints. A
  request that `Accept`s `text/html` gets the same styled page as any `.md`
  doc; `?format=json` returns the raw `NotesFile`/`DocNotes` structure.
- The `/p/` alias resolves only after everything real at that path has failed
  to — content actually named `notes` shadows it. `GET /notes/{path...}`
  always works.
- `PUT` replaces the whole notes file at the `rev` it was loaded at (see
  Concurrency); `DELETE` removes every note on the document but preserves the
  incremented revision as an internal empty tombstone.

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

**Text (Markdown).** Markdown is rendered server-side, so anchors target the
*rendered* text: a W3C-style quote selector (`exact` plus `prefix`/`suffix`
disambiguators), matched by string search over the rendered document,
**whitespace-normalized** on both sides — a selection spanning soft-wrapped
lines reads differently than the source markup's own line breaks, so raw
comparison would miss real matches.

**Unanchored.** The store keeps no content history, so every anchor
re-resolves against the current document on every view. If the selector or
quote doesn't resolve — or resolves but the fingerprint no longer matches —
the note is still shown, flagged as **unanchored** rather than dropped or
highlighted on the wrong spot. Its stored selector/fingerprint or quote stays
in the report either way, which is what an agent needs to find the spot
without a live highlight.

## Concurrency

Two writers exist against the same sidecar file: the viewer's `PUT` and the
CLI's `resolve`/`reply`. Every write must carry the `rev` it last read; a
mismatch is rejected rather than silently overwriting the other writer — a
409 over HTTP, with the current file in the body so the viewer can refetch
and replay in one round trip; a plain error from the CLI, which loads the
file fresh immediately before each write. Per-document locking makes the
revision check and replacement one serialized operation, so concurrent writes
at one revision produce exactly one success. Empty tombstones make that guard
durable across deletion.
