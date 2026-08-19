package web

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"scratchpad/internal/store"
)

// testRoot sets up an isolated store root for one test, mirroring
// store_test.go's helper (unexported there, so web keeps its own copy).
func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(store.RootEnv, root)
	return root
}

// notesMux builds the routes under test. NewServer(nil) works fine here: the
// nil *watch.Hub is only dereferenced by the /events handler, which none of
// these tests exercise.
func notesMux(t *testing.T) http.Handler {
	t.Helper()
	return NewServer(nil)
}

// seedArtifact writes a minimal single-page artifact at relPath (a project
// path plus artifact name, e.g. "demo/q3") and returns its doc path
// ("demo/q3/index.html"). Any directory directly containing a .html file is
// an artifact per store's own rule, so a bare WriteFile is enough — no need
// to go through store.Publish.
func seedArtifact(t *testing.T, root, relPath string) string {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>hi</body></html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return relPath + "/index.html"
}

// seedFolder makes an ordinary (non-artifact) directory, used for the
// /p/<folder>/notes shadowing test where "notes" is real content.
func seedFolder(t *testing.T, root, relPath string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(relPath)), 0o755); err != nil {
		t.Fatal(err)
	}
}

func doReq(t *testing.T, mux http.Handler, method, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req := httptest.NewRequest(method, target, r)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestNotesWriteConflict(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)

	seeded := store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "fix this"},
	}}
	saved, err := store.SaveNotes(doc, seeded, 0)
	if err != nil {
		t.Fatalf("seed SaveNotes: %v", err)
	}
	if saved.Rev != 1 {
		t.Fatalf("seeded rev = %d, want 1", saved.Rev)
	}

	// PUT with a stale rev (0, but the file is now at rev 1) must 409 and
	// carry the current file so the caller can refetch-and-replay.
	body, _ := json.Marshal(store.NotesFile{Rev: 0, Annotations: []store.Annotation{
		{ID: "a1", Status: "resolved", Body: "fix this"},
	}})
	rec := doReq(t, mux, "PUT", "/notes/"+doc, body, nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var cur store.NotesFile
	if err := json.Unmarshal(rec.Body.Bytes(), &cur); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if cur.Rev != 1 || len(cur.Annotations) != 1 || cur.Annotations[0].ID != "a1" {
		t.Errorf("conflict body = %+v, want the current rev-1 file", cur)
	}
}

func TestNotesWriteHappyPathBumpsRev(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)

	body, _ := json.Marshal(store.NotesFile{Rev: 0, Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "hello"},
	}})
	rec := doReq(t, mux, "PUT", "/notes/"+doc, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	var saved store.NotesFile
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if saved.Rev != 1 {
		t.Fatalf("returned rev = %d, want 1", saved.Rev)
	}

	// A subsequent GET ?format=json should show the write.
	rec = doReq(t, mux, "GET", "/notes/"+doc+"?format=json", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET status = %d", rec.Code)
	}
	var docs []store.DocNotes
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if len(docs) != 1 || docs[0].Doc != doc || len(docs[0].Notes.Annotations) != 1 {
		t.Fatalf("GET docs = %+v, want one doc with one annotation", docs)
	}
}

func TestNotesWriteEmptyAnnotationsRemovesSidecar(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)

	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "x"},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	body, _ := json.Marshal(store.NotesFile{Rev: 1, Annotations: nil})
	rec := doReq(t, mux, "PUT", "/notes/"+doc, body, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	rec = doReq(t, mux, "GET", "/notes/"+doc+"?format=json&status=all", nil, nil)
	var docs []store.DocNotes
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("docs = %+v, want empty (sidecar removed)", docs)
	}
}

func TestNotesStatusFilter(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)

	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "open1", Status: "open", Body: "still broken"},
		{ID: "res1", Status: "resolved", Body: "fixed", Replies: []store.Reply{
			{By: "agent", Action: "resolve", Body: "done"},
		}},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Default (?status omitted) hides resolved notes.
	rec := doReq(t, mux, "GET", "/notes/"+doc+"?format=json", nil, nil)
	var docs []store.DocNotes
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decode default: %v", err)
	}
	if len(docs) != 1 || len(docs[0].Notes.Annotations) != 1 || docs[0].Notes.Annotations[0].ID != "open1" {
		t.Fatalf("default-status docs = %+v, want just open1", docs)
	}

	// status=all shows both.
	rec = doReq(t, mux, "GET", "/notes/"+doc+"?format=json&status=all", nil, nil)
	docs = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &docs); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(docs) != 1 || len(docs[0].Notes.Annotations) != 2 {
		t.Fatalf("status=all docs = %+v, want both notes", docs)
	}

	// status=open is explicit-equivalent to the default.
	rec = doReq(t, mux, "GET", "/notes/"+doc+"?format=json&status=open", nil, nil)
	docs = nil
	json.Unmarshal(rec.Body.Bytes(), &docs)
	if len(docs) != 1 || len(docs[0].Notes.Annotations) != 1 {
		t.Fatalf("status=open docs = %+v, want just the open note", docs)
	}

	// A bad status value is rejected.
	rec = doReq(t, mux, "GET", "/notes/"+doc+"?status=bogus", nil, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad status: status = %d, want 400", rec.Code)
	}
}

func TestNotesFormatNegotiation(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)
	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "hello"},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// ?format=json.
	rec := doReq(t, mux, "GET", "/notes/"+doc+"?format=json", nil, nil)
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=utf-8" {
		t.Errorf("json Content-Type = %q", ct)
	}

	// Default, no Accept header: plain markdown for curl/agents.
	rec = doReq(t, mux, "GET", "/notes/"+doc, nil, nil)
	if ct := rec.Header().Get("Content-Type"); ct != "text/markdown; charset=utf-8" {
		t.Errorf("default Content-Type = %q", ct)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("hello")) {
		t.Errorf("markdown report missing note body: %s", rec.Body.String())
	}

	// Default with Accept: text/html: the styled goldmark page.
	rec = doReq(t, mux, "GET", "/notes/"+doc, nil, map[string]string{"Accept": "text/html,*/*"})
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("html Content-Type = %q", ct)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("<html")) {
		t.Errorf("expected a rendered HTML page, got: %s", rec.Body.String())
	}
}

func TestNotesFolderShadowing(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)
	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "shadow test"},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Nothing real at demo/q3/notes -> the convenience form serves the report.
	rec := doReq(t, mux, "GET", "/p/demo/q3/notes", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("shadow test")) {
		t.Errorf("expected the notes report, got: %s", rec.Body.String())
	}

	// Now make "notes" real content (a folder) directly under demo/q3's
	// parent; real content must win over the convenience form.
	seedFolder(t, root, "demo/notes")
	rec = doReq(t, mux, "GET", "/p/demo/notes", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("real folder: status = %d, want 200", rec.Code)
	}
	// A real, empty folder page renders the folder page template (an HTML
	// page listing its — empty — contents), not the markdown notes report.
	if bytes.Contains(rec.Body.Bytes(), []byte("shadow test")) {
		t.Errorf("real folder named notes must not be shadowed by the notes report")
	}
}

func TestNotesReadHiddenPath404s(t *testing.T) {
	testRoot(t)
	mux := notesMux(t)
	rec := doReq(t, mux, "GET", "/notes/.annotations", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestNotesDelete(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)
	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "x"},
		{ID: "a2", Status: "resolved", Body: "y"},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doReq(t, mux, "DELETE", "/notes/"+doc, nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("delete body = %q, want empty", rec.Body.String())
	}

	rec = doReq(t, mux, "GET", "/notes/"+doc+"?format=json&status=all", nil, nil)
	var docs []store.DocNotes
	json.Unmarshal(rec.Body.Bytes(), &docs)
	if len(docs) != 0 {
		t.Errorf("docs after delete = %+v, want none", docs)
	}
}

func TestNotesReportGzipped(t *testing.T) {
	root := testRoot(t)
	doc := seedArtifact(t, root, "demo/q3")
	mux := notesMux(t)
	if _, err := store.SaveNotes(doc, store.NotesFile{Annotations: []store.Annotation{
		{ID: "a1", Status: "open", Body: "gzip me please, this needs to be long enough that compression is worth negotiating over the wire in a real deployment"},
	}}, 0); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rec := doReq(t, mux, "GET", "/notes/"+doc, nil, map[string]string{"Accept-Encoding": "gzip"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	zr, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	got, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !bytes.Contains(got, []byte("gzip me please")) {
		t.Errorf("decompressed body missing note text: %s", got)
	}
}
