// Package watch turns filesystem events under the store root into debounced
// change broadcasts for SSE clients.
package watch

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"syscall"
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

type dirIdentity struct {
	dev uint64
	ino uint64
}

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
			if !required && os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("resolve directory %q: %w", dir, err)
		}
		if _, seen := dirs[canonical]; seen {
			return nil
		}
		id, err := identity(canonical)
		if err != nil {
			return fmt.Errorf("identify directory %q: %w", dir, err)
		}
		dirs[canonical] = desiredDir{path: canonical, id: id}
		entries, err := os.ReadDir(dir)
		if err != nil {
			if !required && os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("read directory %q: %w", dir, err)
		}
		for _, entry := range entries {
			path := filepath.Join(dir, entry.Name())
			isLink := entry.Type()&fs.ModeSymlink != 0
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

func identity(path string) (dirIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return dirIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return dirIdentity{}, fmt.Errorf("filesystem does not expose inode identity")
	}
	return dirIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, nil
}

func canonicalDir(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
