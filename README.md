# scratchpad

Self-hosted artifact hosting for coding agents. Agents publish html/css/js
artifacts via MCP (or CLI); every artifact folder under `~/.scratchpad` is
served instantly on an auto-refreshing htmx site.

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
filesystem is the sole source of truth — the MCP server and the web server
only coordinate through it, so anything that writes a folder (agents,
`cp -r`, this repo's CLI) publishes an artifact.

**Publishing is create-only.** A taken name is an error, never an overwrite;
deleting is a human action in the web UI. Agents are told to pick a fresh
name instead.

## Run

Everything ships in one container image (built from `scratch`, three static
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
make register   # claude + opencode + goose + pi, or register-<client> individually
```

The MCP surface is deliberately minimal: two tools plus server instructions.

- `publish_artifact` (project?, name, files[{path, content, base64?}]) —
  create-only; needs one top-level `.html`; binary assets via base64.
  Annotated non-destructive. Returns the URL, plus a warning note when the
  web server isn't reachable.
- `list_artifacts` — read-only annotated; names, URLs, sizes, newest first.
- Server **instructions** carry the guardrails (create-only policy, file
  rules, name pattern, self-containment) so every client injects them as
  context without bloating tool descriptions.

| Client | Mechanism |
|---|---|
| Claude Code | `claude mcp add -s user scratchpad -- podman run -i --rm -v ~/.scratchpad:/data:z localhost/scratchpad:latest /scratchpad-mcp` |
| opencode | `mcp.scratchpad` block in `~/.config/opencode/opencode.json` (1.x schema; v2 nests under `mcp.servers` and drops `enabled`) |
| goose | `extensions.scratchpad` stdio block in `~/.config/goose/config.yaml` |
| pi | pi has no MCP support by design — `contrib/pi/scratchpad.ts` is installed to `~/.pi/agent/extensions/`, registering the same two tools natively |

## CLI

`bin/scratchpad` (also at `/scratchpad` in the image) for humans and
bash-driven agents:

```bash
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -
scratchpad publish -project lab/graphs -name chart -dir ./chart-folder   # whole folder, any files
scratchpad list [-json]
scratchpad delete -project lab/graphs -name chart
```

## Development

```bash
make build      # host binaries into bin/
make test       # go vet + unit tests (store rules, traversal rejection)
```

Layout: `internal/store` (scan/publish/delete rules, name sanitization),
`internal/watch` (fsnotify → debounced fan-out hub), `internal/web` (htmx
site, SSE, static serving; assets embedded), `cmd/{scratchpad,scratchpad-mcp,scratchpad-web}`.

Notes:
- Artifact names/projects must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`.
- Card previews run in `sandbox="allow-scripts"` iframes without
  `allow-same-origin`: auto-running artifact JS can't touch the parent page or
  the delete endpoint. Clicking a card opens the artifact in an in-page viewer
  overlay (Esc or ✕ to close) with same-origin allowed — a deliberate open
  grants the same trust as the ↗ new-tab link, so localStorage and same-origin
  fetch work there; only top-navigation stays blocked.
- Override the artifact root with `SCRATCHPAD_ROOT` (the container sets it to
  `/data`), and the advertised URL with `SCRATCHPAD_URL`.
