//go:build unix

package watch

import (
	"fmt"
	"os"
	"syscall"
)

// dirIdentity's field layout lives here, not in watch.go (ADR §3.5): shared
// code only compares values with == and stores them as map values, so an
// opaque, per-platform, comparable struct is enough. Windows's counterpart
// (identity_windows.go) needs a volume serial plus a 128-bit file id instead
// of a dev+ino pair, so the two platforms do not share one struct shape.
type dirIdentity struct {
	dev uint64
	ino uint64
}

// identity reads dir's device+inode pair from its already-open handle (an
// Fstat, not a path-based Stat), so two calls a moment apart about a
// directory that was replaced underneath the watcher answer about the same
// object dir names, not about "whatever is at this path now". Reconciliation
// (watch.go's reconcile) compares the result with ==, so replacement shows
// up as an identity change even when the path is unchanged.
//
// See threat-model-windows.md §3.17 and R14: the Windows counterpart
// (identity_windows.go) must satisfy the same contract with
// VolumeSerialNumber + FileId from the handle, not a separate lookup.
func identity(dir *os.File) (dirIdentity, error) {
	fi, err := dir.Stat()
	if err != nil {
		return dirIdentity{}, err
	}
	stat, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return dirIdentity{}, fmt.Errorf("filesystem does not expose inode identity")
	}
	return dirIdentity{dev: uint64(stat.Dev), ino: uint64(stat.Ino)}, nil
}

// openWatchDir opens dir for desiredDirs' walk. On Linux os.Open already
// grants every share Windows would call FILE_SHARE_*  — the concept doesn't
// exist here — so this is a direct passthrough; see identity_windows.go's
// openWatchDir for why Windows cannot use os.Open for this (RW24,
// P13.go_share_mode).
func openWatchDir(path string) (*os.File, error) {
	return os.Open(path)
}

// skipWalkError reports whether an error hit while resolving, opening,
// identifying or reading a directory during desiredDirs' walk means "this
// one entry is unreachable, skip it" rather than a fatal failure of the
// walk (ADR §6.11 / finding F6, RW23). On Linux the only such case is
// disappearance mid-walk — os.IsNotExist — because a permission error or a
// busy file does not make openat/(f)stat/getdents fail in a way that is
// otherwise indistinguishable from "gone", and this package has never
// tolerated those. See identity_windows.go's skipWalkError for the much
// larger Windows skip set and why it is larger.
func skipWalkError(err error) bool {
	return os.IsNotExist(err)
}
