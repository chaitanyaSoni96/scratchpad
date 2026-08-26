//go:build windows

package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// This file is P3.7-P3.10: the Windows annotation backend, ported from
// internal/winspike/atomicwrite.go (the write path and removeTreeAt) and
// mechanically equivalent to annotationfs_linux.go everywhere the two
// platforms can agree (ADR §3.4, §4.5, §4.8, §6.7). Where this file disagrees
// with the winspike prototype, the prototype and its CI measurements win —
// port, do not reinvent.
//
// annotationFS anchors every sidecar operation to an open .annotations
// handle, exactly as the Linux twin anchors to an open .annotations inode.
// storeRoot and lock are additional pinned handles the Windows design needs
// that Linux does not: storeRoot because the rendezvous lock (below) is a
// sibling of .annotations, not .annotations itself, and lock because
// LockFileEx cannot lock a directory handle at all (M14.dir_readhandle,
// M14.dir_writehandle), so the store-root flock this whole design otherwise
// mirrors has no direct Windows equivalent (§6.7 — the F5 rework).
type annotationFS struct {
	storeRoot *os.File // pinned root
	lock      *os.File // pinned <root>\.scratchpad-lock — the rendezvous object (§6.7)
	root      *os.File // pinned .annotations
}

// openAnnotationFS pins the root, then the lock file, then .annotations, in
// that order and before any lock is taken (§6.7's ordering rule, which is
// what closes the RR6 race between Delete and SaveNotes in the normal case).
func openAnnotationFS() (*annotationFS, error) {
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, err
	}
	// Take ownership of the pinned root handle; rfs's other fields (path,
	// id, volume) are plain values with nothing further to release, so
	// there is no leak in not calling rfs.close() here.
	storeRoot := rfs.root

	lock, err := openRendezvousLockFile(int(storeRoot.Fd()), rfs.path)
	if err != nil {
		storeRoot.Close()
		return nil, err
	}

	// .annotations is created and opened relative to the pinned root with
	// the STRICT open (openRealDirAt, via mkdirClaim+openRealDirAt below),
	// so a replacement by symlink, junction or an unrecognised reparse tag
	// is refused on the tag read from the handle itself — never on a mode
	// bit, never on whether some filter driver happens to service it
	// (§4.8; the same F-a argument as every other traversal in this
	// package).
	if err := mkdirClaim(int(storeRoot.Fd()), AnnotationsDir); err != nil && !errors.Is(err, errExists) {
		lock.Close()
		storeRoot.Close()
		return nil, err
	}
	rootFD, err := openRealDirAt(int(storeRoot.Fd()), AnnotationsDir)
	if err != nil {
		lock.Close()
		storeRoot.Close()
		return nil, fmt.Errorf("annotations: %q is not a real directory: %w", AnnotationsDir, err)
	}
	return &annotationFS{
		storeRoot: storeRoot,
		lock:      lock,
		root:      os.NewFile(uintptr(rootFD), AnnotationsDir),
	}, nil
}

func (a *annotationFS) close() error {
	err := a.root.Close()
	if lockErr := a.lock.Close(); err == nil {
		err = lockErr
	}
	if rootErr := a.storeRoot.Close(); err == nil {
		err = rootErr
	}
	return err
}

// ---------------------------------------------------------------------------
// The annotation rendezvous (ADR §6.7 — the F5 rework).
//
// Linux flocks the STORE ROOT descriptor itself: the very object every
// operation is anchored on, so losing the rendezvous needs write access to
// the root's PARENT. LockFileEx refuses a directory handle outright
// (M14.dir_readhandle/dir_writehandle — ERROR_INVALID_PARAMETER on both
// GENERIC_READ and GENERIC_READ|GENERIC_WRITE opens; M14.file_control
// succeeds on an ordinary file), so the rendezvous must live on a CHILD of
// the anchor instead. That is a structural difference between the
// platforms, not a choice this design made, and the residual is stated
// below rather than argued away.
// ---------------------------------------------------------------------------

// lockFileName is the second reserved name (§6.7): the rendezvous, one hop
// from the pinned root. It is a shared, untagged constant (annotations.go)
// so ignore.go's Visible reserved-name check compiles and behaves the same
// on Linux, which never creates the file but reserves the name anyway for
// the same "a store built on one OS stays movable to the other" reason
// checkPortableName already applies to created names (names.go).

// lockIdentityCache is the process-level last-seen identity of the
// rendezvous lock file, keyed on the resolved root PATH — the same detector
// shape §4.1 gives the root itself (rootIdentityCache, storefs_windows.go).
// This is a DETECTOR, not a control: it turns a lock-file swap BETWEEN
// OPERATIONS IN THIS PROCESS into a loud error, but it cannot see a swap
// between two different PROCESSES' opens of the lock file, which is exactly
// the residual RW5/§6.7 states. Do not read more into this cache than that.
var (
	lockIdentityMu    sync.Mutex
	lockIdentityCache = map[string]objectID{}
)

func checkLockIdentity(rootPath string, id objectID) error {
	lockIdentityMu.Lock()
	defer lockIdentityMu.Unlock()
	if prev, ok := lockIdentityCache[rootPath]; ok {
		if prev != id {
			return fmt.Errorf("scratchpad: the annotation lock file %q was replaced since this process last used it — refusing to trust the rendezvous (identity was %v, now %v); restart the server",
				filepath.Join(rootPath, lockFileName), prev, id)
		}
		return nil
	}
	lockIdentityCache[rootPath] = id
	return nil
}

// resetLockIdentityCacheForTest clears the process-level cache, mirroring
// resetRootIdentityCacheForTest (storefs_windows.go) for tests that reuse a
// root path across subtests.
func resetLockIdentityCacheForTest() {
	lockIdentityMu.Lock()
	defer lockIdentityMu.Unlock()
	lockIdentityCache = map[string]objectID{}
}

// openRendezvousLockFile claims (or reopens — FILE_OPEN_IF is the atomic
// create-or-open a two-step FILE_CREATE-then-FILE_OPEN would otherwise need)
// <root>\.scratchpad-lock relative to the pinned root handle, and records
// its identity in the detector above.
func openRendezvousLockFile(parent int, rootPath string) (*os.File, error) {
	h, err := ntOpenAt(windows.Handle(parent), lockFileName,
		windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN_IF,
		windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return nil, translateOpen("open annotation lock file", err)
	}
	fid, err := fileIDOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	id := objectID{vol: fid.VolumeSerialNumber, id: fid.FileID}
	if err := checkLockIdentity(rootPath, id); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	return os.NewFile(uintptr(h), lockFileName), nil
}

// lockFileHandleWithRetry issues LockFileEx with the given flags and byte
// range, retrying a mandatory-lock conflict under the SAME bound §8.4 gives
// the annotation write's replace loop (10 attempts, 2ms doubling to 256ms,
// 2s ceiling — see defaultReplacePolicy's doc comment for where those
// numbers come from). There is no separate measurement for the lock
// specifically; reusing the replace loop's measured bound is the least
// arbitrary choice available, and this comment exists so a reader does not
// mistake reuse for a second measurement.
//
// LOCKFILE_FAIL_IMMEDIATELY plus a bounded retry replaces a blocking
// LockFileEx because Windows byte-range locks are MANDATORY, not advisory
// (M14.mandatory: a second handle's ReadFile over a locked range fails with
// ERROR_LOCK_VIOLATION) — so a HUNG holder must never be waited on
// indefinitely. A CRASHED holder is fine either way: the kernel releases the
// lock unconditionally when the last handle to it closes.
func lockFileHandleWithRetry(h windows.Handle, flags, bytesLow, bytesHigh uint32) error {
	pol := defaultReplacePolicy()
	backoff := pol.initialBackoff
	start := time.Now()
	var last error
	for attempt := 1; attempt <= pol.maxAttempts; attempt++ {
		var ov windows.Overlapped
		if last = windows.LockFileEx(h, flags, 0, bytesLow, bytesHigh, &ov); last == nil {
			return nil
		}
		if attempt == pol.maxAttempts || time.Since(start) >= pol.totalBudget {
			break
		}
		time.Sleep(backoff)
		if backoff *= 2; backoff > pol.maxBackoff {
			backoff = pol.maxBackoff
		}
	}
	return fmt.Errorf("scratchpad: could not acquire a mandatory Windows file lock after %d attempts over %v: %w "+
		"(another scratchpad operation appears to be busy; if this persists, close whatever is holding it and retry)",
		pol.maxAttempts, time.Since(start).Round(time.Millisecond), last)
}

// lockRendezvous locks byte range [0,1) of a.lock — shared for normal
// annotation work, exclusive for Delete/Unwatch across removing both
// content and notes (lockAnnotations, annotations.go). Linux's twin
// (annotationfs_linux.go) is flockFile(a.storeRoot, exclusive): byte-for-byte
// identical POLICY, over a structurally different OBJECT, for the reason
// given in this file's header comment.
func lockRendezvous(a *annotationFS, exclusive bool) error {
	var flags uint32 = windows.LOCKFILE_FAIL_IMMEDIATELY
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return lockFileHandleWithRetry(windows.Handle(a.lock.Fd()), flags, 1, 0)
}

func unlockRendezvous(a *annotationFS) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(a.lock.Fd()), 0, 1, 0, &ov)
}

// ---------------------------------------------------------------------------
// Per-document locks (annotations.go's lockDocument). Unlike the rendezvous,
// these lock an ORDINARY FILE the store itself created under
// .annotations/.locks/, which LockFileEx handles natively — no redesign
// needed here, only the mechanism.
// ---------------------------------------------------------------------------

// openLockFileAt creates or opens name (a per-document lock file) relative
// to parent, via the strict open's disposition (FILE_OPEN_IF) so a lock file
// the store creates itself is never silently redirected by a planted link:
// OBJ_DONT_REPARSE and FILE_NON_DIRECTORY_FILE together refuse a reparse
// point or a directory at this name outright rather than transparently
// opening through it.
func openLockFileAt(parent int, name string) (*os.File, error) {
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
		windows.FILE_OPEN_IF, windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return nil, translateOpen("open document lock file", err)
	}
	return os.NewFile(uintptr(h), name), nil
}

// flockFile takes (exclusive=true) or shares (exclusive=false) a lock on the
// whole addressable range of f — LockFileEx's twin to flock(2) on a REGULAR
// FILE. It is only ever called on an ordinary file the store created
// (openLockFileAt's result): LockFileEx refuses a directory handle outright,
// which is why the annotation-ROOT rendezvous needed lockRendezvous/
// unlockRendezvous above instead of this function.
func flockFile(f *os.File, exclusive bool) error {
	var flags uint32 = windows.LOCKFILE_FAIL_IMMEDIATELY
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	return lockFileHandleWithRetry(windows.Handle(f.Fd()), flags, ^uint32(0), ^uint32(0))
}

// funlockFile releases a lock taken by flockFile. The byte range MUST match
// the one flockFile locked.
func funlockFile(f *os.File) error {
	var ov windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, ^uint32(0), ^uint32(0), &ov)
}

// ---------------------------------------------------------------------------
// Directory walk and plain read (P3.7/P3.10), mechanically equivalent to
// annotationfs_linux.go's openDir/readFile.
// ---------------------------------------------------------------------------

func (a *annotationFS) openDir(segs []string, create bool) (int, error) {
	fd, err := dupFD(int(a.root.Fd()))
	if err != nil {
		return -1, err
	}
	for _, seg := range segs {
		next, openErr := openRealDirAt(fd, seg)
		if openErr != nil && create && errors.Is(openErr, fs.ErrNotExist) {
			if mkErr := mkdirClaim(fd, seg); mkErr != nil && !errors.Is(mkErr, errExists) {
				closeFD(fd)
				return -1, mkErr
			}
			next, openErr = openRealDirAt(fd, seg)
		}
		closeFD(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func (a *annotationFS) readFile(segs []string) ([]byte, error) {
	parent, err := a.openDir(segs[:len(segs)-1], false)
	if err != nil {
		return nil, err
	}
	defer closeFD(parent)
	// readAllAt (storefs_windows.go) is openRealFileAt wrapped with the full
	// share mode (R15) — the same primitive OpenDocument's read path uses,
	// so a notes read can never veto, or be vetoed by, a concurrent replace.
	return readAllAt(parent, segs[len(segs)-1])
}

// ---------------------------------------------------------------------------
// P3.8: the atomic write. Ported from internal/winspike/atomicwrite.go's
// AtomicWriteFile, which is itself the measured twin of
// annotationfs_linux.go:123-166's writeFile.
// ---------------------------------------------------------------------------

// replacePolicy bounds the retry of a transiently-failing replace (and, via
// lockFileHandleWithRetry above, the annotation lock's acquire retry).
//
// The BOUND's behaviour is measured (P13.bound, run 32908643117): a
// destination held without FILE_SHARE_DELETE for the whole bound produced
// exactly 10 attempts over 771ms, terminating in an actionable error with
// the destination still holding its complete previous content
// (P13.bound_preserves_dest). The DISTRIBUTION the bound is SIZED against —
// antivirus- and indexer-induced sharing violations — is explicitly NOT
// measurable on a CI runner (M13.av), so the choice of 10/2ms/256ms/2s is a
// documented judgment call, not a measurement: a replace that is going to
// succeed succeeds on the FIRST attempt once the interfering handle closes
// (M13.retry), so the retry rides out a short-lived scan rather than polling
// for minutes, and the caller is an interactive HTTP request (PUT
// /notes/...) whose patience the 2s ceiling is sized against.
type replacePolicy struct {
	maxAttempts    int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	totalBudget    time.Duration
	flush          bool
}

// defaultReplacePolicy is the bound ADR §8.4 specifies: 10 attempts, 2ms
// doubling to a 256ms ceiling (sleeps before attempts 2..10: 2, 4, 8, 16,
// 32, 64, 128, 256, 256ms — 766ms of retrying in the worst case), under a 2s
// wall-clock hard budget for the case where an individual attempt itself
// BLOCKS rather than failing immediately (a share-mode veto fails
// immediately; a filter driver need not).
//
// Flush defaults to false: P13.flush_cost measured 811µs/op without
// FlushFileBuffers versus 4.594ms/op with it (5.7x) over 100 writes of a 4
// KiB payload. annotationfs_linux.go's writeFile does not fsync either, so
// flushing here would make Windows strictly MORE durable than the Linux
// backend rather than reach parity, at 5.7x the per-save cost. What the
// atomic replace already guarantees without any flush is the property that
// matters: the destination name always resolves to one COMPLETE version.
// What it does not guarantee is WHICH version survives a power loss — and a
// lost note revision is recoverable by the user, a torn one is not.
func defaultReplacePolicy() replacePolicy {
	return replacePolicy{
		maxAttempts:    10,
		initialBackoff: 2 * time.Millisecond,
		maxBackoff:     256 * time.Millisecond,
		totalBudget:    2 * time.Second,
		flush:          false,
	}
}

// retryableRenameStatuses is chosen from documentation, not a measured
// distribution (M13.av is explicitly NOT MEASURED), and justified per entry:
//
//	STATUS_SHARING_VIOLATION   another opener's share mode vetoes the
//	                           operation — the canonical AV/Explorer-
//	                           preview/editor case.
//	STATUS_DELETE_PENDING      a legacy (non-POSIX) delete of the same name
//	                           is still completing. Self-clearing.
//	STATUS_LOCK_NOT_GRANTED    a byte-range lock conflicts. Windows locks
//	STATUS_FILE_LOCK_CONFLICT  are mandatory (M14.mandatory), so this
//	                           reaches us.
//	STATUS_USER_MAPPED_FILE    a section is mapped over the destination.
//	                           Clears when the mapping is torn down.
//	STATUS_DIRECTORY_NOT_EMPTY a concurrent writer repopulated a directory
//	                           between enumeration and removal.
//
// Deliberately NOT retryable: STATUS_ACCESS_DENIED (at the NT layer this is
// an ACL denial — the delete-pending case Win32 collapses into
// ERROR_ACCESS_DENIED keeps its own distinct status here, M13.pending_status
// — so retrying ACCESS_DENIED only adds latency to a permanent failure);
// STATUS_REPARSE_POINT_ENCOUNTERED (a link appeared where a real entry is
// required — an ATTACK signal, A2/RR1, and retrying it would loop against
// the attacker); STATUS_OBJECT_NAME_COLLISION / STATUS_OBJECT_PATH_NOT_FOUND
// / STATUS_NOT_SAME_DEVICE / STATUS_DISK_FULL / STATUS_MEDIA_WRITE_PROTECTED
// (permanent by construction).
var retryableRenameStatuses = []windows.NTStatus{
	windows.STATUS_SHARING_VIOLATION,
	windows.STATUS_DELETE_PENDING,
	windows.STATUS_LOCK_NOT_GRANTED,
	windows.STATUS_FILE_LOCK_CONFLICT,
	windows.STATUS_USER_MAPPED_FILE,
	windows.STATUS_DIRECTORY_NOT_EMPTY,
}

// retryableRenameErrnos is the same set as Win32 codes, for a path that goes
// through a Win32 wrapper rather than the NT call and so never produces an
// NTSTATUS. Nothing in this package's write path currently takes that path,
// but the set is kept alongside its NTSTATUS twin for the reader who checks.
var retryableRenameErrnos = []syscall.Errno{
	32,   // ERROR_SHARING_VIOLATION
	33,   // ERROR_LOCK_VIOLATION
	145,  // ERROR_DIR_NOT_EMPTY
	1224, // ERROR_USER_MAPPED_FILE
}

// isRetryableRenameErr operates on the RAW error a rename call fails with
// (renameAtNT returns an untranslated NTSTATUS-shaped error, not one passed
// through translateOpen), mirroring win32_windows.go's isNotADir/isNotExist/
// isNameCollision, which are documented as operating on raw errors for the
// same reason.
func isRetryableRenameErr(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := ntStatusOf(err); ok {
		for _, s := range retryableRenameStatuses {
			if st == s {
				return true
			}
		}
		return false
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		for _, r := range retryableRenameErrnos {
			if errno == r {
				return true
			}
		}
	}
	return false
}

// isUnsupportedRenameClass recognises the "this build or filesystem does not
// implement FileRenameInformationEx" answers, which are the ONLY ones the
// class-10 fallback is allowed to cover (A9.rename_failure_statuses) — this
// is an allowlist, not "retry on any error". The Go standard library's
// blanket fallback would, on measured rows, silently retry an ATTACK with a
// class that has no POSIX semantics: dest_held_no_share_delete returns
// STATUS_SHARING_VIOLATION (retryable) from class 65 but STATUS_ACCESS_DENIED
// (NOT retryable) from class 10, and dest_is_directory returns
// STATUS_OBJECT_IS_A_DIRECTORY, which is permanent.
func isUnsupportedRenameClass(err error) bool {
	st, ok := ntStatusOf(err)
	if !ok {
		return false
	}
	return st == windows.STATUS_INVALID_PARAMETER ||
		st == windows.STATUS_NOT_SUPPORTED ||
		st == windows.STATUS_INVALID_INFO_CLASS ||
		st == windows.STATUS_INVALID_DEVICE_REQUEST
}

// replaceError is what SaveNotes' caller sees after the retry bound is
// exhausted — the spec's "Antivirus/indexer sharing violations exceed retry
// bounds" actionable case, ported from internal/winspike/atomicwrite.go's
// ReplaceError.
type replaceError struct {
	op       string
	dest     string
	attempts int
	elapsed  time.Duration
	last     error
}

func (e *replaceError) Error() string {
	return fmt.Sprintf(
		"%s: could not replace %q after %d attempts over %v: %v. "+
			"Another program is holding the file open without allowing deletion — "+
			"most often an editor, Explorer's preview pane, an antivirus scanner or a backup agent. "+
			"Close it and retry; the previous contents of %q were left untouched.",
		e.op, e.dest, e.attempts, e.elapsed.Round(time.Millisecond), e.last, e.dest)
}

func (e *replaceError) Unwrap() error { return e.last }

// newAnnotationTempName generates the unique temp name — the same shape as
// annotationfs_linux.go:110 (a dot-prefixed, generated name that can never
// collide with a document name and is invisible to the store's listings).
func newAnnotationTempName() (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return ".notes-" + hex.EncodeToString(nonce[:]) + ".tmp", nil
}

// atomicWriteFileAt writes data to name under the pinned directory handle
// parent, replacing any existing entry atomically. Contract, ported from
// internal/winspike/atomicwrite.go's AtomicWriteFile and every clause
// tested there:
//
//	R-1 nothing is resolved from a path: parent anchors the temp creation,
//	    the replace and the cleanup.
//	R-2 the temp name is unique and claimed create-only (FILE_CREATE), so
//	    two concurrent writers cannot share a temp.
//	R-3 the destination is NEVER unlinked: it goes straight from holding the
//	    complete old bytes to holding the complete new bytes
//	    (NtSetInformationFile(FileRenameInformationEx), never a Win32
//	    SetFileInformationByHandle wrapper, which refuses a non-NULL
//	    RootDirectory with ERROR_INVALID_PARAMETER — M9.win32_control_nullroot
//	    — and never a remove-then-rename decomposition).
//	R-4 on EVERY failure path the temp is removed through its own HANDLE,
//	    never its name, so cleanup cannot be redirected by a name
//	    substitution and still finds the temp even if it was renamed away.
//	R-5 a transient failure is retried within the bound above; past it the
//	    caller gets a *replaceError and the destination still holds the old
//	    bytes.
func atomicWriteFileAt(parent int, name string, data []byte) error {
	if name == "" || strings.ContainsAny(name, `\/`) {
		return fmt.Errorf("scratchpad: atomic write requires a single path component, got %q", name)
	}
	pol := defaultReplacePolicy()

	var (
		tmp string
		h   windows.Handle
		err error
	)
	for i := 0; i < 100; i++ {
		tmp, err = newAnnotationTempName()
		if err != nil {
			return err
		}
		h, err = ntOpenAt(windows.Handle(parent), tmp, windows.FILE_GENERIC_WRITE|windows.DELETE,
			windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
		if !isNameCollision(err) {
			break
		}
	}
	if err != nil {
		return translateOpen("create annotation temp file", err)
	}
	f := os.NewFile(uintptr(h), tmp)

	// done is the single switch deciding whether the deferred cleanup
	// removes the temp: it is set ONLY after a successful replace, at which
	// point `name` on `parent` names the DESTINATION we just wrote, and
	// deleting through f's handle would destroy exactly that.
	done := false
	defer func() {
		if done {
			return
		}
		_ = deleteByHandlePosix(windows.Handle(f.Fd()))
		f.Close()
	}()

	if _, err = f.Write(data); err != nil {
		return fmt.Errorf("scratchpad: writing annotation temp file: %w", err)
	}
	if pol.flush {
		if err = windows.FlushFileBuffers(windows.Handle(f.Fd())); err != nil {
			return fmt.Errorf("scratchpad: flushing annotation temp file: %w", err)
		}
	}

	backoff := pol.initialBackoff
	start := time.Now()
	var last error
	for attempt := 1; attempt <= pol.maxAttempts; attempt++ {
		last = renameAtNT(windows.Handle(f.Fd()), windows.Handle(parent), name, fileRenameInformationEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		if last != nil && isUnsupportedRenameClass(last) {
			// Windows 10 1709+ only, and not on every filesystem (M9). Class
			// 10 replaces atomically too — it just leaves the replaced file
			// delete-pending instead of removing it at once
			// (M10.legacy_pending) — but the fallback fires ONLY on the
			// allowlisted "this build/filesystem does not implement the
			// class" statuses above, never as a blanket retry.
			if legacy := renameAtNT(windows.Handle(f.Fd()), windows.Handle(parent), name,
				fileRenameInformation, fileRenameReplaceIfExists); legacy == nil {
				last = nil
			} else {
				last = legacy
			}
		}
		if last == nil {
			done = true
			return f.Close()
		}
		if !isRetryableRenameErr(last) {
			return translateOpen("replace annotation file", last)
		}
		if attempt == pol.maxAttempts || time.Since(start) >= pol.totalBudget {
			break
		}
		time.Sleep(backoff)
		if backoff *= 2; backoff > pol.maxBackoff {
			backoff = pol.maxBackoff
		}
	}
	return &replaceError{op: "annotation write", dest: name, attempts: pol.maxAttempts, elapsed: time.Since(start), last: last}
}

func (a *annotationFS) writeFile(segs []string, data []byte) error {
	parent, err := a.openDir(segs[:len(segs)-1], true)
	if err != nil {
		return err
	}
	defer closeFD(parent)
	return atomicWriteFileAt(parent, segs[len(segs)-1], data)
}

// ---------------------------------------------------------------------------
// P3.9: safe recursive removal. removeTreeAt is called directly from
// store.go (Publish's rollback, Delete's real-directory branch) as well as
// from annotationFS.removeSubtree below — the same function serves both
// callers on Linux too.
// ---------------------------------------------------------------------------

// deleteEntryAt removes the single directory entry name relative to parent,
// classifying NOTHING and constraining neither FILE_DIRECTORY_FILE nor
// FILE_NON_DIRECTORY_FILE: it opens the entry as whatever it is — a plain
// file, a directory-shaped link, a file-shaped link, or an entry carrying an
// unrecognised reparse tag — and POSIX-disposes it. This is removeTreeAt's
// "not a real directory" leaf case (ADR §4.5): the classification IS the
// openRealDirAt call above failing, so this function must never itself
// re-classify or branch on what it finds — doing so would reopen exactly the
// check-then-use window the whole design removes.
func deleteEntryAt(parent int, name string) error {
	recordNamespaceRemoval(name)
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return translateOpen("delete entry", err)
	}
	defer windows.CloseHandle(h)
	return translateOpen("delete entry", deleteByHandlePosix(h))
}

// ---------------------------------------------------------------------------
// Namespace-removal audit — TEST INSTRUMENTATION ONLY, off by default (a
// single untaken branch on the production path when not armed). Migrates
// winspike's audit (P13.audit/P13.no_dest_removal, with its own
// P13.audit_control) into a permanent property: proving atomicWriteFileAt's
// replace never degrades into a separate removal of the destination
// followed by a rename. deleteEntryAt is the one name-based removal
// primitive a "remove-then-rename" degradation would call to unlink the
// destination by name before renaming the temp into place; the write path's
// OWN temp cleanup goes through deleteByHandlePosix directly (never through
// this function), so it never appears in the log during a correct replace.
// ---------------------------------------------------------------------------

var (
	writeAuditMu  sync.Mutex
	writeAuditOn  bool
	writeAuditLog []string
)

func writeAuditStart() {
	writeAuditMu.Lock()
	writeAuditOn = true
	writeAuditLog = nil
	writeAuditMu.Unlock()
}

func writeAuditStop() []string {
	writeAuditMu.Lock()
	defer writeAuditMu.Unlock()
	writeAuditOn = false
	out := writeAuditLog
	writeAuditLog = nil
	return out
}

func recordNamespaceRemoval(name string) {
	writeAuditMu.Lock()
	if writeAuditOn {
		writeAuditLog = append(writeAuditLog, name)
	}
	writeAuditMu.Unlock()
}

// removeTreeAt is removeTreeAtByHandle at depth 0 — see that function for
// the containment argument. The exported (package-level) two-argument shape
// matches annotationfs_linux.go's removeTreeAt exactly, so both of
// store.go's call sites (Publish's cleanup, Delete) need no platform
// awareness at all.
func removeTreeAt(parent int, name string) error {
	return removeTreeAtDepth(parent, name, 0)
}

// removeTreeAtDepth is RemoveTreeAt (internal/winspike/atomicwrite.go)
// ported, with one addition the prototype deliberately left as a named gap:
// a depth bound (R16), the same maxArtifactWalkDepth (store.go) sizeWalkAt
// and List's descent use — a carried-forward gap this store had on BOTH
// platforms before P3.9 (ADR §4.5), fixed here and in
// annotationfs_linux.go's twin in the same change.
//
// THE OPERATION IS THE CLASSIFICATION (ADR §4.5, RR1's release gate, this
// task's binding constraint 1). Revision 1 of the ADR specified the weaker
// algorithm — readDirFD for entries, statAt per entry for classification,
// recurse only into IsDir — and the prototype's own negative control
// (removeTreeAtByAttributeUnsafeForTest, in the _test.go file next to this
// one) proves that shape destroys the external target tree: it is the
// mechanical translation of Linux's fstatat(AT_SYMLINK_NOFOLLOW)+S_IFDIR
// classification, and FILE_ATTRIBUTE_DIRECTORY is SET on a junction
// (P14.delete_attr_trap), so a walk that decided from the attribute alone
// would descend through the link and delete the junction's target — RR1
// exactly.
//
// Here, nothing is decided from a separately-observed attribute. The
// attempted no-follow directory open (openRealDirAt, the STRICT primitive —
// never openDirAt's weaker OBJ_DONT_REPARSE-only form, and never a raw
// ntOpenAt) comes FIRST, and its FAILURE is the classification: if the
// entry is, or BECOMES (A6.swap_midwalk), a reparse point of ANY tag, the
// strict open fails and the entry is removed as a link/opaque entry via
// deleteEntryAt, never descended into. statAt/classifyEntry play no role in
// this function at all — not even for reporting — because there is nothing
// left to report that the open's own success or failure did not already
// decide.
func removeTreeAtDepth(parent int, name string, depth int) error {
	if depth > maxArtifactWalkDepth {
		return fmt.Errorf("scratchpad: %q exceeds the maximum artifact tree depth (%d)", name, maxArtifactWalkDepth)
	}
	h, err := openRealDirAt(parent, name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		var rr *reparseRefusal
		if errors.As(err, &rr) || errors.Is(err, errNotDir) {
			// Not a real directory — or it BECAME one between two calls to
			// this very function, which is exactly what A6.swap_midwalk
			// exercises. Remove the ENTRY, never a target: deleteEntryAt's
			// lack of a FILE_DIRECTORY_FILE/FILE_NON_DIRECTORY_FILE
			// constraint covers a junction, either symlink flavour, an
			// unknown-tag directory and a plain file in one call.
			return deleteEntryAt(parent, name)
		}
		return err
	}
	entries, readErr := readDirFD(h)
	if readErr == nil {
		for _, e := range entries {
			// Deterministic-race hook (A6.swap_midwalk, migrated from
			// winspike's OpTreeEntry): fires after enumeration and before
			// the recursive descent, so a test can swap this exact entry
			// for a junction/symlink/unknown-tag directory at this exact
			// moment. The safety property under test is that the
			// FOLLOWING removeTreeAtDepth call re-opens with the STRICT
			// primitive regardless of what this loop observed a moment
			// ago — nil outside tests, same shape as runStoreOpHook
			// (store.go).
			runStoreOpHook("annotation-tree-entry")
			if err := removeTreeAtDepth(h, e.Name(), depth+1); err != nil && !errors.Is(err, fs.ErrNotExist) {
				readErr = err
				break
			}
		}
	}
	closeFD(h)
	if readErr != nil {
		return readErr
	}
	// rmdirAt (storefs_windows.go) is FILE_DIRECTORY_FILE + POSIX dispose:
	// exactly the final "remove the now-empty real directory" step, and
	// errNotEmpty (STATUS_DIRECTORY_NOT_EMPTY) surfaces loudly rather than
	// silently if a concurrent writer repopulated it mid-walk.
	return rmdirAt(parent, name)
}

func (a *annotationFS) removeSubtree(segs []string) error {
	parent, err := a.openDir(segs[:len(segs)-1], false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer closeFD(parent)
	if err := removeTreeAt(parent, segs[len(segs)-1]); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := deleteEntryAt(parent, segs[len(segs)-1]+".json"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	// Prune empty mirrored ancestors, reopening each parent from the
	// anchored ROOT so a concurrent rename can only make pruning stop,
	// never redirect it (unchanged policy from Linux).
	for i := len(segs) - 1; i > 0; i-- {
		ancestor, err := a.openDir(segs[:i-1], false)
		if err != nil {
			break
		}
		err = rmdirAt(ancestor, segs[i-1])
		closeFD(ancestor)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			break
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// P3.10: the annotation walk/report support. Matches
// annotationfs_linux.go's walk/walkAnnotationDir shape and ordering exactly:
// directories are entered depth-first via readDirFD, files ending in .json
// are read and handed to visit, everything else (an allow-listed link, an
// unrecognised reparse tag) is silently skipped — mirroring Linux's silent
// skip of anything that is neither S_IFDIR nor S_IFREG. Malformed-JSON
// handling lives one layer up in WalkNotes (annotations.go), which is
// shared, untagged code: json.Unmarshal failing there simply drops that one
// document rather than aborting the walk, identically on both platforms.
// ---------------------------------------------------------------------------

func (a *annotationFS) walk(segs []string, visit func([]string, []byte)) error {
	fd, err := a.openDir(segs, false)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	defer closeFD(fd)
	return walkAnnotationDir(fd, append([]string(nil), segs...), visit)
}

func walkAnnotationDir(fd int, prefix []string, visit func([]string, []byte)) error {
	entries, err := readDirFD(fd)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		meta, explore := classifyEntry(fd, entry.Name())
		if !explore {
			continue // Scope C: a reparse tag we do not understand — never listed, never visited
		}
		path := append(append([]string(nil), prefix...), entry.Name())
		switch {
		case meta.IsDir:
			child, err := openRealDirAt(fd, entry.Name())
			if err != nil {
				continue
			}
			_ = walkAnnotationDir(child, path, visit)
			closeFD(child)
		case meta.IsRegular:
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := readAllAt(fd, entry.Name())
			if err == nil {
				visit(path, data)
			}
		default:
			// An allow-listed link (Scope A) inside .annotations: this store
			// never creates one there, but if one exists it is neither a
			// directory nor a regular file for this walk's purposes, and is
			// skipped exactly as Linux's switch on S_IFMT skips anything
			// that is not S_IFDIR/S_IFREG.
		}
	}
	return nil
}
