// Package watch turns filesystem events under the store root into debounced
// change broadcasts for SSE clients.
package watch

import (
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
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

	// watchTree adds dir and every directory below it. Called for the root
	// at startup and for any newly created directory: a dir can arrive with
	// children already inside (mkdir -p, mv, cp -r), so one Create event
	// must hook the whole subtree.
	watchTree := func(dir string) {
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if err := w.Add(p); err != nil {
				log.Printf("watch %s: %v", p, err)
			}
			return nil
		})
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
			hub.Broadcast()
		}
	}
}
