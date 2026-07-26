// Package web serves the htmx index site, artifact static files, the SSE
// change stream, and the delete endpoint.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"scratchpad/internal/store"
	"scratchpad/internal/watch"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

//go:embed assets
var assetFS embed.FS

var tmpl = template.Must(template.New("").
	Funcs(template.FuncMap{"size": humanSize}).
	ParseFS(templateFS, "templates/*.tmpl"))

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func NewServer(hub *watch.Hub) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", handleFolderPage)
	mux.HandleFunc("GET /p/{path...}", handleFolderPage)
	mux.HandleFunc("GET /fragments/list", handleListFragment)
	mux.HandleFunc("GET /fragments/viewer/{path...}", handleViewerFragment)
	mux.HandleFunc("GET /a/{path...}", handleArtifact)
	mux.HandleFunc("DELETE /a/{path...}", handleDelete)
	mux.HandleFunc("GET /events", handleEvents(hub))
	assets, err := fs.Sub(assetFS, "assets")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	return mux
}

type crumb struct {
	Name string
	Href string
	Last bool
}

// crumbs builds breadcrumb entries for a slash path; when artifact is true
// the final segment links nowhere (it is the current artifact).
func crumbs(path string) []crumb {
	if path == "" {
		return nil
	}
	segs := strings.Split(path, "/")
	out := make([]crumb, len(segs))
	for i, s := range segs {
		out[i] = crumb{Name: s, Href: "/p/" + strings.Join(segs[:i+1], "/"), Last: i == len(segs)-1}
	}
	return out
}

// card is the view model for one tile: an artifact, or a whole subfolder
// (subfolders holding a single artifact collapse into that artifact's card).
type card struct {
	Kind        string // "artifact" | "project"
	Artifact    store.Artifact
	PrefixLabel string // artifact card: "sub/path" prefix shown before the name
	PrefixHref  string
	Label       string // project card
	Href        string
	Count       int
	Preview     store.Artifact
}

type pageView struct {
	Folder string // "" on the home page
	Crumbs []crumb
}

type listView struct {
	Folder string
	Cards  []card
}

// buildCards turns the newest-first artifact list into tiles for folder f.
func buildCards(artifacts []store.Artifact, f string) []card {
	prefix := ""
	if f != "" {
		prefix = f + "/"
	}
	perChild := map[string]int{}
	for _, a := range artifacts {
		if a.Project != f && strings.HasPrefix(a.Project+"/", prefix) {
			child := strings.SplitN(strings.TrimPrefix(a.Project, prefix), "/", 2)[0]
			perChild[child]++
		}
	}
	var cards []card
	seen := map[string]bool{}
	for _, a := range artifacts {
		switch {
		case a.Project == f:
			cards = append(cards, card{Kind: "artifact", Artifact: a})
		case !strings.HasPrefix(a.Project+"/", prefix):
			// outside this folder
		default:
			child := strings.SplitN(strings.TrimPrefix(a.Project, prefix), "/", 2)[0]
			if perChild[child] == 1 {
				cards = append(cards, card{Kind: "artifact", Artifact: a,
					PrefixLabel: strings.TrimPrefix(a.Project, prefix),
					PrefixHref:  "/p/" + a.Project})
			} else if !seen[child] {
				seen[child] = true
				cards = append(cards, card{Kind: "project", Label: child,
					Href: "/p/" + prefix + child, Count: perChild[child], Preview: a})
			}
		}
	}
	return cards
}

// folderExists reports whether f is the root or holds at least one artifact.
func folderExists(artifacts []store.Artifact, f string) bool {
	if f == "" {
		return true
	}
	for _, a := range artifacts {
		if a.Project == f || strings.HasPrefix(a.Project, f+"/") {
			return true
		}
	}
	return false
}

func handleFolderPage(w http.ResponseWriter, r *http.Request) {
	f := strings.Trim(r.PathValue("path"), "/")
	if f != "" {
		if _, err := store.SplitProject(f); err != nil {
			http.NotFound(w, r)
			return
		}
		artifacts, err := store.List()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !folderExists(artifacts, f) {
			http.NotFound(w, r)
			return
		}
	}
	if err := tmpl.ExecuteTemplate(w, "index.tmpl", pageView{Folder: f, Crumbs: crumbs(f)}); err != nil {
		log.Printf("render folder page: %v", err)
	}
}

func handleListFragment(w http.ResponseWriter, r *http.Request) {
	artifacts, err := store.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	f := strings.Trim(r.URL.Query().Get("project"), "/")
	view := listView{Folder: f, Cards: buildCards(artifacts, f)}
	if err := tmpl.ExecuteTemplate(w, "list.tmpl", view); err != nil {
		log.Printf("render list: %v", err)
	}
}

type viewerView struct {
	Artifact store.Artifact
	Crumbs   []crumb
}

func handleViewerFragment(w http.ResponseWriter, r *http.Request) {
	a, file, ok := resolveRequest(r)
	if !ok || file != "" {
		http.NotFound(w, r)
		return
	}
	view := viewerView{Artifact: a, Crumbs: crumbs(a.Project)}
	for i := range view.Crumbs {
		view.Crumbs[i].Last = false // artifact name is the final crumb
	}
	if err := tmpl.ExecuteTemplate(w, "viewer.tmpl", view); err != nil {
		log.Printf("render viewer: %v", err)
	}
}

func resolveRequest(r *http.Request) (store.Artifact, string, bool) {
	raw := strings.Trim(r.PathValue("path"), "/")
	if raw == "" {
		return store.Artifact{}, "", false
	}
	return store.ResolvePath(strings.Split(raw, "/"))
}

func handleArtifact(w http.ResponseWriter, r *http.Request) {
	a, file, ok := resolveRequest(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if file == "" {
		// Redirect /a/x -> /a/x/ so the artifact's relative URLs resolve.
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		file = a.Entry
	}
	clean := filepath.Clean(filepath.FromSlash(file))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.IsAbs(clean) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, filepath.Join(a.Dir, clean))
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	a, file, ok := resolveRequest(r)
	if !ok || file != "" {
		http.NotFound(w, r)
		return
	}
	if err := store.Delete(a.Project, a.Name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// 200 with empty body: htmx swaps the card away instantly; the
	// watcher-driven SSE refresh reconciles the full list moments later.
	w.WriteHeader(http.StatusOK)
}

func handleEvents(hub *watch.Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		ch, cancel := hub.Subscribe()
		defer cancel()

		fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()

		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ch:
				fmt.Fprint(w, "event: change\ndata: 1\n\n")
				flusher.Flush()
			case <-ping.C:
				fmt.Fprint(w, ": ping\n\n")
				flusher.Flush()
			}
		}
	}
}
