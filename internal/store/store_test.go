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
	// after human delete, the name is reusable
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
	mk(".hidden/index.html", "<p>")

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("List() = %d artifacts, want 2: %+v", len(list), list)
	}
	got := map[string]bool{}
	for _, a := range list {
		got[a.RelPath()] = true
	}
	if !got["flat"] || !got["x/y/z/art"] {
		t.Errorf("unexpected list: %+v", got)
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
