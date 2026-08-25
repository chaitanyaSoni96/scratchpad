//go:build windows

package winspike

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P1.3 — the annotation write path, end to end.
//
// This file is the Windows twin of annotationFS.writeFile
// (annotationfs_linux.go:122-163) and of removeTreeAt
// (annotationfs_linux.go:174-213). Both are handle-anchored: nothing below
// re-resolves a pathname from the process namespace, and every mutation is
// issued relative to a directory HANDLE pinned by the caller before the
// operation began.
//
// The Linux original is four calls:
//
//	openat(parent, ".notes-<nonce>.tmp", O_WRONLY|O_CREAT|O_EXCL|O_NOFOLLOW)
//	write / chmod / close
//	renameat(parent, tmp, parent, dest)          <- atomic replace
//	unlinkat(parent, tmp) on every failure path  <- cleanup
//
// The Windows twin is the same four steps, with three differences that M9,
// M10 and M13 forced:
//
//  1. The rename MUST go through NtSetInformationFile(FileRenameInformationEx).
//     The documented Win32 wrapper SetFileInformationByHandle refuses a
//     non-NULL RootDirectory with ERROR_INVALID_PARAMETER (M9), so the
//     handle-relative form of renameat has no supported Win32 spelling.
//  2. FILE_RENAME_POSIX_SEMANTICS is set so the replaced destination leaves
//     the namespace at once instead of lingering delete-pending (M10).
//  3. The replace can fail transiently, which renameat(2) cannot. Windows
//     sharing modes let any other opener veto it (M13), so the operation is
//     wrapped in a BOUNDED retry with an actionable terminal error.
// ---------------------------------------------------------------------------

// spikeOpHook makes validation/use race tests deterministic. It is the direct
// analogue of internal/store's testStoreOpHook (store.go:24-31) and obeys the
// same rule: every call site sits AFTER the handle it protects has been
// pinned, so a test firing from inside the hook is racing the real window and
// not a fabricated one. nil outside tests.
var spikeOpHook func(op string)

func runSpikeOpHook(op string) {
	if spikeOpHook != nil {
		spikeOpHook(op)
	}
}

// Hook operation names. Kept as constants so a test cannot silently install a
// hook for an op that no longer exists.
const (
	// OpTempCreated fires once the unique temp file exists and is open, before
	// any byte is written. The destination still holds its old content.
	OpTempCreated = "annot-temp-created"
	// OpBeforeReplace fires after the temp is fully written and flushed and
	// immediately before the atomic replace. This is the exact window an
	// attacker has to substitute the destination.
	OpBeforeReplace = "annot-before-replace"
	// OpReplaceRetry fires before each retry sleep.
	OpReplaceRetry = "annot-replace-retry"
	// OpAfterReplace fires after a successful replace.
	OpAfterReplace = "annot-after-replace"
	// OpLinkNameClaimed fires between FILE_CREATE and FSCTL_SET_REPARSE_POINT
	// in the two-step link creation (M8). A test that panics/returns here
	// simulates the crash that leaves an empty real directory behind.
	OpLinkNameClaimed = "link-name-claimed"
	// OpTreeEntry fires in the recursive removal after an entry has been
	// classified and before it is removed.
	OpTreeEntry = "tree-entry"
	// OpBrowseBoundary fires in OpenBrowsableDir between reading the watch
	// link's target out of the reparse buffer and opening that target BY PATH
	// — the one path-string re-resolution the design still contains.
	OpBrowseBoundary = "browse-boundary"
)

// ---------------------------------------------------------------------------
// Namespace-removal audit.
//
// Every call in this package that removes a name from the namespace records
// it here. This exists for one test: proving that the atomic replace never
// degrades into remove-then-rename. A future rewrite that unlinked the
// destination first would have to route around DeleteAt/DeleteByHandle to
// stay invisible, and those are the only removal primitives the package has.
// ---------------------------------------------------------------------------

var (
	auditMu  sync.Mutex
	auditLog []AuditEntry
	auditOn  bool
)

// AuditEntry is one recorded namespace removal.
type AuditEntry struct {
	Op   string // "DeleteAt" or "DeleteByHandle"
	Name string // the name removed; "" when the removal was issued by handle
}

func (a AuditEntry) String() string { return a.Op + "(" + a.Name + ")" }

// AuditStart clears the log and begins recording.
func AuditStart() {
	auditMu.Lock()
	auditLog = nil
	auditOn = true
	auditMu.Unlock()
}

// AuditStop stops recording and returns everything captured.
func AuditStop() []AuditEntry {
	auditMu.Lock()
	defer auditMu.Unlock()
	auditOn = false
	out := auditLog
	auditLog = nil
	return out
}

func recordRemoval(op, name string) {
	auditMu.Lock()
	if auditOn {
		auditLog = append(auditLog, AuditEntry{Op: op, Name: name})
	}
	auditMu.Unlock()
}

// ---------------------------------------------------------------------------
// The bounded retry policy.
// ---------------------------------------------------------------------------

// ReplacePolicy bounds the retry of a transiently-failing replace or removal.
//
// The bound is a design decision, not a measurement: the distribution of
// antivirus- and indexer-induced sharing violations is explicitly NOT
// measurable on a CI runner (M13.av). What IS measured here is the
// deterministic half — an interfering handle we open ourselves — and the two
// facts the bound has to respect:
//
//   - a replace that is going to succeed succeeds on the FIRST attempt once
//     the interfering handle closes (M13.retry), so the retry exists to ride
//     out a short-lived scan, not to poll for minutes;
//   - the caller is an interactive HTTP request (PUT /notes/...), so the total
//     budget must stay inside a human's patience for a save.
type ReplacePolicy struct {
	MaxAttempts    int           // total attempts, including the first
	InitialBackoff time.Duration // sleep before attempt 2
	MaxBackoff     time.Duration // per-sleep ceiling
	TotalBudget    time.Duration // wall-clock ceiling across all attempts
	Flush          bool          // FlushFileBuffers on the temp before replacing
}

// DefaultReplacePolicy is the bound this spike recommends: 8 attempts,
// 1ms→128ms doubling backoff, 1s total wall clock. Worst case ~255ms of sleep
// over 8 attempts, so the budget is reached only when individual attempts
// themselves block.
func DefaultReplacePolicy() ReplacePolicy {
	return ReplacePolicy{
		MaxAttempts:    8,
		InitialBackoff: time.Millisecond,
		MaxBackoff:     128 * time.Millisecond,
		TotalBudget:    time.Second,
		Flush:          false,
	}
}

// RetryableStatuses is the retryable set, chosen from documentation rather
// than from a measured distribution (M13.av), and justified per entry:
//
//	STATUS_SHARING_VIOLATION   another opener's share mode vetoes the operation.
//	                           The canonical AV / Explorer-preview / editor case.
//	STATUS_DELETE_PENDING      a legacy (non-POSIX) delete of the same name is
//	                           still completing. Self-clearing.
//	STATUS_LOCK_NOT_GRANTED    a byte-range lock conflicts. Windows locks are
//	STATUS_FILE_LOCK_CONFLICT  mandatory (M14.mandatory), so this reaches us.
//	STATUS_USER_MAPPED_FILE    a section is mapped over the destination. Clears
//	                           when the mapping is torn down.
//	STATUS_DIRECTORY_NOT_EMPTY a concurrent writer re-populated a directory
//	                           between our enumeration and its removal.
//
// Deliberately NOT retryable, each for a stated reason:
//
//	STATUS_ACCESS_DENIED       at the NT layer this is an ACL denial. The
//	                           delete-pending case that Win32 collapses into
//	                           ERROR_ACCESS_DENIED keeps its own status here
//	                           (STATUS_DELETE_PENDING), so retrying ACCESS_DENIED
//	                           would only add latency to a permanent failure.
//	                           (Measured: M13.pending_status.)
//	STATUS_REPARSE_POINT_ENCOUNTERED  a link appeared where a real entry is
//	                           required. This is an ATTACK signal (A2/RR1);
//	                           retrying it would loop against the attacker.
//	STATUS_OBJECT_NAME_COLLISION / STATUS_OBJECT_PATH_NOT_FOUND / STATUS_NOT_SAME_DEVICE /
//	STATUS_DISK_FULL / STATUS_MEDIA_WRITE_PROTECTED  permanent by construction.
var RetryableStatuses = []windows.NTStatus{
	windows.STATUS_SHARING_VIOLATION,
	windows.STATUS_DELETE_PENDING,
	windows.STATUS_LOCK_NOT_GRANTED,
	windows.STATUS_FILE_LOCK_CONFLICT,
	windows.STATUS_USER_MAPPED_FILE,
	windows.STATUS_DIRECTORY_NOT_EMPTY,
}

// retryableErrnos is the same set expressed as Win32 codes, for the paths that
// go through a Win32 wrapper (SetFileInformationByHandle) rather than the NT
// call and therefore never produce an NTSTATUS.
var retryableErrnos = []syscall.Errno{
	32,   // ERROR_SHARING_VIOLATION
	33,   // ERROR_LOCK_VIOLATION
	145,  // ERROR_DIR_NOT_EMPTY
	1224, // ERROR_USER_MAPPED_FILE
}

// IsRetryable reports whether err is in the retryable set above.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if st, ok := StatusOf(err); ok {
		for _, s := range RetryableStatuses {
			if st == s {
				return true
			}
		}
		return false
	}
	var e syscall.Errno
	if errors.As(err, &e) {
		for _, r := range retryableErrnos {
			if e == r {
				return true
			}
		}
	}
	return false
}

// ReplaceError is what the user sees after the bound is exhausted. It names
// the operation, the destination, what was tried, and what to do — the spec's
// "Antivirus/indexer sharing violations exceed retry bounds" actionable case.
type ReplaceError struct {
	Op       string
	Dest     string
	Attempts int
	Elapsed  time.Duration
	Last     error
}

func (e *ReplaceError) Error() string {
	return fmt.Sprintf(
		"%s: could not replace %q after %d attempts over %v: %v. "+
			"Another program is holding the file open without allowing deletion — "+
			"most often an editor, Explorer's preview pane, an antivirus scanner or a backup agent. "+
			"Close it and retry; the previous contents of %q were left untouched.",
		e.Op, e.Dest, e.Attempts, e.Elapsed.Round(time.Millisecond), DescribeErr(e.Last), e.Dest)
}

func (e *ReplaceError) Unwrap() error { return e.Last }

// ---------------------------------------------------------------------------
// The write path.
// ---------------------------------------------------------------------------

// WriteResult reports what the write actually did, so a measurement can state
// facts instead of inferring them.
type WriteResult struct {
	Temp       string        // the unique temp name that was claimed
	Attempts   int           // replace attempts made
	Elapsed    time.Duration // wall clock spent in the replace loop
	UsedLegacy bool          // FileRenameInformation (class 10) fallback was used
	Flushed    bool
}

// newTempName generates the unique temp name. Same shape as
// annotationfs_linux.go:110 — a dot-prefixed, generated name, so it can never
// collide with a document name and is invisible to the store's listings.
func newTempName() (string, error) {
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return ".notes-" + hex.EncodeToString(nonce[:]) + ".tmp", nil
}

// AtomicWriteFile writes data to `name` under the pinned directory handle
// `parent`, replacing any existing entry atomically.
//
// Contract, and every clause is tested:
//
//	R-1 nothing is resolved from a path: `parent` is the anchor for the temp
//	    creation, the replace and the cleanup.
//	R-2 the temp name is unique and claimed create-only (FILE_CREATE), so two
//	    concurrent writers cannot share a temp.
//	R-3 the destination is NEVER unlinked. It goes straight from holding the
//	    complete old bytes to holding the complete new bytes.
//	R-4 on EVERY failure path the temp is removed through its own HANDLE, so
//	    cleanup cannot be redirected by a name substitution.
//	R-5 a transient failure is retried within a bound; past the bound the
//	    caller gets a ReplaceError and the destination still holds the old bytes.
func AtomicWriteFile(parent windows.Handle, name string, data []byte, pol ReplacePolicy) (WriteResult, error) {
	var res WriteResult
	if strings.ContainsAny(name, `\/`) {
		return res, fmt.Errorf("winspike: AtomicWriteFile requires a single component, got %q", name)
	}

	// --- claim a unique temp, create-only, relative to the pinned parent ---
	var (
		src windows.Handle
		err error
	)
	for i := 0; i < 100; i++ {
		res.Temp, err = newTempName()
		if err != nil {
			return res, err
		}
		src, err = CreateFileAt(parent, res.Temp)
		if !isExist(err) {
			break
		}
	}
	if err != nil {
		return res, fmt.Errorf("winspike: could not claim a temp file: %w", err)
	}

	// From here on the temp exists. `done` is the single switch that decides
	// whether the deferred cleanup removes it: it is set ONLY after a
	// successful replace, at which point `src` names the DESTINATION and
	// deleting through it would destroy exactly what we just wrote.
	done := false
	defer func() {
		if done {
			_ = windows.CloseHandle(src)
			return
		}
		// Remove by HANDLE, not by name: a name-based cleanup could be
		// redirected onto a substituted entry (A2), and would also fail if an
		// ancestor were renamed. The handle refers to the object we created.
		_ = DeleteByHandle(src, true)
		_ = windows.CloseHandle(src)
	}()

	runSpikeOpHook(OpTempCreated)

	if _, err = windows.Write(src, data); err != nil {
		return res, fmt.Errorf("winspike: writing the temp file: %w", err)
	}
	if pol.Flush {
		if err = windows.FlushFileBuffers(src); err != nil {
			return res, fmt.Errorf("winspike: flushing the temp file: %w", err)
		}
		res.Flushed = true
	}

	runSpikeOpHook(OpBeforeReplace)

	// --- the atomic replace, bounded ---
	start := time.Now()
	backoff := pol.InitialBackoff
	var last error
	for attempt := 1; attempt <= pol.MaxAttempts; attempt++ {
		res.Attempts = attempt
		last = RenameAtNT(src, parent, name, fileRenameInformationEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		if last != nil && isUnsupportedRenameClass(last) {
			// Windows 10 1709+ only, and not on every filesystem. The Go
			// standard library falls back from class 65 to class 10 the same
			// way; class 10 replaces atomically too, it just leaves the
			// replaced file delete-pending instead of removing it at once.
			legacy := RenameAtNT(src, parent, name, fileRenameInformation, fileRenameReplaceIfExists)
			if legacy == nil {
				res.UsedLegacy = true
				last = nil
			} else {
				last = legacy
			}
		}
		if last == nil {
			res.Elapsed = time.Since(start)
			done = true
			runSpikeOpHook(OpAfterReplace)
			return res, nil
		}
		if !IsRetryable(last) {
			res.Elapsed = time.Since(start)
			return res, last
		}
		if attempt == pol.MaxAttempts || time.Since(start) >= pol.TotalBudget {
			break
		}
		runSpikeOpHook(OpReplaceRetry)
		time.Sleep(backoff)
		if backoff *= 2; backoff > pol.MaxBackoff {
			backoff = pol.MaxBackoff
		}
	}
	res.Elapsed = time.Since(start)
	return res, &ReplaceError{Op: "notes write", Dest: name, Attempts: res.Attempts, Elapsed: res.Elapsed, Last: last}
}

// isUnsupportedRenameClass recognises the "this build or filesystem does not
// know FileRenameInformationEx" answers, which are the only ones the class-10
// fallback is allowed to cover. Everything else is a real failure.
func isUnsupportedRenameClass(err error) bool {
	st, ok := StatusOf(err)
	if !ok {
		return false
	}
	return st == windows.STATUS_INVALID_PARAMETER ||
		st == windows.STATUS_NOT_SUPPORTED ||
		st == windows.STATUS_INVALID_INFO_CLASS ||
		st == windows.STATUS_INVALID_DEVICE_REQUEST
}

// ---------------------------------------------------------------------------
// Recursive removal — the RR1 primitive.
// ---------------------------------------------------------------------------

// RemoveTreeAt is removeTreeAt (annotationfs_linux.go:174-213) ported, and the
// port deliberately does NOT translate the Linux classification.
//
// Linux decides with fstatat(AT_SYMLINK_NOFOLLOW) + S_IFDIR. The mechanical
// Windows translation of that is FILE_ATTRIBUTE_TAG_INFO + FILE_ATTRIBUTE_DIRECTORY,
// and it is WRONG: FILE_ATTRIBUTE_DIRECTORY is SET on a junction
// (P14.delete_attr_trap), so the translated walk descends through the link and
// destroys the target. That is RR1, the critical residual risk.
//
// The rule here replaces the classification with an operation:
//
//	attempt the no-follow directory open FIRST (OBJ_DONT_REPARSE);
//	its FAILURE is the classification.
//
// Nothing is decided from a separately-observed attribute, so there is no
// check-then-use window at all: if the entry is or becomes a reparse point,
// the open fails with STATUS_REPARSE_POINT_ENCOUNTERED and the entry is
// unlinked as a link. This is strictly stronger than classify-then-open, and
// it is the shape Phase 3 should copy.
func RemoveTreeAt(parent windows.Handle, name string) error {
	if strings.ContainsAny(name, `\/`) {
		return fmt.Errorf("winspike: RemoveTreeAt requires a single component, got %q", name)
	}
	h, err := OpenDirAt(parent, name)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		if isReparse(err) || isNotADir(err) {
			// Not a real directory. Remove the ENTRY, never its target: the
			// open below carries FILE_OPEN_REPARSE_POINT and no
			// FILE_DIRECTORY_FILE / FILE_NON_DIRECTORY_FILE constraint, so one
			// call covers a junction, a symlink of either flavour, an
			// unknown-tag directory and a plain file.
			return DeleteAt(parent, name, 0, true)
		}
		return err
	}
	entries, readErr := ReadDirHandle(h)
	if readErr == nil {
		for _, e := range entries {
			runSpikeOpHook(OpTreeEntry)
			if err := RemoveTreeAt(h, e.Name()); err != nil && !isNotExist(err) {
				readErr = err
				break
			}
		}
	}
	windows.CloseHandle(h)
	if readErr != nil {
		return readErr
	}
	return DeleteAt(parent, name, windows.FILE_DIRECTORY_FILE, true)
}

// removeTreeAtByAttributeUNSAFE is the NEGATIVE CONTROL and must never be
// used for anything but proving that RemoveTreeAt's test has teeth.
//
// It is the mechanical port of the Linux code: classify each entry from its
// attributes, recurse when FILE_ATTRIBUTE_DIRECTORY is set. On Windows that
// bit is set on a junction, so this function walks THROUGH a planted link and
// deletes the target tree. It is deliberately unexported.
func removeTreeAtByAttributeUNSAFE(parent windows.Handle, name string) error {
	at, err := StatAt(parent, name)
	if err != nil {
		if isNotExist(err) {
			return nil
		}
		return err
	}
	if !at.IsDir() {
		return DeleteAt(parent, name, 0, true)
	}
	// The defect, in one line: the reparse tag is never consulted, and the
	// open that follows does NOT carry OBJ_DONT_REPARSE, so it traverses.
	h, err := ntOpenAt(parent, name, dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return err
	}
	entries, readErr := ReadDirHandle(h)
	if readErr == nil {
		for _, e := range entries {
			if err := removeTreeAtByAttributeUNSAFE(h, e.Name()); err != nil && !isNotExist(err) {
				readErr = err
				break
			}
		}
	}
	windows.CloseHandle(h)
	if readErr != nil {
		return readErr
	}
	return DeleteAt(parent, name, windows.FILE_DIRECTORY_FILE, true)
}

// ---------------------------------------------------------------------------
// The deliberately-wrong write, kept next to the right one.
//
// removeThenRenameUNSAFE is what a port "simplifying" the atomic replace into
// two steps would look like, and it exists so the regression tests that guard
// against it can be shown to CATCH it. A test that never fails against a
// broken implementation is not a test.
// ---------------------------------------------------------------------------

func removeThenRenameUNSAFE(parent windows.Handle, name string, data []byte) error {
	tmp, err := newTempName()
	if err != nil {
		return err
	}
	src, err := CreateFileAt(parent, tmp)
	if err != nil {
		return err
	}
	if _, err := windows.Write(src, data); err != nil {
		windows.CloseHandle(src)
		return err
	}
	// The defect: the destination leaves the namespace here, and for the
	// duration of the next two calls the document does not exist.
	if err := DeleteAt(parent, name, windows.FILE_NON_DIRECTORY_FILE, true); err != nil && !isNotExist(err) {
		windows.CloseHandle(src)
		return err
	}
	err = RenameAtNT(src, parent, name, fileRenameInformationEx, fileRenamePosixSemantics)
	windows.CloseHandle(src)
	return err
}
