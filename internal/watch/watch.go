// Package watch turns filesystem events under the store root into debounced
// change broadcasts for SSE clients.
package watch

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"scratchpad/internal/store"
)

const debounce = 250 * time.Millisecond

// Run watches the whole tree under root recursively, broadcasting one hub
// signal per burst of events. Blocks until ctx is done.
func Run(ctx context.Context, root string, hub *Hub) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()

	// watchTree adds dir and every directory below it, following symlinks
	// (watched external folders) with a cycle guard. Called for the root at
	// startup, for any newly created directory (a dir can arrive with
	// children already inside via mkdir -p, mv, cp -r, ln -s, so one Create
	// event must hook the whole subtree), and again after every debounced
	// burst to repair watches fsnotify dropped along the way.
	//
	// That repair matters because of a case fsnotify itself won't report: if
	// a watched directory's own inode is replaced (a build tool cleaning and
	// regenerating its output dir, rsync resyncing a tree, etc.), the old
	// watch dies with it. Ordinarily that'd surface as a Remove event on the
	// directory's own path, but fsnotify deliberately swallows that specific
	// event whenever the parent directory is also watched — which, since
	// watchTree watches every level, is always true here — on the assumption
	// that the parent's own delete event already covers it. That assumption
	// doesn't hold across a symlink boundary (a watched folder is a symlink
	// to elsewhere): replacing the symlink's target leaves the symlink's
	// dentry in its parent untouched, so no such parent-level event ever
	// comes. Re-walking after every burst sidesteps the swallowed event
	// instead of trying to detect it: w.Add on a path still standing is a
	// cheap no-op, and EvalSymlinks freshly resolves anything replaced.
	watchTree := func(top string) {
		visited := map[string]bool{} // cycle guard, fresh per traversal
		var add func(dir string)
		add = func(dir string) {
			real, err := filepath.EvalSymlinks(dir)
			if err != nil || visited[real] {
				return
			}
			visited[real] = true
			if err := w.Add(dir); err != nil {
				log.Printf("watch %s: %v", dir, err)
				return
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".") || store.Ignored(e.Name()) {
					continue
				}
				p := filepath.Join(dir, e.Name())
				if e.IsDir() {
					add(p)
				} else if e.Type()&fs.ModeSymlink != 0 {
					if fi, err := os.Stat(p); err == nil && fi.IsDir() {
						add(p)
					}
				}
			}
		}
		add(top)
	}
	watchTree(root)

	var timer *time.Timer
	var timerC <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			// fsnotify drops watches on removal by itself.
			if ev.Op.Has(fsnotify.Create) {
				if fi, err := os.Stat(ev.Name); err == nil && fi.IsDir() {
					watchTree(ev.Name)
				}
			}
			if timer == nil {
				timer = time.NewTimer(debounce)
				timerC = timer.C
			} else {
				timer.Reset(debounce)
			}
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			log.Printf("watcher error: %v", err)
		case <-timerC:
			timer = nil
			timerC = nil
			watchTree(root)
			hub.Broadcast()
		}
	}
}
