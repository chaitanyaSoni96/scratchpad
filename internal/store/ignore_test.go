package store

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestIgnoreCacheConcurrentRefresh(t *testing.T) {
	root := testRoot(t)
	writeFile(t, root, ".scratchpadignore", "temp\n")
	if Visible(root, "temp", true) {
		t.Fatal("rule should apply")
	}

	ignoreMu.Lock()
	e := ignoreCache[root]
	e.checked = time.Time{}
	ignoreCache[root] = e
	ignoreMu.Unlock()

	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if Visible(root, "temp", true) {
					t.Error("concurrent refresh lost the ignore rule")
				}
			}
		}()
	}
	wg.Wait()
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

// TestRootLevelHTMLDoesNotDisableIgnoreRules is F-1's store-layer
// regression: visibleSegments' artifact short-circuit ("we are inside an
// artifact, the rest is assets") must never fire against the store root
// itself. Before the fix, a single stray .html directly in the root made
// hasHTML(root) true on the very first loop iteration (dir == root), so
// visibleSegments returned true unconditionally for every path — disabling
// the hard-coded .annotations guard and every defaultIgnores/
// .scratchpadignore rule. Reproduces the P3.13 F-1 finding's appendix.
func TestRootLevelHTMLDoesNotDisableIgnoreRules(t *testing.T) {
	root := testRoot(t)
	resetIgnoreCache()
	// The bug trigger: an .html file sitting directly in the store root,
	// which is never itself an artifact for visibility purposes (List,
	// ResolvePath and Watches all start their artifact test one level down).
	writeFile(t, root, "index.html", "<h1>root</h1>")
	writeFile(t, root, ".git/secret.md", "SECRET-IN-GIT")
	writeFile(t, root, "node_modules/pkg/readme.md", "SECRET-IN-NM")
	mkdirs(t, root, ".venv/site")
	writeFile(t, root, ".venv/site/index.html", "<h1>v</h1>")
	writeFile(t, root, ".venv/site/key.pem", "PRIVATE-KEY-BYTES")

	hidden := []string{
		".annotations",   // hard-coded reserved-name guard
		".git",           // defaultIgnores directory rule
		".git/secret.md", // credential-adjacent content below it
		"node_modules",   // defaultIgnores directory rule
		"node_modules/pkg/readme.md",
		".venv",              // defaultIgnores directory rule
		".venv/site",         // an artifact living inside a hidden dir
		".venv/site/key.pem", // the credential itself
	}
	for _, p := range hidden {
		if VisiblePath(p) {
			t.Errorf("VisiblePath(%q) = true, want false (a root-level .html must not disable ignore rules)", p)
		}
	}

	// Positive control: assets inside a REAL, non-hidden artifact must stay
	// reachable and unfiltered, exactly as TestIgnoreDoesNotFilterArtifactAssets
	// checks without a stray root-level .html in play.
	mkdirs(t, root, "demo/build")
	writeFile(t, root, "demo/index.html", "<h1>hi</h1>")
	writeFile(t, root, "demo/build/app.js", "// asset")
	writeFile(t, root, ".scratchpadignore", "build\n")
	if !VisiblePath("demo/build/app.js") {
		t.Error("VisiblePath(demo/build/app.js) = false, want true (assets inside a real artifact must stay reachable even with a stray root .html present)")
	}
	if a, file, ok := ResolvePath([]string{"demo", "build", "app.js"}); !ok || file != "build/app.js" || a.Name != "demo" {
		t.Errorf("ResolvePath into a real artifact's assets must still work, got ok=%v file=%q", ok, file)
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

// TestIgnoreHidesContentNotJustListings establishes what an ignore-rule
// bypass actually costs, on both platforms, by exercising the two routes
// that serve bytes out of the store: ResolveDoc (any .md under a visible
// path) and ResolvePath (an artifact directory and every asset beneath it).
//
// This is the correction to P6.3 F1's impact statement, made executable
// rather than argued. F1 bounded an ignore-rule bypass to "listing
// disclosure, not content", reasoning that /a/ serves a file only under an
// .html-bearing ancestor and that neither .annotations nor a credential
// directory has one. That is right about .annotations — note sidecars are
// <doc>.html.json and dirHasHTMLFD's HasSuffix(…, ".html") correctly does
// not match them, which the first block below pins — but it does not hold
// for a hidden DIRECTORY in general: a directory hidden by defaultIgnores or
// by a .scratchpadignore rule may perfectly well contain markdown or a
// complete artifact, and node_modules/.venv routinely contain both.
//
// So the rule is load-bearing for content, and the positive control proves
// the assertions have teeth: byte-identical content one directory over, not
// covered by the rule, IS served.
func TestIgnoreHidesContentNotJustListings(t *testing.T) {
	root := testRoot(t)

	// A defaultIgnores directory holding both servable content types.
	writeFile(t, root, "node_modules/pkg/index.html", "<h1>hidden artifact</h1>")
	writeFile(t, root, "node_modules/pkg/readme.md", "# hidden markdown")
	// The same shapes under a .scratchpadignore rule the user wrote.
	writeFile(t, root, ".scratchpadignore", "private\n")
	writeFile(t, root, "private/deck/index.html", "<h1>private artifact</h1>")
	writeFile(t, root, "private/notes.md", "# private markdown")
	// Positive control: identical content, no rule covering it.
	writeFile(t, root, "public/deck/index.html", "<h1>public artifact</h1>")
	writeFile(t, root, "public/notes.md", "# public markdown")
	// The annotations tree, whose sidecars are .json and therefore reachable
	// by neither route even if the reserved-name deny were bypassed.
	writeFile(t, root, AnnotationsDir+"/public/deck/index.html.json", `{"rev":1,"annotations":[]}`)
	resetIgnoreCache()

	hiddenDocs := [][]string{
		{"node_modules", "pkg", "readme.md"},
		{"private", "notes.md"},
	}
	for _, segs := range hiddenDocs {
		if _, ok := ResolveDoc(segs); ok {
			t.Errorf("ResolveDoc(%v) succeeded: markdown inside an ignore-hidden directory is CONTENT, and the ignore rule is the only thing withholding it", segs)
		}
	}
	hiddenArtifacts := [][]string{
		{"node_modules", "pkg", "index.html"},
		{"private", "deck", "index.html"},
	}
	for _, segs := range hiddenArtifacts {
		if _, _, ok := ResolvePath(segs); ok {
			t.Errorf("ResolvePath(%v) succeeded: an artifact inside an ignore-hidden directory is CONTENT, and its whole asset subtree comes with it", segs)
		}
	}

	// Positive control: the same two routes work for identical content that
	// no rule covers, so the assertions above are not passing vacuously.
	if _, ok := ResolveDoc([]string{"public", "notes.md"}); !ok {
		t.Error("ResolveDoc(public/notes.md) failed: the control content must be served, or the assertions above prove nothing")
	}
	if _, _, ok := ResolvePath([]string{"public", "deck", "index.html"}); !ok {
		t.Error("ResolvePath(public/deck/index.html) failed: the control content must be served, or the assertions above prove nothing")
	}

	// The annotations tree carries no .html and no .md, so even with the
	// reserved-name deny out of the picture neither route can serve a
	// sidecar. This is the half of F1's bounding that does hold, pinned so a
	// future sidecar rename (to .md, say) cannot quietly falsify it.
	if _, ok := ResolveDoc([]string{AnnotationsDir, "public", "deck", "index.html.json"}); ok {
		t.Error("ResolveDoc served a note sidecar: sidecars must not be reachable as documents")
	}
	if _, _, ok := ResolvePath([]string{AnnotationsDir, "public", "deck"}); ok {
		t.Error("ResolvePath treated a sidecar directory as an artifact: <doc>.html.json must not satisfy dirHasHTMLFD")
	}
}
