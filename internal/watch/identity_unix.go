//go:build unix

package watch

import (
	"fmt"
	"os"
	"syscall"
)

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
