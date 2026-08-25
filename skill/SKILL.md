---
name: scratchpad
description: Host html/css/js artifacts (demos, dashboards, reports, visualizations, docs) on the local scratchpad site with the `scratchpad` CLI — normally by watching the project's `.scratchpad/` folder so edits go live on save, or by publishing a frozen snapshot. Use whenever the user asks to publish, host, share, preview, or "put on the scratchpad" a page, demo, dashboard, report, or front-end artifact, wants a folder hosted live while they keep editing it, or asks what is already hosted. Also use when the user has left review notes on an artifact to read, reply to, or resolve.
---

# scratchpad

Any folder under `~/.scratchpad` (override: `SCRATCHPAD_ROOT`) that directly
contains an `.html` file is served at `http://localhost:8737/a/<path>/`
(override: `SCRATCHPAD_URL`). The site refreshes as files change; `.md` files
render as styled pages. The server is loopback-only by default; `make web LAN=1`
or `make install LAN=1` opts into unauthenticated LAN exposure, including delete
and notes-write endpoints, and should be used only on a trusted network.

## Default: watch the project's `.scratchpad/`

```bash
mkdir -p .scratchpad                          # gitignore it unless the user wants it committed
scratchpad watch .scratchpad -name <project>  # -name required: ".scratchpad" is not a valid name
```

Both lines are safe to repeat — re-watching the same folder under the same
name is a no-op. Keep `.scratchpad/` itself free of `.html`: with no top-level
`.html` the folder is hosted as a project tree, and the first folder down each
path that holds an `.html` is its own artifact.

From then on an artifact is just files: write
`.scratchpad/<artifact>/index.html` plus relative css, js, images and `.md`,
then report `http://localhost:8737/a/<project>/<artifact>/`. Revise by editing
on disk. Folder names are the URL — use kebab-case.

## Publish: a frozen snapshot

Use only when the user wants a frozen version, or the source is a temp dir
that will not outlive the task. `publish` copies the folder in and is
create-only: a taken name is an error, never an overwrite — pick a fresh name
(`-v2`), never delete or unwatch to free one.

```bash
scratchpad list                                        # what is hosted, newest first; -json
scratchpad publish -name q3-report -dir ./out          # needs a top-level *.html; index.html is the entry page
scratchpad publish -project lab/graphs -name q3 -dir ./out    # group under a path of any depth
echo '<h1>hi</h1>' | scratchpad publish -name hello -html -   # single page; -css/-js too, "-" reads stdin
```

The name, every project segment, and every file path inside the folder must
match `^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$` — one bad filename (`.gitignore`,
`my file.png`) fails the whole publish. Also rejected, so a store stays
movable to Windows: reserved device basenames regardless of extension
(`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`, `LPT0`-`LPT9` — case-insensitive,
so `nul.html` and `Com1.tar.gz` fail too) and any name ending in a dot or
space. `-dir` accepts regular files and
directories only, not symlinks or special files. Exactly one of `-dir` and
`-html` is required; `-css`/`-js` require `-html`, and only one input may use
`-` for stdin.

## Review notes

Notes are review feedback the user anchored in the viewer; an open note is
outstanding work, not a suggestion.

```bash
scratchpad notes <project>/q3-report                  # open notes as a markdown report; -all, -json
scratchpad notes resolve <project>/q3-report/index.html k7f2ac -m "moved the legend above the plot"
scratchpad notes reply   <project>/q3-report/index.html k7f2ac -m "which breakpoint — 640 or 768?"
```

`resolve` and `reply` take the **document** path and the `id` from the report;
the read form takes any path (document, artifact, folder — or none for the
whole store). Reply when a note needs clarification rather than a fix.
`create`, `edit`, `delete` and `reopen` do not exist for agents: authoring,
erasing and reopening are the user's, in the web UI.

## Rules

- Always report the artifact URL. A warning on stderr means the files are in
  place but the host is down — tell the user to run `make web`.
- Keep artifacts self-contained, with relative links. Card previews run in an
  opaque-origin sandbox: a `fetch()` of a sibling file fails there (it works
  in the viewer and a direct tab), and an entry page over 1 MiB draws a
  placeholder instead of a preview. Opening the viewer is a deliberate trust
  decision: it grants `allow-same-origin` so the annotator can run; automatic
  card previews remain opaque and cannot reach mutation endpoints.
- One artifact per folder: the shallowest folder on a path holding an `.html`
  is the artifact and everything below it is its assets. A subfolder with its
  own `.html` is *not* a second artifact, and an `.html` at the top of a
  watched tree collapses the whole tree into one. A `-project` path that is
  itself an artifact is refused.
- `delete` and `unwatch` are the user's — run them only when explicitly asked,
  never to recycle a name.
- A watch link is the only symlink boundary the site follows. Do not rely on
  nested symlinks inside a watched tree: they are not browsed or listed, and
  publish/delete/unwatch refuse symlinked project ancestors.
