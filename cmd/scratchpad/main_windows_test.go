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
	if _, err := filesFromDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular directory") {
		t.Fatalf("expected non-regular-directory error, got %v", err)
	}
}
