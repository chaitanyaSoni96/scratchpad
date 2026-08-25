package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv(RootEnv, root)
	return root
}

func TestValidateName(t *testing.T) {
	valid := []string{"a", "my-artifact", "v1.2_final", "A9", "img.png"}
	for _, s := range valid {
		if err := validateName(s); err != nil {
			t.Errorf("validateName(%q) = %v, want nil", s, err)
		}
	}
	invalid := []string{"", ".", "..", "a/b", `a\b`, "../x", ".hidden", "-lead", "_lead",
		strings.Repeat("x", 200)}
	for _, s := range invalid {
		if err := validateName(s); err == nil {
			t.Errorf("validateName(%q) = nil, want error", s)
		}
	}
}

func TestValidateFilePath(t *testing.T) {
	for _, p := range []string{"index.html", "img/logo.png", "a/b/c.js"} {
		if err := ValidateFilePath(p); err != nil {
			t.Errorf("ValidateFilePath(%q) = %v, want nil", p, err)
		}
	}
	for _, p := range []string{"", "/etc/passwd", "../x.html", "a/../b.html", ".hidden/x.png", `a\b.png`} {
		if err := ValidateFilePath(p); err == nil {
			t.Errorf("ValidateFilePath(%q) = nil, want error", p)
		}
	}
}

func TestPublishCreateOnly(t *testing.T) {
	testRoot(t)
	files := map[string][]byte{"index.html": []byte("<h1>hi</h1>")}
	if _, err := Publish("", "once", files); err != nil {
		t.Fatal(err)
	}
	_, err := Publish("", "once", files)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("republish should fail with already-exists, got %v", err)
	}
	// after the user deletes, the name is reusable
	if err := Delete("", "once"); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish("", "once", files); err != nil {
		t.Fatalf("reuse after delete should succeed, got %v", err)
	}
}

func TestPublishFilesAndRules(t *testing.T) {
	testRoot(t)

	// html required at the artifact top level
	if _, err := Publish("", "no-entry", map[string][]byte{"img/x.png": {1}}); err == nil {
		t.Error("publish without top-level html should fail")
	}
	if _, err := Publish("", "sub-only", map[string][]byte{"sub/page.html": []byte("<p>")}); err == nil {
		t.Error("html only in subdir should fail")
	}

	// arbitrary relative assets, deep project path
	a, err := Publish("a/b/c", "deep", map[string][]byte{
		"index.html":   []byte("<img src=img/logo.png>"),
		"img/logo.png": {0x89, 0x50, 0x4e, 0x47},
		"data/d.json":  []byte(`{"k":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Project != "a/b/c" || a.RelPath() != "a/b/c/deep" {
		t.Errorf("unexpected artifact: %+v", a)
	}
	if a.Size == 0 {
		t.Error("Size should be > 0")
	}

	// artifacts cannot nest inside artifacts
	if _, err := Publish("a/b/c/deep", "inner", map[string][]byte{"index.html": []byte("<p>")}); err == nil {
		t.Error("publishing under an artifact should fail")
	}

	// delete prunes now-empty ancestors
	if err := Delete("a/b/c", "deep"); err != nil {
		t.Fatal(err)
	}
	root, _ := Root()
	if _, err := os.Stat(filepath.Join(root, "a")); !os.IsNotExist(err) {
		t.Error("empty ancestor dirs should be pruned after delete")
	}
}

func TestWatch(t *testing.T) {
	testRoot(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>src</h1>"), 0o644)
	os.MkdirAll(filepath.Join(src, "img"), 0o755)
	os.WriteFile(filepath.Join(src, "img", "a.png"), []byte{1, 2}, 0o644)

	if _, err := Watch("", "linked-art", src); err != nil {
		t.Fatal(err)
	}
	// re-watching the same folder under the same name is a no-op, so the
	// call can be made unconditionally
	if _, err := Watch("", "linked-art", src); err != nil {
		t.Fatalf("re-watch of the same target should be a no-op, got %v", err)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].IsLink || list[0].Size != 14 {
		t.Fatalf("watched artifact wrong: %+v", list)
	}

	// files inside a watched folder are not deletable
	err = Delete("linked-art", "img")
	if err == nil || !strings.Contains(err.Error(), "watched folder") {
		t.Errorf("delete inside watched folder should be refused, got %v", err)
	}

	// deleting the watch unlinks only; source stays intact
	if err := Delete("", "linked-art"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "index.html")); err != nil {
		t.Errorf("source must survive unwatch: %v", err)
	}

	// watching a tree without top-level html => project tree of artifacts
	tree := t.TempDir()
	os.MkdirAll(filepath.Join(tree, "one"), 0o755)
	os.WriteFile(filepath.Join(tree, "one", "index.html"), []byte("<p>"), 0o644)
	if _, err := Watch("", "tree", tree); err != nil {
		t.Fatal(err)
	}
	list, _ = List()
	if len(list) != 1 || list[0].RelPath() != "tree/one" || !list[0].Linked {
		t.Fatalf("tree watch wrong: %+v", list)
	}
	if err := Delete("tree", "one"); err == nil {
		t.Error("artifact inside watched tree must not be deletable")
	}
}

// The no-op in TestWatch is the *only* relaxation of create-only: the name
// still cannot be taken over by anything else.
func TestWatchCreateOnly(t *testing.T) {
	testRoot(t)
	src, other := t.TempDir(), t.TempDir()
	for _, d := range []string{src, other} {
		os.WriteFile(filepath.Join(d, "index.html"), []byte("<p>"), 0o644)
	}
	if _, err := Watch("", "taken", src); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "taken", other); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("watch of a different target should fail with already-exists, got %v", err)
	}
	if _, err := Publish("", "taken", map[string][]byte{"index.html": []byte("<p>")}); err == nil {
		t.Error("publish over a watch link should fail")
	}
	// ...and a real directory is never adopted as if it were the link
	if _, err := Publish("", "solid", map[string][]byte{"index.html": []byte("<p>")}); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "solid", src); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("watch over a published artifact should fail, got %v", err)
	}
}

func TestUnwatch(t *testing.T) {
	root := testRoot(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "index.html"), []byte("<h1>src</h1>"), 0o644)
	tree := t.TempDir()
	os.MkdirAll(filepath.Join(tree, "one"), 0o755)
	os.WriteFile(filepath.Join(tree, "one", "index.html"), []byte("<p>"), 0o644)

	if _, err := Watch("lab", "art", src); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "tree", tree); err != nil {
		t.Fatal(err)
	}
	if _, err := Publish("", "owned", map[string][]byte{"index.html": []byte("<p>")}); err != nil {
		t.Fatal(err)
	}

	links, err := Watches()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 2 || links[0].Path != "lab/art" || links[0].Target != src || links[1].Path != "tree" {
		t.Fatalf("Watches() = %+v", links)
	}

	// only links can be unwatched; published artifacts are never touched
	if err := Unwatch("", "owned"); err == nil || !strings.Contains(err.Error(), "not a watched folder") {
		t.Errorf("unwatch of a published artifact = %v, want refusal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "owned", "index.html")); err != nil {
		t.Errorf("published artifact must survive a refused unwatch: %v", err)
	}
	if err := Unwatch("", "missing"); err == nil {
		t.Error("unwatch of a missing entry should fail")
	}
	// an entry inside a watched tree points the user at the link itself
	err = Unwatch("tree", "one")
	if err == nil || !strings.Contains(err.Error(), `watched folder "tree"`) {
		t.Errorf("unwatch inside a watched tree = %v, want a pointer to the link", err)
	}

	if got := WatchLinkFor("tree/one"); got != "tree" {
		t.Errorf("WatchLinkFor(tree/one) = %q, want tree", got)
	}
	if got := WatchLinkFor("owned"); got != "" {
		t.Errorf("WatchLinkFor(owned) = %q, want empty", got)
	}

	// unwatching a project tree that is not an artifact (Delete cannot reach it)
	if err := Unwatch("", "tree"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree, "one", "index.html")); err != nil {
		t.Errorf("source tree must survive unwatch: %v", err)
	}

	// unwatching the last link in a project prunes the empty project dir
	if err := Unwatch("lab", "art"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(src, "index.html")); err != nil {
		t.Errorf("source must survive unwatch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "lab")); !os.IsNotExist(err) {
		t.Error("empty project dir should be pruned after unwatch")
	}
	if links, _ := Watches(); len(links) != 0 {
		t.Errorf("Watches() = %+v, want none left", links)
	}
}

func TestListAndResolvePath(t *testing.T) {
	root := testRoot(t)

	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
	}
	mk("flat/page.html", "<p>")
	mk("flat/assets/deep/tex.png", "x") // asset subtree, not an artifact
	mk("x/y/z/art/index.html", "<p>")
	mk("junk/notes.txt", "x")
	mk(".git/index.html", "<p>")    // built-in ignore: never scanned
	mk(".agents/index.html", "<p>") // ordinary dot-folder: shown

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("List() = %d artifacts, want 3: %+v", len(list), list)
	}
	got := map[string]bool{}
	for _, a := range list {
		got[a.RelPath()] = true
	}
	if !got["flat"] || !got["x/y/z/art"] || !got[".agents"] {
		t.Errorf("unexpected list: %+v", got)
	}
	if got[".git"] {
		t.Error(".git must stay out of the scan")
	}

	a, file, ok := ResolvePath([]string{"flat", "assets", "deep", "tex.png"})
	if !ok || a.Name != "flat" || file != "assets/deep/tex.png" {
		t.Errorf("ResolvePath asset = %+v %q %v", a, file, ok)
	}
	a, file, ok = ResolvePath([]string{"x", "y", "z", "art"})
	if !ok || a.RelPath() != "x/y/z/art" || file != "" {
		t.Errorf("ResolvePath deep artifact = %+v %q %v", a, file, ok)
	}
	if _, _, ok := ResolvePath([]string{"x", "y"}); ok {
		t.Error("project dir must not resolve as artifact")
	}
	if _, _, ok := ResolvePath([]string{"..", "etc"}); ok {
		t.Error("traversal must not resolve")
	}
}

func TestResolveFolderContainmentAndWatchedTrees(t *testing.T) {
	root := testRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "visible", "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if dir, ok := ResolveFolder("visible/child"); !ok {
		t.Fatalf("ResolveFolder visible = %v", ok)
	} else {
		dir.Close()
	}
	for _, path := range []string{"../outside", "/etc", `visible\\child`, "missing", ".git"} {
		if _, ok := ResolveFolder(path); ok {
			t.Errorf("ResolveFolder(%q) unexpectedly succeeded", path)
		}
	}

	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "watched", source); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveFolder("watched/nested"); !ok {
		t.Error("folder inside deliberate watch should remain browsable")
	}
	escape := t.TempDir()
	if err := os.Symlink(escape, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, ok := ResolveFolder("watched/escape"); ok {
		t.Error("second symlink inside watched tree must be rejected")
	}
}

func TestPublishAndNestedWatchRejectSymlinkProject(t *testing.T) {
	root := testRoot(t)
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "sentinel"), []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "project")); err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{"index.html": []byte("<p>unsafe</p>")}
	if _, err := Publish("project/nested", "artifact", files); err == nil {
		t.Fatal("Publish beneath symlink project succeeded")
	}
	target := t.TempDir()
	if _, err := Watch("project/nested", "watch", target); err == nil {
		t.Fatal("Watch beneath symlink project succeeded")
	}
	if got, err := os.ReadFile(filepath.Join(external, "sentinel")); err != nil || string(got) != "unchanged" {
		t.Fatalf("external sentinel changed: %q, %v", got, err)
	}
	for _, rel := range []string{"nested", "nested/artifact", "nested/watch"} {
		if _, err := os.Lstat(filepath.Join(external, filepath.FromSlash(rel))); !os.IsNotExist(err) {
			t.Errorf("external path %q was created: %v", rel, err)
		}
	}
}

func TestListDoesNotFollowSymlinksInsideWatch(t *testing.T) {
	testRoot(t)
	source := t.TempDir()
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "index.html"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(source, "cycle")); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "tree", source); err != nil {
		t.Fatal(err)
	}
	artifacts, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts) != 0 {
		t.Fatalf("List followed nested watched-tree symlink: %+v", artifacts)
	}
}

func TestPinnedMutationsIgnoreProjectSwap(t *testing.T) {
	root := testRoot(t)
	if err := os.Mkdir(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "project-original")
	outside := t.TempDir()
	testStoreOpHook = func(op string) {
		if op != "publish-claim" {
			return
		}
		testStoreOpHook = nil
		if err := os.Rename(filepath.Join(root, "project"), original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "project")); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { testStoreOpHook = nil })
	if _, err := Publish("project", "safe", map[string][]byte{"index.html": []byte("safe")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(outside, "safe")); !os.IsNotExist(err) {
		t.Fatalf("publish escaped through swapped ancestor: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(original, "safe", "index.html")); err != nil || string(got) != "safe" {
		t.Fatalf("pinned publish = %q, %v", got, err)
	}
}

func TestOpenDocumentRejectsArtifactAssetSymlink(t *testing.T) {
	root := testRoot(t)
	if _, err := Publish("", "art", map[string][]byte{"index.html": []byte("ok")}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "art", "secret.txt")); err != nil {
		t.Fatal(err)
	}
	if f, ok := OpenDocument([]string{"art", "secret.txt"}); ok {
		f.Close()
		t.Fatal("opened symlink from artifact assets")
	}
}
