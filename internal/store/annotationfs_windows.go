//go:build windows

package store

import "os"

// annotationFS mirrors the Linux type: on Linux it anchors every sidecar
// operation to an open .annotations inode via raw fds. The Windows backend
// (Phase 3) anchors the same way to a handle, opened relative to the pinned
// store root, that refuses any reparse component (spec "Annotation
// atomicity"; threat model §3.12-§3.16). storeRoot mirrors the Linux field
// only so lockAnnotations (shared, annotations.go) keeps compiling — it is
// always nil here because openAnnotationFS never succeeds on this stub.
type annotationFS struct {
	storeRoot *os.File
}

func openAnnotationFS() (*annotationFS, error) {
	return nil, errWindowsUnimplemented
}

func (a *annotationFS) close() error { return nil }

func (a *annotationFS) openDir(segs []string, create bool) (int, error) {
	return -1, errWindowsUnimplemented
}

func (a *annotationFS) readFile(segs []string) ([]byte, error) {
	return nil, errWindowsUnimplemented
}

func (a *annotationFS) writeFile(segs []string, data []byte) error {
	return errWindowsUnimplemented
}

func (a *annotationFS) removeSubtree(segs []string) error {
	return errWindowsUnimplemented
}

func (a *annotationFS) walk(segs []string, visit func([]string, []byte)) error {
	return errWindowsUnimplemented
}

// removeTreeAt is called directly from store.go (Publish's rollback, Delete)
// as well as from annotationFS.removeSubtree on Linux, so it needs the same
// stub shape here. Phase 3's real implementation must walk by handle,
// classify each entry from that handle (never FILE_ATTRIBUTE_DIRECTORY
// alone) and unlink — never descend into — anything carrying a reparse tag
// (threat model §3.8, R8). This is the single highest-severity operation in
// the whole port (RR1): a naive recursive delete through a junction destroys
// the junction's target, not the junction.
func removeTreeAt(parent int, name string) error { return errWindowsUnimplemented }

// flockFile, funlockFile and openLockFileAt are Phase 2 stubs for the
// annotation locking mechanism (annotations.go's lockAnnotations,
// lockDocument). LockFileEx locks byte ranges of a *file*; Windows directory
// handles are not lockable at all (threat model M14), so flockFile's
// store-root rendezvous (the comment at annotations.go:124-126, "the
// store-root inode is stable even if a hostile process renames and replaces
// .annotations") has NO direct Windows equivalent and needs a Phase 3
// redesign, not a mechanical port — see platform-api-inventory.md and RR6.
// openLockFileAt's per-document lock file has a real Windows analogue
// (LockFileEx on an ordinary file opened with FILE_SHARE_READ|WRITE|DELETE)
// and can likely be ported mechanically in Phase 3.
func flockFile(f *os.File, exclusive bool) error { return errWindowsUnimplemented }

func funlockFile(f *os.File) error { return errWindowsUnimplemented }

func openLockFileAt(parent int, name string) (*os.File, error) {
	return nil, errWindowsUnimplemented
}
