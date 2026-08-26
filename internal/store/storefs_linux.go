//go:build linux

package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// rootedFS keeps operations anchored to the inode opened as SCRATCHPAD_ROOT.
type rootedFS struct {
	root *os.File
}

func openRootedFS(create bool) (*rootedFS, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return &rootedFS{root: os.NewFile(uintptr(fd), root)}, nil
}

func (r *rootedFS) close() error { return r.root.Close() }

func dupFD(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0) }

func fdPath(fd int) string { return fmt.Sprintf("/proc/self/fd/%d", fd) }

// closeFD closes fd, discarding the error like every existing call site
// already did (most are deferred; the rest are best-effort cleanup on an
// error path already being reported through a different return value).
func closeFD(fd int) { unix.Close(fd) }

// mkdirClaim atomically claims name as a new directory relative to the open
// directory parent: os.Mkdir's create-only guarantee, fd-relative. A
// collision (a race or an existing artifact) surfaces as errExists so
// callers never need to import unix to recognize it.
func mkdirClaim(parent int, name string) error {
	if err := unix.Mkdirat(parent, name, 0o755); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errExists
		}
		return err
	}
	return nil
}

// rmdirAt removes the single empty directory entry name relative to the
// open directory parent — used to roll back a claim that failed after
// mkdirClaim succeeded (Publish) so a partially created name is not left
// behind.
func rmdirAt(parent int, name string) error {
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}

func dirHasHTMLFD(fd int) bool {
	entries, err := readDirFD(fd)
	if err != nil {
		return false
	}
	for _, e := range entries {
		var st unix.Stat_t
		if strings.HasSuffix(strings.ToLower(e.Name()), ".html") && unix.Fstatat(fd, e.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
			return true
		}
	}
	return false
}

// openRealDir walks only real directory entries. The returned descriptor owns
// its reference, so renaming any checked ancestor cannot redirect later work.
func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	for i, seg := range segs {
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
			if errors.Is(openErr, unix.ELOOP) {
				return -1, fmt.Errorf("project ancestor %q is a symlink", strings.Join(segs[:i+1], "/"))
			}
			return -1, openErr
		}
		fd = next
		if rejectArtifacts && dirHasHTMLFD(fd) {
			unix.Close(fd)
			return -1, fmt.Errorf("%q is an artifact, not a project", strings.Join(segs[:i+1], "/"))
		}
	}
	return fd, nil
}

func mkdirsAt(root int, segs []string) (int, error) {
	fd, err := dupFD(root)
	if err != nil {
		return -1, err
	}
	for _, seg := range segs {
		next, e := openDirAt(fd, seg)
		if errors.Is(e, unix.ENOENT) {
			if e = unix.Mkdirat(fd, seg, 0o755); e == nil {
				next, e = openDirAt(fd, seg)
			}
		}
		unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}

func writeFileAt(root int, segs []string, data []byte) error {
	parent, err := mkdirsAt(root, segs[:len(segs)-1])
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, segs[len(segs)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), segs[len(segs)-1])
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func openFileAt(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		if err == nil {
			err = fmt.Errorf("%s is not a regular file", name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readAllAt(parent int, name string) ([]byte, error) {
	f, err := openFileAt(parent, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func pruneAt(r *rootedFS, segs []string) {
	for i := len(segs); i > 0; i-- {
		parent, err := r.openRealDir(segs[:i-1], false, false)
		if err != nil {
			return
		}
		err = unix.Unlinkat(parent, segs[i-1], unix.AT_REMOVEDIR)
		unix.Close(parent)
		if err != nil {
			return
		}
	}
}

// openBrowsableDir permits exactly one symlink entry: the store-owned watch
// boundary. All source-tree descendants are opened without following links.
func (r *rootedFS) openBrowsableDir(segs []string) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	crossed := false
	for _, seg := range segs {
		next, openErr := openDirAt(fd, seg)
		if (errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR)) && !crossed && !dirHasHTMLFD(fd) {
			buf := make([]byte, 4096)
			n, readErr := unix.Readlinkat(fd, seg, buf)
			if readErr != nil {
				unix.Close(fd)
				return -1, readErr
			}
			next, openErr = openAbsoluteDirNoFollow(string(buf[:n]))
			if openErr == nil {
				crossed = true
			}
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

// openFilesystemRootNoFollow opens the OS filesystem root "/" as the
// trusted, fixed anchor for walking an absolute watch-target path
// component by component (openAbsoluteDirNoFollow below). "/" is the root
// of the process's mount namespace: open(2) resolves it specially and it
// cannot itself be a symlink, so there is no earlier, more-trusted point to
// pin the walk to than this one. Every step after this is handle-relative,
// so nothing between here and the final component is ever resolved from a
// path string again.
func openFilesystemRootNoFollow() (int, error) {
	return unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// openAbsoluteDirNoFollow opens target — the string read back from the
// store's one permitted watch-link boundary — by walking its components
// one at a time from the filesystem root (openFilesystemRootNoFollow),
// refusing to traverse a symlink, or anything that is not a plain
// directory, at ANY component, not just the last one.
//
// This replaces unix.Open(target, ...|O_NOFOLLOW), which protected only the
// FINAL path component: every intermediate component was still resolved by
// the kernel following symlinks normally. Watch stores a
// filepath.EvalSymlinks-resolved target (store.go, Watch), so at the
// instant a watch is created there are no symlinks anywhere in this path —
// but the store never re-checked that on any later browse. If something
// later replaced a directory on this path with a symlink — content inside
// the watched source doing it (a `git checkout` swapping a tracked
// directory for a symlink is the realistic case, not only a hostile local
// process with write access above the watched folder) or an ancestor
// outside it being repointed — the old code silently followed it on every
// subsequent browse, and the redirected tree was reachable over the
// unauthenticated HTTP endpoint (`internal/web`). This is
// A11.ancestor_swapped, spike-findings.md §10.1: "the structural fix on
// both platforms is to walk the target's components handle-by-handle
// instead of opening the string."
//
// Handle-by-handle closes it: each step opens the next component relative
// to the PREVIOUS step's descriptor with openDirAt (O_NOFOLLOW), so there
// is no path string left, at any point, for a later substitution to
// redirect — only a chain of already-pinned descriptors. A symlink at any
// component surfaces as ELOOP (or ENOTDIR for a non-directory) and the walk
// fails closed; the caller (openBrowsableDir) treats that exactly like the
// old open failing. This is a read-only path: every mutation walk
// (openRealDir) was already handle-relative end to end and is unaffected.
//
// Ancestor symlinks that are legitimate (a moved /home, a convenience
// symlink the caller watched through) are handled at Watch-creation time,
// not here: Watch resolves and stores the fully-real target, so a
// symlink-free path is exactly what a normal, unmolested watch presents to
// this function on every browse. Anything this function refuses is
// therefore a path that changed since the watch was created — which is the
// condition the fix exists to catch.
func openAbsoluteDirNoFollow(target string) (int, error) {
	// filepath.IsAbs on Linux is exactly this prefix check; avoiding the
	// import keeps this file's dependency list unchanged. Watch only ever
	// stores an EvalSymlinks-resolved absolute path as a link target, so a
	// relative one here means the store's own symlink was not written by
	// Watch. There is no safe anchor to walk a relative path from — opening
	// it as a bare string (what the old code implicitly did for even an
	// absolute target) would resolve it against the process's current
	// working directory, exactly the ambient-authority mistake this
	// function exists to remove — so refuse rather than guess an anchor.
	if !strings.HasPrefix(target, "/") {
		return -1, fmt.Errorf("watch link target %q is not an absolute path", target)
	}
	fd, err := openFilesystemRootNoFollow()
	if err != nil {
		return -1, err
	}
	for _, seg := range strings.Split(target, "/") {
		if seg == "" {
			continue // leading "/", a trailing "/", or "//" between components
		}
		next, openErr := openDirAt(fd, seg)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openPathFile(segs []string) (*os.File, error) {
	if len(segs) == 0 {
		return nil, unix.ENOENT
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, err
	}
	defer rfs.close()
	parent, err := rfs.openBrowsableDir(segs[:len(segs)-1])
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	return openFileAt(parent, segs[len(segs)-1])
}

// OpenDocument pins a validated store-relative regular file for HTTP serving.
func OpenDocument(segs []string) (*os.File, bool) {
	root, err := Root()
	if err != nil || !visibleSegments(root, segs) {
		return nil, false
	}
	for _, seg := range segs {
		if validateSegment(seg) != nil {
			return nil, false
		}
	}
	f, err := openPathFile(segs)
	return f, err == nil
}
