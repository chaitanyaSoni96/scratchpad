package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"scratchpad/internal/testutil"
)

type fakeBackend struct {
	mu       sync.Mutex
	watches  map[string]bool
	events   chan fsnotify.Event
	errors   chan error
	addErr   error
	adds     []string
	removes  []string
	closed   chan struct{}
	closeOne sync.Once
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{watches: map[string]bool{}, events: make(chan fsnotify.Event, 32), errors: make(chan error, 4), closed: make(chan struct{})}
}
func (f *fakeBackend) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.addErr != nil {
		return f.addErr
	}
	f.watches[path] = true
	f.adds = append(f.adds, path)
	return nil
}
func (f *fakeBackend) Remove(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.watches, path)
	f.removes = append(f.removes, path)
	return nil
}
func (f *fakeBackend) WatchList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for path := range f.watches {
		paths = append(paths, path)
	}
	return paths
}
func (f *fakeBackend) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeBackend) Errors() <-chan error          { return f.errors }
func (f *fakeBackend) Close() error {
	f.closeOne.Do(func() { close(f.closed) })
	return nil
}

// mustCanonical is canonicalDir, fatal on error. Backend bookkeeping
// (fakeBackend.adds/removes, Watcher.registered) is keyed on desiredDirs'
// canonicalized form, not on whatever spelling a test happened to build a
// path with. On Windows those two can disagree in spelling while naming the
// same directory: TEMP resolving to an 8.3 short-name alias (observed:
// RUNNER~1) versus canonicalDir's filepath.EvalSymlinks, which normalizes to
// the long name (runneradmin) — precisely the case the ADR's §7.1/§7.2
// identity-over-spelling reasoning covers, and the same fix already applied
// to internal/store's tests for the identical symptom (commit 4d87801,
// TestUnwatch/TestWatchResolvesSymlinkedAncestorAtCreation's sameTarget). A
// byte-exact comparison against a raw t.TempDir()-built path is the wrong
// tool here, on both platforms — canonicalizing first is correct always and
// only visibly matters on Windows.
func mustCanonical(t *testing.T, path string) string {
	t.Helper()
	canonical, err := canonicalDir(path)
	if err != nil {
		t.Fatalf("canonicalDir(%q): %v", path, err)
	}
	return canonical
}

func TestNewWatcherRegistersInitialTreeAndReportsFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b"), 0o755); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	if _, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if got := len(b.WatchList()); got != 3 {
		t.Fatalf("got %d watches, want 3", got)
	}

	b = newFakeBackend()
	b.addErr = errors.New("out of watches")
	if _, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour); err == nil {
		t.Fatal("expected initial registration error")
	}
}

func TestReconcileAddsPopulatedTreeRemovesStaleAndGuardsCycles(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := t.TempDir()
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, "gone")
	b.watches[stale] = true
	leaf := filepath.Join(root, "new", "full", "leaf")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(root, filepath.Join(leaf, "cycle")); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	if b.watches[stale] {
		t.Fatal("stale watch was not removed")
	}
	if got := len(b.WatchList()); got != 4 {
		t.Fatalf("got %d watches, want root plus three new directories", got)
	}
}

func TestReconcileReplacesWatchWhenDirectoryIdentityChanges(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(t.TempDir(), "old")
	if err := os.Rename(dir, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	dirCanonical := mustCanonical(t, dir)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.removes) != 1 || b.removes[0] != dirCanonical {
		t.Fatalf("removes = %v, want replaced path", b.removes)
	}
	if len(b.adds) != 3 || b.adds[len(b.adds)-1] != dirCanonical {
		t.Fatalf("adds = %v, want replacement registration", b.adds)
	}
}

func TestReconcileReplacesWatchWhenLinkTargetIdentityChanges(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := t.TempDir()
	external := t.TempDir()
	target := filepath.Join(external, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+"-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	targetCanonical := mustCanonical(t, target)
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.removes) != 1 || b.removes[0] != targetCanonical {
		t.Fatalf("removes = %v, want old target removed", b.removes)
	}
	if b.adds[len(b.adds)-1] != targetCanonical {
		t.Fatalf("adds = %v, want new target registered", b.adds)
	}
}

func TestDesiredDirsFollowsWatchLinkButNotNestedDirectorySymlink(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := t.TempDir()
	source := t.TempDir()
	escape := t.TempDir()
	if err := os.Mkdir(filepath.Join(source, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(escape, filepath.Join(source, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, filepath.Join(root, "watch")); err != nil {
		t.Fatal(err)
	}
	dirs, err := desiredDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	inside, _ := canonicalDir(filepath.Join(source, "inside"))
	escaped, _ := canonicalDir(escape)
	if _, ok := dirs[inside]; !ok {
		t.Fatal("directory below deliberate watch link was not included")
	}
	if _, ok := dirs[escaped]; ok {
		t.Fatal("nested directory symlink escaped the watched source")
	}
}

func TestReconcileRemovesTargetWatchAfterUnwatch(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(root, "watch")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	targetCanonical := mustCanonical(t, target)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.watches[targetCanonical] {
		t.Fatal("target remained watched after link removal")
	}
	if len(b.removes) != 1 || b.removes[0] != targetCanonical {
		t.Fatalf("removes = %v, want target", b.removes)
	}
}

func TestRealBackendWatchesReplacementDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// w.registered is keyed by canonicalDir's output (watch.go's reconcile),
	// not by whatever spelling this test built dir with — see mustCanonical's
	// doc comment. Canonicalizing once, before the replacement, is correct:
	// the canonical *string* for a given name is stable across the directory
	// object being swapped out from under it.
	dirCanonical := mustCanonical(t, dir)
	w, err := New(root, NewHub())
	if err != nil {
		t.Fatal(err)
	}
	oldID, ok := w.registered[dirCanonical]
	if !ok {
		t.Fatalf("dir %q was not registered under its canonical key %q", dir, dirCanonical)
	}
	var zero dirIdentity
	if oldID == zero {
		t.Fatal("initial identity was the zero value — identity() is not reading real data")
	}
	if err := os.Rename(dir, filepath.Join(t.TempDir(), "old")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(); err != nil {
		t.Fatal(err)
	}
	if got := w.registered[dirCanonical]; got == oldID {
		t.Fatalf("replacement retained old identity %+v", got)
	}
	if err := w.backend.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCreateEventRegistersPopulatedSubtreeImmediately(t *testing.T) {
	root := t.TempDir()
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	top := filepath.Join(root, "arrived")
	if err := os.MkdirAll(filepath.Join(top, "already", "full"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.events <- fsnotify.Event{Name: top, Op: fsnotify.Create}
	deadline := time.Now().Add(time.Second)
	for len(b.WatchList()) != 4 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := len(b.WatchList()); got != 4 {
		t.Fatalf("got %d watches, want populated subtree registered", got)
	}
	stop()
	<-done
}

func TestOverflowReconcilesAndBroadcastsImmediately(t *testing.T) {
	root := t.TempDir()
	b := newFakeBackend()
	hub := NewHub()
	w, err := newWatcher(root, hub, b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	changes, cancel := hub.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	if err := os.Mkdir(filepath.Join(root, "added"), 0o755); err != nil {
		t.Fatal(err)
	}
	b.errors <- fsnotify.ErrEventOverflow
	select {
	case <-changes:
	case <-time.After(time.Second):
		t.Fatal("overflow did not broadcast")
	}
	if got := len(b.WatchList()); got != 2 {
		t.Fatalf("overflow did not reconcile: got %d watches", got)
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRunHasBoundedAndTrailingRefreshes(t *testing.T) {
	root := t.TempDir()
	b := newFakeBackend()
	hub := NewHub()
	w, err := newWatcher(root, hub, b, 40*time.Millisecond, 90*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	changes, cancel := hub.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	deadline := time.Now().Add(130 * time.Millisecond)
	for time.Now().Before(deadline) {
		b.events <- fsnotify.Event{Name: filepath.Join(root, "file"), Op: fsnotify.Write}
		time.Sleep(15 * time.Millisecond)
	}
	select {
	case <-changes: // maximum latency refresh
	case <-time.After(80 * time.Millisecond):
		t.Fatal("continuous events postponed refresh")
	}
	select {
	case <-changes: // trailing refresh after the final event
	case <-time.After(100 * time.Millisecond):
		t.Fatal("missing trailing refresh")
	}
	stop()
	<-done
	select {
	case <-b.closed:
	case <-time.After(time.Second):
		t.Fatal("backend was not closed")
	}
}

func TestRunRefreshesAcrossRepeatedMaximumLatencyWindows(t *testing.T) {
	root := t.TempDir()
	b := newFakeBackend()
	hub := NewHub()
	w, err := newWatcher(root, hub, b, 35*time.Millisecond, 70*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	changes, cancel := hub.Subscribe()
	defer cancel()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()

	sending := time.NewTimer(260 * time.Millisecond)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	count := 0
	for sending != nil || count < 3 {
		select {
		case <-tick.C:
			if sending != nil {
				b.events <- fsnotify.Event{Name: filepath.Join(root, "file"), Op: fsnotify.Write}
			}
		case <-changes:
			count++
		case <-func() <-chan time.Time {
			if sending == nil {
				return nil
			}
			return sending.C
		}():
			sending = nil
		case <-time.After(600 * time.Millisecond):
			t.Fatalf("got %d refreshes, want at least 3", count)
		}
	}
	stop()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRunReturnsBackendFailures(t *testing.T) {
	root := t.TempDir()
	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("backend died")
	b.errors <- want
	if err := w.Run(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Run returned %v, want %v", err, want)
	}
}

func TestRunReturnsWhenBackendChannelsClose(t *testing.T) {
	for _, channel := range []string{"events", "errors"} {
		t.Run(channel, func(t *testing.T) {
			b := newFakeBackend()
			w, err := newWatcher(t.TempDir(), NewHub(), b, time.Hour, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if channel == "events" {
				close(b.events)
			} else {
				close(b.errors)
			}
			if err := w.Run(context.Background()); err == nil {
				t.Fatal("Run returned nil after backend channel closure")
			}
			select {
			case <-b.closed:
			default:
				t.Fatal("backend was not closed")
			}
		})
	}
}
