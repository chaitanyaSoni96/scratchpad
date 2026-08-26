//go:build windows

package watch

import (
	"context"
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
// This test previously asserted the BROKEN behaviour P4.7 finding P-5
// diagnosed and 185b37d then measured on real Windows CI (run 32969235815),
// disproving P-5's own "registered under a different key" framing:
// canonicalDir (then filepath.EvalSymlinks) succeeded resolving a path UP TO
// AND INCLUDING a junction component, but FAILED with "the system cannot
// find the path specified" for any path that continued PAST it — even
// though the OS itself resolves that exact path transparently for ordinary
// I/O (ReadDir through the junction's handle, and openWatchDir's CreateFile,
// both work fine). desiredDirs' walk reaches that failing path (it must, to
// have discovered "inside" exists at all), and skipWalkError/skipEntry
// treated the failure as "the entry disappeared" and silently dropped it.
// So unlike a directory symlink, where everything below the link is
// watched, a junction's watch coverage stopped one level short: content
// nested inside it was never registered, and changes there never triggered
// a live refresh.
//
// canonicalDir is now handle-based on Windows (identity_windows.go):
// it opens the path FOLLOWING reparse points — the same kind of open
// openWatchDir already performs — and reads
// GetFinalPathNameByHandleW(VOLUME_NAME_DOS) from that handle instead of
// walking the path string component by component, so a junction resolves
// transparently the same way CreateFile always did. This test now asserts
// the FIXED behaviour: a directory nested below a junction watch link is
// registered, the same as one nested below a directory symlink
// (TestDesiredDirsFollowsWatchLinkButNotNestedDirectorySymlink, watch_test.go).
// TestNestedChangeUnderJunctionReachesHub below goes one step further and
// proves the property this mechanism exists for — that a change nested
// below a junction actually reaches a live-refresh subscriber, not just
// that the directory is present in desiredDirs' map.
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

	insideCanonical, err := canonicalDir(filepath.Join(link, "inside"))
	if err != nil {
		t.Fatalf("canonicalDir(%q): %v — P-5's fix requires this to resolve THROUGH the junction, not just up to it",
			filepath.Join(link, "inside"), err)
	}
	if _, ok := dirs[insideCanonical]; !ok {
		t.Fatal("a directory nested below the junction watch link was not registered — P-5: junction watch coverage stopped one level short")
	}
}

// TestNestedChangeUnderJunctionReachesHub is
// TestDesiredDirsFollowsJunctionWatchLink taken one step further:
// "registered in desiredDirs' map" is the mechanism, but "a change there
// triggers a live refresh" is the property a user actually observes, and
// that is exactly what P-5's gap broke — a junction-backed watch tree's
// nested content was never registered with the real fsnotify backend at
// all, so saving a file inside it never produced a Hub.Broadcast and the
// site never live-refreshed. This exercises the full stack — New's real
// fsnotify.Watcher and Watcher.Run, not newWatcher+fakeBackend — so a
// regression here would mean the fix works for desiredDirs' bookkeeping but
// not for an actual ReadDirectoryChangesW registration.
func TestNestedChangeUnderJunctionReachesHub(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := t.TempDir()
	source := t.TempDir()
	inside := filepath.Join(source, "inside")
	if err := os.Mkdir(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "watch")
	if err := testutil.MakeJunction(link, source); err != nil {
		t.Fatal(err)
	}

	hub := NewHub()
	w, err := New(root, hub)
	if err != nil {
		t.Fatal(err)
	}
	changes, cancel := hub.Subscribe()
	defer cancel()

	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	defer func() {
		stop()
		if err := <-done; err != nil && err != context.Canceled {
			t.Errorf("Run: %v", err)
		}
	}()

	if err := os.WriteFile(filepath.Join(inside, "new.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("a change nested below a junction watch link never reached the hub — " +
			"junction watch coverage stopped one level short of the link (P-5)")
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
