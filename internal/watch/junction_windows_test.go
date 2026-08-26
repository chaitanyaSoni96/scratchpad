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
// This test is also the empirical check P4.7 finding P-5 asked for, and it
// settles P-5 with real evidence from Windows CI (Go 1.26.5,
// windows-amd64), not just reasoning from Go's source: canonicalDir
// (filepath.EvalSymlinks) succeeds resolving a path UP TO AND INCLUDING a
// junction component, but FAILS with "the system cannot find the path
// specified" for any path that continues PAST it — even though the OS
// itself resolves that exact path transparently for ordinary I/O (ReadDir
// through the junction's handle, below, works fine). desiredDirs' walk
// reaches that failing path (it must, to have discovered "inside" exists
// at all), and skipWalkError/skipEntry treat the failure as "the entry
// disappeared" and silently drop it. So unlike a directory symlink, where
// everything below the link is watched, a junction's watch coverage stops
// one level short: content nested inside it is never registered, and
// changes there never trigger a live refresh. This is a real functional
// gap — worse than P-5's original "registered under a different key"
// framing — and belongs to P-5's own owner to fix, not P-4 (which asked
// only for the coverage that would settle it either way).
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
		t.Fatal("the junction's own directory was not registered — junction watch links are not being followed at all")
	}

	if _, err := canonicalDir(filepath.Join(link, "inside")); err == nil {
		t.Fatalf("canonicalDir(%q) unexpectedly succeeded — if a Go toolchain change made EvalSymlinks "+
			"resolve through an intermediate junction, P-5's nested-registration gap may now be fixed, "+
			"and this test should be updated to assert the nested directory IS registered instead",
			filepath.Join(link, "inside"))
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
