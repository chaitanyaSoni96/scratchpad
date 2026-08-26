// Package watch turns filesystem events under the store root into debounced
// change broadcasts for SSE clients.
package watch

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fsnotify/fsnotify"

	"scratchpad/internal/store"
)

const (
	debounce   = 250 * time.Millisecond
	maxLatency = time.Second
)

type backend interface {
	Add(string) error
	Remove(string) error
	WatchList() []string
	Events() <-chan fsnotify.Event
	Errors() <-chan error
	Close() error
}

type fsBackend struct{ *fsnotify.Watcher }

func (b fsBackend) Events() <-chan fsnotify.Event { return b.Watcher.Events }
func (b fsBackend) Errors() <-chan error          { return b.Watcher.Errors }

// Watcher recursively watches a store tree and broadcasts changes.
type Watcher struct {
	root       string
	hub        *Hub
	backend    backend
	debounce   time.Duration
	maxLatency time.Duration
	registered map[string]dirIdentity
}

// dirIdentity is defined per-platform (identity_unix.go / identity_windows.go),
// not here (ADR §3.5): Linux's dev+ino pair and Windows's volume-serial +
// 128-bit FILE_ID_INFO.FileId don't share a natural common shape, and shared
// code only ever compares values with == or stores them as map values, so an
// opaque, per-platform, comparable struct is all this file needs.

type desiredDir struct {
	path string
	id   dirIdentity
}

// New creates a watcher and synchronously registers the complete initial tree.
// The caller must call Run and treat any error it returns as fatal.
func New(root string, hub *Hub) (*Watcher, error) {
	b, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create filesystem watcher: %w", err)
	}
	w, err := newWatcher(root, hub, fsBackend{b}, debounce, maxLatency)
	if err != nil {
		b.Close()
		return nil, err
	}
	return w, nil
}

func newWatcher(root string, hub *Hub, b backend, debounce, maxLatency time.Duration) (*Watcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve watch root %q: %w", root, err)
	}
	w := &Watcher{root: abs, hub: hub, backend: b, debounce: debounce, maxLatency: maxLatency, registered: make(map[string]dirIdentity)}
	if err := w.reconcile(); err != nil {
		return nil, fmt.Errorf("register initial watch tree %q: %w", root, err)
	}
	return w, nil
}

// Run processes filesystem events until cancellation or a backend failure.
func (w *Watcher) Run(ctx context.Context) error {
	defer w.backend.Close()

	var trailing, maximum *time.Timer
	var trailingC, maximumC <-chan time.Time
	stopTimers := func() {
		if trailing != nil {
			trailing.Stop()
		}
		if maximum != nil {
			maximum.Stop()
		}
	}
	defer stopTimers()

	refresh := func() error {
		if err := w.reconcile(); err != nil {
			return fmt.Errorf("reconcile watch tree: %w", err)
		}
		w.hub.Broadcast()
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.backend.Events():
			if !ok {
				return errors.New("filesystem watcher event channel closed")
			}
			// A directory can arrive already populated. Reconcile immediately so
			// changes below it are not missed while the debounce timer is running.
			if ev.Op.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					if err := w.reconcile(); err != nil {
						return fmt.Errorf("watch created subtree %q: %w", ev.Name, err)
					}
				}
			}
			if trailing == nil {
				trailing = time.NewTimer(w.debounce)
				trailingC = trailing.C
				maximum = time.NewTimer(w.maxLatency)
				maximumC = maximum.C
			} else {
				if !trailing.Stop() {
					select {
					case <-trailing.C:
					default:
					}
				}
				trailing.Reset(w.debounce)
				if maximum == nil {
					maximum = time.NewTimer(w.maxLatency)
					maximumC = maximum.C
				}
			}
		case err, ok := <-w.backend.Errors():
			if !ok {
				return errors.New("filesystem watcher error channel closed")
			}
			if errors.Is(err, fsnotify.ErrEventOverflow) {
				if err := refresh(); err != nil {
					return fmt.Errorf("recover from filesystem event overflow: %w", err)
				}
				continue
			}
			return fmt.Errorf("filesystem watcher: %w", err)
		case <-maximumC:
			maximum.Stop()
			maximum = nil
			maximumC = nil
			if err := refresh(); err != nil {
				return err
			}
		case <-trailingC:
			trailing.Stop()
			trailing = nil
			trailingC = nil
			if maximum != nil {
				maximum.Stop()
				maximum = nil
				maximumC = nil
			}
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}

func (w *Watcher) reconcile() error {
	desired, err := desiredDirs(w.root)
	if err != nil {
		return err
	}

	current := make(map[string]string)
	for _, path := range w.backend.WatchList() {
		canonical, err := canonicalDir(path)
		if err != nil {
			// A removed path cannot be canonicalized, but is still a stale watch.
			canonical = filepath.Clean(path)
		}
		current[canonical] = path
	}

	for canonical, path := range current {
		want, ok := desired[canonical]
		if ok && w.registered[canonical] == want.id {
			continue
		}
		if err := w.backend.Remove(path); err != nil && !errors.Is(err, fsnotify.ErrNonExistentWatch) {
			return fmt.Errorf("remove stale watch %q: %w", path, err)
		}
		delete(current, canonical)
		delete(w.registered, canonical)
	}
	keys := make([]string, 0, len(desired))
	for canonical := range desired {
		keys = append(keys, canonical)
	}
	sort.Strings(keys)
	for _, canonical := range keys {
		if _, ok := current[canonical]; ok {
			continue
		}
		if err := w.backend.Add(desired[canonical].path); err != nil {
			return fmt.Errorf("add watch %q: %w", desired[canonical].path, err)
		}
		w.registered[canonical] = desired[canonical].id
	}
	return nil
}

func desiredDirs(root string) (map[string]desiredDir, error) {
	dirs := make(map[string]desiredDir)
	var walk func(string, bool, bool) error
	walk = func(dir string, required, crossedLink bool) error {
		canonical, err := canonicalDir(dir)
		if err != nil {
			if !required && skipEntry(dir, err) {
				return nil
			}
			return fmt.Errorf("resolve directory %q: %w", dir, err)
		}
		if _, seen := dirs[canonical]; seen {
			return nil
		}
		// Identity and the entry listing come from one open handle (rather
		// than two independent path lookups) so the two questions "what is
		// this" and "what does it contain" are answered about the same
		// object — see identity's doc comment and platform-api-inventory.md.
		//
		// openWatchDir, not os.Open: on Windows os.Open's share mode omits
		// FILE_SHARE_DELETE (P13.go_share_mode, RW24), so a directory the
		// watcher happens to have open here could block the user's own
		// rename/delete of it, or one of the store's own atomic replaces,
		// for a reason that has nothing to do with watching it.
		f, err := openWatchDir(dir)
		if err != nil {
			if !required && skipEntry(dir, err) {
				return nil
			}
			return fmt.Errorf("open directory %q: %w", dir, err)
		}
		defer f.Close()
		id, err := identity(f)
		if err != nil {
			if !required && skipEntry(dir, err) {
				return nil
			}
			return fmt.Errorf("identify directory %q: %w", dir, err)
		}
		dirs[canonical] = desiredDir{path: canonical, id: id}
		entries, err := f.ReadDir(-1)
		if err != nil {
			if !required && skipEntry(dir, err) {
				return nil
			}
			return fmt.Errorf("read directory %q: %w", dir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			isLink := store.IsLinkEntry(entry)
			isDir := entry.IsDir()
			if isLink && !crossedLink {
				if info, err := os.Stat(path); err == nil && info.IsDir() {
					isDir = true
				}
			}
			if isLink && crossedLink {
				continue
			}
			if isDir && store.Visible(dir, entry.Name(), true) {
				if err := walk(path, false, crossedLink || isLink); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(root, true, false); err != nil {
		return nil, err
	}
	return dirs, nil
}

// canonicalDir is defined per-platform (identity_unix.go / identity_windows.go),
// not here, for the same reason as dirIdentity above and P-5
// (.agents/plans/in-progress/native-windows-support/reviews/P4.7-semantic-parity.md):
// filepath.EvalSymlinks — sufficient on Linux, where every link flavour is a
// true ModeSymlink — cannot canonicalise a Windows junction, the default
// watch-link flavour for a Developer-Mode-off user. See identity_windows.go
// for the measured failure mode and the handle-based replacement.

// skipEntry reports whether err, encountered while resolving, opening,
// identifying or reading dir during desiredDirs' walk, means "this one entry
// is unreachable" rather than "the walk itself is broken" — and, when it
// does, logs the skip once. skipWalkError (identity_unix.go /
// identity_windows.go) is where the actual classification lives, since what
// counts as "just this entry" is platform-specific (R16's third clause, ADR
// §6.11 / finding F6): on Windows, an unserviced reparse tag — an
// APPEXECLINK, a OneDrive placeholder, a ProjFS entry — must not stop the
// whole tree from being watched, and per §6.11 item 3 that includes the very
// first walk newWatcher runs at startup, or a single such entry anywhere
// under the store root permanently prevents the web server from starting at
// all (the "boot loop").
//
// This is a deliberate, narrow carve-out, not a relaxation of "watcher
// failures are fatal": everything skipWalkError does NOT recognize —
// a backend Add/Remove failure, or the event/error channel closing —
// still propagates as a hard error out of desiredDirs/reconcile/newWatcher,
// which Run and main.go still treat as fatal so the process supervisor
// restarts a watcher that is actually broken (see CLAUDE.md's
// internal/watch section). Only entry-scoped, expected-on-Windows
// conditions are ever forgiven here.
func skipEntry(dir string, err error) bool {
	if !skipWalkError(err) {
		return false
	}
	log.Printf("scratchpad: watch: skipping unreadable or unclassifiable directory %q: %v", dir, err)
	return true
}
