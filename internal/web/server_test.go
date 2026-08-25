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
