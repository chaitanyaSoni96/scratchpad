//go:build windows

package store

import (
	"errors"
	"os"
)

// errWindowsUnimplemented is returned by every Windows store/annotation
// mechanism stub in this package (storefs_windows.go, link_windows.go,
// annotationfs_windows.go). It is deliberately a distinct sentinel — never
// nil, never an existing Go stdlib error — so a caller (and a test) cannot
// mistake "not implemented yet" for a real result: an empty read, a
// not-found, or a successful no-op. Phase 3 replaces every one of these
// functions with real handle-anchored Win32 mechanism per the ADR; nothing
// here should still exist once that lands.
var errWindowsUnimplemented = errors.New("scratchpad: the Windows store backend is not implemented yet (plan phase 3)")

// unreachableOnWindows panics. It backs the handful of stubs whose Linux
// signature has no error channel — they return a bool, a string, or nothing
// at all, so "not implemented" is indistinguishable from a real answer and
// every available constant is wrong. dirHasHTMLFD is the clearest case: it
// decides both openRealDir's artifact-nesting rejection and
// openBrowsableDir's single-watch-boundary guard, so false is fail-OPEN in
// two callers and fail-closed in a third, and true simply inverts which.
//
// Every one of these is downstream of a rootedFS operation that already
// fails on Windows, so reaching one means that ordering assumption broke.
// Crashing says so; a benign constant would silently disable a containment
// rule. P2.7 review finding F1.
func unreachableOnWindows(fn string) {
	panic("scratchpad: " + fn + " reached on Windows, but the Windows store backend is not implemented (plan phase 3). " +
		"Its caller was supposed to fail earlier at openRootedFS or openAnnotationFS; that ordering no longer holds.")
}

// rootedFS mirrors the Linux type: on Linux it keeps operations anchored to
// the inode opened as SCRATCHPAD_ROOT via a raw fd; the Windows backend
// (Phase 3) anchors the same way to a handle opened with
// FILE_FLAG_BACKUP_SEMANTICS|FILE_FLAG_OPEN_REPARSE_POINT and OBJ_DONT_REPARSE
// segment walks (spec "Windows rooted filesystem"; threat model R1-R3). The
// field is unused until then.
type rootedFS struct{}

func openRootedFS(create bool) (*rootedFS, error) {
	return nil, errWindowsUnimplemented
}

func (r *rootedFS) close() error { unreachableOnWindows("rootedFS.close"); return nil }

// dupFD, fdPath and closeFD exist only to keep storefs_linux.go's fd-based
// call sites (store.go) compiling identically on both platforms; the
// Windows backend does not use bare ints as handles (Phase 3 will use *os.File
// or a small handle wrapper per the spec's "Handle and path representation"),
// so these are unreachable stub shapes rather than a real mechanism.
func dupFD(fd int) (int, error) { return -1, errWindowsUnimplemented }

// fdPath has no Windows equivalent at all — see platform-api-inventory.md
// and threat-model-windows.md §2 ("fdPath ... is the single hardest porting
// constraint"; GetFinalPathNameByHandleW is a display primitive, not a
// substitute, because it re-resolves a string and reintroduces the TOCTOU
// the fd removed). Every caller of fdPath is downstream of a rootedFS
// operation that already fails before reaching it on Windows.
// The empty string would be actively dangerous: filepath.Join("", x) == x,
// so a caller would silently operate on a CWD-relative path.
func fdPath(fd int) string { unreachableOnWindows("fdPath"); return "" }

func closeFD(fd int) { unreachableOnWindows("closeFD") }

func mkdirClaim(parent int, name string) error { return errWindowsUnimplemented }

func rmdirAt(parent int, name string) error { return errWindowsUnimplemented }

func openDirAt(parent int, name string) (int, error) { return -1, errWindowsUnimplemented }

func dirHasHTMLFD(fd int) bool { unreachableOnWindows("dirHasHTMLFD"); return false }

func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error) {
	return -1, errWindowsUnimplemented
}

// openBrowsableDir's Linux implementation (storefs_linux.go) crosses the
// single permitted watch-link boundary by walking the link target's path
// components handle-by-handle from the filesystem root — openDirAt-style,
// refusing a reparse point at any component, not just the final one — after
// A11.ancestor_swapped (spike-findings.md §10.1) showed that re-opening the
// readlink(2) result as a whole path string only protects the final
// component from being a symlink, not any ancestor of it. The Windows
// backend has the handle-relative primitive to do the same thing: the
// strict open (`FILE_OPEN_REPARSE_POINT` + a `FILE_ATTRIBUTE_TAG_INFO`
// check on the resulting handle, per P1.7 red-team finding F1 and
// spike-findings.md §10.3) generalizes to every component of the resolved
// target, not just the final one, exactly the way openDirAt already does on
// Linux. That is Phase 3 scope; nothing here implements it.
func (r *rootedFS) openBrowsableDir(segs []string) (int, error) {
	return -1, errWindowsUnimplemented
}

func mkdirsAt(root int, segs []string) (int, error) { return -1, errWindowsUnimplemented }

func writeFileAt(root int, segs []string, data []byte) error { return errWindowsUnimplemented }

func openFileAt(parent int, name string) (*os.File, error) { return nil, errWindowsUnimplemented }

func readAllAt(parent int, name string) ([]byte, error) { return nil, errWindowsUnimplemented }

func pruneAt(r *rootedFS, segs []string) { unreachableOnWindows("pruneAt") }

func openPathFile(segs []string) (*os.File, error) { return nil, errWindowsUnimplemented }

// OpenDocument returns false rather than erroring or panicking: unlike the
// stubs above, false is unambiguously fail-CLOSED here — it serves a 404,
// which is the correct answer while there is no backend to open through.
func OpenDocument(segs []string) (*os.File, bool) { return nil, false }
