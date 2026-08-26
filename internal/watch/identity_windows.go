//go:build windows

package watch

import (
	"errors"
	"io/fs"
	"os"
	"structs"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// dirIdentity's field layout lives here, not in watch.go (ADR §3.5): shared
// code only compares values with == and stores them as map values, so an
// opaque, per-platform, comparable struct is enough, and the shape Windows
// needs does not fit Linux's dev+ino pair (identity_unix.go). vol is
// FILE_ID_INFO.VolumeSerialNumber and id is FILE_ID_INFO's 128-bit FileId.
// This is P2.3's deferred decision, made here: the two platforms get their
// own struct instead of trying to force one shared shape, because Windows's
// identity is not two independent numbers the way dev+ino are — it is one
// opaque 16-byte object id plus a separate volume serial.
//
// BY_HANDLE_FILE_INFORMATION's 64-bit file index is deliberately NOT used:
// the win32-primitive-survey's Finding 6 documents it as insufficient on
// ReFS, which is exactly why FILE_ID_INFO (128-bit FileId) exists and is
// what internal/store's R13/R14 identity is built from (ADR §3.2/§3.5).
type dirIdentity struct {
	vol uint64
	id  [16]byte
}

// fileIDInfo mirrors FILE_ID_INFO byte for byte. golang.org/x/sys/windows
// v0.41.0 does not export this structure (ADR §3.6/M17.gaps — internal/store's
// win32_windows.go carries the identical layout as fileIDInfoRaw for the
// same reason). That file is package-private to internal/store and
// GetFileInformationByHandleEx/windows.FileIdInfo are already exported
// directly by golang.org/x/sys/windows, so this package declares its own
// copy of the struct shape rather than asking internal/store to export a
// helper for a single 24-byte read — see the P4 report for the fuller
// reasoning and what would change if a shared helper is preferred later.
type fileIDInfo struct {
	_                  structs.HostLayout // L3 (P3.14 red-team): promise host-native layout, not just today's convention
	VolumeSerialNumber uint64
	FileID             [16]byte
}

// identity reads dir's volume serial and 128-bit FileId from its already-open
// handle via GetFileInformationByHandleEx(FileIdInfo) — never a fresh path
// lookup — so it answers "what object is this handle open on", not
// "whatever is at this path now" (R14; see identity_unix.go's doc comment
// for the shared cross-platform contract this satisfies). A handle's
// FILE_ID_INFO is unchanged across a rename of the object it names
// (measured: R13.rename) but differs from any other object's, including one
// freshly created under the same name after a delete (R13.replace,
// REQUIRED) — which is exactly the "renamed" vs. "replaced" distinction
// reconcile() (watch.go) needs when it compares two dirIdentity values
// with ==.
func identity(dir *os.File) (dirIdentity, error) {
	var info fileIDInfo
	h := windows.Handle(dir.Fd())
	err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	if err != nil {
		return dirIdentity{}, err
	}
	return dirIdentity{vol: info.VolumeSerialNumber, id: info.FileID}, nil
}

// openWatchDir opens dir for desiredDirs' walk with the full share mode
// (R15): FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE. It replaces
// os.Open, which on Windows omits FILE_SHARE_DELETE — measured directly
// against this package's use case, P13.go_share_mode: Go's syscall.Open
// (what os.Open calls) and golang.org/x/sys/windows.Open both hard-code
// FILE_SHARE_READ|FILE_SHARE_WRITE only ($GOROOT/src/syscall/syscall_windows.go
// and x/sys/windows/syscall_windows.go's Open agree on this). Without
// FILE_SHARE_DELETE, a directory the watcher happens to have open when the
// user (or the store's own atomic rename) tries to delete or rename it fails
// with STATUS_SHARING_VIOLATION/ERROR_SHARING_VIOLATION — the watcher vetoes
// an operation that has nothing to do with watching (RW24). This is the
// Windows-only counterpart of identity_unix.go's openWatchDir, which is a
// plain os.Open because the concept does not exist on that platform.
//
// FILE_FLAG_BACKUP_SEMANTICS is required to open a directory at all (the
// same requirement internal/store's win32_windows.go documents for its own
// opens); this function does not reuse that package's ntOpenAt/openStrictAt
// because those are handle-relative, single-component, root-pinned
// primitives built for internal/store's containment proof — desiredDirs
// walks arbitrary absolute paths, including ones outside SCRATCHPAD_ROOT
// entirely (a watched project tree reached through a symlink target), so
// the root-anchored primitive does not apply here. This function is a
// plain, path-based, share-mode-correct CreateFile — the same shape as
// os.Open, with one flag fixed.
func openWatchDir(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(
		p,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS,
		0,
	)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// skipWalkError reports whether an error hit while resolving, opening,
// identifying or reading a directory during desiredDirs' walk means "this
// one entry is unreachable" rather than "the walk itself is broken" — the
// distinction P4.2 exists to draw correctly (ADR §6.11, finding F6, RW23).
// Everything this function recognizes becomes a logged skip (skipEntry in
// watch.go); everything else still propagates as a hard error, which
// reconcile/newWatcher/main.go correctly still treat as fatal. This is a
// narrow carve-out for entry-scoped conditions, not a relaxation of "watcher
// failures are fatal" (see watch.go's skipEntry and CLAUDE.md).
//
// The skip set, and why each member belongs in it:
//
//   - errors.Is(err, fs.ErrNotExist): the entry disappeared between being
//     listed and being opened. Identical to Linux's existing (and unchanged)
//     behaviour. Deliberately errors.Is, not os.IsNotExist (P3.14 red-team
//     L4): os.IsNotExist does not walk an Unwrap chain, which is exactly the
//     defect annotations.go's loadNotesRaw documents at length after it bit
//     that function on Windows — it happens to work here today only because
//     every error actually reaching this function is a bare syscall.Errno or
//     *os.PathError, both of which os.IsNotExist also handles, and stops
//     working the moment anything wrapped (a *winError, a future
//     fmt.Errorf("%w")) reaches it. errors.Is is what the very next check
//     already uses.
//   - fs.ErrPermission: an ACL the server's account cannot read. Not a
//     platform-specific finding, but grouped here because Windows's default
//     ACL surface makes it far more likely to be hit by an ordinary,
//     non-adversarial entry than Linux's mode bits are.
//   - ERROR_CANT_ACCESS_FILE (1920): what STATUS_IO_REPARSE_TAG_NOT_HANDLED
//     (0xC0000279) becomes once CreateFile has translated it to a Win32
//     code — "no filter driver on this machine understands this entry's
//     reparse tag". This is the exact error the ADR traces end to end as
//     the boot-loop trigger: observed live for APPEXECLINK
//     (M2.appexeclink, in WindowsApps), and the same failure shape for a
//     OneDrive placeholder or an unserviced ProjFS entry that M2.cloud
//     could not measure on a runner with no OneDrive installed (documented
//     below as unverified by CI).
//   - ERROR_SHARING_VIOLATION / ERROR_LOCK_VIOLATION: something else has
//     the entry locked in a way that conflicts even with a full-share-mode,
//     read-only open (rare once openWatchDir grants FILE_SHARE_*, but a
//     filter driver can still hold an exclusive lock).
//   - the Windows file-system-virtualization family (ProjFS: VFS for Git,
//     Windows Containers' WCIFS): the provider service is not running, its
//     metadata is corrupt, it is busy, or the entry's provider is unknown
//     to this machine. A watched repository can carry these even when the
//     provider is not installed at all — the ADR's third named boot-loop
//     example alongside APPEXECLINK and OneDrive.
//   - the ERROR_CLOUD_FILE_* family (OneDrive/cloud-sync placeholders,
//     RW13/RW23): listed by name, not by numeric range — the Win32 error
//     space interleaves unrelated codes (thread/process background-mode,
//     GDI handle leaks, SMB1) among the cloud-file codes, so a range check
//     would misclassify them. **NOT MEASURED** (M2.cloud): no OneDrive on a
//     GitHub Actions runner, so this branch is unverified by CI and is a
//     documented exclusion, not a tested one — see the P4 report.
func skipWalkError(err error) bool {
	if errors.Is(err, fs.ErrNotExist) {
		return true
	}
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case
		windows.ERROR_CANT_ACCESS_FILE,
		windows.ERROR_SHARING_VIOLATION,
		windows.ERROR_LOCK_VIOLATION,
		// ProjFS / Windows Containers file-system virtualization.
		windows.ERROR_FILE_SYSTEM_VIRTUALIZATION_UNAVAILABLE,
		windows.ERROR_FILE_SYSTEM_VIRTUALIZATION_METADATA_CORRUPT,
		windows.ERROR_FILE_SYSTEM_VIRTUALIZATION_BUSY,
		windows.ERROR_FILE_SYSTEM_VIRTUALIZATION_PROVIDER_UNKNOWN,
		windows.ERROR_FILE_SYSTEM_VIRTUALIZATION_INVALID_OPERATION,
		// ERROR_CLOUD_FILE_* — OneDrive/cloud-sync placeholders (RW13/RW23).
		// NOT MEASURED: no OneDrive on a GitHub Actions runner (M2.cloud).
		windows.ERROR_CLOUD_FILE_SYNC_ROOT_METADATA_CORRUPT,
		windows.ERROR_CLOUD_FILE_PROVIDER_NOT_RUNNING,
		windows.ERROR_CLOUD_FILE_METADATA_CORRUPT,
		windows.ERROR_CLOUD_FILE_METADATA_TOO_LARGE,
		windows.ERROR_CLOUD_FILE_PROPERTY_BLOB_TOO_LARGE,
		windows.ERROR_CLOUD_FILE_PROPERTY_BLOB_CHECKSUM_MISMATCH,
		windows.ERROR_CLOUD_FILE_TOO_MANY_PROPERTY_BLOBS,
		windows.ERROR_CLOUD_FILE_PROPERTY_VERSION_NOT_SUPPORTED,
		windows.ERROR_CLOUD_FILE_NOT_IN_SYNC,
		windows.ERROR_CLOUD_FILE_ALREADY_CONNECTED,
		windows.ERROR_CLOUD_FILE_NOT_SUPPORTED,
		windows.ERROR_CLOUD_FILE_INVALID_REQUEST,
		windows.ERROR_CLOUD_FILE_READ_ONLY_VOLUME,
		windows.ERROR_CLOUD_FILE_CONNECTED_PROVIDER_ONLY,
		windows.ERROR_CLOUD_FILE_VALIDATION_FAILED,
		windows.ERROR_CLOUD_FILE_AUTHENTICATION_FAILED,
		windows.ERROR_CLOUD_FILE_INSUFFICIENT_RESOURCES,
		windows.ERROR_CLOUD_FILE_NETWORK_UNAVAILABLE,
		windows.ERROR_CLOUD_FILE_UNSUCCESSFUL,
		windows.ERROR_CLOUD_FILE_NOT_UNDER_SYNC_ROOT,
		windows.ERROR_CLOUD_FILE_IN_USE,
		windows.ERROR_CLOUD_FILE_PINNED,
		windows.ERROR_CLOUD_FILE_REQUEST_ABORTED,
		windows.ERROR_CLOUD_FILE_PROPERTY_CORRUPT,
		windows.ERROR_CLOUD_FILE_ACCESS_DENIED,
		windows.ERROR_CLOUD_FILE_INCOMPATIBLE_HARDLINKS,
		windows.ERROR_CLOUD_FILE_PROPERTY_LOCK_CONFLICT,
		windows.ERROR_CLOUD_FILE_REQUEST_CANCELED,
		windows.ERROR_CLOUD_FILE_PROVIDER_TERMINATED,
		windows.ERROR_CLOUD_FILE_REQUEST_TIMEOUT:
		return true
	}
	return false
}
