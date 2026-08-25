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

func (r *rootedFS) close() error { return nil }

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
func fdPath(fd int) string { return "" }

func closeFD(fd int) {}

func mkdirClaim(parent int, name string) error { return errWindowsUnimplemented }

func rmdirAt(parent int, name string) error { return errWindowsUnimplemented }

func openDirAt(parent int, name string) (int, error) { return -1, errWindowsUnimplemented }

func dirHasHTMLFD(fd int) bool { return false }

func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error) {
	return -1, errWindowsUnimplemented
}

func (r *rootedFS) openBrowsableDir(segs []string) (int, error) {
	return -1, errWindowsUnimplemented
}

func mkdirsAt(root int, segs []string) (int, error) { return -1, errWindowsUnimplemented }

func writeFileAt(root int, segs []string, data []byte) error { return errWindowsUnimplemented }

func openFileAt(parent int, name string) (*os.File, error) { return nil, errWindowsUnimplemented }

func readAllAt(parent int, name string) ([]byte, error) { return nil, errWindowsUnimplemented }

func pruneAt(r *rootedFS, segs []string) {}

func openPathFile(segs []string) (*os.File, error) { return nil, errWindowsUnimplemented }

// OpenDocument returns false rather than erroring, matching its Linux
// signature (bool, not error) and its Linux behavior for "not found" —
// which is indistinguishable, from this signature, from "not implemented".
func OpenDocument(segs []string) (*os.File, bool) { return nil, false }
