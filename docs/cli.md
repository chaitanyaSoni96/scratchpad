# CLI

The `scratchpad` binary is the only interface to the store; the web site
reads what it writes.

```bash
scratchpad publish -name hello -dir ./hello-folder
scratchpad publish -project lab/graphs -name chart -dir ./chart-folder
scratchpad publish -name hello -html page.html -css style.css -js app.js
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -
scratchpad watch ./chart-folder     # symlink instead of copy
scratchpad watch ./out -name dashboard -project lab
scratchpad watches                  # every watch link and where it points
scratchpad unwatch lab/graphs/chart # drop the link, keep the folder
scratchpad list [-json]
scratchpad delete -project lab/graphs -name chart
scratchpad notes [<path>] [-all] [-json]
scratchpad notes resolve <doc-path> <id> -m "what changed"
scratchpad notes reply   <doc-path> <id> -m "text"
```

Every publish prints the artifact URL on stdout. A warning on stderr means
the artifact was saved but the hosting server is not running.

## publish

Copies a **snapshot**. Use it for finished output: the hosted copy is frozen,
and later edits to the source do nothing.

- `-dir` publishes a folder. It must contain at least one top-level `.html`;
  `index.html` becomes the entry page, and several pages without an
  `index.html` render as a multi-page collection (loose `.md` files in the
  folder count as pages too).
- `-html` / `-css` / `-js` publish a single page, landing as `index.html`,
  `style.css`, and `script.js`. `-` reads that file from stdin.
- `-project` groups the artifact under a slash-separated path of any depth.
  Each level is browsable as its own page. Artifacts don't nest: a project
  path that is itself an artifact is refused.

**Publishing is create-only.** A taken name is an error, never an overwrite;
deleting is the user's action in the web UI — agents pick a fresh name
instead.

Names, project segments, and every path segment inside a published folder
must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`. One bad filename
(`.gitignore`, `my file.png`) fails the whole publish, so build a clean
folder.

## watch / unwatch

`watch` symlinks an external folder in — a single artifact or a whole tree of
them — so every saved edit shows up live. The source stays where it is and
stays yours:

- A watched folder with no top-level `.html` is a project tree; only the
  subfolders containing html show up as artifacts.
- The name is create-only like publish, with one exception: re-watching the
  same folder under the same name is a no-op, so the command is safe to
  repeat. Any *other* target — or a real directory of that name — is refused.
- Watching a folder already inside the scratchpad is refused.
- `unwatch` (or the button on any watched card) removes only the link. Files
  inside a watched folder can't be deleted through scratchpad at all, and the
  container mounts `$HOME` read-only, so the web process physically cannot
  modify a watched source.

Lookups of what already exists — URLs, delete, unwatch — validate looser than
publish (a watched repo names its own folders) but still reject traversal.

## notes

`<path>` is a document, an artifact, or a project folder (omit it for the
whole store). The read form reports open notes as a markdown report; `-all`
includes resolved notes, `-json` prints the raw structure instead. `resolve`
marks a note fixed with a summary — the normal way one gets closed; `reply`
comments without closing (e.g. a clarifying question). Both take the note's
**document** path, not the artifact path, plus the `id` the report prints;
`-m`/`-message` is required for both.

There is no `create`, `edit`, `delete`, or `reopen` — those verbs are the
user's, in the web UI. Lifecycle, storage, and the HTTP API:
[docs/notes.md](notes.md).

## Markdown

Any `.md` renders as a styled page (`?raw=1` for source). Loose `.md` files
get their own cards, so watching a docs or plans tree gives you a browsable
site of mockups and notes alongside the html.
