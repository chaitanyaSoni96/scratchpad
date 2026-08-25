//go:build windows

package winspike

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// Link creation, in the three flavours P1.4 has to choose between.
// ---------------------------------------------------------------------------

// CreateDirSymlink is CreateSymbolicLinkW(link, target, SYMBOLIC_LINK_FLAG_DIRECTORY).
// unprivileged adds SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE, which needs
// Developer Mode; without it an unprivileged caller gets ERROR_PRIVILEGE_NOT_HELD.
func CreateDirSymlink(link, target string, unprivileged bool) error {
	l, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	flags := uint32(windows.SYMBOLIC_LINK_FLAG_DIRECTORY)
	if unprivileged {
		flags |= symbolicLinkFlagAllowUnprivilegedCreate
	}
	return windows.CreateSymbolicLink(l, t, flags)
}

// CreateFileSymlink is the same call without SYMBOLIC_LINK_FLAG_DIRECTORY.
func CreateFileSymlink(link, target string, unprivileged bool) error {
	l, err := windows.UTF16PtrFromString(link)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	var flags uint32
	if unprivileged {
		flags |= symbolicLinkFlagAllowUnprivilegedCreate
	}
	return windows.CreateSymbolicLink(l, t, flags)
}

// CreateJunctionAt creates a MOUNT_POINT reparse point named `name` under the
// pinned parent handle, pointing at the absolute `target`.
//
// It is NOT atomic: the directory is created first (create-only, so the name
// claim itself races correctly) and the reparse tag is applied afterwards.
// The window between the two steps is exactly what the win32 primitive survey
// (Finding 2) asked the spike to characterise, so the caller can observe the
// intermediate state.
func CreateJunctionAt(parent windows.Handle, name, target string) error {
	if err := MkdirAt(parent, name); err != nil {
		return fmt.Errorf("junction: claim %q: %w", name, err)
	}
	h, err := ntOpenAt(parent, name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return fmt.Errorf("junction: reopen %q: %w", name, err)
	}
	defer windows.CloseHandle(h)
	return SetMountPoint(h, target)
}

// SetMountPoint writes a MOUNT_POINT reparse buffer onto an open, empty
// directory handle.
func SetMountPoint(h windows.Handle, target string) error {
	sub := `\??\` + target
	subU := windows.StringToUTF16(sub)      // includes NUL
	printU := windows.StringToUTF16(target) // includes NUL

	subBytes := (len(subU) - 1) * 2
	printBytes := (len(printU) - 1) * 2
	// MountPointReparseBuffer: 4 uint16 fields then PathBuffer.
	const fixed = 8
	pathBytes := subBytes + 2 + printBytes + 2

	buf := make([]byte, 8+fixed+pathBytes)
	put32(buf, 0, ioReparseTagMountPoint)
	put16(buf, 4, uint16(fixed+pathBytes)) // ReparseDataLength
	put16(buf, 6, 0)                       // Reserved
	put16(buf, 8, 0)                       // SubstituteNameOffset
	put16(buf, 10, uint16(subBytes))       // SubstituteNameLength
	put16(buf, 12, uint16(subBytes+2))     // PrintNameOffset
	put16(buf, 14, uint16(printBytes))     // PrintNameLength
	off := 16
	for _, c := range subU {
		put16(buf, off, c)
		off += 2
	}
	for _, c := range printU {
		put16(buf, off, c)
		off += 2
	}
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		&buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

// SetUnknownTag attempts to write a NON-Microsoft reparse tag using the
// REPARSE_GUID_DATA_BUFFER form. Microsoft documents this as requiring
// SE_CREATE_SYMBOLIC_LINK-class privilege / a filter driver; measurement M4
// exists to find out whether an unprivileged process can do it, because the
// "unknown tag" cell of the security test matrix depends on it.
func SetUnknownTag(h windows.Handle, tag uint32) error {
	// REPARSE_GUID_DATA_BUFFER: tag(4) len(2) reserved(2) guid(16) data...
	const dataLen = 8
	buf := make([]byte, 24+dataLen)
	put32(buf, 0, tag)
	put16(buf, 4, dataLen)
	put16(buf, 6, 0)
	// An arbitrary, fixed GUID: {5C0E9B4A-7F1D-4D2E-9A3B-0F6C8E2D1A77}
	guid := [16]byte{0x4a, 0x9b, 0x0e, 0x5c, 0x1d, 0x7f, 0x2e, 0x4d,
		0x9a, 0x3b, 0x0f, 0x6c, 0x8e, 0x2d, 0x1a, 0x77}
	copy(buf[8:24], guid[:])
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		&buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

func put16(b []byte, i int, v uint16) {
	b[i] = byte(v)
	b[i+1] = byte(v >> 8)
}

func put32(b []byte, i int, v uint32) {
	b[i] = byte(v)
	b[i+1] = byte(v >> 8)
	b[i+2] = byte(v >> 16)
	b[i+3] = byte(v >> 24)
}

// ---------------------------------------------------------------------------
// Handle-relative symlink creation: the Symlinkat that x/sys/windows lacks.
//
// The stdlib has internal/syscall/windows.Symlinkat, which creates the entry
// with NtCreateFile(FILE_CREATE) and then applies FSCTL_SET_REPARSE_POINT. We
// reimplement it here so P1.4 can measure whether the two-step sequence keeps
// Watch's create-only atomicity (store.go:637's unix.Symlinkat -> EEXIST).
// ---------------------------------------------------------------------------

// SymlinkAt creates a SYMLINK reparse point named `name` under `parent`,
// pointing at the absolute directory `target`.
func SymlinkAt(parent windows.Handle, name, target string) error {
	if err := MkdirAt(parent, name); err != nil {
		return err
	}
	h, err := ntOpenAt(parent, name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	if err := setSymlinkReparse(h, target); err != nil {
		// Leave no half-built entry behind.
		_ = DeleteAt(parent, name, windows.FILE_DIRECTORY_FILE, true)
		return err
	}
	return nil
}

func setSymlinkReparse(h windows.Handle, target string) error {
	sub := `\??\` + target
	subU := windows.StringToUTF16(sub)
	printU := windows.StringToUTF16(target)
	subBytes := (len(subU) - 1) * 2
	printBytes := (len(printU) - 1) * 2
	const fixed = 12 // 4 uint16 + Flags uint32
	pathBytes := subBytes + 2 + printBytes + 2

	buf := make([]byte, 8+fixed+pathBytes)
	put32(buf, 0, ioReparseTagSymlink)
	put16(buf, 4, uint16(fixed+pathBytes))
	put16(buf, 6, 0)
	put16(buf, 8, 0)
	put16(buf, 10, uint16(subBytes))
	put16(buf, 12, uint16(subBytes+2))
	put16(buf, 14, uint16(printBytes))
	put32(buf, 16, 0) // Flags: 0 = absolute target
	off := 20
	for _, c := range subU {
		put16(buf, off, c)
		off += 2
	}
	for _, c := range printU {
		put16(buf, off, c)
		off += 2
	}
	var n uint32
	return windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		&buf[0], uint32(len(buf)), nil, 0, &n, nil)
}

// ---------------------------------------------------------------------------
// Name resolution helpers used by the measurements.
// ---------------------------------------------------------------------------

// FinalPath is GetFinalPathNameByHandleW. It is a DISPLAY primitive: the
// string it returns has to be re-resolved by the OS, which is why the threat
// model refuses it as an input to any subsequent operation.
func FinalPath(h windows.Handle, flags uint32) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags)
	if err != nil {
		return "", err
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n+1)
		if _, err = windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), flags); err != nil {
			return "", err
		}
	}
	return windows.UTF16ToString(buf), nil
}

// NameInfo is GetFileInformationByHandleEx(FileNameInfo): the volume-relative
// path of the object the handle names, re-read from the handle.
func NameInfo(h windows.Handle) (string, error) {
	buf := make([]byte, 4+2*windows.MAX_LONG_PATH)
	if err := windows.GetFileInformationByHandleEx(h, windows.FileNameInfo,
		&buf[0], uint32(len(buf))); err != nil {
		return "", err
	}
	n := int(uint32(buf[0]) | uint32(buf[1])<<8 | uint32(buf[2])<<16 | uint32(buf[3])<<24)
	if 4+n > len(buf) {
		return "", fmt.Errorf("winspike: FILE_NAME_INFO length %d exceeds buffer", n)
	}
	return decodeUTF16(buf[4 : 4+n]), nil
}

// VolumeInfo reports the filesystem name and serial of the volume a handle
// lives on, which is how the store would detect a non-NTFS root (R18).
func VolumeInfo(h windows.Handle) (fsName string, serial uint32, flags uint32, err error) {
	name := make([]uint16, 64)
	fsn := make([]uint16, 64)
	err = windows.GetVolumeInformationByHandle(h, &name[0], uint32(len(name)),
		&serial, nil, &flags, &fsn[0], uint32(len(fsn)))
	if err != nil {
		return "", 0, 0, err
	}
	return windows.UTF16ToString(fsn), serial, flags, nil
}

var _ = unsafe.Sizeof(0)
