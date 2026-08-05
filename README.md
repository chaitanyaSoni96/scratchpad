# scratchpad

Self-hosted artifact hosting for coding agents. Agents publish html/css/js
artifacts with the `scratchpad` CLI; every artifact folder under
`~/.scratchpad` is served instantly on an auto-refreshing htmx site.

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
path, nested to any depth, each level browsable as its own page. The
filesystem is the sole source of truth — the CLI and the web server only
coordinate through it, so anything that writes a folder (agents, `cp -r`,
this repo's CLI) publishes an artifact.

**Publishing is create-only.** A taken name is an error, never an overwrite;
deleting is a human action in the web UI. Agents are told to pick a fresh
name instead.

## Run

Everything ships in one container image (built from `scratch`, two static
binaries):

```bash
make web        # builds the image and runs the site at http://localhost:8737
make stop       # stop it
```

The site lists all artifacts newest-first with sandboxed live iframe previews
and per-card delete buttons. An fsnotify watcher inside the container streams
changes over SSE, so the list refreshes the moment anything is published — no
polling.

## Connect your agents

```bash
make install-skill   # CLI on PATH + skill for claude/pi
```

**One interface: CLI + skill.** `scratchpad` goes on `~/.local/bin`; a single
[Agent Skills](https://agentskills.io)-format `skill/SKILL.md` teaches agents
to build a folder and `publish -dir` it — the natural fit for agents that
already write files, and it needs nothing beyond bash. Installed to
`~/.claude/skills/scratchpad/` and `~/.pi/agent/skills/scratchpad/`; any other
agent that reads Agent Skills or just runs shell commands works the same way.

There is no MCP server. It existed early on and was removed: it duplicated the
CLI's surface, forced files through a JSON envelope (base64 for every binary
asset) when agents can already write folders, and made the guardrails live in
two places. `make install-skill` also runs `make drop-mcp`, which clears
registrations left by older installs (claude, opencode, goose).

## CLI

`bin/scratchpad` (also at `/scratchpad` in the image) for humans and
bash-driven agents:

```bash
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -
scratchpad publish -project lab/graphs -name chart -dir ./chart-folder   # whole folder, any files
scratchpad watch ./chart-folder     # symlink instead of copy: edits go live as you save
scratchpad watches                  # every watch link and where it points
scratchpad unwatch lab/graphs/chart # drop the link, keep the folder
scratchpad list [-json]
scratchpad delete -project lab/graphs -name chart
```

`watch` mounts a folder into the scratchpad by symlink — it can be a single
artifact folder or a whole tree of them. `unwatch` (or the "unwatch" button
on any watched card in the UI) removes only the link, never the source, and
refuses anything that is not a link. Files inside watched folders are not
deletable through scratchpad at all, and the container mounts `$HOME`
read-only, so the web process physically cannot modify watched sources.

**Markdown is first-class.** Any `.md` under the scratchpad renders as a
styled page (`?raw=1` for source). Folders with several html/md files and no
`index.html` become page collections (one card per file); loose `.md` files
in project folders get cards too, and folders containing only markdown show
as "N docs" folders — so watching a docs/plans tree gives you a browsable
site of mockups and notes.

**Everything is visible unless a rule hides it** — dot-folders like `.agents`
or `.github` included. The built-in ruleset is deliberately short, and every
entry is there for one of two reasons: directories whose *cost* would sink a
watched repo (`.git`, `node_modules`, `.venv`, `dist`, `build`, `target`,
`.next`, … — thousands of directories, an inotify watch each, churning on
every build), and files whose *contents* shouldn't be one URL away from a
server on your LAN (`.env`, `.env.*`, `.netrc`, `*.pem`, `.ssh/`, `.aws/`).

**`.scratchpadignore`** tunes that. Drop one at the scratchpad root to govern
everything, and/or one in any folder below it — including inside a watched
source repo, where it lives next to the `.gitignore` it can pull in:

```
include .gitignore   # merge another ignore file, resolved next to this one
uploads/             # directories only
*.log                # glob on the name, at any depth below here
/scratch             # leading slash: only this folder's own "scratch"
docs/**/draft-*      # ** spans any number of segments
.agents              # hide a folder you'd rather not see
!bin                 # negation un-hides, including the built-ins above
```

Syntax is a gitignore subset: blank lines and `#` comments (a `#` after
whitespace starts one), `!` to negate, trailing `/` for directories only, a
slash anywhere else anchors the pattern to the file's own directory, and a
bare name matches at any depth. The built-ins are consulted first, then every
`.scratchpadignore` from the root down: the deepest file wins, and within one
file the last matching line wins — so any `!line` overrides a built-in.
Hidden means unreachable, not merely unlisted: its pages 404. Rules apply to
the folder tree only — once inside an artifact, every file is an asset and is
served as published.

## Development

```bash
make build      # host binaries into bin/
make test       # go vet + unit tests (store rules, traversal rejection)
```

Layout: `internal/store` (scan/publish/delete rules, name sanitization),
`internal/watch` (fsnotify → debounced fan-out hub), `internal/web` (htmx
site, SSE, static serving; assets embedded), `cmd/{scratchpad,scratchpad-web}`.

Notes:
- Artifact names/projects must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$` when
  the store *creates* them (publish, watch). Looking up what already exists is
  looser — a watched repo names its own folders — but still rejects `.`, `..`,
  separators and control characters, and hidden paths never resolve.
- Card previews run in `sandbox="allow-scripts"` iframes without
  `allow-same-origin`: auto-running artifact JS can't touch the parent page or
  the delete endpoint. Clicking a card opens the artifact in an in-page viewer
  overlay (Esc or ✕ to close) with same-origin allowed — a deliberate open
  grants the same trust as the ↗ new-tab link, so localStorage and same-origin
  fetch work there; only top-navigation stays blocked.
- Override the artifact root with `SCRATCHPAD_ROOT` (the container sets it to
  `/data`), and the advertised URL with `SCRATCHPAD_URL`.
