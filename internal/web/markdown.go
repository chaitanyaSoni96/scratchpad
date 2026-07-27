package web

import (
	"bytes"
	"fmt"
	"html"
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

// serveMarkdown renders a markdown file to a full standalone HTML page.
// ?raw=1 serves the source instead.
func serveMarkdown(w http.ResponseWriter, r *http.Request, path, title string) {
	// Rendered fresh off disk on every request, so the browser must always
	// revalidate rather than risk showing a stale render of an edited file.
	w.Header().Set("Cache-Control", "no-cache")
	if r.URL.Query().Get("raw") != "" {
		http.ServeFile(w, r, path)
		return
	}
	src, err := os.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var buf bytes.Buffer
	if err := md.Convert(src, &buf); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, mdShell, html.EscapeString(title), buf.String())
}
