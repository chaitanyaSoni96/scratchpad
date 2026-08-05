---
name: scratchpad
description: Publish html/css/js artifacts (demos, dashboards, reports, visualizations) to the local scratchpad host using the `scratchpad` CLI, host a folder live via watch, and list what is already hosted. Use whenever the user asks to publish, host, share, or "put on the scratchpad" a web page, demo, dashboard, report, or front-end artifact, wants a folder hosted live while they keep editing it, or asks what artifacts exist on the scratchpad.
---

# scratchpad

Local artifact hosting, driven entirely by the `scratchpad` CLI. Every folder
under `~/.scratchpad` (override: `SCRATCHPAD_ROOT`) that directly contains an
`.html` file is served as an artifact at
`http://localhost:8737/a/<project>/<name>/` (override base: `SCRATCHPAD_URL`)
on a live-updating site.

## publish vs watch — pick one

- **`publish -dir`** copies a **snapshot**. Use for finished output: the
  hosted copy is frozen; later edits to your source do nothing.
- **`watch`** **symlinks** the folder in place. The source stays where it is
  and stays the user's; every saved edit shows up live. Use when the user will
  keep iterating on the folder, or wants a whole tree (mockups, docs, plans)
  browsable. `.md` files in watched trees render as styled pages alongside
  html.

## Commands

```bash
# publish a folder you built (needs a top-level *.html; index.html becomes the entry page)
scratchpad publish -name my-demo -dir ./my-demo-folder

# group under a project path (any depth, slash-separated)
scratchpad publish -project lab/graphs -name q3-report -dir ./out

# quick single-page publish: -html becomes index.html, -css style.css, -js script.js
scratchpad publish -name hello -html page.html -css style.css -js app.js
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -     # "-" reads stdin (works for -css/-js too)

# what's hosted (path, size, modified, url; newest first)
scratchpad list
scratchpad list -json

# host a folder in place via symlink — folder is a positional arg, before or after flags
scratchpad watch ./my-demo-folder                    # name defaults to the folder's base name
scratchpad watch ./out -name dashboard -project lab
scratchpad watch .agents/plans -name plans           # md + html tree, browsable

# list watch links and where they point
scratchpad watches

# human affordances — see Rules before touching these
scratchpad unwatch lab/dashboard                     # positional project/name path, as the URL shows it
scratchpad unwatch -name dashboard -project lab      # ...or via flags
scratchpad delete -name my-demo -project lab
```

## Hard rules

- **Publish is CREATE-ONLY.** A taken name fails with "already exists" — it
  never overwrites. **Never delete or unwatch to free a name**: deletion is
  the human's job in the web UI. Run `scratchpad list` first and pick a fresh
  name (e.g. suffix `-v2`).
- The folder must contain at least one **top-level `.html`** file.
  `index.html` becomes the entry page; several html files without an
  `index.html` render as a multi-page collection.
- **Names, project segments, and every file path segment inside a published
  folder** must match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$` — no spaces, no
  leading dots. A single bad filename (e.g. `.gitignore`, `my file.png`)
  fails the whole publish, so build a clean folder.
- Artifacts cannot nest: you cannot publish under a project path that is
  itself an artifact.
- Keep artifacts self-contained; reference assets with **relative URLs**. The
  index page live-previews each artifact in a sandboxed iframe.
- `watch` links, it never copies. It refuses folders already inside the
  scratchpad; a watched folder with no top-level `.html` is treated as a
  project tree (only subfolders containing html show as artifacts — the CLI
  prints a note). `unwatch` removes only the symlink; files inside a watched
  folder can never be deleted through scratchpad.
- `delete` and `unwatch` exist for humans at the terminal. Do not use them
  unless the user explicitly asks — and never to recycle a name.

## Workflow

1. Build the artifact folder in a temp/scratch dir first — html entry page,
   css, js, images, data files, subfolders — with relative links between them.
2. Check `scratchpad list` if there is any chance the name is taken.
3. `scratchpad publish -name <fresh-name> -dir <folder>` (or `watch` if the
   source should stay live).
4. The command prints the artifact URL on stdout — **always report that URL
   back to the user**.
5. A warning on **stderr** means the artifact was saved but the hosting
   server is down; tell the user to run `make web` in the scratchpad repo.
