//go:build windows

package winspike

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// Constants x/sys/windows v0.41.0 does not export.
// ---------------------------------------------------------------------------

const (
	// NT information classes (NtSetInformationFile), not the Win32 ones.
	fileRenameInformation        = 10
	fileRenameInformationEx      = 65
	fileDispositionInformationEx = 64

	// FILE_RENAME_INFORMATION_EX / FILE_RENAME_INFO Flags.
	fileRenameReplaceIfExists = 0x001
	fileRenamePosixSemantics  = 0x002

	// FILE_DISPOSITION_INFORMATION_EX Flags.
	fileDispositionDelete                  = 0x001
	fileDispositionPosixSemantics          = 0x002
	fileDispositionIgnoreReadonlyAttribute = 0x010

	// CreateSymbolicLinkW flags.
	symbolicLinkFlagAllowUnprivilegedCreate = 0x2

	// GetFinalPathNameByHandleW flags.
	volumeNameDOS  = 0x0
	volumeNameGUID = 0x1
	volumeNameNT   = 0x2
	volumeNameNone = 0x4

	// Reparse tags beyond the two x/sys exports. Values from the Microsoft
	// "Reparse Point Tags" reference.
	ioReparseTagMountPoint  = 0xA0000003
	ioReparseTagSymlink     = 0xA000000C
	ioReparseTagAppExecLink = 0x8000001B
	ioReparseTagDedup       = 0x80000013
	ioReparseTagAFUnix      = 0x80000023
	ioReparseTagWCILink     = 0x80000027
	ioReparseTagCloud       = 0x9000001A
	ioReparseTagProjFS      = 0x9000001C

	maxReparseSize = 16 * 1024
)

// Access masks. dirRead mirrors what internal/syscall/windows.Openat requests
// for an O_RDONLY|O_DIRECTORY open, so measurements here are comparable to
// os.Root's.
const (
	dirReadAccess = windows.FILE_GENERIC_READ | windows.FILE_LIST_DIRECTORY |
		windows.STANDARD_RIGHTS_READ | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA
	fileReadAccess = windows.FILE_GENERIC_READ |
		windows.STANDARD_RIGHTS_READ | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA
	shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
)

// noFollowAttrs is the OBJECT_ATTRIBUTES.Attributes value used for every
// traversal open in this prototype. OBJ_DONT_REPARSE is the property the whole
// design rests on: it fails the open with STATUS_REPARSE_POINT_ENCOUNTERED if
// ANY component of ObjectName is a reparse point, which is strictly stronger
// than FILE_FLAG_OPEN_REPARSE_POINT's final-component-only guarantee.
const noFollowAttrs = windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE

// ---------------------------------------------------------------------------
// Core: handle-relative open (the Openat / Mkdirat / Deleteat that
// x/sys/windows does not ship; they live in the stdlib-internal package
// internal/syscall/windows and are not importable).
// ---------------------------------------------------------------------------

// ntOpenAt opens name relative to the parent HANDLE. Nothing here re-resolves a
// path from the process namespace: OBJECT_ATTRIBUTES.RootDirectory is the
// anchor, exactly as openat(2) uses a directory descriptor.
//
// name is normally a single path component. The multi-component form is used
// only by the M1 probe, which measures whether OBJ_DONT_REPARSE covers
// intermediate components or only the final one.
func ntOpenAt(parent windows.Handle, name string, access, disposition, options, objAttrs, fileAttrs uint32) (windows.Handle, error) {
	if name == "" {
		return windows.InvalidHandle, windows.ERROR_FILE_NOT_FOUND
	}
	u16, err := windows.UTF16FromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	us := windows.NTUnicodeString{
		Length:        uint16((len(u16) - 1) * 2),
		MaximumLength: uint16(len(u16) * 2),
		Buffer:        &u16[0],
	}
	oa := windows.OBJECT_ATTRIBUTES{
		RootDirectory: parent,
		ObjectName:    &us,
		Attributes:    objAttrs,
	}
	oa.Length = uint32(unsafe.Sizeof(oa))
	var h windows.Handle
	var iosb windows.IO_STATUS_BLOCK
	st := windows.NtCreateFile(
		&h,
		access|windows.SYNCHRONIZE,
		&oa,
		&iosb,
		nil,
		fileAttrs,
		shareAll,
		disposition,
		options|windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_FOR_BACKUP_INTENT,
		0,
		0,
	)
	runtime.KeepAlive(u16)
	runtime.KeepAlive(us)
	if st != nil {
		return windows.InvalidHandle, st
	}
	return h, nil
}

// OpenDirAt is openat(parent, name, O_RDONLY|O_DIRECTORY|O_NOFOLLOW).
// FILE_DIRECTORY_FILE supplies O_DIRECTORY; OBJ_DONT_REPARSE supplies
// O_NOFOLLOW and, unlike O_NOFOLLOW, also covers intermediate components.
func OpenDirAt(parent windows.Handle, name string) (windows.Handle, error) {
	return ntOpenAt(parent, name, dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, noFollowAttrs, 0)
}

// OpenRegularFileAt is openat(parent, name, O_RDONLY|O_NOFOLLOW) plus the
// S_IFREG check, in ONE kernel operation: FILE_NON_DIRECTORY_FILE refuses a
// directory and OBJ_DONT_REPARSE refuses every reparse tag, so there is no
// open-then-fstat window as there is in storefs_linux.go's openFileAt.
func OpenRegularFileAt(parent windows.Handle, name string) (windows.Handle, error) {
	return ntOpenAt(parent, name, fileReadAccess, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
}

// MkdirAt is the create-only claim: FILE_CREATE is the O_EXCL analogue and
// returns STATUS_OBJECT_NAME_COLLISION when the name is taken.
func MkdirAt(parent windows.Handle, name string) error {
	h, err := ntOpenAt(parent, name, dirReadAccess, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

// CreateFileAt is O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW. DELETE access is included
// so the caller can rename or POSIX-delete through the same handle.
func CreateFileAt(parent windows.Handle, name string) (windows.Handle, error) {
	return ntOpenAt(parent, name, windows.FILE_GENERIC_WRITE|windows.DELETE, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
}

// OpenForDeleteAt opens a single entry relative to parent for removal.
//
// It deliberately uses FILE_OPEN_REPARSE_POINT rather than OBJ_DONT_REPARSE:
// removing a link must open the LINK, not its target. That is safe only
// because name is a single component, so "final component" and "whole path"
// coincide; the caller must never pass a multi-component name here.
//
// options selects FILE_DIRECTORY_FILE / FILE_NON_DIRECTORY_FILE / 0.
func OpenForDeleteAt(parent windows.Handle, name string, options uint32) (windows.Handle, error) {
	if strings.ContainsAny(name, `\/`) {
		return windows.InvalidHandle, fmt.Errorf("winspike: OpenForDeleteAt requires a single component, got %q", name)
	}
	return ntOpenAt(parent, name, windows.FILE_READ_ATTRIBUTES|windows.DELETE, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT|options, windows.OBJ_CASE_INSENSITIVE, 0)
}

// DeleteAt removes one entry relative to parent, never following it.
// posix selects FILE_DISPOSITION_POSIX_SEMANTICS (name leaves the namespace at
// once) over the legacy FileDispositionInfo (delete-on-last-close).
func DeleteAt(parent windows.Handle, name string, options uint32, posix bool) error {
	h, err := OpenForDeleteAt(parent, name, options)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return DeleteByHandle(h, posix)
}

// DeleteByHandle marks an already-open handle for deletion.
func DeleteByHandle(h windows.Handle, posix bool) error {
	if posix {
		var info struct{ Flags uint32 }
		info.Flags = fileDispositionDelete | fileDispositionPosixSemantics | fileDispositionIgnoreReadonlyAttribute
		var iosb windows.IO_STATUS_BLOCK
		return windows.NtSetInformationFile(h, &iosb, (*byte)(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)), fileDispositionInformationEx)
	}
	var info struct{ DeleteFile uint32 }
	info.DeleteFile = 1
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
}

// ---------------------------------------------------------------------------
// Classification from a handle (never from a path, never from fs.FileMode).
// ---------------------------------------------------------------------------

// AttrTag is FILE_ATTRIBUTE_TAG_INFO. The tag, not the reparse ATTRIBUTE and
// not Go's fs.ModeSymlink, is the authoritative classification (threat model
// R4, R5).
type AttrTag struct {
	FileAttributes uint32
	ReparseTag     uint32
}

func (a AttrTag) IsDir() bool { return a.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 }
func (a AttrTag) IsReparse() bool {
	return a.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

// IsNameSurrogate mirrors the kernel's IsReparseTagNameSurrogate macro: bit 29.
func (a AttrTag) IsNameSurrogate() bool { return a.ReparseTag&0x20000000 != 0 }

// IsMicrosoft mirrors IsReparseTagMicrosoft: bit 31.
func (a AttrTag) IsMicrosoft() bool { return a.ReparseTag&0x80000000 != 0 }

func (a AttrTag) String() string {
	return fmt.Sprintf("attrs=0x%08X tag=0x%08X(%s) dir=%v surrogate=%v microsoft=%v",
		a.FileAttributes, a.ReparseTag, TagName(a.ReparseTag), a.IsDir(), a.IsNameSurrogate(), a.IsMicrosoft())
}

// TagName names the tags this project has an opinion about.
func TagName(tag uint32) string {
	switch tag {
	case 0:
		return "none"
	case ioReparseTagMountPoint:
		return "MOUNT_POINT"
	case ioReparseTagSymlink:
		return "SYMLINK"
	case ioReparseTagAppExecLink:
		return "APPEXECLINK"
	case ioReparseTagDedup:
		return "DEDUP"
	case ioReparseTagAFUnix:
		return "AF_UNIX"
	case ioReparseTagWCILink:
		return "WCI_LINK"
	case ioReparseTagCloud:
		return "CLOUD"
	case ioReparseTagProjFS:
		return "PROJFS"
	default:
		return "UNKNOWN"
	}
}

// AttrTagOf reads FILE_ATTRIBUTE_TAG_INFO from an open handle.
func AttrTagOf(h windows.Handle) (AttrTag, error) {
	var info AttrTag
	err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return info, err
}

// StatAt is fstatat(parent, name, AT_SYMLINK_NOFOLLOW): it opens the entry
// itself (FILE_OPEN_REPARSE_POINT on a single component) and classifies it
// from that handle.
func StatAt(parent windows.Handle, name string) (AttrTag, error) {
	if strings.ContainsAny(name, `\/`) {
		return AttrTag{}, fmt.Errorf("winspike: StatAt requires a single component, got %q", name)
	}
	h, err := ntOpenAt(parent, name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return AttrTag{}, err
	}
	defer windows.CloseHandle(h)
	return AttrTagOf(h)
}

// FileIDInfo is FILE_ID_INFO: the only stable directory identity on Windows
// (the watcher's dev+ino replacement, threat model R14).
type FileIDInfo struct {
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func (f FileIDInfo) String() string {
	return fmt.Sprintf("vol=0x%016X id=%x", f.VolumeSerialNumber, f.FileID)
}

func FileIDOf(h windows.Handle) (FileIDInfo, error) {
	var info FileIDInfo
	err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return info, err
}

// ---------------------------------------------------------------------------
// Directory reading through a retained handle.
//
// This is the replacement for fdPath (/proc/self/fd/N, storefs_linux.go:41),
// which the threat model calls the single hardest porting constraint. The
// handle is duplicated so the os.File's Close does not consume the anchor.
// ---------------------------------------------------------------------------

func DupHandle(h windows.Handle) (windows.Handle, error) {
	var dup windows.Handle
	p := windows.CurrentProcess()
	err := windows.DuplicateHandle(p, h, p, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS)
	return dup, err
}

// ReadDirHandle enumerates a directory through a duplicate of an already-open
// handle. No path is re-resolved.
func ReadDirHandle(h windows.Handle) ([]os.DirEntry, error) {
	dup, err := DupHandle(h)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "winspike-dir")
	if f == nil {
		windows.CloseHandle(dup)
		return nil, fmt.Errorf("winspike: os.NewFile rejected the duplicated directory handle")
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// DirHasHTML is dirHasHTMLFD (storefs_linux.go:43) through a handle. Note the
// name test is still Go's strings.ToLower, which is NOT the volume's $UpCase
// folding — see measurement M11.
func DirHasHTML(h windows.Handle) (bool, error) {
	entries, err := ReadDirHandle(h)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
			continue
		}
		at, err := StatAt(h, e.Name())
		if err != nil {
			continue
		}
		if !at.IsDir() && !at.IsReparse() {
			return true, nil
		}
	}
	return false, nil
}

// ---------------------------------------------------------------------------
// Reparse point reading (readlinkat analogue).
// ---------------------------------------------------------------------------

type reparseHeader struct {
	ReparseTag        uint32
	ReparseDataLength uint16
	Reserved          uint16
}

// ReadLinkAt reads the substitute name of the reparse point `name` in parent,
// together with its tag. It is readlinkat(2) plus the tag, which Linux does not
// need because there is only one kind of link.
func ReadLinkAt(parent windows.Handle, name string) (tag uint32, substitute string, err error) {
	h, err := ntOpenAt(parent, name, fileReadAccess, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return 0, "", err
	}
	defer windows.CloseHandle(h)
	return ReadLinkHandle(h)
}

func ReadLinkHandle(h windows.Handle) (tag uint32, substitute string, err error) {
	buf := make([]byte, maxReparseSize)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0,
		&buf[0], uint32(len(buf)), &n, nil); err != nil {
		return 0, "", err
	}
	if n < uint32(unsafe.Sizeof(reparseHeader{})) {
		return 0, "", fmt.Errorf("winspike: short reparse buffer (%d bytes)", n)
	}
	hdr := (*reparseHeader)(unsafe.Pointer(&buf[0]))
	tag = hdr.ReparseTag
	body := buf[unsafe.Sizeof(reparseHeader{}):n]
	switch tag {
	case ioReparseTagSymlink:
		if len(body) < 12 {
			return tag, "", fmt.Errorf("winspike: short SYMLINK reparse body")
		}
		off := int(u16at(body, 0))
		length := int(u16at(body, 2))
		const pathStart = 12
		if pathStart+off+length > len(body) {
			return tag, "", fmt.Errorf("winspike: SYMLINK reparse body overruns")
		}
		substitute = decodeUTF16(body[pathStart+off : pathStart+off+length])
	case ioReparseTagMountPoint:
		if len(body) < 8 {
			return tag, "", fmt.Errorf("winspike: short MOUNT_POINT reparse body")
		}
		off := int(u16at(body, 0))
		length := int(u16at(body, 2))
		const pathStart = 8
		if pathStart+off+length > len(body) {
			return tag, "", fmt.Errorf("winspike: MOUNT_POINT reparse body overruns")
		}
		substitute = decodeUTF16(body[pathStart+off : pathStart+off+length])
	}
	return tag, substitute, nil
}

func u16at(b []byte, i int) uint16 { return uint16(b[i]) | uint16(b[i+1])<<8 }

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = u16at(b, i*2)
	}
	return windows.UTF16ToString(u)
}

// ---------------------------------------------------------------------------
// Root: the pinned store root and the two walks storefs_linux.go performs.
// ---------------------------------------------------------------------------

// Root is the analogue of rootedFS (storefs_linux.go:16). One handle, pinned
// for the lifetime of the value, with the identity recorded at pin time so
// every mutation can re-verify it (threat model R13).
type Root struct {
	h    windows.Handle
	id   FileIDInfo
	name string
}

// OpenRoot pins an absolute path as a directory, refusing a reparse point at
// the root itself (the O_DIRECTORY|O_NOFOLLOW of openRootedFS).
func OpenRoot(path string) (*Root, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, shareAll, nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	at, err := AttrTagOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if !at.IsDir() {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("winspike: root %q is not a directory (%s)", path, at)
	}
	if at.IsReparse() {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("winspike: root %q is a reparse point (%s)", path, at)
	}
	id, err := FileIDOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return &Root{h: h, id: id, name: path}, nil
}

func (r *Root) Handle() windows.Handle { return r.h }
func (r *Root) ID() FileIDInfo         { return r.id }
func (r *Root) Close() error           { return windows.CloseHandle(r.h) }

// Verify re-reads the pinned handle's identity and compares it against the one
// recorded at pin time (R13). A handle follows its object through a rename, so
// this detects replacement, not renaming.
func (r *Root) Verify() error {
	id, err := FileIDOf(r.h)
	if err != nil {
		return err
	}
	if id != r.id {
		return fmt.Errorf("winspike: the scratchpad root was replaced (pinned %s, now %s)", r.id, id)
	}
	return nil
}

// OpenRealDir mirrors rootedFS.openRealDir (storefs_linux.go:59): walk only
// real directories, one segment at a time, each relative to the previous
// HANDLE, so renaming any checked ancestor cannot redirect later work.
func (r *Root) OpenRealDir(segs []string, create, rejectArtifacts bool) (windows.Handle, error) {
	h, err := DupHandle(r.h)
	if err != nil {
		return windows.InvalidHandle, err
	}
	for i, seg := range segs {
		next, openErr := OpenDirAt(h, seg)
		if openErr != nil && create && isNotExist(openErr) {
			if mkErr := MkdirAt(h, seg); mkErr != nil && !isExist(mkErr) {
				windows.CloseHandle(h)
				return windows.InvalidHandle, mkErr
			}
			next, openErr = OpenDirAt(h, seg)
		}
		windows.CloseHandle(h)
		if openErr != nil {
			if isReparse(openErr) {
				return windows.InvalidHandle, fmt.Errorf("project ancestor %q is a link or reparse point", strings.Join(segs[:i+1], "/"))
			}
			return windows.InvalidHandle, openErr
		}
		h = next
		if rejectArtifacts {
			hasHTML, err := DirHasHTML(h)
			if err == nil && hasHTML {
				windows.CloseHandle(h)
				return windows.InvalidHandle, fmt.Errorf("%q is an artifact, not a project", strings.Join(segs[:i+1], "/"))
			}
		}
	}
	return h, nil
}

// OpenBrowsableDir mirrors rootedFS.openBrowsableDir (storefs_linux.go:169):
// exactly one link boundary is forgiven, and only for a tag on the allowlist.
// Every other segment is opened with OBJ_DONT_REPARSE.
func (r *Root) OpenBrowsableDir(segs []string, allowedTags ...uint32) (windows.Handle, error) {
	if len(allowedTags) == 0 {
		allowedTags = []uint32{ioReparseTagSymlink}
	}
	allowed := func(tag uint32) bool {
		for _, t := range allowedTags {
			if t == tag {
				return true
			}
		}
		return false
	}
	h, err := DupHandle(r.h)
	if err != nil {
		return windows.InvalidHandle, err
	}
	crossed := false
	for _, seg := range segs {
		next, openErr := OpenDirAt(h, seg)
		// Mirror storefs_linux.go:177, which forgives ELOOP *or* ENOTDIR: the
		// Linux walk cannot tell a symlink-to-file from a symlink-to-dir
		// before readlink. STATUS_NOT_A_DIRECTORY is the ENOTDIR analogue;
		// ReadLinkAt fails harmlessly for a genuine regular file.
		if (isReparse(openErr) || isNotADir(openErr)) && !crossed {
			hasHTML, htmlErr := DirHasHTML(h)
			if htmlErr == nil && !hasHTML {
				tag, target, readErr := ReadLinkAt(h, seg)
				if readErr != nil {
					windows.CloseHandle(h)
					return windows.InvalidHandle, readErr
				}
				if !allowed(tag) {
					windows.CloseHandle(h)
					return windows.InvalidHandle, fmt.Errorf("%q is a %s reparse point (0x%08X), which is not an allowed watch boundary", seg, TagName(tag), tag)
				}
				next, openErr = openAbsoluteDirNoFollow(stripNTPrefix(target))
				if openErr == nil {
					crossed = true
				}
			}
		}
		windows.CloseHandle(h)
		if openErr != nil {
			return windows.InvalidHandle, openErr
		}
		h = next
	}
	return h, nil
}

// openAbsoluteDirNoFollow is the analogue of the absolute reopen at
// storefs_linux.go:184: open the watch target itself, refusing a reparse point
// at that final component so a link-to-a-link cannot chain.
func openAbsoluteDirNoFollow(path string) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, shareAll, nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, err
	}
	at, err := AttrTagOf(h)
	if err != nil || !at.IsDir() || at.IsReparse() {
		windows.CloseHandle(h)
		if err == nil {
			err = fmt.Errorf("winspike: watch target %q is not a plain directory (%s)", path, at)
		}
		return windows.InvalidHandle, err
	}
	return h, nil
}

// stripNTPrefix turns the \??\ substitute-name form stored in a reparse point
// into a Win32 path.
func stripNTPrefix(s string) string {
	if strings.HasPrefix(s, `\??\UNC\`) {
		return `\\` + s[len(`\??\UNC\`):]
	}
	return strings.TrimPrefix(s, `\??\`)
}

// ---------------------------------------------------------------------------
// Error helpers. NtCreateFile returns an NTStatus; callers want both the raw
// status (for the log) and an errors.Is-able Errno (for policy).
// ---------------------------------------------------------------------------

func StatusOf(err error) (windows.NTStatus, bool) {
	st, ok := err.(windows.NTStatus)
	return st, ok
}

// ErrnoOf maps an NtCreateFile failure the way
// internal/syscall/windows.ntCreateFileError does, so measurements here are
// directly comparable with what os.Root would have reported.
func ErrnoOf(err error) syscall.Errno {
	st, ok := StatusOf(err)
	if !ok {
		if e, ok := err.(syscall.Errno); ok {
			return e
		}
		return 0
	}
	switch st {
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED:
		return syscall.ELOOP
	case windows.STATUS_NOT_A_DIRECTORY:
		return syscall.ENOTDIR
	case windows.STATUS_FILE_IS_A_DIRECTORY:
		return syscall.EISDIR
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return syscall.EEXIST
	}
	return st.Errno()
}

// DescribeErr renders an error in the stable form the CI log is grepped for.
func DescribeErr(err error) string {
	if err == nil {
		return "nil"
	}
	if st, ok := StatusOf(err); ok {
		return fmt.Sprintf("NTSTATUS=0x%08X(%s) errno=%d(%v) goerr=%v",
			uint32(st), st.Error(), uint32(ErrnoOf(err)), ErrnoOf(err), err)
	}
	if e, ok := err.(syscall.Errno); ok {
		return fmt.Sprintf("WIN32=%d(0x%X) %v", uint32(e), uint32(e), err)
	}
	return fmt.Sprintf("%T %v", err, err)
}

func isReparse(err error) bool {
	if err == nil {
		return false
	}
	st, ok := StatusOf(err)
	if !ok {
		return false
	}
	return st == windows.STATUS_REPARSE_POINT_ENCOUNTERED ||
		st == windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED
}

func isNotADir(err error) bool {
	st, ok := StatusOf(err)
	return ok && st == windows.STATUS_NOT_A_DIRECTORY
}

func isNotExist(err error) bool {
	st, ok := StatusOf(err)
	if !ok {
		return false
	}
	return st == windows.STATUS_OBJECT_NAME_NOT_FOUND || st == windows.STATUS_OBJECT_PATH_NOT_FOUND
}

func isExist(err error) bool {
	st, ok := StatusOf(err)
	if !ok {
		return false
	}
	return st == windows.STATUS_OBJECT_NAME_COLLISION
}

// ---------------------------------------------------------------------------
// Handle-relative rename: the Renameat analogue (annotationfs_linux.go:135
// uses unix.Renameat with the SAME parent descriptor on both sides).
//
// The layout below is FILE_RENAME_INFO / FILE_RENAME_INFORMATION_EX:
//
//	union { BOOLEAN ReplaceIfExists; DWORD Flags; }  offset 0
//	HANDLE RootDirectory                             offset 8   (64-bit)
//	DWORD  FileNameLength   (BYTES, excluding NUL)   offset 16
//	WCHAR  FileName[]                                offset 20
//
// The buffer is built by hand rather than as a Go struct so the offsets are
// visible and auditable, and so no 64 KiB struct is allocated per rename.
// ---------------------------------------------------------------------------

// Win32 information classes (SetFileInformationByHandle).
const (
	win32FileRenameInfo      = 3  // windows.FileRenameInfo
	win32FileRenameInfoEx    = 22 // windows.FileRenameInfoEx
	win32FileDispositionInfo = 4
	win32FileDispositionInfoEx = 21
)

func renameBuffer(destParent windows.Handle, newName string, flags uint32) ([]byte, error) {
	if unsafe.Sizeof(uintptr(0)) != 8 {
		return nil, fmt.Errorf("winspike: this layout assumes a 64-bit HANDLE")
	}
	u16, err := windows.UTF16FromString(newName)
	if err != nil {
		return nil, err
	}
	nameBytes := (len(u16) - 1) * 2
	buf := make([]byte, 20+len(u16)*2)
	put32(buf, 0, flags)
	put64(buf, 8, uint64(destParent))
	put32(buf, 16, uint32(nameBytes))
	for i, c := range u16 {
		put16(buf, 20+i*2, c)
	}
	return buf, nil
}

func put64(b []byte, i int, v uint64) {
	for k := 0; k < 8; k++ {
		b[i+k] = byte(v >> (8 * k))
	}
}

// RenameAtWin32 renames the object `src` names to `newName` under
// `destParent`, through the Win32 SetFileInformationByHandle wrapper.
//
// M9 asks whether that wrapper honours FILE_RENAME_INFO.RootDirectory for a
// RELATIVE FileName, or whether only NtSetInformationFile does. If it does
// not, every atomic annotation replace has to go through the NT call.
func RenameAtWin32(src, destParent windows.Handle, newName string, class, flags uint32) error {
	buf, err := renameBuffer(destParent, newName, flags)
	if err != nil {
		return err
	}
	return windows.SetFileInformationByHandle(src, class, &buf[0], uint32(len(buf)))
}

// RenameAtNT is the same operation through NtSetInformationFile, which is what
// $GOROOT/src/internal/syscall/windows/at_windows.go Renameat uses.
func RenameAtNT(src, destParent windows.Handle, newName string, class, flags uint32) error {
	buf, err := renameBuffer(destParent, newName, flags)
	if err != nil {
		return err
	}
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(src, &iosb, &buf[0], uint32(len(buf)), class)
}

// DeleteByHandleWin32 is the Win32-wrapper form of the POSIX-semantics delete,
// so M10 can compare it against the NT form used by DeleteByHandle.
func DeleteByHandleWin32(h windows.Handle, posix bool) error {
	if posix {
		var flags uint32 = fileDispositionDelete | fileDispositionPosixSemantics | fileDispositionIgnoreReadonlyAttribute
		return windows.SetFileInformationByHandle(h, win32FileDispositionInfoEx,
			(*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)))
	}
	var del uint32 = 1
	return windows.SetFileInformationByHandle(h, win32FileDispositionInfo,
		(*byte)(unsafe.Pointer(&del)), uint32(unsafe.Sizeof(del)))
}

// unsafePointer keeps the single unsafe conversion privilege.go needs in one
// audited place.
func unsafePointer(b *byte) unsafe.Pointer { return unsafe.Pointer(b) }
