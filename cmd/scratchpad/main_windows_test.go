//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/testutil"
)

// TestFilesFromDirRejectsJunction is the missing half of
// TestFilesFromDirRejectsNonRegularEntries's coverage on a
// symlink-incapable Windows box (P4.7 semantic-parity finding P-4 / the
// 27-skip audit). TestFilesFromDirRejectsNonRegularEntries requires
// testutil.RequireSymlinks to plant its symlink, and its only sibling,
// TestFilesFromDirRejectsNamedPipe, is unix-only — so on a Developer-Mode-off
// Windows machine, publish -dir's documented "symlinks, FIFOs, devices, and
// other special files reject the whole publish" rule had zero coverage in
// either direction. A junction needs no privilege at all to create, so it
// closes that hole.
//
// The error message is asserted loosely ("not a regular" rather than a
// specific "file" or "directory" suffix): measured on real Windows CI
// (Go 1.26.5), fs.WalkDir's DirEntry reports a junction's Type() as
// ModeSymlink there, not ModeDir|ModeIrregular as ADR §3.3 documents for
// os.Lstat — so filesFromDir's d.IsDir() branch (and this file's own
// ModeIrregular check in it) is never reached for an ordinary junction;
// rejection instead comes from the regular-file branch, the same path a
// directory symlink already takes. Both branches are kept in filesFromDir
// regardless (defence in depth for whichever DirEntry shape a future Go
// version, or an exotic non-junction reparse type, actually produces); this
// test only commits to "rejected", not to which branch did it.
func TestFilesFromDirRejectsJunction(t *testing.T) {
	testutil.RequireWatchLinks(t)
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("<h1>ok</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := testutil.MakeJunction(filepath.Join(dir, "linked"), target); err != nil {
		t.Fatal(err)
	}
	if _, err := filesFromDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular") {
		t.Fatalf("expected a non-regular rejection, got %v", err)
	}
}
