// End-to-end tests for the scratchpad CLI: every test here drives the
// actual built binary (see TestMain) against a real temporary
// SCRATCHPAD_ROOT, exactly as an agent or a human would from a shell. They
// are deliberately not unit tests of helper functions — main_test.go
// already covers publishFiles/filesFromDir in isolation — these assert on
// process exit codes, stdout/stderr text, and the resulting filesystem
// state, and they must pass unmodified on native Windows as well as Linux
// (see .agents/plans/in-progress/native-windows-support/native-windows-support.md,
// task P4.5).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/store"
)

// cliHelperEnv, when set to "1" in the environment, tells TestMain that this
// process invocation IS the scratchpad CLI (argv is the CLI's own argument
// list, not go test's) rather than the test binary itself.
const cliHelperEnv = "SCRATCHPAD_CLI_E2E_HELPER"

// TestMain lets the compiled test binary double as the `scratchpad` binary
// under test: re-exec'd with cliHelperEnv=1 (see runCLI), it calls main()
// directly on the real os.Args and exits exactly as `go build`'s binary
// would, on every OS the suite runs on (including windows/amd64 and
// windows/arm64) — no separate `go build` step, no ".exe" bookkeeping, and
// every exit path in main.go (including its os.Exit calls) behaves
// identically to production.
func TestMain(m *testing.M) {
	if os.Getenv(cliHelperEnv) == "1" {
		main()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// cliResult is the outcome of one CLI invocation.
type cliResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// runCLI re-execs this same test binary as the scratchpad CLI (TestMain)
// with SCRATCHPAD_ROOT=root, capturing stdout/stderr and the real exit
// code. SCRATCHPAD_URL is pointed at a closed local port so every command's
// webAlive() liveness probe fails on connection refusal instead of
// blocking for its 700ms timeout — this keeps the suite fast without
// touching any production code path.
func runCLI(t *testing.T, root, stdin string, args ...string) cliResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(),
		cliHelperEnv+"=1",
		store.RootEnv+"="+root,
		"SCRATCHPAD_URL=http://127.0.0.1:1",
	)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running scratchpad %v: %v", args, runErr)
		}
	}
	return cliResult{Stdout: out.String(), Stderr: errOut.String(), ExitCode: exitCode}
}

// mustWriteFile writes content to a fresh file at path, creating parent
// directories as needed.
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// sameDir reports whether a and b name the same directory by identity
// (os.SameFile), not by string comparison. Watch canonicalizes its target
// (see store.Watch/canonicalizeWatchTarget), and t.TempDir() itself can
// return an 8.3 short-name spelling on some Windows configurations while
// the store reports the long-name canonical form — the exact trap noted in
// commit 4d87801 and internal/watch's mustCanonical helper — so any
// assertion comparing "what I asked to watch" against "what the CLI reports
// back" MUST go through filesystem identity, never a raw path-string
// comparison.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(fa, fb)
}

// ---- publish ----

func TestCLIPublishHTML(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html>hello</html>")

	res := runCLI(t, root, "", "publish", "-project", "grp", "-name", "hello", "-html", src)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "published") || !strings.Contains(res.Stdout, "grp/hello") {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}
	got := mustReadFile(t, filepath.Join(root, "grp", "hello", "index.html"))
	if got != "<html>hello</html>" {
		t.Fatalf("index.html = %q", got)
	}
}

func TestCLIPublishHTMLWithCSSAndJS(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()
	htmlPath := filepath.Join(dir, "p.html")
	cssPath := filepath.Join(dir, "s.css")
	jsPath := filepath.Join(dir, "a.js")
	mustWriteFile(t, htmlPath, "<html></html>")
	mustWriteFile(t, cssPath, "body{}")
	mustWriteFile(t, jsPath, "console.log(1)")

	res := runCLI(t, root, "", "publish", "-name", "full", "-html", htmlPath, "-css", cssPath, "-js", jsPath)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if mustReadFile(t, filepath.Join(root, "full", "style.css")) != "body{}" {
		t.Fatal("style.css not published")
	}
	if mustReadFile(t, filepath.Join(root, "full", "script.js")) != "console.log(1)" {
		t.Fatal("script.js not published")
	}
}

func TestCLIPublishStdin(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "<html>from-stdin</html>", "publish", "-name", "stdin-page", "-html", "-")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	got := mustReadFile(t, filepath.Join(root, "stdin-page", "index.html"))
	if got != "<html>from-stdin</html>" {
		t.Fatalf("index.html = %q", got)
	}
}

func TestCLIPublishDir(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html>dir</html>")
	mustWriteFile(t, filepath.Join(src, "assets", "logo.png"), "PNGDATA")

	res := runCLI(t, root, "", "publish", "-name", "fromdir", "-dir", src)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if mustReadFile(t, filepath.Join(root, "fromdir", "index.html")) != "<html>dir</html>" {
		t.Fatal("index.html mismatch")
	}
	if mustReadFile(t, filepath.Join(root, "fromdir", "assets", "logo.png")) != "PNGDATA" {
		t.Fatal("nested asset mismatch")
	}
}

// TestCLIPublishCreateOnly is the create-only guarantee: a taken name is an
// error, never an overwrite, and the original artifact is left untouched by
// the failed attempt.
func TestCLIPublishCreateOnly(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html>original</html>")
	res := runCLI(t, root, "", "publish", "-name", "dup", "-html", src)
	if res.ExitCode != 0 {
		t.Fatalf("initial publish: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	src2 := filepath.Join(t.TempDir(), "page2.html")
	mustWriteFile(t, src2, "<html>replacement</html>")
	res2 := runCLI(t, root, "", "publish", "-name", "dup", "-html", src2)
	if res2.ExitCode != 1 {
		t.Fatalf("expected exit 1 on name collision, got %d (stderr=%q)", res2.ExitCode, res2.Stderr)
	}
	if !strings.Contains(res2.Stderr, "already exists") {
		t.Fatalf("expected actionable 'already exists' message, got %q", res2.Stderr)
	}
	// The original must be completely untouched by the failed attempt.
	if got := mustReadFile(t, filepath.Join(root, "dup", "index.html")); got != "<html>original</html>" {
		t.Fatalf("original artifact was modified: %q", got)
	}
}

// TestCLIPublishReservedNames covers the portable-name rule from P2.5:
// reserved DOS device basenames (any extension, case-insensitive) and
// trailing dot/space are rejected at create time, both for the artifact
// name itself and for file paths inside a published directory.
func TestCLIPublishReservedNames(t *testing.T) {
	htmlSrc := func(t *testing.T) string {
		p := filepath.Join(t.TempDir(), "page.html")
		mustWriteFile(t, p, "<html></html>")
		return p
	}

	t.Run("reserved artifact name CON", func(t *testing.T) {
		root := t.TempDir()
		res := runCLI(t, root, "", "publish", "-name", "CON", "-html", htmlSrc(t))
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stderr, "reserved Windows device name") {
			t.Fatalf("stderr=%q", res.Stderr)
		}
	})

	t.Run("reserved project segment COM1", func(t *testing.T) {
		root := t.TempDir()
		res := runCLI(t, root, "", "publish", "-project", "COM1", "-name", "ok", "-html", htmlSrc(t))
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})

	t.Run("trailing dot name", func(t *testing.T) {
		root := t.TempDir()
		res := runCLI(t, root, "", "publish", "-name", "bad.", "-html", htmlSrc(t))
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stderr, "dot or space") {
			t.Fatalf("stderr=%q", res.Stderr)
		}
	})

	t.Run("trailing space name", func(t *testing.T) {
		root := t.TempDir()
		res := runCLI(t, root, "", "publish", "-name", "bad ", "-html", htmlSrc(t))
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})

	t.Run("reserved file inside dir: nul.html", func(t *testing.T) {
		root := t.TempDir()
		src := t.TempDir()
		mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")
		mustWriteFile(t, filepath.Join(src, "nul.html"), "<html></html>")
		res := runCLI(t, root, "", "publish", "-name", "hasreserved", "-dir", src)
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
		if !strings.Contains(res.Stderr, "reserved Windows device name") {
			t.Fatalf("stderr=%q", res.Stderr)
		}
		// The whole publish must fail, not just the bad file.
		if _, err := os.Stat(filepath.Join(root, "hasreserved")); err == nil {
			t.Fatal("partial publish leaked onto disk")
		}
	})

	t.Run("reserved file inside dir: COM1.tar.gz", func(t *testing.T) {
		root := t.TempDir()
		src := t.TempDir()
		mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")
		mustWriteFile(t, filepath.Join(src, "COM1.tar.gz"), "data")
		res := runCLI(t, root, "", "publish", "-name", "hasreserved2", "-dir", src)
		if res.ExitCode != 1 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})

	// Sanity: names that merely resemble a reserved name are fine.
	t.Run("not actually reserved: console and nul2.html", func(t *testing.T) {
		root := t.TempDir()
		src := t.TempDir()
		mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")
		mustWriteFile(t, filepath.Join(src, "nul2.html"), "<html></html>")
		res := runCLI(t, root, "", "publish", "-name", "console", "-dir", src)
		if res.ExitCode != 0 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})
}

func TestCLIPublishRequiresExactlyOneInput(t *testing.T) {
	root := t.TempDir()
	tests := []struct {
		name string
		args []string
	}{
		{"neither -dir nor -html", []string{"publish", "-name", "x"}},
		{"both -dir and -html", []string{"publish", "-name", "x", "-dir", t.TempDir(), "-html", "p.html"}},
		{"css without html", []string{"publish", "-name", "x", "-dir", t.TempDir(), "-css", "s.css"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := runCLI(t, root, "", tt.args...)
			if res.ExitCode != 2 {
				t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
			}
		})
	}
}

func TestCLIPublishExcessArguments(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html></html>")
	res := runCLI(t, root, "", "publish", "-name", "x", "-html", src, "surplus")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// ---- list ----

func TestCLIListJSON(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"alpha", "beta"} {
		src := filepath.Join(t.TempDir(), "page.html")
		mustWriteFile(t, src, "<html>"+name+"</html>")
		res := runCLI(t, root, "", "publish", "-name", name, "-html", src)
		if res.ExitCode != 0 {
			t.Fatalf("publish %s: exit=%d stderr=%q", name, res.ExitCode, res.Stderr)
		}
	}

	res := runCLI(t, root, "", "list", "-json")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	var artifacts []store.Artifact
	if err := json.Unmarshal([]byte(res.Stdout), &artifacts); err != nil {
		t.Fatalf("invalid JSON %q: %v", res.Stdout, err)
	}
	if len(artifacts) != 2 {
		t.Fatalf("got %d artifacts, want 2: %+v", len(artifacts), artifacts)
	}
	names := map[string]bool{}
	for _, a := range artifacts {
		names[a.Name] = true
		if a.Entry != "index.html" {
			t.Errorf("artifact %s: Entry = %q", a.Name, a.Entry)
		}
		if a.Size <= 0 {
			t.Errorf("artifact %s: Size = %d", a.Name, a.Size)
		}
	}
	if !names["alpha"] || !names["beta"] {
		t.Fatalf("missing expected names: %+v", artifacts)
	}
}

// TestCLIListJSONEmpty asserts the empty shape is a JSON array, not the
// bare "null" a nil Go slice would otherwise encode as — the same
// normalization notesRead already applies via openOnly, kept consistent
// here so every -json surface in this CLI agrees on how "nothing here"
// looks to a caller parsing the output.
func TestCLIListJSONEmpty(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "list", "-json")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "[]" {
		t.Fatalf("list -json on an empty store = %q, want []", res.Stdout)
	}
}

func TestCLIListEmptyText(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "list")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "no artifacts" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

func TestCLIListExcessArguments(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "list", "surplus")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// ---- watch / watches / unwatch ----

func TestCLIWatchListUnwatch(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html>live</html>")

	res := runCLI(t, root, "", "watch", src, "-project", "grp", "-name", "proj")
	if res.ExitCode != 0 {
		t.Fatalf("watch: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "watching") {
		t.Fatalf("stdout=%q", res.Stdout)
	}

	// It shows up in `watches` pointing at the real source, by identity
	// (not string equality — see sameDir).
	wres := runCLI(t, root, "", "watches")
	if wres.ExitCode != 0 {
		t.Fatalf("watches: exit=%d stderr=%q", wres.ExitCode, wres.Stderr)
	}
	line := findLineContaining(t, wres.Stdout, "grp/proj")
	target := strings.TrimSpace(strings.SplitN(line, "->", 2)[1])
	if !sameDir(t, target, src) {
		t.Fatalf("watches target %q is not the same directory as %q", target, src)
	}

	// And it shows up in `list` as a hosted artifact.
	lres := runCLI(t, root, "", "list")
	if !strings.Contains(lres.Stdout, "grp/proj") {
		t.Fatalf("list did not show the watched artifact: %q", lres.Stdout)
	}

	// unwatch removes only the link.
	ures := runCLI(t, root, "", "unwatch", "grp/proj")
	if ures.ExitCode != 0 {
		t.Fatalf("unwatch: exit=%d stderr=%q", ures.ExitCode, ures.Stderr)
	}
	if !strings.Contains(ures.Stdout, "unwatched") {
		t.Fatalf("stdout=%q", ures.Stdout)
	}
	wres2 := runCLI(t, root, "", "watches")
	if !strings.Contains(wres2.Stdout, "no watched folders") {
		t.Fatalf("expected empty watches after unwatch, got %q", wres2.Stdout)
	}
	// The source is untouched.
	if got := mustReadFile(t, filepath.Join(src, "index.html")); got != "<html>live</html>" {
		t.Fatalf("source folder was modified by unwatch: %q", got)
	}
}

func findLineContaining(t *testing.T, s, substr string) string {
	t.Helper()
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	t.Fatalf("no line containing %q in %q", substr, s)
	return ""
}

// TestCLIWatchSameTargetIsIdempotent: re-watching the same folder under the
// same name is a no-op, not an error, so an agent can call `watch`
// unconditionally.
func TestCLIWatchSameTargetIsIdempotent(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")

	first := runCLI(t, root, "", "watch", src, "-name", "again")
	if first.ExitCode != 0 {
		t.Fatalf("first watch: exit=%d stderr=%q", first.ExitCode, first.Stderr)
	}
	second := runCLI(t, root, "", "watch", src, "-name", "again")
	if second.ExitCode != 0 {
		t.Fatalf("re-watching the same target must be a no-op, got exit=%d stderr=%q", second.ExitCode, second.Stderr)
	}
	wres := runCLI(t, root, "", "watches")
	count := strings.Count(wres.Stdout, "again")
	if count != 1 {
		t.Fatalf("expected exactly one 'again' watch entry after idempotent re-watch, got %d in %q", count, wres.Stdout)
	}
}

// TestCLIWatchDifferentTargetCollision: watching a different folder under a
// name already claimed by another target is refused.
func TestCLIWatchDifferentTargetCollision(t *testing.T) {
	root := t.TempDir()
	srcA := t.TempDir()
	srcB := t.TempDir()
	mustWriteFile(t, filepath.Join(srcA, "index.html"), "<html>a</html>")
	mustWriteFile(t, filepath.Join(srcB, "index.html"), "<html>b</html>")

	first := runCLI(t, root, "", "watch", srcA, "-name", "clash")
	if first.ExitCode != 0 {
		t.Fatalf("first watch: exit=%d stderr=%q", first.ExitCode, first.Stderr)
	}
	second := runCLI(t, root, "", "watch", srcB, "-name", "clash")
	if second.ExitCode != 1 {
		t.Fatalf("expected exit 1 on target collision, got %d (stderr=%q)", second.ExitCode, second.Stderr)
	}
	if !strings.Contains(second.Stderr, "already exists") {
		t.Fatalf("stderr=%q", second.Stderr)
	}
	// The original watch must still point at srcA.
	wres := runCLI(t, root, "", "watches")
	line := findLineContaining(t, wres.Stdout, "clash")
	target := strings.TrimSpace(strings.SplitN(line, "->", 2)[1])
	if !sameDir(t, target, srcA) {
		t.Fatalf("watch target changed after a rejected collision: %q", target)
	}
}

func TestCLIWatchExcessArguments(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")
	res := runCLI(t, root, "", "watch", src, "surplus")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestCLIUnwatchExcessArguments(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html></html>")
	if res := runCLI(t, root, "", "watch", src, "-name", "w"); res.ExitCode != 0 {
		t.Fatalf("setup watch: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	res := runCLI(t, root, "", "unwatch", "w", "surplus")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestCLIWatchesEmpty(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "watches")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "no watched folders") {
		t.Fatalf("stdout=%q", res.Stdout)
	}
}

// ---- delete ----

func TestCLIDelete(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html></html>")
	if res := runCLI(t, root, "", "publish", "-name", "gone", "-html", src); res.ExitCode != 0 {
		t.Fatalf("publish: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res := runCLI(t, root, "", "delete", "-name", "gone")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "deleted" {
		t.Fatalf("stdout=%q", res.Stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "gone")); !os.IsNotExist(err) {
		t.Fatalf("artifact directory still present, err=%v", err)
	}
	lres := runCLI(t, root, "", "list")
	if strings.Contains(lres.Stdout, "gone") {
		t.Fatalf("deleted artifact still listed: %q", lres.Stdout)
	}
}

func TestCLIDeleteMissing(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "delete", "-name", "never-existed")
	if res.ExitCode != 1 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestCLIDeleteExcessArguments(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "delete", "-name", "x", "surplus")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// TestCLIDeleteWatchedEntry: CLAUDE.md documents that Delete unlinks a
// watched top-level entry without touching its source, the same as
// unwatch — delete is not restricted to copies made by publish.
func TestCLIDeleteWatchedEntry(t *testing.T) {
	root := t.TempDir()
	src := t.TempDir()
	mustWriteFile(t, filepath.Join(src, "index.html"), "<html>keep-me</html>")
	if res := runCLI(t, root, "", "watch", src, "-name", "linked"); res.ExitCode != 0 {
		t.Fatalf("watch: exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}

	res := runCLI(t, root, "", "delete", "-name", "linked")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if got := mustReadFile(t, filepath.Join(src, "index.html")); got != "<html>keep-me</html>" {
		t.Fatalf("source was modified by delete: %q", got)
	}
	wres := runCLI(t, root, "", "watches")
	if strings.Contains(wres.Stdout, "linked") {
		t.Fatalf("watch link still present after delete: %q", wres.Stdout)
	}
}

// ---- notes ----

// publishForNotes publishes a minimal artifact and returns its
// store-relative document path (e.g. "proj/index.html").
func publishForNotes(t *testing.T, root, name string) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html><body>content</body></html>")
	if res := runCLI(t, root, "", "publish", "-name", name, "-html", src); res.ExitCode != 0 {
		t.Fatalf("publish %s: exit=%d stderr=%q", name, res.ExitCode, res.Stderr)
	}
	return name + "/index.html"
}

// saveNote authors an annotation directly through the store package — the
// CLI deliberately has no way to create one (userOnlyVerbs), exactly the
// asymmetry these tests protect, so test setup must go around it the same
// way the web viewer would.
func saveNote(t *testing.T, root, doc, id, body string) {
	t.Helper()
	t.Setenv(store.RootEnv, root)
	f := store.NotesFile{Annotations: []store.Annotation{{
		ID:     id,
		Status: "open",
		Body:   body,
		Target: store.Target{Type: "element", Selector: "#x"},
	}}}
	if _, err := store.SaveNotes(doc, f, 0); err != nil {
		t.Fatalf("saveNote: %v", err)
	}
}

func TestCLINotesReportReplyResolve(t *testing.T) {
	root := t.TempDir()
	doc := publishForNotes(t, root, "reviewed")
	saveNote(t, root, doc, "n1", "fix the header")

	// Default report: open notes as markdown.
	report := runCLI(t, root, "", "notes", "reviewed")
	if report.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", report.ExitCode, report.Stderr)
	}
	if !strings.Contains(report.Stdout, "n1") || !strings.Contains(report.Stdout, "fix the header") {
		t.Fatalf("report missing note: %q", report.Stdout)
	}

	// JSON form.
	jres := runCLI(t, root, "", "notes", "reviewed", "-json")
	if jres.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", jres.ExitCode, jres.Stderr)
	}
	var docs []store.DocNotes
	if err := json.Unmarshal([]byte(jres.Stdout), &docs); err != nil {
		t.Fatalf("invalid JSON %q: %v", jres.Stdout, err)
	}
	if len(docs) != 1 || len(docs[0].Notes.Annotations) != 1 || docs[0].Notes.Annotations[0].ID != "n1" {
		t.Fatalf("unexpected docs: %+v", docs)
	}

	// reply: comments without closing.
	rres := runCLI(t, root, "", "notes", "reply", doc, "n1", "-m", "which breakpoint?")
	if rres.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", rres.ExitCode, rres.Stderr)
	}
	if !strings.Contains(rres.Stdout, "replied to n1") {
		t.Fatalf("stdout=%q", rres.Stdout)
	}
	nf, err := store.LoadNotes(doc)
	if err != nil {
		t.Fatal(err)
	}
	if nf.Annotations[0].Status != "open" || len(nf.Annotations[0].Replies) != 1 {
		t.Fatalf("reply did not append or changed status: %+v", nf.Annotations[0])
	}

	// resolve: closes with a summary.
	sres := runCLI(t, root, "", "notes", "resolve", doc, "n1", "-m", "moved the legend above the plot")
	if sres.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", sres.ExitCode, sres.Stderr)
	}
	if !strings.Contains(sres.Stdout, "resolved n1") {
		t.Fatalf("stdout=%q", sres.Stdout)
	}
	nf2, err := store.LoadNotes(doc)
	if err != nil {
		t.Fatal(err)
	}
	if nf2.Annotations[0].Status != "resolved" {
		t.Fatalf("status after resolve = %q", nf2.Annotations[0].Status)
	}

	// Default report now shows no open notes; -all still shows the resolved one.
	afterOpen := runCLI(t, root, "", "notes", "reviewed")
	if !strings.Contains(afterOpen.Stdout, "No open notes") {
		t.Fatalf("expected no open notes after resolve, got %q", afterOpen.Stdout)
	}
	afterAll := runCLI(t, root, "", "notes", "reviewed", "-all")
	if !strings.Contains(afterAll.Stdout, "n1") {
		t.Fatalf("expected resolved note under -all, got %q", afterAll.Stdout)
	}
}

func TestCLINotesResolveMissingID(t *testing.T) {
	root := t.TempDir()
	doc := publishForNotes(t, root, "reviewed2")
	saveNote(t, root, doc, "n1", "fix this")

	res := runCLI(t, root, "", "notes", "resolve", doc, "does-not-exist", "-m", "x")
	if res.ExitCode != 1 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "no such note") {
		t.Fatalf("stderr=%q", res.Stderr)
	}
}

func TestCLINotesResolveRequiresMessage(t *testing.T) {
	root := t.TempDir()
	doc := publishForNotes(t, root, "reviewed3")
	saveNote(t, root, doc, "n1", "fix this")

	res := runCLI(t, root, "", "notes", "resolve", doc, "n1")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "-m is required") {
		t.Fatalf("stderr=%q", res.Stderr)
	}
}

func TestCLINotesEmptyStore(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "notes")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "No open notes.") {
		t.Fatalf("stdout=%q", res.Stdout)
	}
	jres := runCLI(t, root, "", "notes", "-json")
	if strings.TrimSpace(jres.Stdout) != "[]" {
		t.Fatalf("notes -json on empty store = %q, want []", jres.Stdout)
	}
}

// TestCLINotesUserOnlyVerbsRejected: create/edit/delete/reopen are the
// user's, in the web UI, and must produce the explanation rather than being
// silently read as a <path> (which would print a misleading "no notes").
func TestCLINotesUserOnlyVerbsRejected(t *testing.T) {
	root := t.TempDir()
	for _, verb := range []string{"create", "edit", "delete", "reopen"} {
		t.Run(verb, func(t *testing.T) {
			res := runCLI(t, root, "", "notes", verb, "some/doc.html")
			if res.ExitCode != 2 {
				t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "the user's, in the web UI") {
				t.Fatalf("expected the user-only explanation, got stderr=%q", res.Stderr)
			}
			if strings.Contains(res.Stderr, "No open notes") || strings.Contains(res.Stdout, "No open notes") {
				t.Fatalf("verb %q was silently read as a path instead of rejected: stdout=%q stderr=%q", verb, res.Stdout, res.Stderr)
			}
		})
	}
}

// TestCLINotesRealDocNotConfusedWithUserOnlyVerb: a real path that merely
// starts with a rejected verb's letters (not an exact match) must still be
// read as a path, not rejected.
func TestCLINotesRealDocNotConfusedWithUserOnlyVerb(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "notes", "deleteme-report/index.html")
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestCLINotesExcessArguments(t *testing.T) {
	root := t.TempDir()
	doc := publishForNotes(t, root, "reviewed4")
	saveNote(t, root, doc, "n1", "x")

	t.Run("read", func(t *testing.T) {
		res := runCLI(t, root, "", "notes", "reviewed4", "surplus")
		if res.ExitCode != 2 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})
	t.Run("resolve", func(t *testing.T) {
		res := runCLI(t, root, "", "notes", "resolve", doc, "n1", "surplus", "-m", "x")
		if res.ExitCode != 2 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})
	t.Run("reply", func(t *testing.T) {
		res := runCLI(t, root, "", "notes", "reply", doc, "n1", "surplus", "-m", "x")
		if res.ExitCode != 2 {
			t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
		}
	})
}

// ---- general exit status / usage ----

func TestCLINoArguments(t *testing.T) {
	root := t.TempDir()
	res := runCLINoArgs(t, root)
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

// runCLINoArgs invokes the CLI with zero arguments (os.Args[1:] empty),
// which runCLI's variadic signature cannot express cleanly.
func runCLINoArgs(t *testing.T, root string) cliResult {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.Command(exe)
	cmd.Env = append(os.Environ(), cliHelperEnv+"=1", store.RootEnv+"="+root, "SCRATCHPAD_URL=http://127.0.0.1:1")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			t.Fatalf("running scratchpad with no args: %v", runErr)
		}
	}
	return cliResult{Stdout: out.String(), Stderr: errOut.String(), ExitCode: exitCode}
}

func TestCLIUnknownCommand(t *testing.T) {
	root := t.TempDir()
	res := runCLI(t, root, "", "frobnicate")
	if res.ExitCode != 2 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
}

func TestCLIHelp(t *testing.T) {
	root := t.TempDir()
	for _, flag := range []string{"-h", "--help", "help"} {
		t.Run(flag, func(t *testing.T) {
			res := runCLI(t, root, "", flag)
			if res.ExitCode != 0 {
				t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
			}
			if !strings.Contains(res.Stdout, "scratchpad") {
				t.Fatalf("stdout=%q", res.Stdout)
			}
		})
	}
}

// TestCLIWebDownWarning: every command that talks about a hosted URL warns
// on stderr, without failing the command, when the web server is not
// reachable (which it never is in these tests, by construction of
// SCRATCHPAD_URL in runCLI).
func TestCLIWebDownWarning(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(t.TempDir(), "page.html")
	mustWriteFile(t, src, "<html></html>")
	res := runCLI(t, root, "", "publish", "-name", "warn", "-html", src)
	if res.ExitCode != 0 {
		t.Fatalf("exit=%d stderr=%q", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "not reachable") {
		t.Fatalf("expected a web-down warning on stderr, got %q", res.Stderr)
	}
}
