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
	"os"
	"path/filepath"
	"sort"
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
	mux.HandleFunc("GET /fragments/siblings", handleSiblings)
	mux.HandleFunc("GET /a/{path...}", handleArtifact)
	mux.HandleFunc("DELETE /a/{path...}", handleDelete)
	mux.HandleFunc("DELETE /watch/{path...}", handleUnwatch)
	mux.HandleFunc("GET /events", handleEvents(hub))
	assets, err := fs.Sub(assetFS, "assets")
	if err != nil {
		panic(err)
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(assets)))
	return withGzip(mux)
}

type crumb struct {
	Name string
	Href string
	Rel  string // slash path under the root, for the sibling popover
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
		rel := strings.Join(segs[:i+1], "/")
		out[i] = crumb{Name: s, Href: "/p/" + rel, Rel: rel, Last: i == len(segs)-1}
	}
	return out
}

// card is the view model for one tile: an artifact, a whole subfolder
// (subfolders holding a single artifact and nothing else collapse into that
// artifact's card), or one html page of a multi-page artifact.
type card struct {
	Kind        string // "artifact" | "project" | "page"
	Artifact    store.Artifact
	PrefixLabel string // artifact card: "sub/path" prefix shown before the name
	PrefixHref  string
	Label       string // project + page cards
	Href        string
	Count       int
	Unit        string // "artifacts" | "pages"
	Preview     store.Artifact
	PageHref    string // page cards: served URL (/a/...)
	ViewerHref  string // page cards: viewer fragment URL
	PageIsDoc   bool   // page cards: markdown rather than html
	PageSize    int64
	PageMod     time.Time
	Unwatch     *unwatchAction // set when this tile is (or lives under) a watch link
}

// unwatchAction is the unwatch button on a card: the watch link that would be
// removed plus the confirm line. Cards inside a watched tree point at the
// ancestor link, so the wording has to say the whole tree goes with it.
type unwatchAction struct {
	Path    string
	Confirm string
}

func unwatchLink(link, rel string) *unwatchAction {
	if link == "" {
		return nil
	}
	if link == rel {
		return &unwatchAction{Path: link, Confirm: "Stop watching " + link + "? The source folder is kept."}
	}
	return &unwatchAction{Path: link,
		Confirm: "Stop watching " + link + "? That unlinks the whole watched folder, so " + rel + " goes with it. The source files are kept."}
}

// artifactUnwatch resolves the watch link an artifact card would remove.
func artifactUnwatch(a store.Artifact) *unwatchAction {
	rel := a.RelPath()
	switch {
	case a.IsLink:
		return unwatchLink(rel, rel)
	case a.Linked:
		return unwatchLink(store.WatchLinkFor(rel), rel)
	}
	return nil
}

// folderUnwatch offers unwatch on a folder tile only when the folder itself
// is the link: tiles further down a watched tree would remove far more than
// they show.
func folderUnwatch(rel string) *unwatchAction {
	root, err := store.Root()
	if err != nil {
		return nil
	}
	fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	return unwatchLink(rel, rel)
}

type pageView struct {
	Folder string // "" on the home page
	View   string // deep-linked overlay target, "" for a plain folder page
	Crumbs []crumb
	List   listView
}

type listView struct {
	Folder string
	Cards  []card
}

// collectionCard renders a multi-page artifact as a browsable folder tile.
func collectionCard(a store.Artifact, label string) card {
	return card{Kind: "project", Label: label, Href: "/p/" + a.RelPath(),
		Count: len(a.Pages), Unit: "pages", Preview: a, Unwatch: artifactUnwatch(a)}
}

func pageCard(label, href, viewerHref, absPath string, isDoc bool) card {
	c := card{Kind: "page", Label: label, PageHref: href, ViewerHref: viewerHref, PageIsDoc: isDoc}
	if fi, err := os.Stat(absPath); err == nil {
		c.PageSize = fi.Size()
		c.PageMod = fi.ModTime()
	}
	return c
}

// pageCards renders one tile per top-level html/md file of a multi-page artifact.
func pageCards(a store.Artifact) []card {
	var cards []card
	for _, f := range a.Pages {
		base := "/a/" + a.RelPath() + "/" + f
		cards = append(cards, pageCard(
			strings.TrimSuffix(f, filepath.Ext(f)),
			base, "/fragments/viewer/"+a.RelPath()+"/"+f,
			filepath.Join(a.Dir, f),
			strings.HasSuffix(strings.ToLower(f), ".md")))
	}
	return cards
}

// docCount counts markdown files in a subtree, skipping ignored dirs and not
// descending into artifacts (their pages are counted as theirs).
func docCount(dir string) int {
	n := 0
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		name := e.Name()
		isDir := entryIsDirFS(dir, e)
		if !store.Visible(dir, name, isDir) {
			continue
		}
		if isDir {
			sub := filepath.Join(dir, name)
			if !dirHasHTML(sub) {
				n += docCount(sub)
			}
		} else if strings.HasSuffix(strings.ToLower(name), ".md") {
			n++
		}
	}
	return n
}

func entryIsDirFS(parent string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&fs.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(parent, e.Name()))
	return err == nil && fi.IsDir()
}

func dirHasHTML(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
			return true
		}
	}
	return false
}

// folderExtras appends cards artifacts can't produce: loose markdown files
// directly in folder f, and doc-only subfolders (no artifacts anywhere but
// markdown within).
func folderExtras(f string, used map[string]bool) []card {
	root, err := store.Root()
	if err != nil {
		return nil
	}
	dir := filepath.Join(root, filepath.FromSlash(f))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := ""
	if f != "" {
		prefix = f + "/"
	}
	var cards []card
	for _, e := range entries {
		name := e.Name()
		if used[name] || !store.Visible(dir, name, entryIsDirFS(dir, e)) {
			continue
		}
		if entryIsDirFS(dir, e) {
			if n := docCount(filepath.Join(dir, name)); n > 0 {
				cards = append(cards, card{Kind: "project", Label: name,
					Href: "/p/" + prefix + name, Count: n, Unit: "docs",
					Unwatch: folderUnwatch(prefix + name)})
			}
		} else if strings.HasSuffix(strings.ToLower(name), ".md") {
			cards = append(cards, pageCard(
				strings.TrimSuffix(name, filepath.Ext(name)),
				"/a/"+prefix+name, "/fragments/viewer/"+prefix+name,
				filepath.Join(dir, name), true))
		}
	}
	return cards
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
	used := map[string]bool{}
	for _, a := range artifacts {
		switch {
		case a.Project == f:
			used[a.Name] = true
			if a.MultiPage() {
				cards = append(cards, collectionCard(a, a.Name))
			} else {
				cards = append(cards, card{Kind: "artifact", Artifact: a, Unwatch: artifactUnwatch(a)})
			}
		case !strings.HasPrefix(a.Project+"/", prefix):
			// outside this folder
		default:
			child := strings.SplitN(strings.TrimPrefix(a.Project, prefix), "/", 2)[0]
			docs := 0
			if perChild[child] == 1 && !used[child] {
				docs = childDocs(prefix, child)
			}
			if perChild[child] == 1 && docs == 0 {
				used[child] = true
				if a.MultiPage() {
					cards = append(cards, collectionCard(a, strings.TrimPrefix(a.RelPath(), prefix)))
				} else {
					cards = append(cards, card{Kind: "artifact", Artifact: a,
						PrefixLabel: strings.TrimPrefix(a.Project, prefix),
						PrefixHref:  "/p/" + a.Project,
						Unwatch:     artifactUnwatch(a)})
				}
			} else if !used[child] {
				used[child] = true
				c := card{Kind: "project", Label: child,
					Href: "/p/" + prefix + child, Count: perChild[child], Unit: "artifacts", Preview: a,
					Unwatch: folderUnwatch(prefix + child)}
				if docs > 0 {
					c.Count += docs
					c.Unit = "items"
				}
				cards = append(cards, c)
			}
		}
	}
	return append(cards, folderExtras(f, used)...)
}

// childDocs counts markdown under child folder of the current folder; a
// non-zero count blocks the single-artifact collapse so those docs stay
// reachable from the folder page.
func childDocs(prefix, child string) int {
	root, err := store.Root()
	if err != nil {
		return 0
	}
	return docCount(filepath.Join(root, filepath.FromSlash(prefix+child)))
}

// folderExists reports whether f names a real directory under the root.
func folderExists(f string) bool {
	if f == "" {
		return true
	}
	root, err := store.Root()
	if err != nil {
		return false
	}
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(f)))
	return err == nil && fi.IsDir()
}

// resolveCollection returns the multi-page artifact at path f, if that is
// what f names.
func resolveCollection(f string) (store.Artifact, bool) {
	if f == "" {
		return store.Artifact{}, false
	}
	a, file, ok := store.ResolvePath(strings.Split(f, "/"))
	if !ok || file != "" || !a.MultiPage() {
		return store.Artifact{}, false
	}
	return a, true
}

// resolveView reports whether f names something the viewer overlay can show
// as a /p/ deep link — a top-level page of an artifact, a single-page
// artifact, or a loose markdown doc — and returns the folder page to render
// underneath the overlay.
func resolveView(f string) (folder string, ok bool) {
	segs := strings.Split(f, "/")
	if a, file, ok := store.ResolvePath(segs); ok {
		if file == "" {
			if a.MultiPage() {
				return "", false // collection pages render as folders, not overlays
			}
			return a.Project, true
		}
		for _, p := range a.Pages {
			if p == file {
				if a.MultiPage() {
					return a.RelPath(), true
				}
				return a.Project, true
			}
		}
		return "", false
	}
	if _, isDoc := store.ResolveDoc(segs); isDoc {
		return strings.Join(segs[:len(segs)-1], "/"), true
	}
	return "", false
}

func handleFolderPage(w http.ResponseWriter, r *http.Request) {
	f := strings.Trim(r.PathValue("path"), "/")
	view := ""
	if f != "" {
		// Hidden folders are unreachable, not just unlisted: the same rules
		// that keep a path off the index keep it off its own page.
		if !store.VisiblePath(f) {
			http.NotFound(w, r)
			return
		}
		if folder, ok := resolveView(f); ok {
			view, f = f, folder
		} else if _, isCollection := resolveCollection(f); !isCollection && !folderExists(f) {
			http.NotFound(w, r)
			return
		}
	}
	list, err := buildListView(f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "index.tmpl", pageView{Folder: f, View: view, Crumbs: crumbs(f), List: list}); err != nil {
		log.Printf("render folder page: %v", err)
	}
}

func buildListView(f string) (listView, error) {
	view := listView{Folder: f}
	if a, isCollection := resolveCollection(f); isCollection {
		view.Cards = pageCards(a)
		return view, nil
	}
	artifacts, err := store.List()
	if err != nil {
		return view, err
	}
	view.Cards = buildCards(artifacts, f)
	return view, nil
}

func handleListFragment(w http.ResponseWriter, r *http.Request) {
	view, err := buildListView(strings.Trim(r.URL.Query().Get("project"), "/"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmpl.ExecuteTemplate(w, "list.tmpl", view); err != nil {
		log.Printf("render list: %v", err)
	}
}

// sibItem is one entry in a breadcrumb's hover dropdown. Folder items
// navigate; Viewer items open in the artifact overlay instead.
type sibItem struct {
	Name   string
	Href   string
	Viewer bool
}

type sibView struct {
	Items []sibItem
}

// siblingItems lists everything living next to a child of folder parent:
// artifacts (single-page → viewer, multi-page → their collection page),
// subfolders with any renderable content, and loose markdown docs. When
// parent is itself a multi-page artifact, the neighbours are its pages.
func siblingItems(parent string) []sibItem {
	if a, ok := resolveCollection(parent); ok {
		var items []sibItem
		for _, p := range a.Pages {
			items = append(items, sibItem{Name: strings.TrimSuffix(p, filepath.Ext(p)),
				Href: "/fragments/viewer/" + a.RelPath() + "/" + p, Viewer: true})
		}
		return items
	}
	root, err := store.Root()
	if err != nil {
		return nil
	}
	dir := filepath.Join(root, filepath.FromSlash(parent))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	prefix := ""
	if parent != "" {
		prefix = parent + "/"
	}
	var items []sibItem
	for _, e := range entries {
		name := e.Name()
		if !store.Visible(dir, name, entryIsDirFS(dir, e)) {
			continue
		}
		rel := prefix + name
		if entryIsDirFS(dir, e) {
			if dirHasHTML(filepath.Join(dir, name)) {
				if a, _, ok := store.ResolvePath(strings.Split(rel, "/")); ok && !a.MultiPage() {
					items = append(items, sibItem{Name: name, Href: "/fragments/viewer/" + rel, Viewer: true})
					continue
				}
			}
			if hasRenderable(filepath.Join(dir, name)) {
				items = append(items, sibItem{Name: name, Href: "/p/" + rel})
			}
		} else if strings.HasSuffix(strings.ToLower(name), ".md") {
			items = append(items, sibItem{Name: strings.TrimSuffix(name, filepath.Ext(name)),
				Href: "/fragments/viewer/" + rel, Viewer: true})
		}
	}
	return items
}

// hasRenderable reports whether any html or markdown lives under dir, so the
// popover skips folders that would render an empty page.
func hasRenderable(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		name := e.Name()
		isDir := entryIsDirFS(dir, e)
		if !store.Visible(dir, name, isDir) {
			continue
		}
		if isDir {
			if hasRenderable(filepath.Join(dir, name)) {
				return true
			}
		} else if l := strings.ToLower(name); strings.HasSuffix(l, ".html") || strings.HasSuffix(l, ".md") {
			return true
		}
	}
	return false
}

func handleSiblings(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(r.URL.Query().Get("path"), "/")
	if rel == "" || !store.VisiblePath(rel) {
		http.NotFound(w, r)
		return
	}
	segs := strings.Split(rel, "/")
	norm := func(s string) string {
		return strings.ToLower(strings.TrimSuffix(s, filepath.Ext(s)))
	}
	items := siblingItems(strings.Join(segs[:len(segs)-1], "/"))
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})
	// The hovered entry itself stays out of its own dropdown.
	cur := norm(segs[len(segs)-1])
	var view sibView
	for _, it := range items {
		if norm(it.Name) != cur {
			view.Items = append(view.Items, it)
		}
	}
	if err := tmpl.ExecuteTemplate(w, "siblings.tmpl", view); err != nil {
		log.Printf("render siblings: %v", err)
	}
}

type viewerView struct {
	URL     string // iframe src
	Title   string // final crumb
	Path    string // display path under ~/.scratchpad
	Size    int64
	ModTime int64 // unix seconds; appended to the iframe src so an SSE-driven
	// re-render actually reloads it instead of idiomorph leaving an
	// unchanged src attribute (and therefore the iframe) untouched
	Crumbs []crumb
}

func handleViewerFragment(w http.ResponseWriter, r *http.Request) {
	a, file, ok := resolveRequest(r)
	if !ok {
		// Viewer for loose markdown in project directories.
		raw := strings.Trim(r.PathValue("path"), "/")
		segs := strings.Split(raw, "/")
		if p, isDoc := store.ResolveDoc(segs); isDoc {
			view := viewerView{
				URL:    "/a/" + raw,
				Title:  strings.TrimSuffix(segs[len(segs)-1], filepath.Ext(segs[len(segs)-1])),
				Path:   raw,
				Crumbs: crumbs(strings.Join(segs[:len(segs)-1], "/")),
			}
			for i := range view.Crumbs {
				view.Crumbs[i].Last = false
			}
			if fi, err := os.Stat(p); err == nil {
				view.Size = fi.Size()
				view.ModTime = fi.ModTime().Unix()
			}
			if err := tmpl.ExecuteTemplate(w, "viewer.tmpl", view); err != nil {
				log.Printf("render viewer: %v", err)
			}
			return
		}
		http.NotFound(w, r)
		return
	}
	view := viewerView{Crumbs: crumbs(a.Project)}
	for i := range view.Crumbs {
		view.Crumbs[i].Last = false // the title is the final crumb
	}
	if file == "" {
		view.URL = "/a/" + a.RelPath() + "/"
		view.Title = a.Name
		view.Path = a.RelPath()
		view.Size = a.Size
		view.ModTime = a.ModTime.Unix()
	} else {
		// Only top-level html pages get a viewer; other files just serve.
		page := false
		for _, p := range a.Pages {
			page = page || p == file
		}
		if !page {
			http.NotFound(w, r)
			return
		}
		view.URL = "/a/" + a.RelPath() + "/" + file
		view.Title = strings.TrimSuffix(file, filepath.Ext(file))
		view.Path = a.RelPath() + "/" + file
		view.Crumbs = append(view.Crumbs, crumb{Name: a.Name, Href: "/p/" + a.RelPath(), Rel: a.RelPath()})
		if fi, err := os.Stat(filepath.Join(a.Dir, file)); err == nil {
			view.Size = fi.Size()
			view.ModTime = fi.ModTime().Unix()
		}
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
	// Served straight off disk on every request, so the browser must always
	// revalidate rather than risk showing a stale copy of a file that was
	// just edited (watched folders in particular churn on every save).
	w.Header().Set("Cache-Control", "no-cache")
	a, file, ok := resolveRequest(r)
	if !ok {
		// Loose markdown living in plain project directories renders too.
		raw := strings.Trim(r.PathValue("path"), "/")
		if p, isDoc := store.ResolveDoc(strings.Split(raw, "/")); isDoc {
			serveMarkdown(w, r, p, filepath.Base(p))
			return
		}
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
	full := filepath.Join(a.Dir, clean)
	if strings.HasSuffix(strings.ToLower(clean), ".md") {
		serveMarkdown(w, r, full, filepath.Base(clean))
		return
	}
	http.ServeFile(w, r, full)
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

// handleUnwatch removes a watch link. It is separate from handleDelete
// because a watched project tree is not an artifact — nothing resolves it —
// and because store.Unwatch can only ever unlink, never remove files.
func handleUnwatch(w http.ResponseWriter, r *http.Request) {
	rel := strings.Trim(r.PathValue("path"), "/")
	if rel == "" {
		http.NotFound(w, r)
		return
	}
	project, name := "", rel
	if i := strings.LastIndex(rel, "/"); i >= 0 {
		project, name = rel[:i], rel[i+1:]
	}
	if err := store.Unwatch(project, name); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
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
