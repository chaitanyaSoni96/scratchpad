//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"scratchpad/internal/testutil"
)

// TestFilesFromDirRejectsNamedPipe asserts genuinely Unix-specific semantics:
// FIFO special files (syscall.Mkfifo) do not exist on Windows, so the test
// lives behind the unix build tag. The RequireUnix call keeps the skip reason
// explicit and greppable should the build constraint ever widen.
func TestFilesFromDirRejectsNamedPipe(t *testing.T) {
	testutil.RequireUnix(t)
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "input"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesFromDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular error, got %v", err)
	}
}
