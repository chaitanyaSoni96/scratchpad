# Screenshot manifest

Captures taken against a throwaway `SCRATCHPAD_ROOT` with hand-authored mock
artifacts (no real user data), via `scratchpad-web` on `localhost:8899`.
Chrome headless, driven over CDP, 1440px-wide viewport at 2x device scale
(matching `docs/screenshot.png`); heights vary per shot to avoid dead space.

| File | Dimensions | Feature | Caption |
| --- | --- | --- | --- |
| `index.png` | 2880x2080 | Index grid of live artifact previews | The scratchpad home page: live artifact previews alongside a watched folder, a multi-page collection, project folders, and open-note badges. |
| `watched-folder.png` | 864x600 | Watched folder card | A watched folder's card shows `unwatch` instead of `delete` — the link can be removed without touching the source files. |
| `folder-page.png` | 2200x960 | Project/folder page | A nested project folder (`/p/reports`) with its own artifacts, reached via the breadcrumb trail. |
| `multi-page-collection.png` | 2880x780 | Multi-page artifact collection | An artifact folder with several top-level pages and no `index.html` renders as a browsable collection, one tile per page. |
| `viewer-overlay.png` | 2880x1440 | Viewer overlay | Opening an artifact in the full-page viewer overlay, with the annotate and notes-panel controls in the header. |
| `notes-viewer.png` | 2880x1440 | Review notes — gutter markers and an open note | Numbered gutter markers for open notes and a checkmark for a resolved one; the open note's bubble shows the original comment plus the agent's reply. |
| `notes-panel.png` | 2880x1804 | Review notes — notes panel | The side notes panel listing every note on a document, open and resolved, with reply threads inline. |
| `notes-report.png` | 2880x3014 | Review notes — markdown report | The notes report at `/notes/<path>`, rendered as a styled page, summarizing open and resolved notes across documents. |
| `markdown-doc.png` | 2880x1360 | Markdown document rendering | A loose `.md` file rendered as a styled standalone page via goldmark. |

`docs/screenshot.png` (the original, pre-existing capture) is left untouched.

Note: `notes-panel.png` and `notes-report.png` were re-shot later than the
rest, against a rebuilt mock. The `checkout-funnel` artifact renders slightly
differently there (a "Funnel stages" card wrapper, pill-styled deltas) than in
`index.png` and `notes-viewer.png`. The README uses `index.png` and
`notes-viewer.png`, which agree with each other.
