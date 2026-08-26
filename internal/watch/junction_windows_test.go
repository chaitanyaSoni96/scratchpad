//go:build windows

package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"scratchpad/internal/testutil"
)

// TestDesiredDirsFollowsJunctionWatchLink is the junction-flavour twin of
// TestDesiredDirsFollowsWatchLinkButNotNestedDirectorySymlink (watch_test.go).
// A junction is the default watch-link flavour for any Developer-Mode-off
// Windows user (docs/windows.md), yet before this test internal/watch had
// zero junction coverage: every link test in this package used
// testutil.RequireSymlinks and os.Symlink (P4.7 semantic-parity finding
// P-4). RequireWatchLinks, not RequireSymlinks: junction creation needs no
// privilege at all, so this should never skip on an ordinary box.
//
// This test is also the empirical check P4.7 finding P-5 asks for.
// filepath.EvalSymlinks (canonicalDir) does not resolve a junction — Go
// reports it as ModeDir|ModeIrregular, never ModeSymlink, and
// walkSymlinks only ever substitutes a ModeSymlink component — so unlike a
// directory symlink (resolved to its SOURCE path), a junction and
// everything walked through it is registered under its STORE-SIDE path.
// If a future Go version (or this reasoning) is wrong, the first assertion
// below is where that would show up, not the second.
func TestDesiredDirsFollowsJunctionWatchLink(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := t.TempDir()
	source := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "watch")
	if err := testutil.MakeJunction(link, source); err != nil {
		t.Fatal(err)
	}

	dirs, err := desiredDirs(root)
	if err != nil {
		t.Fatal(err)
	}

	linkCanonical, err := canonicalDir(link)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dirs[linkCanonical]; !ok {
		t.Fatal("the junction's own store-side path was not registered — junction watch links are not being followed at all")
	}

	// desiredDirs opens the junction path directly (openWatchDir follows
	// the reparse point at the I/O level, same as any CreateFile without
	// FILE_FLAG_OPEN_REPARSE_POINT would), reads the TARGET's entries
	// through that handle, then joins each entry name onto the STORE-SIDE
	// dir variable — so a nested entry is keyed by canonicalDir(link/entry),
	// not canonicalDir(source/entry) (P-5).
	insideViaLink, err := canonicalDir(filepath.Join(link, "inside"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dirs[insideViaLink]; !ok {
		t.Fatal("directory below a junction watch link was not included — the junction's contents are not being walked")
	}
}

// TestReconcileRegistersAndRemovesJunctionWatch proves the reconciler both
// registers a junction-backed watch target at startup and stops watching it
// once the junction link itself is removed — the "watched by the
// reconciler" and "unwatchable" properties P-4 asks internal/watch to cover
// for a junction, mirroring what TestReconcileRemovesTargetWatchAfterUnwatch
// already proves for a directory symlink. Per the same P-5 reasoning as
// above, the registered key is the junction's own (store-side) canonical
// path, not the target's.
func TestReconcileRegistersAndRemovesJunctionWatch(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "watch")
	if err := testutil.MakeJunction(link, target); err != nil {
		t.Fatal(err)
	}
	linkCanonical, err := canonicalDir(link)
	if err != nil {
		t.Fatal(err)
	}

	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	watched := b.watches[linkCanonical]
	b.mu.Unlock()
	if !watched {
		t.Fatal("junction watch link was not registered by the initial reconcile")
	}

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watches[linkCanonical] {
		t.Fatal("junction remained watched after the link was removed")
	}
	if len(b.removes) == 0 {
		t.Fatal("reconcile did not remove any watch after the junction link was deleted")
	}
}
