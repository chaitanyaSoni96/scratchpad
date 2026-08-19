package web

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"scratchpad/internal/store"
)

// maxNotesBody bounds a PUT body. The endpoint replaces one document's
// notes file wholesale, which is always small (a handful of comments and
// their threads); the cap exists because the endpoint is unauthenticated —
// a local tool should still not accept an unbounded payload by mistake.
const maxNotesBody = 4 << 20

// looksLikeDoc reports whether path's final segment marks it as an
// annotatable document, mirroring store's own hasDocExt check (unexported,
// so the web layer keeps its own copy rather than reaching into store's
// internals for it).
func looksLikeDoc(path string) bool {
	l := strings.ToLower(path)
	return strings.HasSuffix(l, ".html") || strings.HasSuffix(l, ".md")
}

// handleNotesRead is the canonical GET /notes/{path...} route.
func handleNotesRead(w http.ResponseWriter, r *http.Request) {
	serveNotesRead(w, r, strings.Trim(r.PathValue("path"), "/"))
}

// serveNotesRead renders the notes report for a store-relative path — a
// document, an artifact, a folder, or "" for the whole store. It takes path
// as a parameter (rather than reading r.PathValue itself) so
// handleFolderPage's /p/{path...}/notes convenience form can delegate here
// with the parent path once it has confirmed nothing real resolves there.
func serveNotesRead(w http.ResponseWriter, r *http.Request, path string) {
	// Hidden paths stay unreachable, same rule as handleFolderPage.
	if path != "" && !store.VisiblePath(path) {
		http.NotFound(w, r)
		return
	}

	status := r.URL.Query().Get("status")
	if status == "" {
		status = "open"
	}
	var all bool
	switch status {
	case "open":
		all = false
	case "all":
		all = true
	default:
		http.Error(w, `invalid status: must be "open" or "all"`, http.StatusBadRequest)
		return
	}

	docs, err := store.WalkNotes(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Read fresh off disk on every request, like everything else here.
	w.Header().Set("Cache-Control", "no-cache")

	if r.URL.Query().Get("format") == "json" {
		out := filterStatus(docs, all)
		if out == nil {
			out = []store.DocNotes{} // a JS client should never special-case null
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			log.Printf("encode notes: %v", err)
		}
		return
	}

	// FormatReport does its own open/resolved split (it needs the resolved
	// count even in open-only mode, to print "N resolved, not shown"), so it
	// gets the unfiltered docs — unlike the JSON branch above.
	report := store.FormatReport(docs, store.ReportOptions{Path: path, All: all})

	title := path
	if title == "" {
		title = "scratchpad"
	}
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		// Same styled page every other .md doc gets.
		if err := writeMarkdownPage(w, "Notes — "+title, []byte(report)); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	io.WriteString(w, report)
}

// filterStatus keeps only open annotations (all==false) and drops any
// document left with none, producing the JSON shape for GET
// /notes?status=open — the default a client normally wants.
func filterStatus(docs []store.DocNotes, all bool) []store.DocNotes {
	if all {
		return docs
	}
	out := make([]store.DocNotes, 0, len(docs))
	for _, d := range docs {
		kept := make([]store.Annotation, 0, len(d.Notes.Annotations))
		for _, a := range d.Notes.Annotations {
			if a.Status == "open" {
				kept = append(kept, a)
			}
		}
		if len(kept) == 0 {
			continue
		}
		d.Notes.Annotations = kept
		out = append(out, d)
	}
	return out
}

// handleNotesWrite is PUT /notes/{path...}: replace one document's notes
// file. The viewer is the only writer over HTTP (the CLI edits sidecars
// directly); this is a coarse whole-file PUT guarded by the rev the client
// loaded, per the spec's optimistic-concurrency design.
func handleNotesWrite(w http.ResponseWriter, r *http.Request) {
	doc := strings.Trim(r.PathValue("path"), "/")
	if doc != "" && !store.VisiblePath(doc) {
		http.NotFound(w, r)
		return
	}
	if !looksLikeDoc(doc) {
		http.Error(w, "path must name a .html or .md document", http.StatusBadRequest)
		return
	}
	if !store.DocExists(doc) {
		http.Error(w, "no such document", http.StatusBadRequest)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxNotesBody)
	var f store.NotesFile
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	saved, err := store.SaveNotes(doc, f, f.Rev)
	if err != nil {
		if errors.Is(err, store.ErrRevMismatch) {
			// The conflict carries the current file (not just a bare 409) so
			// the viewer can refetch-and-replay its edit in one round trip
			// instead of a second GET.
			cur, lerr := store.LoadNotes(doc)
			if lerr != nil {
				http.Error(w, lerr.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(withEmptyList(cur))
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(withEmptyList(saved))
}

// withEmptyList makes a note-less file encode as [] rather than null, so the
// viewer's JS can treat every response the same way — a nil slice is a Go
// distinction the wire has no reason to carry.
func withEmptyList(f store.NotesFile) store.NotesFile {
	if f.Annotations == nil {
		f.Annotations = []store.Annotation{}
	}
	return f
}

// handleNotesDelete is DELETE /notes/{path...}: remove every note on one
// document. Same trust tier and 200-empty-body convention as handleDelete.
func handleNotesDelete(w http.ResponseWriter, r *http.Request) {
	doc := strings.Trim(r.PathValue("path"), "/")
	if doc != "" && !store.VisiblePath(doc) {
		http.NotFound(w, r)
		return
	}
	if !looksLikeDoc(doc) {
		http.Error(w, "path must name a .html or .md document", http.StatusBadRequest)
		return
	}
	if err := store.DeleteNotes(doc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
