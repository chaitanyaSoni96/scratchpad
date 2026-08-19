package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// publishDoc is a small helper: publish a single-page artifact so its
// index.html is a real, DocExists-able document at project/name/index.html.
func publishDoc(t *testing.T, project, name string) string {
	t.Helper()
	if _, err := Publish(project, name, map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
		t.Fatalf("publish %s/%s: %v", project, name, err)
	}
	if project == "" {
		return name + "/index.html"
	}
	return project + "/" + name + "/index.html"
}

func TestNotesPath(t *testing.T) {
	testRoot(t)
	// A leading/trailing "/" is normalized away (store-relative paths are
	// already slash-rooted at the store, so this just tolerates URL-style
	// input), not treated as an absolute-path escape.
	valid := []string{"a/b.html", "lab/todo.md", "x/y/z/index.html", "/a/b.html"}
	for _, doc := range valid {
		if _, err := notesPath(doc); err != nil {
			t.Errorf("notesPath(%q) = %v, want nil", doc, err)
		}
	}
	invalid := []string{
		"", "..", "../x.html", "a/../b.html", "a//b.html", `a\b.html`,
		"a/b.txt", "noext", ".hidden",
	}
	for _, doc := range invalid {
		if _, err := notesPath(doc); err == nil {
			t.Errorf("notesPath(%q) = nil, want error", doc)
		}
	}
}

func TestLoadSaveNotesLifecycle(t *testing.T) {
	testRoot(t)
	doc := publishDoc(t, "", "art")

	f, err := LoadNotes(doc)
	if err != nil {
		t.Fatalf("LoadNotes on doc with no sidecar: %v", err)
	}
	if f.Rev != 0 || len(f.Annotations) != 0 {
		t.Errorf("LoadNotes zero value = %+v, want Rev 0, no annotations", f)
	}

	f.Annotations = []Annotation{{ID: "n1", Status: "open", Body: "fix this", Target: Target{Type: "element", Selector: "#x"}}}
	saved, err := SaveNotes(doc, f, 0)
	if err != nil {
		t.Fatalf("SaveNotes create: %v", err)
	}
	if saved.Rev != 1 || len(saved.Annotations) != 1 {
		t.Fatalf("SaveNotes create result = %+v", saved)
	}

	root, _ := Root()
	p := filepath.Join(root, AnnotationsDir, "art", "index.html.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("sidecar not written at expected path: %v", err)
	}

	reloaded, err := LoadNotes(doc)
	if err != nil || reloaded.Rev != 1 || len(reloaded.Annotations) != 1 {
		t.Fatalf("reload after save = %+v, %v", reloaded, err)
	}

	// Saving with zero annotations removes the file and prunes empty dirs.
	empty, err := SaveNotes(doc, NotesFile{}, 1)
	if err != nil {
		t.Fatalf("SaveNotes empty: %v", err)
	}
	if empty.Rev != 2 || len(empty.Annotations) != 0 {
		t.Fatalf("SaveNotes empty result = %+v", empty)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("sidecar should be removed once annotations reach zero, stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, AnnotationsDir, "art")); !os.IsNotExist(err) {
		t.Errorf("now-empty .annotations/art should be pruned, stat err = %v", err)
	}
}

func TestSaveNotesRevMismatch(t *testing.T) {
	testRoot(t)
	doc := publishDoc(t, "", "art")

	f := NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open", Body: "x"}}}
	if _, err := SaveNotes(doc, f, 0); err != nil {
		t.Fatal(err)
	}
	// stale expectRev
	_, err := SaveNotes(doc, f, 0)
	if !errors.Is(err, ErrRevMismatch) {
		t.Fatalf("SaveNotes with stale rev = %v, want ErrRevMismatch", err)
	}
	// file must be untouched
	cur, err := LoadNotes(doc)
	if err != nil || cur.Rev != 1 {
		t.Fatalf("file mutated after rejected save: %+v, %v", cur, err)
	}
}

func TestSaveNotesRequiresDocExists(t *testing.T) {
	testRoot(t)
	_, err := SaveNotes("ghost/index.html", NotesFile{Annotations: []Annotation{{ID: "n1"}}}, 0)
	if err == nil {
		t.Fatal("SaveNotes on a nonexistent doc should fail")
	}
}

func TestResolveAndReplyNote(t *testing.T) {
	testRoot(t)
	doc := publishDoc(t, "", "art")
	f := NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open", Body: "fix the header"}}}
	if _, err := SaveNotes(doc, f, 0); err != nil {
		t.Fatal(err)
	}

	a, err := ResolveNote(doc, "n1", "moved the header")
	if err != nil {
		t.Fatalf("ResolveNote: %v", err)
	}
	if a.Status != "resolved" {
		t.Errorf("ResolveNote status = %q, want resolved", a.Status)
	}
	if len(a.Replies) != 1 || a.Replies[0].By != "agent" || a.Replies[0].Action != "resolve" || a.Replies[0].Body != "moved the header" {
		t.Errorf("ResolveNote reply = %+v", a.Replies)
	}

	a, err = ReplyNote(doc, "n1", "actually, one more question")
	if err != nil {
		t.Fatalf("ReplyNote: %v", err)
	}
	if a.Status != "resolved" {
		t.Errorf("ReplyNote must not change status, got %q", a.Status)
	}
	if len(a.Replies) != 2 || a.Replies[1].Action != "" {
		t.Errorf("ReplyNote reply = %+v", a.Replies)
	}

	if _, err := ResolveNote(doc, "missing", "x"); !errors.Is(err, ErrNoteNotFound) {
		t.Errorf("ResolveNote(missing id) = %v, want ErrNoteNotFound", err)
	}
	if _, err := ReplyNote("no/such.html", "n1", "x"); !errors.Is(err, ErrNoteNotFound) {
		t.Errorf("ReplyNote on a doc with no sidecar = %v, want ErrNoteNotFound", err)
	}
}

func TestAnnotationsInvisible(t *testing.T) {
	root := testRoot(t)
	resetIgnoreCache()

	// Even an explicit override in a real ignore file cannot un-hide it.
	writeFile(t, root, ".scratchpadignore", "!.annotations\n")
	if Visible(root, AnnotationsDir, true) {
		t.Error("Visible(root, .annotations, true) = true, want false even with a ! override")
	}

	if VisiblePath(".annotations") {
		t.Error("VisiblePath(.annotations) = true, want false")
	}
	if VisiblePath(".annotations/x/index.html.json") {
		t.Error("VisiblePath(.annotations/x/index.html.json) = true, want false")
	}
	if _, _, ok := ResolvePath([]string{".annotations", "x", "index.html"}); ok {
		t.Error("ResolvePath under .annotations should never resolve")
	}

	// A directory under .annotations that would otherwise qualify as an
	// artifact (contains a top-level .html) must never surface via List.
	os.MkdirAll(filepath.Join(root, AnnotationsDir, "foo"), 0o755)
	os.WriteFile(filepath.Join(root, AnnotationsDir, "foo", "index.html"), []byte("<p>"), 0o644)

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range list {
		if a.RelPath() == "foo" || a.RelPath() == ".annotations/foo" {
			t.Errorf("List() surfaced content under .annotations: %+v", a)
		}
	}
}

func TestDeleteAndUnwatchRemoveNotes(t *testing.T) {
	testRoot(t)
	root, _ := Root()

	// Delete of a published artifact drops its notes.
	doc := publishDoc(t, "", "art")
	if _, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open"}}}, 0); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(root, AnnotationsDir, "art", "index.html.json")
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar missing before delete: %v", err)
	}
	if err := Delete("", "art"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Errorf("Delete should remove notes, stat err = %v", err)
	}

	// Republishing under the same name sees zero notes.
	doc2 := publishDoc(t, "", "art")
	f, err := LoadNotes(doc2)
	if err != nil || f.Rev != 0 || len(f.Annotations) != 0 {
		t.Errorf("republished artifact should see no notes, got %+v, %v", f, err)
	}

	// Unwatch drops notes on the watched link's docs too.
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>src</h1>"), 0o644)
	if _, err := Watch("", "linked", src); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveNotes("linked/index.html", NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open"}}}, 0); err != nil {
		t.Fatal(err)
	}
	linkSidecar := filepath.Join(root, AnnotationsDir, "linked", "index.html.json")
	if _, err := os.Stat(linkSidecar); err != nil {
		t.Fatalf("sidecar missing before unwatch: %v", err)
	}
	if err := Unwatch("", "linked"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(linkSidecar); !os.IsNotExist(err) {
		t.Errorf("Unwatch should remove notes, stat err = %v", err)
	}
}

func TestWalkNotesAndOpenCount(t *testing.T) {
	testRoot(t)
	docA := publishDoc(t, "proj", "multi") // proj/multi/index.html

	// second doc in the same artifact
	if _, err := SaveNotes(docA, NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open"}}}, 0); err != nil {
		t.Fatal(err)
	}
	root, _ := Root()
	os.WriteFile(filepath.Join(root, "proj", "multi", "notes.md"), []byte("# notes"), 0o644)
	docB := "proj/multi/notes.md"
	if _, err := SaveNotes(docB, NotesFile{Annotations: []Annotation{
		{ID: "n2", Status: "open"},
		{ID: "n3", Status: "resolved"},
	}}, 0); err != nil {
		t.Fatal(err)
	}

	docs, err := WalkNotes("proj/multi")
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Doc != docA || docs[1].Doc != docB {
		t.Fatalf("WalkNotes(artifact) = %+v, want [%s, %s] sorted", docs, docA, docB)
	}

	single, err := WalkNotes(docA)
	if err != nil {
		t.Fatal(err)
	}
	if len(single) != 1 || single[0].Doc != docA {
		t.Fatalf("WalkNotes(doc) = %+v", single)
	}

	// a doc with no sidecar returns an empty (not nil-erroring) result
	none, err := WalkNotes("proj/multi/ghost.md")
	if err != nil || len(none) != 0 {
		t.Fatalf("WalkNotes(doc with no notes) = %+v, %v", none, err)
	}

	if n := OpenNoteCount("proj/multi"); n != 2 {
		t.Errorf("OpenNoteCount = %d, want 2", n)
	}
	if n := OpenNoteCount(""); n != 2 {
		t.Errorf("OpenNoteCount(whole store) = %d, want 2", n)
	}
}
