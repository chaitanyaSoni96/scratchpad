//go:build windows

package testutil

import (
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// WatchLinkTestEnv overrides watch-link capability detection when set: "0"
// reports incapable, "1" reports capable. Any other value (or unset) falls
// back to probing the OS once per process.
//
// This is the ADR's §8.5 "re-express SymlinkCapable as WatchLinkCapable"
// item, added ADDITIVELY: SymlinkCapable/RequireSymlinks/SymlinkTestEnv above
// are NOT renamed or removed, because internal/web and internal/watch call
// them directly and migrating those call sites is out of this task's scope
// (both packages are off limits here). WatchLinkCapable is the more accurate
// primitive for any NEW Windows test that cares whether `scratchpad watch`
// itself would succeed, since the store accepts a junction as a fallback
// when symlink creation is unavailable (ADR §6.6's measured privilege
// table) — a plain symlink probe alone under-reports capability on a
// Developer-Mode-off, otherwise-ordinary account, where junctions still
// work. A full migration of every existing RequireSymlinks call site, plus
// the SCRATCHPAD_TEST_WATCH_LINKS CI wiring the ADR also asks for, is a
// deliberately separate, larger change (it touches internal/web,
// internal/watch and .github/, none of which this task may edit) and is not
// attempted here.
const WatchLinkTestEnv = "SCRATCHPAD_TEST_WATCH_LINKS"

var (
	watchLinkOnce    sync.Once
	watchLinkCapable bool
)

// RequireWatchLinks skips t when this process can create neither a directory
// symlink nor a junction — i.e. when `scratchpad watch` itself would fail
// with errNoLinkPrivilege. Prefer this over RequireSymlinks for a NEW test
// whose subject is watch-link creation specifically, rather than a symlink
// as such.
func RequireWatchLinks(t testing.TB) {
	t.Helper()
	if WatchLinkCapable() {
		return
	}
	t.Skipf("SKIP(symlink-capability): this process can create neither a directory symlink nor a junction; " +
		"enable Windows Developer Mode (Settings > System > For developers) or grant SeCreateSymbolicLinkPrivilege, " +
		"or set " + WatchLinkTestEnv + "=1 to override detection")
}

// WatchLinkCapable reports whether this process can create at least one of
// the two link flavours the store's Watch accepts (a directory symlink or a
// junction). Non-Windows systems are always considered capable (there is
// only one link type, and SymlinkCapable already covers it). The
// SCRATCHPAD_TEST_WATCH_LINKS environment variable wins when set to "0" or
// "1"; otherwise the answer comes from a single cached probe.
func WatchLinkCapable() bool {
	switch os.Getenv(WatchLinkTestEnv) {
	case "0":
		return false
	case "1":
		return true
	}
	watchLinkOnce.Do(func() { watchLinkCapable = probeSymlink() || probeJunction() })
	return watchLinkCapable
}

// probeJunction attempts to create a junction (MOUNT_POINT reparse point) in
// a fresh temporary directory, independent of internal/store: this package
// is imported BY internal/store's tests, so it must not import internal/store
// itself (that would not be a cycle in the Go sense — a test-only import can
// point at the production package — but it would be a real, needless
// coupling for a package whose whole job is staying dependency-free). The
// reparse-buffer construction below is the minimum needed to plant a
// MOUNT_POINT and is not a general-purpose junction API: it exists only to
// answer "can this process make one at all".
func probeJunction() bool {
	dir, err := os.MkdirTemp("", "scratchpad-watchlink-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		return false
	}
	link := filepath.Join(dir, "link")
	if err := os.Mkdir(link, 0o755); err != nil {
		return false
	}
	p, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return false
	}
	h, err := windows.CreateFile(p, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)

	sub := `\??\` + target
	subU, err := syscall.UTF16FromString(sub)
	if err != nil {
		return false
	}
	printU, err := syscall.UTF16FromString(target)
	if err != nil {
		return false
	}
	subBytes := (len(subU) - 1) * 2
	printBytes := (len(printU) - 1) * 2
	const fixed = 8
	pathBytes := subBytes + 2 + printBytes + 2
	buf := make([]byte, 8+fixed+pathBytes)
	tag := uint32(windows.IO_REPARSE_TAG_MOUNT_POINT)
	buf[0], buf[1], buf[2], buf[3] = byte(tag), byte(tag>>8), byte(tag>>16), byte(tag>>24)
	dataLen := uint16(fixed + pathBytes)
	buf[4], buf[5] = byte(dataLen), byte(dataLen>>8)
	// Reserved buf[6], buf[7] already zero.
	// MountPointReparseBuffer: SubstituteNameOffset/Length, PrintNameOffset/Length
	buf[8], buf[9] = 0, 0 // SubstituteNameOffset
	buf[10], buf[11] = byte(subBytes), byte(subBytes>>8)
	buf[12], buf[13] = byte(subBytes+2), byte((subBytes+2)>>8) // PrintNameOffset
	buf[14], buf[15] = byte(printBytes), byte(printBytes>>8)
	off := 16
	for i, u := range subU[:len(subU)-1] {
		buf[off+i*2], buf[off+i*2+1] = byte(u), byte(u>>8)
	}
	off += subBytes + 2
	for i, u := range printU[:len(printU)-1] {
		buf[off+i*2], buf[off+i*2+1] = byte(u), byte(u>>8)
	}

	var n uint32
	err = windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil)
	return err == nil
}
