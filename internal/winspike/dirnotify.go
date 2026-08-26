//go:build windows

package winspike

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// A raw ReadDirectoryChangesW observer.
//
// This is not a watcher; it is an INSTRUMENT for one question: did the
// filesystem see a rename, or did it see a delete followed by a rename?
//
// WITHDRAWN HYPOTHESIS — read this before trusting the instrument.
//
// This comment used to claim the kernel's action codes distinguish the two
// implementations, so that "no FILE_ACTION_REMOVED naming the destination"
// was a black-box assertion of atomicity. P13.change_records falsified that
// on the runner (run 32908643117): a POSIX-semantics rename that REPLACES a
// destination makes the kernel emit FILE_ACTION_REMOVED for the replaced
// file as PART OF the atomic rename, so an atomic replace and a deliberate
// remove-then-rename produce identical record streams.
//
// The observer is still useful — it detects a 0-byte return, i.e. the
// ReadDirectoryChangesW overflow condition, and reports it rather than
// silently truncating, which is the shape internal/watch should copy. But it
// CANNOT certify atomicity. The guards that actually carry that property are
// the namespace-removal audit (P13.audit) and the continuous-existence
// observer (P13.continuous_existence), plus code review — see
// spike-findings.md §9.6.
// ---------------------------------------------------------------------------

// DirAction is one FILE_NOTIFY_INFORMATION record.
type DirAction struct {
	Action uint32
	Name   string
}

func (d DirAction) String() string { return ActionName(d.Action) + "(" + d.Name + ")" }

// ActionName renders a FILE_ACTION_* code.
func ActionName(a uint32) string {
	switch a {
	case windows.FILE_ACTION_ADDED:
		return "ADDED"
	case windows.FILE_ACTION_REMOVED:
		return "REMOVED"
	case windows.FILE_ACTION_MODIFIED:
		return "MODIFIED"
	case windows.FILE_ACTION_RENAMED_OLD_NAME:
		return "RENAMED_OLD_NAME"
	case windows.FILE_ACTION_RENAMED_NEW_NAME:
		return "RENAMED_NEW_NAME"
	default:
		return fmt.Sprintf("ACTION_%d", a)
	}
}

// DirObserver reports the raw change records for one directory.
type DirObserver struct {
	h        windows.Handle
	path     string
	stopping atomic.Bool
	mu       sync.Mutex
	acts     []DirAction
	done     chan struct{}
	err      error
}

// ObserveDir starts watching path (non-recursively) for name changes. The
// handle is opened with the full share mode so the observer itself can never
// be the reason a rename or delete fails.
func ObserveDir(path string) (*DirObserver, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.FILE_LIST_DIRECTORY, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, err
	}
	o := &DirObserver{h: h, path: path, done: make(chan struct{})}
	go o.loop()
	return o, nil
}

func (o *DirObserver) loop() {
	defer close(o.done)
	buf := make([]byte, 64*1024)
	for {
		var n uint32
		err := windows.ReadDirectoryChanges(o.h, &buf[0], uint32(len(buf)), false,
			windows.FILE_NOTIFY_CHANGE_FILE_NAME|windows.FILE_NOTIFY_CHANGE_DIR_NAME|
				windows.FILE_NOTIFY_CHANGE_LAST_WRITE|windows.FILE_NOTIFY_CHANGE_SIZE,
			&n, nil, 0)
		if err != nil {
			o.mu.Lock()
			o.err = err
			o.mu.Unlock()
			return
		}
		if n == 0 {
			// Buffer overflow: the kernel dropped records. Report it rather
			// than silently returning a short list.
			o.mu.Lock()
			o.err = fmt.Errorf("winspike: ReadDirectoryChangesW reported an overflow (0 bytes)")
			o.mu.Unlock()
			return
		}
		o.mu.Lock()
		o.acts = append(o.acts, parseNotify(buf[:n])...)
		o.mu.Unlock()
		if o.stopping.Load() {
			return
		}
	}
}

func parseNotify(b []byte) []DirAction {
	var out []DirAction
	for off := 0; off+12 <= len(b); {
		next := int(u32at(b, off))
		action := u32at(b, off+4)
		nameLen := int(u32at(b, off+8))
		if off+12+nameLen > len(b) {
			break
		}
		out = append(out, DirAction{Action: action, Name: decodeUTF16(b[off+12 : off+12+nameLen])})
		if next == 0 {
			break
		}
		off += next
	}
	return out
}

func u32at(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

// Actions returns everything recorded so far.
//
// Closing the handle aborts the blocked ReadDirectoryChangesW, so a caller
// must first make a change it can recognise (a sentinel) and wait for it with
// WaitFor; otherwise records the kernel has queued but not yet copied out
// would be lost.
func (o *DirObserver) Actions() []DirAction {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]DirAction, len(o.acts))
	copy(out, o.acts)
	return out
}

// Reset discards the records collected so far. Used after the priming
// sentinel, so the measurement window contains only the operation under test.
func (o *DirObserver) Reset() {
	o.mu.Lock()
	o.acts = nil
	o.mu.Unlock()
}

// WaitFor blocks until a record naming `name` has been collected, or the
// deadline passes. It returns whether the name was seen.
//
// This is what makes the instrument sound in both directions: it is used once
// BEFORE the operation (to prove the read loop is live, so no record can be
// missed at startup) and once AFTER (to drain the records the operation
// produced before the handle is closed).
func (o *DirObserver) WaitFor(name string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		o.mu.Lock()
		for _, a := range o.acts {
			if strings.EqualFold(a.Name, name) {
				o.mu.Unlock()
				return true
			}
		}
		stopped := o.err != nil
		o.mu.Unlock()
		if stopped {
			return false
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// Err reports why the observer stopped, if it has.
func (o *DirObserver) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// Close stops the observer.
//
// It does NOT simply close the handle: this is a SYNCHRONOUS
// ReadDirectoryChangesW, and closing a handle out from under a thread blocked
// in synchronous I/O can block CloseHandle itself. Instead the loop is asked
// to stop, then woken by a change of our own so it observes the request and
// returns on its own. CancelIoEx plus the close happen off the calling
// goroutine as a backstop, with every wait bounded — an instrument must never
// be able to hang the job it is measuring.
func (o *DirObserver) Close() {
	o.stopping.Store(true)
	_ = os.WriteFile(filepath.Join(o.path, ".observer-stop"), []byte("x"), 0o644)
	select {
	case <-o.done:
	case <-time.After(5 * time.Second):
	}
	go func() {
		_ = windows.CancelIoEx(o.h, nil)
		_ = windows.CloseHandle(o.h)
	}()
}
