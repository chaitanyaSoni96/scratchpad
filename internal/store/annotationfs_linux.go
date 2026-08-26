//go:build linux

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// annotationFS anchors all sidecar operations to an open .annotations inode.
// No operation below resolves a pathname from the process root.
type annotationFS struct {
	storeRoot *os.File
	root      *os.File
}

func openAnnotationFS() (*annotationFS, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	rootFD, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	if err := unix.Mkdirat(rootFD, AnnotationsDir, 0o755); err != nil && !errors.Is(err, unix.EEXIST) {
		unix.Close(rootFD)
		return nil, err
	}
	fd, err := unix.Openat(rootFD, AnnotationsDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		unix.Close(rootFD)
		return nil, fmt.Errorf("annotations: %q is not a real directory: %w", AnnotationsDir, err)
	}
	return &annotationFS{
		storeRoot: os.NewFile(uintptr(rootFD), root),
		root:      os.NewFile(uintptr(fd), AnnotationsDir),
	}, nil
}

func (a *annotationFS) close() error {
	err := a.root.Close()
	if rootErr := a.storeRoot.Close(); err == nil {
		err = rootErr
	}
	return err
}

func openDirAt(parent int, name string) (int, error) {
	return unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
}

// flockFile takes (exclusive=true) or shares (exclusive=false) an advisory
// lock on f's descriptor. Used both for the store-root rendezvous, via
// lockRendezvous below, and, via openLockFileAt, per-document locks.
func flockFile(f *os.File, exclusive bool) error {
	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	return unix.Flock(int(f.Fd()), how)
}

// funlockFile releases a lock taken by flockFile.
func funlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}

// lockRendezvous flocks the pinned STORE ROOT descriptor itself — the very
// object every operation in this process is anchored on. Losing the
// rendezvous therefore requires replacing the store root, which needs write
// access to the root's PARENT. This is the property the Windows twin
// (annotationfs_windows.go) cannot fully reproduce: LockFileEx refuses a
// directory handle outright, so its rendezvous must live on a CHILD of the
// anchor instead, and losing it needs write access only inside the store.
// See annotationfs_windows.go's header comment for the full comparison
// (ADR §6.7, the F5 rework).
func lockRendezvous(a *annotationFS, exclusive bool) error {
	return flockFile(a.storeRoot, exclusive)
}

func unlockRendezvous(a *annotationFS) error {
	return funlockFile(a.storeRoot)
}

// openLockFileAt creates or opens name (a per-document lock file) relative
// to parent, never following a symlink.
func openLockFileAt(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (a *annotationFS) openDir(segs []string, create bool) (int, error) {
	fd, err := unix.Dup(int(a.root.Fd()))
	if err != nil {
		return -1, err
	}
	for _, seg := range segs {
		next, openErr := openDirAt(fd, seg)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			if mkErr := unix.Mkdirat(fd, seg, 0o755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				unix.Close(fd)
				return -1, mkErr
			}
			next, openErr = openDirAt(fd, seg)
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func (a *annotationFS) readFile(segs []string) ([]byte, error) {
	parent, err := a.openDir(segs[:len(segs)-1], false)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, segs[len(segs)-1], unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), segs[len(segs)-1])
	defer f.Close()
	return io.ReadAll(f)
}

func (a *annotationFS) writeFile(segs []string, data []byte) error {
	parent, err := a.openDir(segs[:len(segs)-1], true)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	var fd int
	var tmp string
	for i := 0; i < 100; i++ {
		var nonce [8]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return err
		}
		tmp = ".notes-" + hex.EncodeToString(nonce[:]) + ".tmp"
		fd, err = unix.Openat(parent, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
		if !errors.Is(err, unix.EEXIST) {
			break
		}
	}
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		if !ok {
			_ = unix.Unlinkat(parent, tmp, 0)
		}
	}()
	f := os.NewFile(uintptr(fd), tmp)
	if _, err = f.Write(data); err == nil {
		err = f.Chmod(0o644)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	// Deterministic-race hook ("notes-replace"): fires after the temp file is
	// fully written and before the atomic rename over the destination, so a
	// test can substitute the destination in that window (A2.dest_replaced.*
	// -style attacks) without a timing loop.
	runStoreOpHook("notes-replace")
	if err = unix.Renameat(parent, tmp, parent, segs[len(segs)-1]); err != nil {
		return err
	}
	ok = true
	return nil
}

func readDirFD(fd int) ([]os.DirEntry, error) {
	dup, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	// A dup()'d descriptor shares the ORIGINAL's open file description,
	// directory read position included, so a second readDirFD call against
	// the same original fd (now reachable from store.go's handle-anchored
	// List/loadArtifactAt/dirHasHTMLFD, which were not written with a
	// once-per-fd assumption the way the original annotation-tree callers
	// were) would silently see zero entries rather than a fresh listing.
	// Rewinding here — which affects the original fd too, since the offset
	// is shared — makes every call a full, independent enumeration, which is
	// the property callers actually rely on (and the property M16 measured
	// on Windows via a fresh DuplicateHandle + ReadDir per call).
	if _, err := unix.Seek(dup, 0, unix.SEEK_SET); err != nil {
		unix.Close(dup)
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "annotations")
	defer f.Close()
	return f.ReadDir(-1)
}

// removeTreeAt recursively removes the directory named by (parent, name),
// handle-anchored and no-follow throughout (a symlink entry is unlinked, not
// descended). P3.9 originally gave this the same maxArtifactWalkDepth bound
// List/sizeWalkAt use for R16, as a hard error past the bound. P3.14's M2
// finding reproduced why that was wrong: List and sizeWalkAt need the bound
// because an unbounded walk over attacker-reachable structure risks a
// symlink/reparse cycle, but removeTreeAt never follows a link either, so it
// carries none of that risk — real directories cannot cycle. A depth bound
// on removal therefore cannot protect anything; it can only leave an
// artifact the store itself created permanently undeletable (no partial
// destruction either: the recursion errored on the way down, before any
// removal). removeTreeAt is deliberately unbounded: the store must never
// create something it refuses to delete, and the only bound that mattered —
// the filesystem's own — still applies.
func removeTreeAt(parent int, name string) error {
	fd, err := openDirAt(parent, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		// Unlinking a non-directory entry (including a symlink) is safe relative
		// to parent and never follows it.
		if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			return unix.Unlinkat(parent, name, 0)
		}
		return err
	}
	entries, readErr := readDirFD(fd)
	if readErr == nil {
		for _, entry := range entries {
			var st unix.Stat_t
			if err := unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
				if errors.Is(err, unix.ENOENT) {
					continue
				}
				readErr = err
				break
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				if err := removeTreeAt(fd, entry.Name()); err != nil && !errors.Is(err, unix.ENOENT) {
					readErr = err
					break
				}
			} else if err := unix.Unlinkat(fd, entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
				readErr = err
				break
			}
		}
	}
	unix.Close(fd)
	if readErr != nil {
		return readErr
	}
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}

func (a *annotationFS) removeSubtree(segs []string) error {
	parent, err := a.openDir(segs[:len(segs)-1], false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	// Deterministic-race hook ("notes-remove"): fires after the parent
	// directory of the annotation subtree being removed is pinned and before
	// any removal happens, so a test can race a concurrent SaveNotes against
	// this Delete/Unwatch cleanup (the RW5/RW6 "Delete racing SaveNotes"
	// scenario) without a timing loop.
	runStoreOpHook("notes-remove")
	if err := removeTreeAt(parent, segs[len(segs)-1]); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err := unix.Unlinkat(parent, segs[len(segs)-1]+".json", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	// Prune empty mirrored ancestors, reopening each parent from the anchored
	// root so a concurrent rename can only make pruning stop, never redirect it.
	for i := len(segs) - 1; i > 0; i-- {
		ancestor, err := a.openDir(segs[:i-1], false)
		if err != nil {
			break
		}
		err = unix.Unlinkat(ancestor, segs[i-1], unix.AT_REMOVEDIR)
		unix.Close(ancestor)
		if err != nil && !errors.Is(err, unix.ENOENT) {
			break
		}
	}
	return nil
}

func (a *annotationFS) walk(segs []string, visit func([]string, []byte)) error {
	fd, err := a.openDir(segs, false)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return walkAnnotationDir(fd, append([]string(nil), segs...), visit)
}

func walkAnnotationDir(fd int, prefix []string, visit func([]string, []byte)) error {
	entries, err := readDirFD(fd)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var st unix.Stat_t
		if err := unix.Fstatat(fd, entry.Name(), &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		path := append(append([]string(nil), prefix...), entry.Name())
		switch st.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := openDirAt(fd, entry.Name())
			if err != nil {
				continue
			}
			_ = walkAnnotationDir(child, path, visit)
			unix.Close(child)
		case unix.S_IFREG:
			if len(entry.Name()) < 5 || entry.Name()[len(entry.Name())-5:] != ".json" {
				continue
			}
			file, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				continue
			}
			f := os.NewFile(uintptr(file), entry.Name())
			data, readErr := io.ReadAll(f)
			f.Close()
			if readErr == nil {
				visit(path, data)
			}
		}
	}
	return nil
}
