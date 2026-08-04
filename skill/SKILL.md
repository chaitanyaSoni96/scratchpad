---
name: scratchpad
description: Publish html/css/js artifacts (demos, dashboards, reports, visualizations) to the local scratchpad host using the `scratchpad` CLI, and list what is already hosted. Use whenever the user asks to publish, host, share, or "put on the scratchpad" a web page or front-end artifact, or asks what artifacts exist.
---

# scratchpad

Local artifact hosting. Every folder under `~/.scratchpad` that directly
contains an `.html` file is served as an artifact at
`http://localhost:8737/a/<project>/<name>/` on a live-updating site.

## Commands

```bash
# publish a folder you built (any files; needs a top-level *.html, index.html preferred)
scratchpad publish -name my-demo -dir ./my-demo-folder

# group under a project path (any depth)
scratchpad publish -project lab/graphs -name q3-report -dir ./out

# quick single-page publish (stdin html; optional -css/-js files)
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -

# what's hosted (name, size, modified, url)
scratchpad list
scratchpad list -json

# host a folder IN PLACE via symlink — edits to the source show up live.
# Use when the user wants to keep iterating on the folder after hosting.
# Works for single artifacts, whole trees of them, and docs/plans trees:
# .md files render as styled pages alongside html mockups.
scratchpad watch ./my-demo-folder            # name defaults to folder name
scratchpad watch ./out -name dashboard -project lab
scratchpad watch .agents/plans -name plans   # browsable md + html tree

# list the watch links, and drop one (source folder is never touched)
scratchpad watches
scratchpad unwatch lab/dashboard
```

## Rules

- **Create-only.** Publishing to a taken name fails. Never delete to free a
  name — deletion belongs to the human via the web UI. Run `scratchpad list`
  first and pick a fresh name (e.g. suffix `-v2`).
- The published folder must contain at least one top-level `.html` file;
  `index.html` becomes the entry page. Everything else in the folder — css,
  js, images, fonts, json, subfolders — is served alongside it, so build the
  folder first, reference assets with relative URLs, then publish with `-dir`.
- Names and every path segment: `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$` (no
  spaces).
- Keep artifacts self-contained; the index page live-previews each artifact
  in an iframe.
- `watch` links, it never copies: the folder stays where it is and stays
  yours. Unwatching (the button in the UI, or `scratchpad unwatch <path>`)
  removes only the link; files inside a watched folder can never be deleted
  through scratchpad.
- After publishing, give the user the printed URL. A warning on stderr means
  the hosting server is down (`make web` in the scratchpad repo starts it).

`scratchpad delete` and `scratchpad unwatch` exist for humans at the
terminal; do not use them to recycle names.
