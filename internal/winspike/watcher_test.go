//go:build windows

package winspike

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TestM15FsnotifyOnWindows measures the three fsnotify behaviours
// internal/watch depends on. These are timing-sensitive, so a timeout is
// reported as NOT-MEASURED rather than failing the build.
func TestM15FsnotifyOnWindows(t *testing.T) {
	const settle = 3 * time.Second

	drain := func(w *fsnotify.Watcher, d time.Duration) ([]fsnotify.Event, []error) {
		var evs []fsnotify.Event
		var errs []error
		deadline := time.After(d)
		for {
			select {
			case e, ok := <-w.Events:
				if !ok {
					return evs, errs
				}
				evs = append(evs, e)
			case err, ok := <-w.Errors:
				if !ok {
					return evs, errs
				}
				errs = append(errs, err)
			case <-deadline:
				return evs, errs
			}
		}
	}

	// (a) Does a watch survive a rename of the watched directory?
	t.Run("rename_watched_dir", func(t *testing.T) {
		base := scratchDir(t)
		watched := mustMkdir(t, filepath.Join(base, "watched"))
		w, err := fsnotify.NewWatcher()
		if err != nil {
			Report(t, "M15.rename", NotMeasured, "NewWatcher: %v", err)
			return
		}
		defer w.Close()
		if err := w.Add(watched); err != nil {
			Report(t, "M15.rename", NotMeasured, "Add: %v", err)
			return
		}
		moved := filepath.Join(base, "moved")
		renameErr := os.Rename(watched, moved)
		if renameErr != nil {
			Report(t, "M15.rename", NotMeasured, "could not rename a watched directory: %v", renameErr)
			return
		}
		time.Sleep(200 * time.Millisecond)
		mustWrite(t, filepath.Join(moved, "after.txt"), "x")
		evs, errs := drain(w, settle)
		Report(t, "M15.rename", boolVerdict(len(evs) > 0),
			"after renaming the WATCHED directory itself, a create inside it produced %d event(s) %v and %d error(s) %v. "+
				"ReadDirectoryChangesW is registered on a HANDLE, so the watch follows the object; internal/watch's reconcile "+
				"keys on canonicalDir path strings and would see the old path disappear (watch.go:288-297).",
			len(evs), evs, len(errs), errs)
	})

	// (b) Does a populated subtree, created atomically by a rename, produce a
	//     single Create event? watch.go's registration depends on it.
	t.Run("populated_subtree_by_rename", func(t *testing.T) {
		base := scratchDir(t)
		watched := mustMkdir(t, filepath.Join(base, "watched"))
		staging := mustMkdir(t, filepath.Join(base, "staging", "tree", "deep"))
		mustWrite(t, filepath.Join(staging, "leaf.html"), "x")

		w, err := fsnotify.NewWatcher()
		if err != nil {
			Report(t, "M15.subtree", NotMeasured, "NewWatcher: %v", err)
			return
		}
		defer w.Close()
		if err := w.Add(watched); err != nil {
			Report(t, "M15.subtree", NotMeasured, "Add: %v", err)
			return
		}
		if err := os.Rename(filepath.Join(base, "staging", "tree"), filepath.Join(watched, "tree")); err != nil {
			Report(t, "M15.subtree", NotMeasured, "rename into the watched dir: %v", err)
			return
		}
		evs, errs := drain(w, settle)
		creates := 0
		for _, e := range evs {
			if e.Op&fsnotify.Create != 0 {
				creates++
			}
		}
		Report(t, "M15.subtree", boolVerdict(creates >= 1),
			"an atomically-renamed POPULATED subtree produced %d event(s) (%d Create) %v, errors %v. "+
				"A single Create for the top directory is what internal/watch must expand into a recursive registration; "+
				"per-descendant events must NOT be assumed.",
			len(evs), creates, evs, errs)
	})

	// (c) Overflow.
	t.Run("overflow", func(t *testing.T) {
		Report(t, "M15.overflow", NotMeasured,
			"ReadDirectoryChangesW buffer overflow (ERROR_NOTIFY_ENUM_DIR) is not reproducible deterministically on a CI runner. "+
				"fsnotify surfaces it on the Errors channel; internal/watch's existing overflow recovery path (reconcile) is the "+
				"right shape, but the Windows error VALUE differs from Linux's and must be mapped explicitly in Phase 3.")
	})
}
