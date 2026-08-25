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
			next, openErr = unix.Open(string(buf[:n]), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
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
