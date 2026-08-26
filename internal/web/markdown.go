package web

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	ghtml "github.com/yuin/goldmark/renderer/html"
)

var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(ghtml.WithUnsafe()), // local trusted files; iframes are sandboxed anyway
)

const mdShell = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>%s</title>
<style>
:root { color-scheme: light dark; }
body {
  margin: 0 auto;
  max-width: 46rem;
  padding: 2.5rem 1.5rem 4rem;
  font: 16px/1.65 ui-sans-serif, system-ui, sans-serif;
  color: #24242c;
  background: #fbfaf7;
}
@media (prefers-color-scheme: dark) {
  body { color: #d8d8e0; background: #14141a; }
  a { color: #ffb454; }
  code, pre { background: #1e1e26 !important; border-color: #2c2c38 !important; }
  table td, table th { border-color: #2c2c38 !important; }
  blockquote { border-color: #2c2c38; color: #9a9aa8; }
}
h1, h2, h3 { line-height: 1.25; margin-top: 2em; }
h1:first-child { margin-top: 0; }
a { color: #b6690a; }
code { font-family: ui-monospace, monospace; font-size: 0.9em; background: #f0eee8; border: 1px solid #e2e0da; border-radius: 4px; padding: 0.1em 0.35em; }
pre { background: #f0eee8; border: 1px solid #e2e0da; border-radius: 8px; padding: 1rem; overflow-x: auto; }
pre code { background: none; border: 0; padding: 0; }
img { max-width: 100%%; }
table { border-collapse: collapse; }
table td, table th { border: 1px solid #e2e0da; padding: 0.35rem 0.75rem; }
blockquote { margin-left: 0; padding-left: 1rem; border-left: 3px solid #e2e0da; color: #77756e; }
hr { border: 0; border-top: 1px solid #e2e0da; }
</style>
</head>
<body>
%s
</body>
</html>`

func serveMarkdownFile(w http.ResponseWriter, r *http.Request, f *os.File, title string) {
	defer f.Close()
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Query().Get("raw") != "" {
		if info, err := f.Stat(); err == nil {
			http.ServeContent(w, r, title, info.ModTime(), f)
			return
		}
		http.NotFound(w, r)
		return
	}
	src, err := io.ReadAll(f)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := writeMarkdownPage(w, title, src); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// writeMarkdownPage renders src (markdown source) into the standalone HTML
// shell every .md doc gets, so any caller with markdown bytes — a file on
// disk (serveMarkdownFile), or a report assembled in memory (the notes
// endpoint) — gets an identically styled page. It sets Content-Type itself;
// the caller is responsible for any other headers.
//
// P3.14 red-team L6: this file used to also carry serveMarkdown, a
// path-based sibling of serveMarkdownFile with no production caller —
// serveMarkdownFile (handle-based) is the only one server.go wires up.
// Removed rather than left dead: it re-resolved a document from a string
// (os.ReadFile(path)) rather than through a handle already validated by the
// caller, and its own ?raw=1 branch was http.ServeFile, whose os.Open omits
// FILE_SHARE_DELETE on Windows (P13.go_share_mode) — both exactly what this
// design forbids, and a trap for whoever wired it up next in good faith.
func writeMarkdownPage(w http.ResponseWriter, title string, src []byte) error {
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, mdShell, html.EscapeString(title), buf.String())
	return nil
}
