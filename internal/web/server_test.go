package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/store"
	"scratchpad/internal/testutil"
)

func TestListFragmentRejectsInvalidFolders(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := testRoot(t)
	if err := os.MkdirAll(filepath.Join(root, "visible"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(external, "missing"), filepath.Join(external, "nested")); err != nil {
		t.Fatal(err)
	}
	mux := notesMux(t)
	tests := []string{
		"/fragments/list?project=..",
		"/fragments/list?project=%2e%2e%2foutside",
		"/fragments/list?project=%2Fetc",
		"/fragments/list?project=.git",
		"/fragments/list?project=missing",
		"/fragments/list?project=escape%2Fnested",
	}
	for _, target := range tests {
		rec := doReq(t, mux, http.MethodGet, target, nil, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, rec.Code)
		}
	}
}

func TestListFragmentBrowsesWatchWithoutFollowingNestedLinks(t *testing.T) {
	testutil.RequireSymlinks(t)
	testRoot(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "readme.md"), []byte("# watched"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(source, "cycle")); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Watch("", "watched", source); err != nil {
		t.Fatal(err)
	}
	rec := doReq(t, notesMux(t), http.MethodGet, "/fragments/list?project=watched", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "readme") {
		t.Fatalf("watched markdown missing from response: %s", rec.Body.String())
	}
}

func TestListFragmentAppliesFolderIgnoreRules(t *testing.T) {
	root := testRoot(t)
	dir := filepath.Join(root, "project")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".scratchpadignore"), []byte("secret.md\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secret.md"), []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "visible.md"), []byte("visible"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := doReq(t, notesMux(t), http.MethodGet, "/fragments/list?project=project", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "secret") || !strings.Contains(body, "visible") {
		t.Fatalf("folder ignore rules not applied: %s", body)
	}
}

// TestArtifactHandlerRefusesWatchAncestorSymlinkSwap is the HTTP-layer half
// of the A11.ancestor_swapped regression (internal/store's
// TestBrowseRefusesWatchAncestorSymlinkSwap covers the store API directly):
// the actual exposure named by the finding is read disclosure over this
// unauthenticated endpoint, so the proof that matters is that a GET for the
// attacker's planted marker — and even the attacker's own index.html — 404s
// once an ancestor of a watch target has been swapped for a symlink into an
// attacker tree, rather than being served.
func TestArtifactHandlerRefusesWatchAncestorSymlinkSwap(t *testing.T) {
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
	if _, err := store.Watch("", "linked", target); err != nil {
		t.Fatal(err)
	}

	mux := notesMux(t)

	// Sanity: the legitimate artifact serves before the ancestor is swapped.
	rec := doReq(t, mux, http.MethodGet, "/a/linked/index.html", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sanity GET /a/linked/index.html = %d, want 200; body: %s", rec.Code, rec.Body.String())
	}

	attacker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(attacker, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(attacker, "target", "index.html"), []byte("<h1>attacker</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	const loot = "classified-over-http"
	if err := os.WriteFile(filepath.Join(attacker, "target", "LOOT.txt"), []byte(loot), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, subdir); err != nil {
		t.Fatal(err)
	}

	rec = doReq(t, mux, http.MethodGet, "/a/linked/LOOT.txt", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /a/linked/LOOT.txt after ancestor symlink swap = %d, want 404 (marker must not be served); body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), loot) {
		t.Fatalf("response leaked the marker content: %s", rec.Body.String())
	}

	rec = doReq(t, mux, http.MethodGet, "/a/linked/index.html", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET /a/linked/index.html after ancestor symlink swap = %d, want 404 (attacker's own index.html must not be served either); body: %s", rec.Code, rec.Body.String())
	}
}

// TestRootLevelHTMLDoesNotExposeHiddenPathsOverHTTP is the HTTP-layer half of
// the P3.13 F-1 regression (internal/store's
// TestRootLevelHTMLDoesNotDisableIgnoreRules covers the store API directly):
// the actual exposure the finding names is unauthenticated reads and deletes
// reaching content that ignore rules are supposed to make unreachable, once a
// stray .html sits directly in the store root. This reproduces the review's
// live appendix end to end against the real mux.
func TestRootLevelHTMLDoesNotExposeHiddenPathsOverHTTP(t *testing.T) {
	root := testRoot(t)
	// The bug trigger: an .html file directly in the store root.
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<h1>root</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "secret.md"), []byte("SECRET-IN-GIT"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "hooks", "index.html"), []byte("<h1>git artifact</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	seedArtifact(t, root, "demo")

	mux := notesMux(t)

	// Loose markdown inside a hidden directory (.git/secret.md) must 404 and
	// must never leak its content.
	rec := doReq(t, mux, http.MethodGet, "/a/.git/secret.md", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /a/.git/secret.md = %d, want 404 (root-level html must not disable ignore rules); body: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "SECRET-IN-GIT") {
		t.Fatal("response leaked the hidden file's content")
	}

	// An artifact living inside a hidden directory (.git/hooks) must 404 too.
	rec = doReq(t, mux, http.MethodGet, "/a/.git/hooks/", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /a/.git/hooks/ = %d, want 404", rec.Code)
	}

	// The unauthenticated DELETE the review reproduced live must not reach
	// content inside a hidden directory.
	rec = doReq(t, mux, http.MethodDelete, "/a/.git/hooks", nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("DELETE /a/.git/hooks = %d, want 404 (must not delete inside a hidden directory); body: %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, ".git", "hooks", "index.html")); err != nil {
		t.Errorf(".git/hooks/index.html was removed by the refused DELETE: %v", err)
	}

	// Positive control: an ordinary, non-hidden artifact must still be served
	// fine even with the stray root-level index.html present.
	rec = doReq(t, mux, http.MethodGet, "/a/demo/", nil, nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /a/demo/ = %d, want 200 (real artifacts must still serve); body: %s", rec.Code, rec.Body.String())
	}
}
