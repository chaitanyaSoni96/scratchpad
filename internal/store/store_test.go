package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/testutil"
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

	// artifacts cannot nest inside artifacts. Assert the reason (not just
	// that it failed, P2.7 finding F3): the rejectArtifacts ancestor check
	// must be what fired, not some unrelated cause.
	if _, err := Publish("a/b/c/deep", "inner", map[string][]byte{"index.html": []byte("<p>")}); err == nil {
		t.Error("publishing under an artifact should fail")
	} else if !strings.Contains(err.Error(), "deep") || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("publishing under an artifact failed for the wrong reason: %v, want it to name %q as an artifact", err, "a/b/c/deep")
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
	testutil.RequireWatchLinks(t)
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
	} else if !strings.Contains(err.Error(), "watched folder") {
		t.Errorf("delete inside watched tree failed for the wrong reason: %v, want it to name the watched folder", err)
	}
}

// The no-op in TestWatch is the *only* relaxation of create-only: the name
// still cannot be taken over by anything else.
func TestWatchCreateOnly(t *testing.T) {
	testutil.RequireWatchLinks(t)
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
	if _, err := Publish("", "taken", map[string][]byte{"index.html": []byte("<p>")}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("publish over a watch link = %v, want already-exists", err)
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
	testutil.RequireWatchLinks(t)
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
	if len(links) != 2 || links[0].Path != "lab/art" || !sameTarget(links[0].Target, src) || links[1].Path != "tree" {
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
	} else if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unwatch of a missing entry failed for the wrong reason: %v, want \"not found\"", err)
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

// TestDeleteRemovesEmptyNonArtifactDirectory is the shared (RW19) regression
// test for ADR §6.6 rule 3: Delete is widened to remove an empty,
// non-artifact, non-link directory. On Windows this is what turns "delete it
// and retry" into a true recovery instruction for the residue an interrupted
// two-step watch-link creation can leave behind (a crash between the
// FILE_CREATE name claim and the FSCTL that turns it into a link — see
// TestTwoStepCrashResidueRecoversViaDelete, storefs_windows_attack_test.go).
// rmdirAt refuses anything non-empty on every platform (ENOTEMPTY /
// STATUS_DIRECTORY_NOT_EMPTY) and never follows a link, so the same rule is
// safe on Linux too, and is tested here unconditionally rather than only
// behind a Windows build tag.
func TestDeleteRemovesEmptyNonArtifactDirectory(t *testing.T) {
	root := testRoot(t)
	if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Delete("", "empty"); err != nil {
		t.Fatalf("Delete on an empty non-artifact directory = %v, want nil", err)
	}
	if _, err := os.Stat(filepath.Join(root, "empty")); !os.IsNotExist(err) {
		t.Fatalf("directory must be gone after Delete, stat err = %v", err)
	}

	// A non-empty non-artifact directory (an ordinary project folder holding
	// real content) must never be silently removable through this path —
	// only a genuinely EMPTY directory ever qualifies, so this can never
	// become a way to destroy a populated project tree.
	if err := os.MkdirAll(filepath.Join(root, "nonempty", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nonempty", "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Delete("", "nonempty"); err == nil {
		t.Fatal("Delete unexpectedly removed a non-empty non-artifact directory")
	}
	if _, err := os.Stat(filepath.Join(root, "nonempty", "note.txt")); err != nil {
		t.Fatalf("content must survive a refused delete: %v", err)
	}

	// Unwatch deliberately does NOT gain this power — it stays link-only, so
	// the create-only-for-agents asymmetry (Unwatch is agent-reachable,
	// Delete is user-only) is preserved.
	if err := os.Mkdir(filepath.Join(root, "bareagain"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Unwatch("", "bareagain"); err == nil {
		t.Fatal("Unwatch unexpectedly removed a bare non-link directory — that recovery power belongs to Delete only")
	}
	if _, err := os.Stat(filepath.Join(root, "bareagain")); err != nil {
		t.Fatalf("directory must survive a refused unwatch: %v", err)
	}
}

// TestDeleteDeepArtifactTree is the P3.14 red-team M2 reproduction: nothing
// ever bounded artifact *creation*, but removeTreeAt used to enforce
// maxArtifactWalkDepth on *removal* as a hard error, so a deep publish (a
// vendored SDK, a node_modules tree, a deep monorepo path — all legitimate)
// could succeed and then become permanently undeletable, forever wedging the
// name and leaving the artifact listed, with no partial destruction (the
// recursion errored on the way down, before any removal). Fixed by making
// removeTreeAt unbounded — see its doc comment on both platforms for why
// that is safe (no-follow removal cannot cycle). The store must never
// create something it refuses to delete.
func TestDeleteDeepArtifactTree(t *testing.T) {
	testRoot(t)
	var b strings.Builder
	for i := 0; i < maxArtifactWalkDepth+10; i++ {
		b.WriteString("d/")
	}
	deepPath := b.String() + "leaf.txt"
	files := map[string][]byte{
		"index.html": []byte("<h1>deep</h1>"),
		deepPath:     []byte("leaf"),
	}
	if _, err := Publish("", "deep", files); err != nil {
		t.Fatalf("publish of a tree deeper than maxArtifactWalkDepth should succeed, got %v", err)
	}
	if err := Delete("", "deep"); err != nil {
		t.Fatalf("Delete must be able to remove what Publish created, got %v", err)
	}
	arts, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.Name == "deep" {
			t.Fatalf("deleted artifact %q is still listed", a.Name)
		}
	}
}

// TestArtifactTreeDepthInvariant is the P6.3 F5 pairing, asserted as one
// property rather than two limits: maxArtifactTreeDepth bounds BOTH what
// ValidateFilePath will create and how deep removeTreeAt will descend, so
// "the store never creates a tree it then refuses to delete" is checkable in
// one place. The two halves are deliberately tested against each other at the
// exact boundary — a future change that lowers only the removal bound is
// precisely P3.14's M2 coming back, and this is the test that catches it.
// Untagged on purpose: the invariant is identical on both backends.
func TestArtifactTreeDepthInvariant(t *testing.T) {
	testRoot(t)

	// One segment past the bound must be refused at PUBLISH time, not
	// discovered at delete time, and must leave nothing behind.
	over := strings.Repeat("d/", maxArtifactTreeDepth) + "leaf.txt" // maxArtifactTreeDepth+1 segments
	if err := ValidateFilePath(over); err == nil {
		t.Fatalf("ValidateFilePath accepted %d segments, deeper than removeTreeAt will descend", maxArtifactTreeDepth+1)
	}
	if _, err := Publish("", "over", map[string][]byte{"index.html": []byte("x"), over: []byte("y")}); err == nil {
		t.Fatal("Publish accepted a file path deeper than removeTreeAt will descend")
	}
	if arts, err := List(); err == nil {
		for _, a := range arts {
			if a.Name == "over" {
				t.Fatal("a publish rejected for depth still created an artifact")
			}
		}
	}

	// A path at exactly the bound must be publishable AND deletable. If this
	// half ever fails while the half above passes, the bound has become
	// one-directional again.
	at := strings.Repeat("d/", maxArtifactTreeDepth-1) + "leaf.txt" // exactly maxArtifactTreeDepth segments
	if err := ValidateFilePath(at); err != nil {
		t.Fatalf("ValidateFilePath rejected a path at exactly the bound: %v", err)
	}
	if _, err := Publish("", "atbound", map[string][]byte{"index.html": []byte("x"), at: []byte("y")}); err != nil {
		t.Fatalf("Publish rejected a path at exactly the bound: %v", err)
	}
	if err := Delete("", "atbound"); err != nil {
		t.Fatalf("Delete could not remove what Publish had just created at the bound: %v", err)
	}
	arts, err := List()
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range arts {
		if a.Name == "atbound" {
			t.Fatal("artifact published at the depth bound survived Delete")
		}
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
	testutil.RequireSymlinks(t)
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
	testutil.RequireSymlinks(t)
	root := testRoot(t)
	external := t.TempDir()
	if err := os.WriteFile(filepath.Join(external, "sentinel"), []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(root, "project")); err != nil {
		t.Fatal(err)
	}
	// P2.7 finding F3: this test used to assert only err == nil -> fail, which
	// was trivially true on the pre-P3 Windows stub because openRootedFS
	// itself refused with errWindowsUnimplemented before containment logic
	// ever ran — a green assertion for entirely the wrong reason. Now that a
	// real backend exists on both platforms, assert the REASON: the walk must
	// fail specifically because "project" is a link/reparse point, not for
	// some unrelated cause (a typo'd path, a permission error, ...).
	files := map[string][]byte{"index.html": []byte("<p>unsafe</p>")}
	_, err := Publish("project/nested", "artifact", files)
	if err == nil {
		t.Fatal("Publish beneath symlink project succeeded")
	}
	if !strings.Contains(err.Error(), "project") ||
		!(strings.Contains(err.Error(), "symlink") || strings.Contains(err.Error(), "link") || strings.Contains(err.Error(), "reparse")) {
		t.Fatalf("Publish beneath symlink project failed for the wrong reason: %v (want an error naming %q as a symlink/link/reparse point)", err, "project")
	}
	target := t.TempDir()
	_, err = Watch("project/nested", "watch", target)
	if err == nil {
		t.Fatal("Watch beneath symlink project succeeded")
	}
	if !strings.Contains(err.Error(), "project") ||
		!(strings.Contains(err.Error(), "symlink") || strings.Contains(err.Error(), "link") || strings.Contains(err.Error(), "reparse")) {
		t.Fatalf("Watch beneath symlink project failed for the wrong reason: %v (want an error naming %q as a symlink/link/reparse point)", err, "project")
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
	testutil.RequireSymlinks(t)
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
	testutil.RequireSymlinks(t)
	root := testRoot(t)
	if err := os.Mkdir(filepath.Join(root, "project"), 0o755); err != nil {
		t.Fatal(err)
	}
	original := filepath.Join(root, "project-original")
	outside := t.TempDir()
	setStoreOpHook(t, func(op string) {
		if op != "publish-claim" {
			return
		}
		clearStoreOpHook()
		if err := os.Rename(filepath.Join(root, "project"), original); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, filepath.Join(root, "project")); err != nil {
			t.Fatal(err)
		}
	})
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
	testutil.RequireSymlinks(t)
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

// TestIsLinkFalseForPlainArtifact is a regression test for a latent bug found
// while writing the Windows ADR: loadArtifact's two fd-backed callers
// (ResolvePath, Publish) pass fdPath(fd) — a /proc/self/fd/N handle path —
// as the artifact's Dir when annotate() used to run inside loadArtifact.
// os.Lstat on a /proc/self/fd entry always reports ModeSymlink, regardless of
// what the fd itself points to, so every artifact returned by ResolvePath or
// Publish reported IsLink == true even for an ordinary published artifact
// that is not, and never was, a symlink. List (which always passes a real
// path) was never affected. See annotate's doc comment in store.go and ADR
// §6.8 item 2 (.agents/ADRs/2026-08-26-windows-rooted-store-backend.md).
func TestIsLinkFalseForPlainArtifact(t *testing.T) {
	testRoot(t)
	files := map[string][]byte{"index.html": []byte("<p>hi</p>")}

	pub, err := Publish("", "plain", files)
	if err != nil {
		t.Fatal(err)
	}
	if pub.IsLink || pub.Linked {
		t.Errorf("Publish: plain artifact = IsLink=%v Linked=%v, want false, false", pub.IsLink, pub.Linked)
	}

	rp, file, ok := ResolvePath([]string{"plain"})
	if !ok || file != "" {
		t.Fatalf("ResolvePath(plain) = %+v %q %v, want a match with no trailing file", rp, file, ok)
	}
	if rp.IsLink || rp.Linked {
		t.Errorf("ResolvePath: plain artifact = IsLink=%v Linked=%v, want false, false", rp.IsLink, rp.Linked)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IsLink || list[0].Linked {
		t.Fatalf("List: plain artifact wrong: %+v", list)
	}
}

// TestIsLinkTruePositiveThroughResolvePath is the true-positive counterpart
// to TestIsLinkFalseForPlainArtifact: a genuinely watched folder must still
// report IsLink == true (and Linked == false), and an artifact living inside
// a watched *tree* (not itself the link) must still report Linked == true
// (and IsLink == false), through both List (already covered by TestWatch)
// and ResolvePath (untested before this fix, and the one that actually used
// the buggy fdPath-based annotate call).
func TestIsLinkTruePositiveThroughResolvePath(t *testing.T) {
	testutil.RequireWatchLinks(t)
	testRoot(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "index.html"), []byte("<p>src</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "linked", src); err != nil {
		t.Fatal(err)
	}
	rp, _, ok := ResolvePath([]string{"linked"})
	if !ok {
		t.Fatal("ResolvePath(linked) not found")
	}
	if !rp.IsLink || rp.Linked {
		t.Errorf("ResolvePath: watched folder = IsLink=%v Linked=%v, want true, false", rp.IsLink, rp.Linked)
	}

	tree := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tree, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "child", "index.html"), []byte("<p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "tree", tree); err != nil {
		t.Fatal(err)
	}
	rp, _, ok = ResolvePath([]string{"tree", "child"})
	if !ok {
		t.Fatal("ResolvePath(tree/child) not found")
	}
	if rp.IsLink || !rp.Linked {
		t.Errorf("ResolvePath: artifact nested in watched tree = IsLink=%v Linked=%v, want false, true", rp.IsLink, rp.Linked)
	}
}

// TestBrowseRefusesWatchAncestorSymlinkSwap is the regression test for
// A11.ancestor_swapped (spike-findings.md §10.1, P1.7 red-team finding F2):
// an ancestor of a watch target — not the target itself — replaced with a
// symlink into an attacker-controlled tree after the watch was created.
// This is not a race: the store never re-validates a watch target's
// ancestors after Watch runs, so the swap is simply the state on disk at
// the next browse, exactly as a `git checkout` inside a watched repository
// would produce it. Before the fix, openBrowsableDir re-opened the
// readlink(2) result as a whole path string with O_NOFOLLOW, which only
// protects the FINAL component — the "subdir" ancestor here was resolved by
// the kernel like any other path component, and the attacker's tree was
// reached and served.
func TestBrowseRefusesWatchAncestorSymlinkSwap(t *testing.T) {
	testutil.RequireSymlinks(t)
	testRoot(t)

	base := t.TempDir()
	subdir := filepath.Join(base, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(subdir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("<h1>legit</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := Watch("", "linked", target); err != nil {
		t.Fatal(err)
	}

	// Sanity: the legitimate watch browses fine before the ancestor is
	// touched.
	if f, ok := OpenDocument([]string{"linked", "index.html"}); !ok {
		t.Fatal("sanity: legitimate watch did not browse before the swap")
	} else {
		f.Close()
	}

	// The attack: replace "subdir" — an ANCESTOR of the watch target, not
	// the target itself — with a symlink into an attacker tree that mirrors
	// enough of the path to reach a planted marker.
	attacker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(attacker, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attacker, "target", "index.html"), []byte("<h1>attacker</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attacker, "target", "LOOT.txt"), []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, subdir); err != nil {
		t.Fatal(err)
	}

	if a, file, ok := ResolvePath([]string{"linked", "LOOT.txt"}); ok {
		t.Fatalf("ResolvePath reached the attacker's tree through the swapped ancestor: artifact=%+v file=%q", a, file)
	}
	if f, safe := OpenDocument([]string{"linked", "LOOT.txt"}); safe {
		f.Close()
		t.Fatal("OpenDocument served the attacker's marker file through the swapped ancestor")
	}
	if a, file, ok := ResolvePath([]string{"linked", "index.html"}); ok {
		t.Fatalf("ResolvePath reached the attacker's index.html through the swapped ancestor: artifact=%+v file=%q", a, file)
	}
	if f, safe := OpenDocument([]string{"linked", "index.html"}); safe {
		f.Close()
		t.Fatal("OpenDocument served the attacker's index.html through the swapped ancestor")
	}

	// VisiblePath is a listing/ignore-rule filter (internal/store/ignore.go),
	// not a content boundary: its walk (visibleSegments) uses plain
	// os.Stat, which the kernel resolves exactly like any other program
	// would — including through the swapped "subdir" symlink — so it still
	// reports "linked/LOOT.txt" as visible. That is expected and harmless:
	// nothing downstream treats VisiblePath's answer as permission to read
	// content — the actual boundary is the openBrowsableDir walk just
	// proved above (ResolvePath/OpenDocument), and end to end over HTTP in
	// internal/web's TestArtifactHandlerRefusesWatchAncestorSymlinkSwap.
	if !VisiblePath("linked/LOOT.txt") {
		t.Error("VisiblePath unexpectedly refused a path it has no mechanism to detect the swap on; if this now fails, visibleSegments's own walk changed — re-examine, don't just flip this assertion")
	}
}

// sameTarget reports whether a watch link's recorded Target names the same
// directory as want. On Windows, canonicalizeWatchTarget resolves through
// GetFinalPathNameByHandleW (ADR §4.3/§4.7 — required because
// filepath.EvalSymlinks does not resolve junctions), which normalizes case
// and 8.3 short-name aliases; t.TempDir() on a runner whose TEMP env var is
// itself an 8.3 alias (observed: RUNNER~1 vs runneradmin) can therefore
// disagree with the resolved Target by spelling alone while naming the
// identical directory. A byte-exact comparison is exactly the wrong test
// here — the ADR's own §7.2 reasoning for sameWatchTarget applies equally
// to this assertion — so this checks object identity, falling back to
// string equality only if either path cannot be stat'd.
func sameTarget(target, want string) bool {
	if target == want {
		return true
	}
	a, err1 := os.Stat(target)
	b, err2 := os.Stat(want)
	return err1 == nil && err2 == nil && os.SameFile(a, b)
}

// TestWatchResolvesSymlinkedAncestorAtCreation is the "legitimate symlinked
// ancestor" side of the A11.ancestor_swapped trade-off (see Watch,
// store.go): a caller who reaches their target through a symlinked
// ancestor — a convenience symlink, or a system where e.g. /home itself is
// a symlink — must not have the watch refused just because a symlink
// happens to sit above the target. Watch resolves the whole path with
// filepath.EvalSymlinks once, at creation time, and stores that resolved
// path as the actual link target, so every later browse walks a path with
// no symlinks left in it at all.
//
// The cost of resolving instead of refusing: once resolved, the watch is
// pinned to the real directory found at creation time. If the convenience
// symlink is later repointed elsewhere, the existing watch keeps serving
// the OLD real directory rather than following the move — demonstrated
// below. Re-running `scratchpad watch` picks up the new target.
func TestWatchResolvesSymlinkedAncestorAtCreation(t *testing.T) {
	testutil.RequireSymlinks(t)
	testRoot(t)

	realA := t.TempDir()
	projA := filepath.Join(realA, "proj")
	if err := os.Mkdir(projA, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projA, "index.html"), []byte("<h1>A</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	convDir := t.TempDir()
	conv := filepath.Join(convDir, "conv") // symlinked ANCESTOR of the watch target
	if err := os.Symlink(realA, conv); err != nil {
		t.Fatal(err)
	}
	watchTarget := filepath.Join(conv, "proj") // reached only through the symlinked ancestor

	if _, err := Watch("", "viaconv", watchTarget); err != nil {
		t.Fatalf("Watch through a symlinked ancestor should succeed (resolved, not refused): %v", err)
	}

	links, err := Watches()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !sameTarget(links[0].Target, projA) {
		t.Fatalf("Watches() = %+v, want one link resolved to %q", links, projA)
	}

	if f, safe := OpenDocument([]string{"viaconv", "index.html"}); !safe {
		t.Fatal("OpenDocument did not serve through the resolved, symlink-free watch")
	} else {
		f.Close()
	}

	// Repoint the convenience symlink at a different real directory. The
	// existing watch must keep pointing at the ORIGINAL resolved target,
	// not silently follow the move — re-resolving on every browse would
	// reopen exactly the ancestor-swap hole this fix closes.
	realB := t.TempDir()
	projB := filepath.Join(realB, "proj")
	if err := os.Mkdir(projB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projB, "index.html"), []byte("<h1>B</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(conv); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realB, conv); err != nil {
		t.Fatal(err)
	}

	links, err = Watches()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || !sameTarget(links[0].Target, projA) {
		t.Fatalf("after repointing the convenience symlink, Watches() = %+v, want the watch still pinned to %q (not %q)", links, projA, projB)
	}
	if f, safe := OpenDocument([]string{"viaconv", "index.html"}); !safe {
		t.Fatal("OpenDocument stopped serving after the convenience symlink was repointed; want it to keep serving the original resolved target")
	} else {
		f.Close()
	}
}

// TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix reconfirms, after
// the openBrowsableDir rewrite, that "exactly one symlink boundary" still
// holds: a second symlink planted INSIDE the watched source (not the watch
// link itself) is still refused. TestWatch and
// TestListDoesNotFollowSymlinksInsideWatch already cover this at other
// layers; this one calls ResolvePath/OpenDocument directly so a regression
// in the walk introduced by this fix would show up here first.
func TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix(t *testing.T) {
	testutil.RequireSymlinks(t)
	testRoot(t)

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<p>src</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "nested")); err != nil {
		t.Fatal(err)
	}

	if _, err := Watch("", "watched", source); err != nil {
		t.Fatal(err)
	}

	// The watch boundary itself still works.
	if f, safe := OpenDocument([]string{"watched", "index.html"}); !safe {
		t.Fatal("legitimate file through the single watch boundary should still serve")
	} else {
		f.Close()
	}
	// ResolvePath deliberately does NOT validate this: "watched" alone
	// already satisfies the shallowest-html-directory rule, so ResolvePath
	// returns as soon as it finds that boundary and reports everything
	// past it as an unvalidated asset-path string (see ResolvePath's own
	// doc comment) — it never walks "nested" itself. OpenDocument is the
	// one that actually opens the asset path, via openBrowsableDir end to
	// end, and is where the nested symlink must be refused.
	if a, file, ok := ResolvePath([]string{"watched", "nested", "secret.txt"}); !ok || file != "nested/secret.txt" {
		t.Fatalf("ResolvePath(watched/nested/secret.txt) = %+v, %q, %v; want the watched artifact with that file suffix (validation happens in OpenDocument, not here)", a, file, ok)
	}
	if f, safe := OpenDocument([]string{"watched", "nested", "secret.txt"}); safe {
		f.Close()
		t.Fatal("OpenDocument followed a nested symlink inside the watched source")
	}
}
