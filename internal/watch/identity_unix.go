//go:build unix

package watch

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
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

// canonicalDir is desiredDirs'/reconcile's per-platform dedup/identity key
// (watch.go's doc comment on the type). filepath.EvalSymlinks already
// resolves every link flavour a Linux watch tree can contain — there is no
// junction here — so this is unchanged from before the per-platform split;
// see identity_windows.go's canonicalDir for why Windows needs a different
// primitive (P-5,
// .agents/plans/in-progress/native-windows-support/reviews/P4.7-semantic-parity.md).
func canonicalDir(path string) (string, error) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(real)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
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
// walk (ADR §6.11 / finding F6, RW23, and P4.7 semantic-parity finding
// P-3). Two cases:
//
//   - fs.ErrNotExist: the entry disappeared between being listed and being
//     opened.
//   - fs.ErrPermission: a mode bit (or ACL, on a filesystem that layers one
//     on top of Unix permissions, e.g. NFSv4 ACLs) the server's account
//     cannot read — a chmod 000 build artifact, a root-owned directory in a
//     watched repo, any EACCES from openat/(f)stat/getdents. Before this
//     fix, a single such directory anywhere under the store root made
//     desiredDirs — and therefore newWatcher, and therefore
//     cmd/scratchpad-web's startup call — return a hard, fatal error; under
//     systemd --user that is a boot loop identical in shape to the one
//     ADR §6.11 fixed on Windows only (identity_windows.go's skipWalkError
//     already recognized fs.ErrPermission there). A skipped directory is
//     simply not watched: the 250 ms debounce and periodic reconcile
//     already tolerate a missing watch, so the user sees a degraded
//     refresh for that subtree rather than a dead server.
//
// Deliberately errors.Is, not os.IsNotExist/os.IsPermission: those helpers
// do not walk an Unwrap chain, so they stop matching the moment anything
// wrapped (fmt.Errorf("%w"), a typed error) reaches this function — see
// identity_windows.go's skipWalkError, which was fixed to errors.Is for the
// identical reason (P3.14 red-team finding L4).
//
// This is a narrow, entry-scoped carve-out, not a relaxation of "watcher
// failures are fatal": anything else — a backend Add/Remove failure, or the
// event/error channel closing — still propagates and is still fatal (see
// watch.go's skipEntry and CLAUDE.md's internal/watch section). See
// identity_windows.go's skipWalkError for the larger Windows skip set and
// why Windows needs more members (reparse tags, sharing violations, cloud
// placeholders have no Unix analogue).
func skipWalkError(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	return false
}
