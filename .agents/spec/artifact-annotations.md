---
title: Artifact Annotations
status: implemented
created: 2026-08-18
links: [../plans/completed/artifact-annotations/]
---

# Artifact Annotations

## Overview

Let a human reviewing an artifact in the web UI attach notes to specific parts of it — an element of an HTML page, a text range of a markdown doc — and let agents participate in the loop: **read** the notes over HTTP and via the CLI, **reply** to them, and **close** them as addressed. Annotations are review feedback *about* the artifact, never part of it — artifact files on disk are not touched.

The full loop: an agent publishes an artifact → a human annotates it in the viewer → the agent fetches the open notes (`scratchpad notes <path>` or `GET /p/<path>/notes`), revises the artifact, and closes each note with a reply saying what changed (`scratchpad notes resolve`) → the human re-reviews: **reopens** anything not actually fixed, **deletes** what is done. Deletion is the human's final acceptance; resolution is only the agent's claim.

> **Decision — no version streams (2026-08-19).** An earlier draft versioned notes into review rounds (v1, v2, … per document, latest mutable, older frozen). Dropped: the store keeps no content history, so an old round re-resolved against the current document is mostly unanchored comments about text that no longer exists — history without snapshots is noise. Everything rounds actually provided ("what's new since the agent's last pass") falls out of per-note `created` timestamps, and the round-trip structure now lives in the note lifecycle instead (open → resolved → reopened/deleted). The mockup at `~/.scratchpad/scratchpad/annotations` reflects the unversioned model.

## Concepts

- **Subject** — the unit that owns notes: one **document**. Every renderable document is its own subject — each `.html` page, each `.md` doc (inside an artifact or loose in a project folder), and any future annotatable type. Notes anchor inside their subject, so `index.html` and `notes.md` in one artifact hold two separate note sets.
- **Note** — one comment anchored to one target inside its subject document, with a status and a reply thread:
  - `status: "open" | "resolved"`. New notes are open. **Resolve** (usually the agent, after fixing) marks the claim "addressed"; **reopen** (human only) contests it; **delete** (human only) accepts it and removes the note. A document's open notes are its outstanding feedback.
  - `replies` — an ordered thread under the note. Agent replies say what was changed or ask for clarification; the resolve/reopen events live in the same thread so the history reads in one place.
- **Target** — where a note is anchored (see Anchoring).

### Who may do what

| Verb | Human (viewer) | Agent (CLI) |
|---|---|---|
| create / edit note | ✔ | ✘ |
| reply | ✔ | ✔ |
| resolve | ✔ (own notes) | ✔ — the normal way a note gets closed |
| reopen | ✔ | ✘ |
| delete | ✔ | ✘ |

The asymmetry is the point: the agent can *claim* something is fixed but can never make feedback disappear (no delete, no edit of human text), and it can never un-do the human's verdict (no reopen of its own resolved-then-reopened notes — a reopened note just shows as open again, with the thread explaining why). Destruction stays a human action, same trust tier as artifact delete.

## Storage

Filesystem only, consistent with the rest of the system. One sidecar JSON file per document, keyed by the document's store-relative path:

```
~/.scratchpad/.annotations/<doc-path>.json
```

e.g. `.annotations/demo/q3-infra/index.html.json` and `.annotations/demo/q3-infra/notes.md.json` for two docs of one artifact, `.annotations/lab/todo.md.json` for a loose doc. Deleting or unwatching an artifact removes `.annotations/<artifact-path>/` wholesale, which covers every document's file because doc paths nest inside the artifact path.

- Lives under the writable root, so it works for **watched** artifacts too (whose source trees are mounted read-only in the container).
- A document with no notes has no file; deleting the last note deletes the file (so "has notes" is a cheap stat).
- All writers (web handler and CLI) write atomically: temp file + rename, and bump `rev` (below).

### Visibility

`.annotations` must be invisible and unreachable as content. Do **not** put it in `defaultIgnores` — that ruleset is overridable by a user's `!` line. Instead add a reserved-name check directly in `store.Visible` (and therefore in every scan, the watcher, and `ResolvePath`): a top-level `.annotations` directory is system metadata, always hidden, not overridable. Because it is hidden, the watcher never fires SSE events for annotation writes and `ModTime` (which keys preview iframes) is unaffected. Publish/watch name validation (`^[a-zA-Z0-9]…`) already rejects `.annotations` as a creatable name, so no collision is possible.

## Data model

One JSON document per subject:

```json
{
  "rev": 7,
  "annotations": [
    {
      "id": "k7f2ac",
      "created": "2026-08-18T10:12:31Z",
      "updated": "2026-08-18T10:14:02Z",
      "status": "open",
      "body": "This axis label overlaps the legend on narrow widths.",
      "target": {
        "type": "element",
        "selector": "#chart > svg > g.legend",
        "fingerprint": "Legend: revenue, cost, margin"
      },
      "replies": [
        { "by": "agent", "created": "2026-08-18T10:41:00Z", "action": "resolve",
          "body": "Moved the legend above the plot; regenerated with make spend-report." },
        { "by": "user", "created": "2026-08-18T11:02:00Z", "action": "reopen",
          "body": "Still collides below 640px — try it at phone width." }
      ]
    },
    {
      "id": "m3q9zz",
      "created": "2026-08-18T10:14:40Z",
      "status": "resolved",
      "body": "Rephrase — 'utilize' → 'use'.",
      "target": {
        "type": "text",
        "quote": { "exact": "utilize the fallback", "prefix": "we should ", "suffix": " whenever the" }
      },
      "replies": [
        { "by": "agent", "created": "2026-08-18T10:42:10Z", "action": "resolve", "body": "Done." }
      ]
    }
  ]
}
```

- The document is identified by the sidecar path, not by a field — every note in a file anchors in that file's document, so targets carry no `doc` reference.
- `rev` — monotonic counter for optimistic concurrency (below). Every write bumps it.
- `status` is derivable from the last `action` in `replies` but stored explicitly so readers (CLI, card badges) don't replay threads.
- Reply `by` is `"agent"` or `"user"`; `action` is optional (`"resolve"` / `"reopen"`) — a reply without one is just a comment. A reply body may be empty when the action alone is the message (a bare reopen).
- `fingerprint` — first ~80 chars of the element's text content, used only for stale detection.
- `created`/`updated` timestamps are what an agent uses to see what changed since its last pass.

### Concurrency

Two writers now exist (viewer PUTs, CLI resolve/reply), so the whole-file write needs a guard: the file carries `rev`, the viewer's PUT sends the `rev` it loaded (`If-Match`-style field), and the handler rejects with 409 when it doesn't match the file — the viewer refetches and replays. The CLI does read-modify-write under the same rule (re-read on mismatch). This is a single-user local tool; the guard exists so an agent resolving notes while a viewer tab is open doesn't get silently clobbered by that tab's next save, not for real multi-writer safety.

## Anchoring

**HTML (element annotations).** In annotate mode the user hovers/clicks to pick an element. The anchor is a CSS selector built at pick time: prefer a stable `#id` on the element or nearest ancestor, else a structural path of `tag:nth-of-type` segments from that ancestor down. On display, resolve with `querySelector`; if it misses or the fingerprint no longer matches, the note is shown in the panel as **unanchored** (listed, not highlighted) rather than dropped.

**Markdown (text annotations).** Markdown is rendered server-side by goldmark, so anchors target the *rendered* text: a W3C-style TextQuoteSelector (`exact` + `prefix`/`suffix` disambiguators) resolved by string search over the rendered document's text content, then mapped to a DOM Range and wrapped in `<mark>` elements (one per text node the range crosses). Matching is **whitespace-normalized** — quote and document are both compared with whitespace runs collapsed, with a map back to raw offsets for the wrapping — because a selection spanning soft-wrapped lines or paragraph boundaries renders differently than the markup's own line breaks and indentation read. Survives re-rendering and small unrelated edits; a missing quote → unanchored, same as above.

**A changed document.** The store keeps no content history, so anchors always re-resolve against the current document. Per note there are three outcomes: resolves and the fingerprint/quote matches → highlighted; the selector resolves but the fingerprint mismatches (content moved or changed underneath it) → unanchored rather than highlighted on the wrong thing; doesn't resolve → unanchored. Unanchored notes keep their stored fingerprint/quote so they stay readable.

## Mechanism: annotating inside the viewer

The viewer overlay iframe is already **same-origin by deliberate user action** — that is the entire trick. After the iframe loads, the parent (trusted scratchpad page) injects a small annotator script + stylesheet into the iframe's document. The script:

- draws highlights for resolved anchors (element outlines / `<mark>` ranges) inside the document, so scrolling is free;
- in annotate mode, runs the element picker (HTML) or watches text selection (markdown) and reports picks back to the parent via `postMessage`/direct calls;
- re-resolves anchors on a debounced `MutationObserver`, since script-driven artifacts re-render their own DOM.

Nothing on disk changes; the injection lives and dies with the iframe. **Card previews are untouched** — they lack `allow-same-origin` by design (so auto-running artifact JS can't reach delete — or annotation — endpoints), and that boundary stays exactly where it is. A card may show a small read-only badge ("3 open") built from the sidecar during `buildCards`, but all interaction happens in the viewer.

## HTTP API

Same trust tier as the existing delete endpoint — no auth, local tool. Reads are for humans *and agents*; HTTP writes are viewer-driven (the agent writes via the CLI, which edits the sidecar files directly — filesystem-only coordination, like everything else).

### Read

```
GET /p/{path...}/notes            # e.g. http://192.168.1.79:8737/p/scratchpad/annotations/notes
```

- `{path}` is a **document** → that document's notes.
- `{path}` is an **artifact or project folder** → the notes of every document under it, grouped by document (aggregation is a read-time walk of `.annotations/<path>/`; nothing new is stored).
- `?status=open` (default) / `?status=all` — agents normally want only outstanding feedback.
- Default response is `text/markdown` — a readable report (per document: status, anchor quote/fingerprint, body, thread, timestamps, anchored/unanchored) that a human can open in a browser and an agent can consume directly with curl. `?format=json` returns the raw structure.
- Routing: `GET /p/{path...}` already exists as the folder-page route, so the handler treats a trailing `/notes` segment as the notes view **only when nothing real resolves at that path** — real content named `notes` shadows the endpoint. `GET /notes/{path...}` is the unambiguous canonical alias; `/p/…/notes` is the convenience form.

### Write (viewer only)

```
PUT /notes/{doc-path...}          # replace the document's notes file; body carries the rev it was loaded at → 409 on mismatch
DELETE /notes/{doc-path...}       # remove all notes on the document
```

Coarse-grained whole-file PUT: for a single-user local tool this is simpler and strictly less code than per-note CRUD; the `rev` check bounds the lost-update risk now that the CLI also writes.

## CLI

The agent's half of the loop, taught by `skill/SKILL.md`:

```
scratchpad notes <path>                        # open notes, markdown report (same shape as GET /p/<path>/notes)
scratchpad notes <path> --all                  # include resolved
scratchpad notes <path> --json                 # raw JSON
scratchpad notes resolve <doc-path> <id> -m "what changed"   # reply + mark resolved
scratchpad notes reply   <doc-path> <id> -m "text"           # comment without closing (e.g. a clarifying question)
```

Reads and writes go straight to the sidecar files (atomic rename, `rev` bump) — the CLI never calls the HTTP API, per the architecture. The CLI deliberately has **no** `create`, `edit`, `delete`, or `reopen`: an agent can answer and close feedback but can neither author it, erase it, nor overrule the human's reopen. `skill/SKILL.md` gains the workflow: after a human review pass, run `scratchpad notes <artifact>`, address each open note, `resolve` it with a one-line summary of the change, and `reply` instead when a note needs clarification rather than a fix.

## UI

Viewer overlay gains:

- an **annotate toggle** (`✎`) — enters pick/select mode;
- a **doc switcher** — chips for each document in the artifact, shown whenever the artifact has more than one. This is general viewer chrome, not annotation chrome; each chip carries its doc's **open** count (e.g. `notes.md · 2`);
- **inline bubbles** — the primary reading surface: clicking a numbered gutter marker (or its panel row) opens a bubble showing the note **and its thread** (agent replies, resolve/reopen events), with the human's verbs: delete always, **reopen** when resolved. The bubble anchors to the **marker**, not the element: its pointer notch aims at the number (so the bubble carries no number of its own). Its preferred home is the **margin left of the content column** — the one spot that overlaps no content — narrowing a little to fit the space available; only when that margin is too tight (panel open, narrow window) does it fall into the column, below then above then right of the marker, choosing a spot that overlaps no other note's marker or highlight when one exists. The bubble is also the **composer and editor** — there is no separate compose UI: picking a new target opens the same bubble empty and editable (with no delete button, since the note doesn't exist yet), and an existing note's body is editable in place. A small **✓ save** chip sits in the bubble's bottom-right corner, shown only while the text differs from what's stored; clicking it saves (stamping `updated` on edits). Closing an unsaved new-note bubble discards it;
- markers distinguish status: open notes get numbered chips, resolved notes a dimmed ✓ chip — so a returning reviewer can walk exactly the ✓ marks to check the agent's claims;
- a **side panel** (~320 px, right) listing the on-screen document's notes — **collapsed by default**, expanded via a persistent edge tab that never disappears. Grouped: open first, then **resolved** (greyed, showing the agent's closing reply), then unanchored. Clicking an entry scrolls to its anchor, flashes the highlight, and opens its bubble.

No version pill, no rounds ledger, no frozen states. An artifact with no notes shows no chrome beyond the `✎` toggle and doc switcher.

## Trade-offs

- **Unversioned vs. review rounds** — see the decision note in the Overview. The open/resolved lifecycle plus threads carries the round-trip structure rounds were for; if round *history* is ever wanted, versioning can layer back on (the per-document file becomes a per-document directory) without changing anchors, endpoints, or the CLI's output shape.
- **Resolve-vs-delete split** — the agent closing feedback it was given is necessary for the loop to run unattended, but "closed" and "accepted" must stay different states or the agent effectively grades its own work. Resolve is the claim, reopen is the appeal, delete is the human's final word.
- **Sidecar tree vs. inside the artifact dir** — inside the artifact would make everything one place, but artifact subtrees are served as assets (notes would leak as published files), watched sources are read-only, and `ModTime`/SSE would churn on every comment. Sidecar wins on all three.
- **Coarse PUT + rev vs. per-note CRUD** — whole-file writes with an optimistic-concurrency counter are far less code than per-note endpoints and good enough for one human and one agent taking turns.
- **`/p/…/notes` suffix vs. only a dedicated route** — the suffix reads naturally off a browse URL but can be shadowed by real content named `notes`; keeping `/notes/{path…}` as the canonical route bounds the damage to loss of the pretty URL.
- **Injection vs. parent-side highlight layer** — a parent-positioned overlay avoids touching the iframe DOM but must chase scroll/resize/re-layout forever; injection into an already-same-origin document is less code and more robust.

## Risks

- **Anchor rot** — live-watched artifacts change under their notes. Mitigated (not solved) by fingerprints and the unanchored fallback display.
- **Concurrent writers** — an agent resolving notes while a viewer tab holds stale state; the `rev` check turns silent clobbering into a visible 409/refetch. The viewer gets no SSE nudge for annotation writes (`.annotations` is hidden from the watcher by design), so it should refetch notes on viewer open and after its own 409s.
- **Script-heavy artifacts** — SPAs that rebuild their DOM defeat naive one-shot resolution; the MutationObserver re-resolve pass handles the common case, pathological ones degrade to unanchored.
- **XSS via note body** — bodies and replies are rendered into the trusted parent panel; render through the existing goldmark path (or as plain text in v1) — never `innerHTML` of raw input. Agent replies are still untrusted text in this sense.
- **Name collision semantics** — deleting an artifact and publishing a new one under the same name would inherit its `.annotations` subtree if cleanup ever regresses; the Delete/Unwatch cleanup rule above is load-bearing.

## Out of scope (v1) / future work

- **Version streams / review rounds** — frozen per-round history, carry-forward, per-round content snapshots.
- Multi-user attribution (`by` is just `"agent"`/`"user"` in v1).
- Annotating non-HTML/markdown assets (images, PDFs).
- Agent-created notes (e.g. an agent flagging its own uncertainty for review) — explicitly excluded until the human-driven loop settles.
- SSE live-refresh of the notes panel while a viewer is open.

## Next step

The mockup at `~/.scratchpad/scratchpad/annotations` demonstrates the unversioned model with the full lifecycle (open notes, agent-resolved notes with threads, a reopened note, reopen/delete verbs, bubble-as-composer, margin placement). Implementation plan: [`plans/completed/artifact-annotations/`](../plans/completed/artifact-annotations/artifact-annotations.md).
