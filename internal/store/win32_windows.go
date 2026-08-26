//go:build windows

// win32_windows.go is the ONE file where this package uses unsafe and hand-
// rolls Win32/NT structures x/sys/windows v0.41.0 does not export (ADR §3.6,
// M17.gaps). Every byte offset below is commented and, where it matters for
// correctness, guarded by a runtime check rather than trusted as a comment
// (P3.1's F11 checklist: the prototype had three unchecked uint16
// truncations, all fixed here as returned errors instead of silent overflow).
// The hand-rolled structs (attrTagInfo, fileIDInfoRaw, reparseHeader, and
// internal/watch/identity_windows.go's fileIDInfo) each embed structs.
// HostLayout (P3.14 red-team L3): the offsets have always matched the
// Windows headers on amd64/arm64 under Go's current layout rules, but that
// marker is what Go 1.23+ added to make host-native layout a promise the
// compiler/vet can hold this package to, rather than a convention a future
// field reorder could silently violate.
//
// This file is the mechanism layer only: ntOpenAt, attribute/identity reads,
// reparse buffer encode/decode, and the rename buffer builder. Policy
// (containment decisions, what counts as an artifact, error strings users
// see) lives in storefs_windows.go and link_windows.go, which are the ports
// of internal/winspike/winfs.go and links.go into this package's shape
// (ADR §3.2/§3.3).
//
// WHERE THE PROTOTYPE WENT. internal/winspike was the Phase 1 measurement
// instrument: no local Windows machine existed, so a GitHub runner was the
// only way to learn what Win32 actually does. This file and its siblings were
// ported from it, and their comments still cite it by path — read those as
// provenance, not as a place you can go. The package and its workflow were
// deleted once both of ADR §11.2's gates were met, and its evidence survives
// in three places, none of which is the code:
//
//   - The five measurements that could NOT become tests, because they are
//     facts about Windows and Go rather than properties of this store, are
//     quoted verbatim in ADR §11.2 with their run and job ids — including
//     the one that matters most for THIS file: OBJ_DONT_REPARSE is inert for
//     a non-Microsoft reparse tag, which is why noFollowAttrs is necessary
//     and not sufficient and why openStrictAt reads the tag from the handle.
//   - Everything that could become a test did, against the shipped code
//     rather than the prototype; P6.2 §11.3/§11.4 map each property to the
//     test that now carries it, and internal/store/spikemigration*_test.go
//     is where the last six landed.
//   - The measurement logs themselves remain at the run ids §11.2 cites.
//
// So the old instruction "where this file disagrees with the prototype, the
// prototype wins" no longer has a referent. Its successor: where this file
// disagrees with ADR §11.2's quoted measurements, the measurements win — and
// a disagreement that §11.2 does not cover needs a new measurement, not a
// judgement call.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"structs"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Compile-time assertion: the FILE_RENAME_INFO layout below assumes a
// 64-bit HANDLE (offset 8 is the 8-byte RootDirectory field). 32-bit Windows
// is not a target (spike §7); this array-type mismatch fails the build
// rather than silently miscomputing offsets if that ever changes (Go
// requires an exact array-length match, so this only compiles when
// unsafe.Sizeof(uintptr(0)) == 8).
var _ [unsafe.Sizeof(uintptr(0))]byte = [8]byte{}

// ---------------------------------------------------------------------------
// Constants x/sys/windows does not export (ADR §3.6).
// ---------------------------------------------------------------------------

const (
	// NT information classes for NtSetInformationFile — distinct from the
	// Win32 SetFileInformationByHandle classes of the same number.
	fileRenameInformation        = 10
	fileRenameInformationEx      = 65
	fileDispositionInformationEx = 64

	// FILE_RENAME_INFORMATION_EX / FILE_RENAME_INFO .Flags (class 65) /
	// .ReplaceIfExists (class 10, BOOLEAN in the same bit position).
	fileRenameReplaceIfExists = 0x001
	fileRenamePosixSemantics  = 0x002

	// FILE_DISPOSITION_INFORMATION_EX.Flags.
	fileDispositionDelete                  = 0x001
	fileDispositionPosixSemantics          = 0x002
	fileDispositionIgnoreReadonlyAttribute = 0x010

	// CreateSymbolicLinkW flags.
	symbolicLinkFlagAllowUnprivilegedCreate = 0x2

	// GetFinalPathNameByHandleW flags.
	volumeNameDOS = 0x0

	// Reparse tags beyond the two x/sys exports (windows.IO_REPARSE_TAG_*).
	ioReparseTagAppExecLink = 0x8000001B

	maxReparseSize = 16 * 1024

	// Win32 SetFileInformationByHandle classes (distinct numbering from the
	// NT classes above, per M9.win32_control_nullroot's finding that these
	// wrappers cannot express a non-NULL RootDirectory).
	win32FileRenameInfo        = 3
	win32FileDispositionInfo   = 4
	win32FileDispositionInfoEx = 21
	win32FileRenameInfoEx      = 22
)

// Access masks. dirReadAccess mirrors what an O_RDONLY|O_DIRECTORY open
// requests on Linux, so behaviour is comparable across platforms.
const (
	dirReadAccess = windows.FILE_GENERIC_READ | windows.FILE_LIST_DIRECTORY |
		windows.STANDARD_RIGHTS_READ | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA
	fileReadAccess = windows.FILE_GENERIC_READ |
		windows.STANDARD_RIGHTS_READ | windows.FILE_READ_ATTRIBUTES | windows.FILE_READ_EA
	// shareAll is mandatory on every handle this package opens (R15): without
	// FILE_SHARE_DELETE the user cannot rename/delete the store root from
	// Explorer while the server runs, and our own pinned-handle renames stop
	// working (M7).
	shareAll = windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE
)

// noFollowAttrs is OBJECT_ATTRIBUTES.Attributes for every traversal open.
// OBJ_DONT_REPARSE is necessary and NOT sufficient (ADR §2.1) — it is kept
// because it is free and still short-circuits a Microsoft-tagged
// INTERMEDIATE component before a handle even exists (M1.intermediate); the
// containment primitive is the strict open (openStrictAt below), which reads
// the tag from the handle regardless of what OBJ_DONT_REPARSE did.
//
// The strict-open discipline covers CREATE dispositions too, not only
// FILE_OPEN: every FILE_CREATE/FILE_OPEN_IF in this package (mkdirClaim,
// writeFileAt, atomicWriteFileAt's temp, the two lock-file opens) passes
// windows.FILE_OPEN_REPARSE_POINT in CreateOptions alongside noFollowAttrs,
// for the same reason A5.obj_dont_reparse_inert_for_unknown_tags forced it
// onto openStrictAt: OBJ_DONT_REPARSE does nothing for a non-Microsoft tag on
// a machine with a filter driver servicing it (WCIFS, ProjFS, a vendor
// filter), so without the flag a claim or an open-or-create could be
// serviced and land on the driver's target instead of colliding/failing at
// this name. There is no open-then-classify step for a create the way
// openStrictAt has one for FILE_OPEN — FILE_OPEN_REPARSE_POINT is the whole
// fix here: with it, an existing reparse point at the name collides
// (STATUS_OBJECT_NAME_COLLISION for FILE_CREATE) or is opened as itself,
// never traversed through.
const noFollowAttrs = windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE

// ---------------------------------------------------------------------------
// ntOpenAt: the handle-relative open every other primitive is built on.
// ---------------------------------------------------------------------------

// ntOpenAt opens name relative to the parent HANDLE via
// OBJECT_ATTRIBUTES.RootDirectory — the NT analogue of openat(2). name must
// be exactly one path component; this is a runtime check, not a comment
// (ADR §3.2), because every containment argument in this package depends on
// no walk ever handing the kernel more than one component of attacker-
// reachable structure at a time.
func ntOpenAt(parent windows.Handle, name string, access, disposition, options, objAttrs, fileAttrs uint32) (windows.Handle, error) {
	// L1 (P3.14 red-team): "." and ".." are each syntactically ONE path
	// component, so the ContainsAny check alone lets them through — and ".."
	// resolved against an OBJECT_ATTRIBUTES.RootDirectory anchor walks UP,
	// out of containment, the one direction this whole design exists to
	// prevent. Not currently reachable (validateName/validateSegment both
	// reject "." and ".." before any caller gets here, and os.DirEntry.Name()
	// never yields either), so this is defence in depth in the primitive that
	// is supposed to be the last line — matching Go's own Deleteat, which
	// special-cases name == "." for the same reason.
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `\/`) {
		return windows.InvalidHandle, fmt.Errorf("scratchpad: ntOpenAt requires a single path component, got %q", name)
	}
	u16, err := windows.UTF16FromString(name)
	if err != nil {
		return windows.InvalidHandle, err
	}
	nameBytes := (len(u16) - 1) * 2
	// L2 (P3.14 red-team): bounded at 0xFFFD, not 0xFFFF. MaximumLength
	// carries the NUL terminator (nameBytes+2), so nameBytes==0xFFFE would
	// pass an ">0xFFFF" bound and then uint16(nameBytes+2) truncates to 0 —
	// a NTUnicodeString claiming 65534 bytes of content in a 0-byte buffer.
	// Unreachable in practice (names are <=100/255 chars), but this bound is
	// explicitly defensive, so it is sized to make MaximumLength's own
	// truncation impossible rather than merely Length's (the F11 fix below).
	if nameBytes < 0 || nameBytes > 0xFFFD {
		// F11: the prototype computed NTUnicodeString.Length as an
		// unchecked uint16 truncation of this value. A name this long
		// cannot be produced by validateName/validateSegment, but ntOpenAt
		// is the shared primitive underneath every one of them, so it gets
		// its own defensive bound rather than trusting callers.
		return windows.InvalidHandle, fmt.Errorf("scratchpad: name %q is too long for an NT_UNICODE_STRING", name)
	}
	us := windows.NTUnicodeString{
		Length:        uint16(nameBytes),
		MaximumLength: uint16(nameBytes + 2), // +2 for the NUL terminator; cannot overflow uint16 given the bound above
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

// ---------------------------------------------------------------------------
// Classification from a handle.
// ---------------------------------------------------------------------------

// attrTagInfo is FILE_ATTRIBUTE_TAG_INFO (not exported by x/sys/windows).
// The tag — never fs.FileMode, never FILE_ATTRIBUTE_REPARSE_POINT alone —
// is the authoritative classification (R4/R5, ADR §2.1/§5).
type attrTagInfo struct {
	_              structs.HostLayout // L3 (P3.14 red-team): promise host-native layout, not just today's convention
	FileAttributes uint32
	ReparseTag     uint32
}

func (a attrTagInfo) isDir() bool { return a.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 }
func (a attrTagInfo) isReparse() bool {
	return a.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}

func attrTagOf(h windows.Handle) (attrTagInfo, error) {
	var info attrTagInfo
	err := windows.GetFileInformationByHandleEx(h, windows.FileAttributeTagInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return info, err
}

// fileIDInfoRaw is FILE_ID_INFO (not exported by x/sys/windows): the only
// stable directory identity on Windows (R13/R14). ByHandleFileInformation's
// 64-bit file index is insufficient on ReFS (survey Finding 6), which is why
// this — not windows.ByHandleFileInformation.FileIndex{High,Low} — is what
// objectID is built from.
type fileIDInfoRaw struct {
	_                  structs.HostLayout // L3 (P3.14 red-team): promise host-native layout, not just today's convention
	VolumeSerialNumber uint64
	FileID             [16]byte
}

func fileIDOf(h windows.Handle) (fileIDInfoRaw, error) {
	var info fileIDInfoRaw
	err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo,
		(*byte)(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	return info, err
}

// dupHandle duplicates h into a handle this process owns outright, so a
// caller (readDirFD) can hand it to os.NewFile without the resulting
// *os.File's Close consuming the original anchor (M16).
func dupHandle(h windows.Handle) (windows.Handle, error) {
	var dup windows.Handle
	p := windows.CurrentProcess()
	err := windows.DuplicateHandle(p, h, p, &dup, 0, false, windows.DUPLICATE_SAME_ACCESS)
	return dup, err
}

// ---------------------------------------------------------------------------
// Reparse point read/write.
// ---------------------------------------------------------------------------

type reparseHeader struct {
	_                 structs.HostLayout // L3 (P3.14 red-team): promise host-native layout, not just today's convention
	ReparseTag        uint32
	ReparseDataLength uint16
	Reserved          uint16
}

// readLinkHandle reads the substitute name and, for SYMLINK, the Flags word
// (offset 8 of the body) an earlier prototype pass never read — the gap that
// let a SYMLINK_FLAG_RELATIVE link's relative substitute name pass through
// unvalidated (ADR §3.3). tag/substitute/relative are meaningful only when
// err == nil.
func readLinkHandle(h windows.Handle) (tag uint32, substitute string, relative bool, err error) {
	buf := make([]byte, maxReparseSize)
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_GET_REPARSE_POINT, nil, 0,
		&buf[0], uint32(len(buf)), &n, nil); err != nil {
		return 0, "", false, err
	}
	if n < uint32(unsafe.Sizeof(reparseHeader{})) {
		return 0, "", false, fmt.Errorf("scratchpad: short reparse buffer (%d bytes)", n)
	}
	hdr := (*reparseHeader)(unsafe.Pointer(&buf[0]))
	tag = hdr.ReparseTag
	body := buf[unsafe.Sizeof(reparseHeader{}):n]
	switch tag {
	case windows.IO_REPARSE_TAG_SYMLINK:
		if len(body) < 12 {
			return tag, "", false, fmt.Errorf("scratchpad: short SYMLINK reparse body")
		}
		off, length := int(u16at(body, 0)), int(u16at(body, 2))
		flags := u32at(body, 8)
		const pathStart = 12
		if off < 0 || length < 0 || pathStart+off+length > len(body) {
			return tag, "", false, fmt.Errorf("scratchpad: SYMLINK reparse body overruns")
		}
		substitute = decodeUTF16(body[pathStart+off : pathStart+off+length])
		relative = flags&0x1 != 0 // SYMLINK_FLAG_RELATIVE
	case windows.IO_REPARSE_TAG_MOUNT_POINT:
		if len(body) < 8 {
			return tag, "", false, fmt.Errorf("scratchpad: short MOUNT_POINT reparse body")
		}
		off, length := int(u16at(body, 0)), int(u16at(body, 2))
		const pathStart = 8
		if off < 0 || length < 0 || pathStart+off+length > len(body) {
			return tag, "", false, fmt.Errorf("scratchpad: MOUNT_POINT reparse body overruns")
		}
		substitute = decodeUTF16(body[pathStart+off : pathStart+off+length])
	}
	return tag, substitute, relative, nil
}

func u16at(b []byte, i int) uint16 { return uint16(b[i]) | uint16(b[i+1])<<8 }
func u32at(b []byte, i int) uint32 {
	return uint32(b[i]) | uint32(b[i+1])<<8 | uint32(b[i+2])<<16 | uint32(b[i+3])<<24
}

func put16(b []byte, i int, v uint16) { b[i] = byte(v); b[i+1] = byte(v >> 8) }
func put32(b []byte, i int, v uint32) {
	b[i], b[i+1], b[i+2], b[i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}
func put64(b []byte, i int, v uint64) {
	for k := 0; k < 8; k++ {
		b[i+k] = byte(v >> (8 * k))
	}
}

func decodeUTF16(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = u16at(b, i*2)
	}
	return windows.UTF16ToString(u)
}

// checkedNameLen validates a substitute/print name's UTF-16 byte length fits
// the uint16 reparse-buffer fields and the whole point stays under
// maxReparseSize (F11: the prototype's setSymlinkReparse/setMountPointRaw
// truncated SubstituteNameLength/PrintNameLength/ReparseDataLength silently;
// this makes the same three fields fail loudly instead).
func checkedNameLen(nameBytes, totalSoFar int) error {
	if nameBytes < 0 || nameBytes > 0xFFFF {
		return fmt.Errorf("scratchpad: reparse substitute/print name too long (%d bytes)", nameBytes)
	}
	if totalSoFar > 0xFFFF || totalSoFar > maxReparseSize-8 {
		return fmt.Errorf("scratchpad: reparse data too long (%d bytes)", totalSoFar)
	}
	return nil
}

// setMountPointReparse writes a MOUNT_POINT reparse buffer (junction) onto
// an open, empty directory handle. sub is the \??\-prefixed substitute name.
func setMountPointReparse(h windows.Handle, sub, target string) error {
	subU := windows.StringToUTF16(sub)
	printU := windows.StringToUTF16(target)
	subBytes, printBytes := (len(subU)-1)*2, (len(printU)-1)*2
	const fixed = 8
	pathBytes := subBytes + 2 + printBytes + 2
	if err := checkedNameLen(subBytes, fixed+pathBytes); err != nil {
		return err
	}
	if err := checkedNameLen(printBytes, fixed+pathBytes); err != nil {
		return err
	}
	buf := make([]byte, 8+fixed+pathBytes)
	put32(buf, 0, windows.IO_REPARSE_TAG_MOUNT_POINT)
	put16(buf, 4, uint16(fixed+pathBytes))
	put16(buf, 6, 0)
	put16(buf, 8, 0)
	put16(buf, 10, uint16(subBytes))
	put16(buf, 12, uint16(subBytes+2))
	put16(buf, 14, uint16(printBytes))
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

// setSymlinkReparse writes an ABSOLUTE (Flags=0, never SYMLINK_FLAG_RELATIVE)
// SYMLINK reparse buffer onto an open, empty directory handle. The store
// never creates a relative link — readLinkHandle's relative-flag refusal
// (link_windows.go) exists for links it did NOT create but must still
// classify (Scope A forgives an arbitrary pre-existing link, ADR §6.6).
func setSymlinkReparse(h windows.Handle, target string) error {
	sub := `\??\` + target
	subU := windows.StringToUTF16(sub)
	printU := windows.StringToUTF16(target)
	subBytes, printBytes := (len(subU)-1)*2, (len(printU)-1)*2
	const fixed = 12 // 4 x uint16 + Flags uint32
	pathBytes := subBytes + 2 + printBytes + 2
	if err := checkedNameLen(subBytes, fixed+pathBytes); err != nil {
		return err
	}
	if err := checkedNameLen(printBytes, fixed+pathBytes); err != nil {
		return err
	}
	buf := make([]byte, 8+fixed+pathBytes)
	put32(buf, 0, windows.IO_REPARSE_TAG_SYMLINK)
	put16(buf, 4, uint16(fixed+pathBytes))
	put16(buf, 6, 0)
	put16(buf, 8, 0)
	put16(buf, 10, uint16(subBytes))
	put16(buf, 12, uint16(subBytes+2))
	put16(buf, 14, uint16(printBytes))
	put32(buf, 16, 0) // Flags = 0: absolute target, always, for a link WE create
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
// Handle-relative rename (annotationfs_linux.go's Renameat twin, P3.8
// territory) and volume/name-info helpers used by P3.1-P3.6.
// ---------------------------------------------------------------------------

// renameBuffer builds FILE_RENAME_INFO / FILE_RENAME_INFORMATION_EX by hand
// (not as a Go struct) so the byte offsets are auditable, per ADR §3.6:
//
//	union { BOOLEAN ReplaceIfExists; DWORD Flags; }  offset 0
//	HANDLE RootDirectory                             offset 8   (64-bit)
//	DWORD  FileNameLength   (bytes, excluding NUL)   offset 16
//	WCHAR  FileName[]                                offset 20
func renameBuffer(destParent windows.Handle, newName string, flags uint32) ([]byte, error) {
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

// deleteByHandlePosix marks an already-open handle for POSIX-semantics
// deletion (FileDispositionInformationEx, class 64): the name leaves the
// namespace immediately (M10.posix_nt, REQUIRED), unlike the legacy
// FileDispositionInfo, which only schedules delete-on-last-close and is the
// TestPublishCreateOnly flakiness M10.legacy_pending demonstrated. The one
// caller outside link_windows.go/storefs_windows.go that needs the raw
// unsafe.Pointer conversion is confined here, per this file's whole purpose.
func deleteByHandlePosix(h windows.Handle) error {
	var info struct{ Flags uint32 }
	info.Flags = fileDispositionDelete | fileDispositionPosixSemantics | fileDispositionIgnoreReadonlyAttribute
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(h, &iosb, (*byte)(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)), fileDispositionInformationEx)
}

func renameAtNT(src, destParent windows.Handle, newName string, class, flags uint32) error {
	buf, err := renameBuffer(destParent, newName, flags)
	if err != nil {
		return err
	}
	var iosb windows.IO_STATUS_BLOCK
	return windows.NtSetInformationFile(src, &iosb, &buf[0], uint32(len(buf)), class)
}

// volumeInfo reports the filesystem name for the R18 gate (§4.1, §5.5).
func volumeInfo(h windows.Handle) (fsName string, serial uint32, err error) {
	name := make([]uint16, 64)
	fsn := make([]uint16, 64)
	err = windows.GetVolumeInformationByHandle(h, &name[0], uint32(len(name)),
		&serial, nil, nil, &fsn[0], uint32(len(fsn)))
	if err != nil {
		return "", 0, err
	}
	return windows.UTF16ToString(fsn), serial, nil
}

// finalPathDOS is GetFinalPathNameByHandleW(VOLUME_NAME_DOS). It is a
// DISPLAY/diagnostics primitive (§10.3): the string it returns must be
// re-resolved by the OS, so it is never fed into a subsequent operation
// except the two narrow, named exceptions in the ADR (§7.1's advisory
// refusal, and Watch-time canonicalisation whose output is re-validated and
// then walked handle-by-handle before any later use — never trusted as-is).
func finalPathDOS(h windows.Handle) (string, error) {
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameDOS)
	if err != nil {
		return "", err
	}
	if int(n) > len(buf) {
		buf = make([]uint16, n+1)
		if _, err = windows.GetFinalPathNameByHandle(h, &buf[0], uint32(len(buf)), volumeNameDOS); err != nil {
			return "", err
		}
	}
	return strings.TrimPrefix(windows.UTF16ToString(buf), `\\?\`), nil
}

// ---------------------------------------------------------------------------
// Error translation (ADR §3.7). winError keeps both the raw status (for
// diagnostics/logs) and a chain to an fs.Err*/package sentinel so
// errors.Is keeps working for callers exactly as it does against Linux's
// unix.Errno-wrapped errors.
// ---------------------------------------------------------------------------

type winError struct {
	Op     string
	Status windows.NTStatus
	Win32  syscall.Errno
	err    error
}

func (e *winError) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s: %v (NTSTATUS 0x%08X)", e.Op, e.err, uint32(e.Status))
	}
	if e.Win32 != 0 {
		return fmt.Sprintf("%s: %v (winerror %d)", e.Op, e.err, uint32(e.Win32))
	}
	return fmt.Sprintf("%s: %v", e.Op, e.err)
}

func (e *winError) Unwrap() error { return e.err }

var (
	errExistsReparse = fmt.Errorf("scratchpad: name exists as a reparse point: %w", errExists)
	errDeletePending = errors.New("scratchpad: entry is pending deletion")
	errReparse       = errors.New("scratchpad: reparse point encountered where a real directory entry was required")
	errNotDir        = errors.New("scratchpad: not a directory")
	errIsDir         = errors.New("scratchpad: is a directory")
	errSharing       = errors.New("scratchpad: sharing violation (retryable)")
	errLockViolation = errors.New("scratchpad: lock violation (retryable)")
	errNotEmpty      = errors.New("scratchpad: directory not empty")
	// errNoLinkPrivilege is reached only when BOTH link flavours fail
	// (symlinkAt already tried a directory symbolic link, then a junction,
	// before surfacing this): per the ADR §6.6 measured privilege table, a
	// junction succeeds unprivileged with Developer Mode off, so this is the
	// rare case where policy blocks reparse-point creation entirely. The
	// message names Developer Mode as the remediation, never suggests
	// elevation as the default (running elevated is a workaround of last
	// resort, not the fix), and states that every other operation is
	// unaffected — watch is the only thing that needs this privilege.
	errNoLinkPrivilege = errors.New("scratchpad: could not create a watch link (neither a directory symbolic link nor a junction succeeded); " +
		"enable Developer Mode (Settings > System > For developers) and retry — running scratchpad elevated is not the recommended fix. " +
		"publish, list, delete, and notes do not need this privilege and are unaffected")
)

// ntStatusOf extracts the NTSTATUS an NT call failed with, if err is one.
func ntStatusOf(err error) (windows.NTStatus, bool) {
	st, ok := err.(windows.NTStatus)
	return st, ok
}

// translateOpen maps an error from an OPEN (ntOpenAt, or the strict
// primitives built on it) per §3.7's table. STATUS_REPARSE_POINT_ENCOUNTERED
// from an open is ELOOP-shaped (errReparse); the FILE_CREATE-claim reading
// (errExistsReparse) is a distinct function below because the SAME status
// means something different depending on which disposition produced it
// (M8.claim_error_map).
func translateOpen(op string, err error) error {
	if err == nil {
		return nil
	}
	if st, ok := ntStatusOf(err); ok {
		return &winError{Op: op, Status: st, err: mapNTStatus(st)}
	}
	if errno, ok := err.(syscall.Errno); ok {
		return &winError{Op: op, Win32: errno, err: mapWin32Errno(errno)}
	}
	return fmt.Errorf("%s: %w", op, err)
}

// translateClaim maps an error from a FILE_CREATE claim (mkdirClaim,
// symlinkAt's name claim): both STATUS_OBJECT_NAME_COLLISION and
// STATUS_REPARSE_POINT_ENCOUNTERED mean "the name is taken" here — the
// latter specifically because a plain FILE_CREATE resolves the reparse
// point during name resolution before collision is even detected
// (M8.claim_over.*) — so both map to something errors.Is(_, errExists)
// recognises, per §3.7 and §6.6's three-way table. A mechanical port of
// errors.Is(err, unix.EEXIST) alone would miss the reparse case entirely
// and turn every repeat `watch` of an existing link into a hard error
// (M8.claim_error_map) — this function is the fix.
func translateClaim(op string, err error) error {
	if err == nil {
		return nil
	}
	st, ok := ntStatusOf(err)
	if !ok {
		return translateOpen(op, err)
	}
	switch st {
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return &winError{Op: op, Status: st, err: errExists}
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED:
		return &winError{Op: op, Status: st, err: errExistsReparse}
	default:
		return &winError{Op: op, Status: st, err: mapNTStatus(st)}
	}
}

func mapNTStatus(st windows.NTStatus) error {
	switch st {
	case windows.STATUS_OBJECT_NAME_COLLISION:
		return errExists
	case windows.STATUS_DELETE_PENDING:
		return errDeletePending
	case windows.STATUS_OBJECT_NAME_NOT_FOUND, windows.STATUS_OBJECT_PATH_NOT_FOUND:
		return fs.ErrNotExist
	case windows.STATUS_NOT_A_DIRECTORY:
		return errNotDir
	case windows.STATUS_FILE_IS_A_DIRECTORY:
		return errIsDir
	case windows.STATUS_ACCESS_DENIED:
		return fs.ErrPermission
	case windows.STATUS_SHARING_VIOLATION:
		return errSharing
	case windows.STATUS_DIRECTORY_NOT_EMPTY:
		return errNotEmpty
	case windows.STATUS_REPARSE_POINT_ENCOUNTERED, windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED:
		return errReparse
	default:
		return st
	}
}

func mapWin32Errno(e syscall.Errno) error {
	switch e {
	case windows.ERROR_ALREADY_EXISTS:
		return errExists
	case windows.ERROR_PRIVILEGE_NOT_HELD:
		return errNoLinkPrivilege
	case windows.ERROR_SHARING_VIOLATION:
		return errSharing
	case windows.ERROR_LOCK_VIOLATION:
		return errLockViolation
	case windows.ERROR_DIR_NOT_EMPTY:
		return errNotEmpty
	case windows.ERROR_FILE_NOT_FOUND, windows.ERROR_PATH_NOT_FOUND:
		return fs.ErrNotExist
	case windows.ERROR_ACCESS_DENIED:
		return fs.ErrPermission
	default:
		return e
	}
}

// isNotExist/isReparseErr/isNotADir/isExist are the internal predicates the
// higher-level primitives in storefs_windows.go/link_windows.go use to
// branch on a raw (untranslated) NT error before wrapping it — mirroring
// storefs_linux.go's errors.Is(err, unix.ENOENT) etc. shape.
func isNotExist(err error) bool {
	st, ok := ntStatusOf(err)
	return ok && (st == windows.STATUS_OBJECT_NAME_NOT_FOUND || st == windows.STATUS_OBJECT_PATH_NOT_FOUND)
}

func isReparseErr(err error) bool {
	st, ok := ntStatusOf(err)
	return ok && (st == windows.STATUS_REPARSE_POINT_ENCOUNTERED || st == windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED)
}

func isNotADir(err error) bool {
	st, ok := ntStatusOf(err)
	return ok && st == windows.STATUS_NOT_A_DIRECTORY
}

func isNameCollision(err error) bool {
	st, ok := ntStatusOf(err)
	return ok && st == windows.STATUS_OBJECT_NAME_COLLISION
}

// tagName is diagnostics-only (rendered into error messages), never used for
// a classification decision.
func tagName(tag uint32) string {
	switch tag {
	case 0:
		return "none"
	case windows.IO_REPARSE_TAG_MOUNT_POINT:
		return "MOUNT_POINT"
	case windows.IO_REPARSE_TAG_SYMLINK:
		return "SYMLINK"
	case ioReparseTagAppExecLink:
		return "APPEXECLINK"
	default:
		return fmt.Sprintf("0x%08X", tag)
	}
}
