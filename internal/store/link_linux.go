//go:build linux

package store

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// symlinkAt creates a symlink named name, relative to the open directory
// parent, pointing at target. Create-only: an existing name surfaces as
// errExists (see store.go), matching Publish's directory-claim semantics so
// Watch's idempotence check (store.go, Watch) can react the same way to
// either kind of collision.
func symlinkAt(parent int, target, name string) error {
	if err := unix.Symlinkat(target, parent, name); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errExists
		}
		return err
	}
	return nil
}

// readlinkAt reads the target of the symlink named name, relative to the
// open directory parent, without resolving it.
func readlinkAt(parent int, name string) (string, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(parent, name, buf)
	if err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

// unlinkAt removes the single directory entry name (a symlink, in every
// current caller) relative to the open directory parent. It never follows
// the entry and never removes a real directory's contents.
func unlinkAt(parent int, name string) error {
	return unix.Unlinkat(parent, name, 0)
}

// sameWatchTarget is Watch's idempotence comparison (ADR §7.2): on Linux,
// byte-exact string equality, unchanged from before this was given a name.
func sameWatchTarget(existing, abs string) bool { return existing == abs }

// isNotALinkAt reports whether err (from readlinkAt) means "name exists but
// is not a symlink at all" — readlinkat(2) on a real directory or regular
// file fails EINVAL. Watch's collision branch (store.go) uses this to give a
// bare real directory its own remediation message instead of the generic
// "already exists".
func isNotALinkAt(err error) bool { return errors.Is(err, unix.EINVAL) }

// isLinkAt classifies name, relative to the open directory parent, without
// following it: err is the stat failure (e.g. the entry does not exist);
// isLink is meaningful only when err == nil. This backs Unwatch and Delete's
// "is this entry the watch link, or a real artifact" decision.
func isLinkAt(parent int, name string) (isLink bool, err error) {
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	return st.Mode&unix.S_IFMT == unix.S_IFLNK, nil
}

// IsLinkInfo reports whether fi describes a symlink. It is exported because
// internal/web and internal/watch need the exact classification the store
// uses internally (folderUnwatch, entryIsDirFS in internal/web/server.go;
// desiredDirs in internal/watch/watch.go).
func IsLinkInfo(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

// IsLinkEntry is IsLinkInfo for an os.DirEntry (a ReadDir result), avoiding a
// second stat when the entry's type bits already answer the question.
func IsLinkEntry(e os.DirEntry) bool { return e.Type()&os.ModeSymlink != 0 }
