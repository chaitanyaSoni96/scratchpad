# scratchpad

Self-hosted artifact hosting for coding agents. Agents publish html/css/js
artifacts with the `scratchpad` CLI; every artifact folder under
`~/.scratchpad` is served instantly on an auto-refreshing htmx site.

![The scratchpad index: a grid of live artifact previews](docs/screenshot.png)

```
~/.scratchpad/
├── my-artifact/            # artifact: any folder directly containing *.html
│   ├── index.html          # entry (or first *.html)
│   ├── style.css           # any relative assets: css, js, images, fonts...
│   └── img/logo.png        # ...including subfolders
└── lab/graphs/             # project paths nest arbitrarily deep
    └── pixel/
        └── index.html
```

A folder is an **artifact** when it directly contains an `.html` file — its
whole subtree is that artifact's assets. Every folder above it is project
path, each level browsable as its own page. The filesystem is the sole source
of truth, so anything that writes a folder (agents, `cp -r`, this repo's CLI)
publishes an artifact.

**Publishing is create-only.** A taken name is an error, never an overwrite;
deleting is a human action in the web UI.

## Run

```bash
make web        # builds the image and runs the site at http://localhost:8737
make stop       # stop it
```

Everything ships in one container image (built from `scratch`, two static
binaries). The site lists artifacts newest-first with sandboxed live previews
and per-card delete buttons; an fsnotify watcher streams changes over SSE, so
the list refreshes the moment anything is published.

## Connect your agents

```bash
make install-skill   # CLI on PATH + skill for claude/pi
```

`scratchpad` goes on `~/.local/bin`, and an [Agent
Skills](https://agentskills.io)-format `skill/SKILL.md` teaches agents to
build a folder and `publish -dir` it. Installed to `~/.claude/skills/` and
`~/.pi/agent/skills/`; any agent that reads Agent Skills — or just runs shell
commands — works the same way.

There is no MCP server. One existed early on and was removed: it duplicated
the CLI, forced files through a JSON envelope, and split the guardrails across
two places. `make install-skill` also clears registrations left by older
installs.

## CLI

```bash
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -
scratchpad publish -project lab/graphs -name chart -dir ./chart-folder
scratchpad watch ./chart-folder     # symlink instead of copy: edits go live as you save
scratchpad watches                  # every watch link and where it points
scratchpad unwatch lab/graphs/chart # drop the link, keep the folder
scratchpad list [-json]
scratchpad delete -project lab/graphs -name chart
```

`watch` mounts an external folder in by symlink — a single artifact or a whole
tree of them. `unwatch` (or the button on any watched card) removes only the
link, never the source. Files inside watched folders can't be deleted through
scratchpad at all, and the container mounts `$HOME` read-only, so the web
process physically cannot modify watched sources.

**Markdown is first-class.** Any `.md` renders as a styled page (`?raw=1` for
source). Folders with several html/md files and no `index.html` become page
collections, and loose `.md` files get their own cards — so watching a
docs/plans tree gives you a browsable site of mockups and notes.

## Ignore rules

**Everything is visible unless a rule hides it**, dot-folders included. The
built-in ruleset is short and covers two things only: directories whose cost
would sink a watched repo (`.git`, `node_modules`, `.venv`, `dist`, …) and
files whose contents shouldn't be one URL away from a server on your LAN
(`.env*`, `.netrc`, `*.pem`, `.ssh/`, `.aws/`).

Drop a **`.scratchpadignore`** at the scratchpad root or in any folder below
it — including inside a watched source repo, next to the `.gitignore` it can
pull in:

```
include .gitignore   # merge another ignore file, resolved next to this one
uploads/             # directories only
*.log                # glob on the name, at any depth below here
/scratch             # leading slash: only this folder's own "scratch"
docs/**/draft-*      # ** spans any number of segments
!bin                 # negation un-hides, including the built-ins above
```

The syntax is a gitignore subset. Built-ins are consulted first, then every
`.scratchpadignore` from the root down: the deepest file wins, and within one
file the last matching line wins. Hidden means unreachable, not merely
unlisted — hidden pages 404. Rules apply to the folder tree only; once inside
an artifact, every file is an asset and is served as published.

## Development

```bash
make build      # host binaries into bin/
make test       # go vet + unit tests (store rules, traversal rejection)
```

Layout: `internal/store` (scan/publish/delete rules, name sanitization),
`internal/watch` (fsnotify → debounced fan-out hub), `internal/web` (htmx
site, SSE, static serving; assets embedded), `cmd/{scratchpad,scratchpad-web}`.

Notes:
- Names must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$` when the store *creates*
  them (publish, watch). Looking up what already exists is looser — a watched
  repo names its own folders — but still rejects traversal and control
  characters, and hidden paths never resolve.
- Card previews run in `sandbox="allow-scripts"` iframes *without*
  `allow-same-origin`, so auto-running artifact JS can't reach the parent page
  or the delete endpoint. Clicking a card opens the artifact in a viewer
  overlay with same-origin allowed — a deliberate open grants the same trust
  as the ↗ new-tab link.
- `SCRATCHPAD_ROOT` overrides the artifact root (the container sets `/data`),
  `SCRATCHPAD_URL` the advertised URL.
