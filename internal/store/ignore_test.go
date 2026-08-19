package store

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFile drops a file at root/rel and clears the ignore-parse cache so
// rule edits take effect immediately instead of waiting out the TTL.
func writeFile(t *testing.T, root, rel, body string) string {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	resetIgnoreCache()
	return p
}

func mkdirs(t *testing.T, root string, rels ...string) {
	t.Helper()
	for _, rel := range rels {
		if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(rel)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func TestVisibleDefaults(t *testing.T) {
	root := testRoot(t)
	resetIgnoreCache()
	// Visible unless a rule hides it: ordinary dot-folders are content.
	for _, name := range []string{"demo", "my-artifact", ".agents", ".github", ".claude"} {
		if !Visible(root, name, true) {
			t.Errorf("Visible(%q, dir) = false, want true", name)
		}
	}
	// Hidden by cost (repo-scale churn) or by contents.
	for _, name := range []string{".git", "node_modules", "bin", "vendor", ".venv", ".next"} {
		if Visible(root, name, true) {
			t.Errorf("Visible(%q, dir) = true, want false", name)
		}
	}
	for _, name := range []string{".env", ".env.local", ".netrc", "key.pem", ".DS_Store", ".scratchpadignore"} {
		if Visible(root, name, false) {
			t.Errorf("Visible(%q, file) = true, want false", name)
		}
	}
	// The costly-directory rules are directory-only: a file named bin is fine.
	for _, name := range []string{"bin", "notes.md", ".gitignore"} {
		if !Visible(root, name, false) {
			t.Errorf("Visible(%q, file) = false, want true", name)
		}
	}
}

func TestIgnoreFileRules(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "proj/uploads", "proj/keep", "proj/deep/nested")
	writeFile(t, root, ".scratchpadignore", `
# comment line
uploads/           # trailing comment
*.log
/toplevel
deep/nested
`)
	proj := filepath.Join(root, "proj")
	cases := []struct {
		dir, name string
		isDir     bool
		want      bool
	}{
		{proj, "uploads", true, false},                      // bare name matches at any depth
		{proj, "uploads", false, true},                      // "uploads/" is directories only
		{proj, "keep", true, true},                          // untouched
		{proj, "build.log", false, false},                   // glob on the basename
		{root, "toplevel", true, false},                     // anchored at the ignore file's dir
		{proj, "toplevel", true, true},                      // ...so not here
		{filepath.Join(proj, "deep"), "nested", true, true}, // "deep/nested" is anchored at root
		{root, "deep", true, true},
	}
	for _, c := range cases {
		if got := Visible(c.dir, c.name, c.isDir); got != c.want {
			t.Errorf("Visible(%s, %s, dir=%v) = %v, want %v", c.dir, c.name, c.isDir, got, c.want)
		}
	}
}

func TestIgnoreDoubleStar(t *testing.T) {
	root := testRoot(t)
	writeFile(t, root, ".scratchpadignore", "docs/**/draft-*\n")
	deep := filepath.Join(root, "docs", "a", "b")
	mkdirs(t, root, "docs/a/b")
	if Visible(deep, "draft-1.md", false) {
		t.Error("docs/**/draft-* should hide a deeply nested draft")
	}
	// ** spans zero segments too, so docs/draft-1.md matches as well.
	if Visible(filepath.Join(root, "docs"), "draft-1.md", false) {
		t.Error("** should also match zero segments")
	}
	if !Visible(deep, "final.md", false) {
		t.Error("unrelated file should stay visible")
	}
}

func TestIgnoreNegationUnhidesDefaults(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "bin", ".git", "node_modules")
	writeFile(t, root, ".scratchpadignore", "!bin\n!.git\n")
	if !Visible(root, "bin", true) {
		t.Error("!bin should un-hide a built-in ignore")
	}
	if !Visible(root, ".git", true) {
		t.Error("!.git should win over the built-in default")
	}
	if Visible(root, "node_modules", true) {
		t.Error("un-negated defaults must stay hidden")
	}
}

func TestIgnoreHidesDotFolderOnRequest(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "repo/.agents")
	writeFile(t, root, "repo/.scratchpadignore", ".agents\n")
	if Visible(filepath.Join(root, "repo"), ".agents", true) {
		t.Error("an explicit rule should hide a dot folder")
	}
	if !Visible(root, ".agents", true) {
		t.Error("the rule is scoped to repo/")
	}
}

func TestIgnoreNestedFileOverridesRoot(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "repo/uploads", "other/uploads")
	writeFile(t, root, ".scratchpadignore", "uploads\n")
	writeFile(t, root, "repo/.scratchpadignore", "!uploads\n")
	if !Visible(filepath.Join(root, "repo"), "uploads", true) {
		t.Error("the deeper ignore file should win")
	}
	if Visible(filepath.Join(root, "other"), "uploads", true) {
		t.Error("the root rule still applies elsewhere")
	}
}

func TestIgnoreLastMatchWins(t *testing.T) {
	root := testRoot(t)
	writeFile(t, root, ".scratchpadignore", "*.md\n!README.md\n")
	if Visible(root, "README.md", false) == false {
		t.Error("!README.md comes last and should win")
	}
	if Visible(root, "notes.md", false) {
		t.Error("*.md should still hide other markdown")
	}
}

func TestIgnoreInclude(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "repo/node_stuff", "repo/coverage-html")
	writeFile(t, root, "repo/.gitignore", "coverage-html/\n*.tmp\n")
	writeFile(t, root, "repo/.scratchpadignore", "include .gitignore\nnode_stuff\n")
	repo := filepath.Join(root, "repo")
	if Visible(repo, "coverage-html", true) {
		t.Error("included .gitignore pattern should apply")
	}
	if Visible(repo, "scratch.tmp", false) {
		t.Error("included glob should apply")
	}
	if Visible(repo, "node_stuff", true) {
		t.Error("the file's own patterns should apply alongside includes")
	}
	if !Visible(repo, "src", true) {
		t.Error("unmatched entry should stay visible")
	}
}

func TestIgnoreIncludeCycleAndMissingFile(t *testing.T) {
	root := testRoot(t)
	writeFile(t, root, "a/.scratchpadignore", "include ../b/.scratchpadignore\nhere\n")
	writeFile(t, root, "b/.scratchpadignore", "include ../a/.scratchpadignore\nthere\n")
	writeFile(t, root, "c/.scratchpadignore", "include nope.txt\nsolo\n")
	if Visible(filepath.Join(root, "a"), "here", true) {
		t.Error("own rule should apply despite the include cycle")
	}
	if Visible(filepath.Join(root, "a"), "there", true) {
		t.Error("the included file's rule should apply once")
	}
	if Visible(filepath.Join(root, "c"), "solo", true) {
		t.Error("a missing include must not discard the rest of the file")
	}
}

func TestIgnoreFileEditIsPickedUp(t *testing.T) {
	root := testRoot(t)
	writeFile(t, root, ".scratchpadignore", "temp\n")
	if Visible(root, "temp", true) {
		t.Fatal("rule should apply")
	}
	writeFile(t, root, ".scratchpadignore", "!temp\n")
	if !Visible(root, "temp", true) {
		t.Error("edited rules should take effect")
	}
}

func TestIgnoreAffectsListAndLookup(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "shown", "hidden", ".agents/design", ".git")
	writeFile(t, root, "shown/index.html", "<h1>ok</h1>")
	writeFile(t, root, "hidden/index.html", "<h1>no</h1>")
	writeFile(t, root, ".agents/design/index.html", "<h1>dot</h1>")
	writeFile(t, root, ".agents/notes.md", "# notes")
	writeFile(t, root, ".git/index.html", "<h1>vcs</h1>")
	writeFile(t, root, ".scratchpadignore", "hidden\n")

	arts, err := List()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, a := range arts {
		got[a.RelPath()] = true
	}
	if !got["shown"] {
		t.Error("plain artifact missing from List")
	}
	if got["hidden"] {
		t.Error("ignored artifact should not be listed")
	}
	if !got[".agents/design"] {
		t.Error("ordinary dot folder should be scanned with no rule at all")
	}
	if got[".git"] {
		t.Error(".git is a built-in ignore and must not be scanned")
	}

	if _, _, ok := ResolvePath([]string{"hidden"}); ok {
		t.Error("ignored artifact should not resolve")
	}
	if _, _, ok := ResolvePath([]string{".agents", "design"}); !ok {
		t.Error("dot path should resolve without a rule")
	}
	if _, _, ok := ResolvePath([]string{".git"}); ok {
		t.Error("built-in ignores must not resolve")
	}
	if _, ok := ResolveDoc([]string{".agents", "notes.md"}); !ok {
		t.Error("markdown under an un-ignored dot folder should resolve")
	}
	if !VisiblePath(".agents/design") {
		t.Error("VisiblePath should allow the un-ignored dot path")
	}
	for _, p := range []string{"hidden", ".git", "../etc", "shown/../../etc"} {
		if VisiblePath(p) {
			t.Errorf("VisiblePath(%q) = true, want false", p)
		}
	}
}

func TestIgnoreDoesNotFilterArtifactAssets(t *testing.T) {
	root := testRoot(t)
	mkdirs(t, root, "demo/build")
	writeFile(t, root, "demo/index.html", "<h1>hi</h1>")
	writeFile(t, root, "demo/build/app.js", "// asset")
	writeFile(t, root, ".scratchpadignore", "build\n")
	a, file, ok := ResolvePath([]string{"demo", "build", "app.js"})
	if !ok || file != "build/app.js" || a.Name != "demo" {
		t.Errorf("assets inside an artifact must stay reachable, got ok=%v file=%q", ok, file)
	}
}

func TestValidateSegment(t *testing.T) {
	for _, s := range []string{"a", "My Docs", ".agents", "v1.2_final", "café"} {
		if err := validateSegment(s); err != nil {
			t.Errorf("validateSegment(%q) = %v, want nil", s, err)
		}
	}
	for _, s := range []string{"", ".", "..", "a/b", `a\b`, "a\x00b", "a\nb"} {
		if err := validateSegment(s); err == nil {
			t.Errorf("validateSegment(%q) = nil, want error", s)
		}
	}
}
