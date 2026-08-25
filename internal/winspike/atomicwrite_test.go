//go:build windows

package winspike

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P1.3 — atomic replacement, end to end.
//
// Every measurement below is against internal/winspike/atomicwrite.go, which
// is the Windows twin of annotationFS.writeFile. The Linux original is the
// specification; the questions are the three things Windows adds:
// share-mode vetoes, a retry bound, and durability.
// ---------------------------------------------------------------------------

// setSpikeOpHook installs the deterministic race hook for one test and
// guarantees it is cleared afterwards. Mirrors setStoreOpHook
// (internal/store/hook_test.go).
func setSpikeOpHook(t *testing.T, fn func(op string)) {
	t.Helper()
	spikeOpHook = fn
	t.Cleanup(func() { spikeOpHook = nil })
}

// onceHook fires fn the first time op matches and then disarms, so a hook
// installed for "the window before the replace" cannot re-enter during the
// retry loop.
func onceHook(want string, fn func()) func(string) {
	var once sync.Once
	return func(op string) {
		if op == want {
			once.Do(fn)
		}
	}
}

func readOrErr(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return string(b)
}

// tempsLeft lists the .notes-*.tmp residue in dir. The write path must leave
// none, on success or on any failure.
func tempsLeft(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	es, err := os.ReadDir(dir)
	if err != nil {
		return []string{"<readdir: " + err.Error() + ">"}
	}
	for _, e := range es {
		if strings.HasPrefix(e.Name(), ".notes-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestP13AnnotationWritePath is the happy path: create, replace, and the two
// structural guarantees (no temp residue, no removal of the destination).
func TestP13AnnotationWritePath(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	pol := DefaultReplacePolicy()
	dst := filepath.Join(dir, "notes.json")

	// (a) the destination does not exist yet.
	AuditStart()
	res, err := AtomicWriteFile(parent, "notes.json", []byte(`{"rev":1}`), pol)
	audit := AuditStop()
	Report(t, "P13.write_new", boolVerdict(err == nil && readOrErr(dst) == `{"rev":1}`),
		"first write through a pinned parent handle: err=%s temp=%q attempts=%d legacy=%v elapsed=%v; destination now %q; "+
			"namespace removals recorded: %v",
		DescribeErr(err), res.Temp, res.Attempts, res.UsedLegacy, res.Elapsed.Round(time.Microsecond), readOrErr(dst), audit)

	// (b) the destination exists and is replaced.
	AuditStart()
	res, err = AtomicWriteFile(parent, "notes.json", []byte(`{"rev":2}`), pol)
	audit = AuditStop()
	replaced := err == nil && readOrErr(dst) == `{"rev":2}`
	Report(t, "P13.replace_existing", boolVerdict(replaced),
		"replace over an existing destination: err=%s attempts=%d legacy=%v; destination now %q",
		DescribeErr(err), res.Attempts, res.UsedLegacy, readOrErr(dst))
	RequireProperty(t, "P13.replace_existing", replaced,
		"NtSetInformationFile(FileRenameInformationEx, RootDirectory=parent, REPLACE|POSIX) must replace the destination "+
			"atomically through the pinned parent handle (err=%s, content=%q)", DescribeErr(err), readOrErr(dst))

	// (c) the destination is never REMOVED. This is the audit half of the
	//     never-degrade-to-remove-then-rename proof; the black-box half is
	//     TestP13NeverRemoveThenRename.
	destRemoved := false
	for _, a := range audit {
		if strings.EqualFold(a.Name, "notes.json") {
			destRemoved = true
		}
	}
	RequireProperty(t, "P13.no_dest_removal", !destRemoved,
		"a successful replace must issue ZERO namespace removals naming the destination; recorded %v", audit)

	// (d) no temp residue.
	left := tempsLeft(t, dir)
	RequireProperty(t, "P13.temp_cleaned", len(left) == 0,
		"the write path must leave no .notes-*.tmp residue; found %v", left)

	// (e) the temp name is unique per call and dot-prefixed, so it can never
	//     collide with a document name or appear in a listing.
	seen := map[string]bool{}
	dup := ""
	for i := 0; i < 64; i++ {
		n, _ := newTempName()
		if seen[n] {
			dup = n
		}
		seen[n] = true
	}
	Report(t, "P13.temp_unique", boolVerdict(dup == "" && len(seen) == 64),
		"64 generated temp names, %d distinct, duplicate=%q, shape=%q (8 random bytes, same as annotationfs_linux.go:110)",
		len(seen), dup, res.Temp)
}

// TestP13CleanupOnFailurePaths drives the failure paths and asserts the two
// invariants that matter after each: no residue, and the destination still
// holds its complete previous content.
func TestP13CleanupOnFailurePaths(t *testing.T) {
	pol := DefaultReplacePolicy()

	// (a) a PERMANENT replace failure: the destination is a directory, which
	//     a file rename can never replace.
	t.Run("permanent_failure", func(t *testing.T) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		mustMkdir(t, filepath.Join(dir, "notes.json"))

		_, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW"), pol)
		left := tempsLeft(t, dir)
		_, statErr := os.Stat(filepath.Join(dir, "notes.json"))
		Report(t, "P13.permanent_failure", boolVerdict(werr != nil && len(left) == 0),
			"replacing a DIRECTORY-shaped destination -> %s ; retryable=%v ; temp residue %v ; destination still present=%v",
			DescribeErr(werr), IsRetryable(werr), left, statErr == nil)
		RequireProperty(t, "P13.cleanup_permanent", len(left) == 0,
			"a permanently failing replace must still remove its temp file; residue %v", left)
	})

	// (b) the retry bound is exhausted: a blocker holds the destination for
	//     longer than the budget.
	t.Run("bound_exhausted", func(t *testing.T) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		dst := filepath.Join(dir, "notes.json")
		mustWrite(t, dst, "COMPLETE-OLD")

		dp, _ := windows.UTF16PtrFromString(dst)
		blocker, berr := windows.CreateFile(dp, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if berr != nil {
			Report(t, "P13.bound", NotMeasured, "could not open the blocking handle: %s", DescribeErr(berr))
			return
		}

		start := time.Now()
		res, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW"), pol)
		elapsed := time.Since(start)
		windows.CloseHandle(blocker)

		var re *ReplaceError
		isReplaceErr := errors.As(werr, &re)
		left := tempsLeft(t, dir)
		content := readOrErr(dst)

		Report(t, "P13.bound", boolVerdict(isReplaceErr),
			"policy{attempts=%d backoff=%v..%v budget=%v}: a destination held WITHOUT FILE_SHARE_DELETE for the whole bound "+
				"produced %d attempt(s) over %v (wall %v) and the terminal error is a *ReplaceError=%v. Message the user sees: %q",
			pol.MaxAttempts, pol.InitialBackoff, pol.MaxBackoff, pol.TotalBudget,
			res.Attempts, res.Elapsed.Round(time.Millisecond), elapsed.Round(time.Millisecond), isReplaceErr, fmt.Sprint(werr))

		RequireProperty(t, "P13.bound_preserves_dest", content == "COMPLETE-OLD",
			"after the retry bound is exhausted the destination must still hold its COMPLETE previous content, "+
				"never a truncated or absent file; found %q", content)
		RequireProperty(t, "P13.cleanup_bound", len(left) == 0,
			"a bound-exhausted replace must still remove its temp file; residue %v", left)
		Report(t, "P13.bound_terminates", boolVerdict(elapsed < 5*time.Second),
			"the loop terminated in %v, well inside a request timeout (the budget is %v and each individual attempt is "+
				"non-blocking: a share-mode veto fails immediately rather than waiting)", elapsed.Round(time.Millisecond), pol.TotalBudget)
	})

	// (c) cleanup is HANDLE-based: the temp is removed through its own handle,
	//     so an attacker who RENAMES the temp between the write and the
	//     replace cannot leave residue behind. A name-based unlinkat-style
	//     cleanup would miss it and leave the renamed file in the store.
	t.Run("cleanup_survives_temp_rename", func(t *testing.T) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		// Make the replace fail permanently so the cleanup path runs.
		mustMkdir(t, filepath.Join(dir, "notes.json"))

		stolen := ""
		setSpikeOpHook(t, onceHook(OpBeforeReplace, func() {
			es, e := os.ReadDir(dir)
			if e != nil {
				return
			}
			for _, entry := range es {
				if strings.HasPrefix(entry.Name(), ".notes-") {
					if os.Rename(filepath.Join(dir, entry.Name()), filepath.Join(dir, "stolen.tmp")) == nil {
						stolen = entry.Name()
					}
					return
				}
			}
		}))

		_, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW"), pol)
		if stolen == "" {
			Report(t, "P13.cleanup_handle_based", NotMeasured, "could not rename the temp out from under the write")
			return
		}
		_, stolenLeft := os.Stat(filepath.Join(dir, "stolen.tmp"))
		left := tempsLeft(t, dir)
		clean := stolenLeft != nil && len(left) == 0
		Report(t, "P13.cleanup_handle_based", boolVerdict(clean),
			"the temp %q was RENAMED to stolen.tmp between the write and the replace, then the replace failed: write err=%v ; "+
				"stolen.tmp still present=%v ; .notes-* residue %v. Cleanup goes through the temp's own HANDLE "+
				"(DeleteByHandle), which follows the object through the rename; a name-based cleanup would have unlinked "+
				"nothing and left an orphan file inside the store.",
			stolen, werr != nil, stolenLeft == nil, left)
		RequireProperty(t, "P13.cleanup_handle_based", clean,
			"handle-based cleanup must remove the temp even after it has been renamed (stolen.tmp present=%v, residue %v)",
			stolenLeft == nil, left)
	})

	// (d) a fact discovered while staging (c): an open child handle blocks the
	//     rename of its parent directory, even though the child granted the
	//     full share mode. Recorded because it NARROWS the A2 ancestor race
	//     during an in-flight write — and must not be relied on.
	t.Run("open_child_blocks_parent_rename", func(t *testing.T) {
		base := scratchDir(t)
		rootPath := mustMkdir(t, filepath.Join(base, "store"))
		r, err := OpenRoot(rootPath)
		if err != nil {
			t.Fatalf("OpenRoot: %s", DescribeErr(err))
		}
		defer r.Close()
		mustMkdir(t, filepath.Join(rootPath, "proj"))
		parent, err := r.OpenRealDir([]string{"proj"}, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)

		empty := os.Rename(filepath.Join(rootPath, "proj"), filepath.Join(rootPath, "moved1"))
		if empty == nil {
			_ = os.Rename(filepath.Join(rootPath, "moved1"), filepath.Join(rootPath, "proj"))
		}
		child, cerr := CreateFileAt(parent, "child.tmp")
		withChild := error(nil)
		if cerr == nil {
			withChild = os.Rename(filepath.Join(rootPath, "proj"), filepath.Join(rootPath, "moved2"))
			windows.CloseHandle(child)
		}
		afterClose := os.Rename(filepath.Join(rootPath, "proj"), filepath.Join(rootPath, "moved3"))
		Report(t, "P13.open_child_blocks_parent_rename", Info,
			"renaming a pinned project directory with NO open children -> %v ; with ONE open child file (opened by us with "+
				"FILE_SHARE_READ|WRITE|DELETE) -> %v ; after that child was closed -> %v. So an in-flight annotation write "+
				"itself narrows the A2 ancestor-substitution window, because the temp file's handle blocks the rename. "+
				"This is a HAPPY ACCIDENT and must not be relied on: it disappears the instant the temp is closed, and it "+
				"does not apply to any operation that holds no child handle.",
			empty, withChild, afterClose)
	})
}

// TestP13SharingModes is the case the spec singles out: "Windows file
// replacement differs when a destination is open". All eight share masks, for
// both a reading and a writing interferer, against all three mutations the
// store performs on a destination.
func TestP13SharingModes(t *testing.T) {
	names := func(share uint32) string {
		if share == 0 {
			return "NONE"
		}
		var p []string
		if share&windows.FILE_SHARE_READ != 0 {
			p = append(p, "READ")
		}
		if share&windows.FILE_SHARE_WRITE != 0 {
			p = append(p, "WRITE")
		}
		if share&windows.FILE_SHARE_DELETE != 0 {
			p = append(p, "DELETE")
		}
		return strings.Join(p, "|")
	}

	type opFn func(parent windows.Handle, dir string) error
	ops := []struct {
		id string
		fn opFn
	}{
		{"renameEx", func(parent windows.Handle, dir string) error {
			src, err := CreateFileAt(parent, "t.tmp")
			if err != nil {
				return err
			}
			defer windows.CloseHandle(src)
			windows.Write(src, []byte("NEW"))
			err = RenameAtNT(src, parent, "dst.json", fileRenameInformationEx,
				fileRenameReplaceIfExists|fileRenamePosixSemantics)
			if err != nil {
				DeleteByHandle(src, true)
			}
			return err
		}},
		{"renameLegacy", func(parent windows.Handle, dir string) error {
			src, err := CreateFileAt(parent, "t2.tmp")
			if err != nil {
				return err
			}
			defer windows.CloseHandle(src)
			windows.Write(src, []byte("NEW"))
			err = RenameAtNT(src, parent, "dst.json", fileRenameInformation, fileRenameReplaceIfExists)
			if err != nil {
				DeleteByHandle(src, true)
			}
			return err
		}},
		{"posixDelete", func(parent windows.Handle, dir string) error {
			return DeleteAt(parent, "dst.json", windows.FILE_NON_DIRECTORY_FILE, true)
		}},
	}

	accesses := []struct {
		id   string
		mask uint32
	}{
		{"GENERIC_READ", windows.GENERIC_READ},
		{"GENERIC_WRITE", windows.GENERIC_WRITE},
	}

	for _, op := range ops {
		for _, acc := range accesses {
			var rows []string
			for share := uint32(0); share <= 7; share++ {
				r, dir := openScratchRoot(t)
				parent, err := r.OpenRealDir(nil, false, false)
				if err != nil {
					t.Fatalf("OpenRealDir: %s", DescribeErr(err))
				}
				dst := filepath.Join(dir, "dst.json")
				mustWrite(t, dst, "OLD")

				dp, _ := windows.UTF16PtrFromString(dst)
				blocker, berr := windows.CreateFile(dp, acc.mask, share, nil,
					windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
				if berr != nil {
					rows = append(rows, fmt.Sprintf("%s: blocker-open-failed(%v)", names(share), berr))
					windows.CloseHandle(parent)
					continue
				}
				opErr := op.fn(parent, dir)
				retry := IsRetryable(opErr)
				windows.CloseHandle(blocker)
				content := readOrErr(dst)
				windows.CloseHandle(parent)

				status := "ok"
				if opErr != nil {
					if st, ok := StatusOf(opErr); ok {
						status = fmt.Sprintf("0x%08X(%s)", uint32(st), st.Error())
					} else {
						status = opErr.Error()
					}
				}
				rows = append(rows, fmt.Sprintf("%s -> %s retryable=%v dst=%q", names(share), status, retry, content))
			}
			Report(t, "P13.sharing."+op.id+"."+acc.id, Info,
				"destination held with %s, by share mask: %s", acc.id, strings.Join(rows, " | "))
		}
	}

	// The one-line answer the ADR needs, asserted rather than eyeballed.
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)
	dst := filepath.Join(dir, "dst.json")
	mustWrite(t, dst, "COMPLETE-OLD")
	dp, _ := windows.UTF16PtrFromString(dst)
	blocker, berr := windows.CreateFile(dp, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if berr != nil {
		Report(t, "P13.sharing_summary", NotMeasured, "blocker: %s", DescribeErr(berr))
		return
	}
	_, werr := AtomicWriteFile(parent, "dst.json", []byte("NEW"), ReplacePolicy{MaxAttempts: 1, TotalBudget: time.Second})
	windows.CloseHandle(blocker)
	content := readOrErr(dst)
	Report(t, "P13.sharing_summary", Info,
		"a destination opened WITHOUT FILE_SHARE_DELETE vetoes the replace (err=%v); the destination is left holding %q. "+
			"An opener that grants FILE_SHARE_DELETE does not veto it. This is the whole rule: the interferer's share mask, "+
			"not its access mask, decides.", werr != nil, content)
	RequireProperty(t, "P13.sharing_never_truncates", content == "COMPLETE-OLD",
		"a vetoed replace must leave the destination byte-identical, never truncated or absent; found %q", content)
}

// TestP13RetryBound measures the deterministic half of M13: a blocker that is
// released on a timer, so the retry loop's ability to ride out a transient
// veto is measured rather than assumed.
func TestP13RetryBound(t *testing.T) {
	for _, hold := range []time.Duration{20 * time.Millisecond, 400 * time.Millisecond} {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		dst := filepath.Join(dir, "notes.json")
		mustWrite(t, dst, "OLD")
		dp, _ := windows.UTF16PtrFromString(dst)
		blocker, berr := windows.CreateFile(dp, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if berr != nil {
			Report(t, "P13.retry", NotMeasured, "blocker: %s", DescribeErr(berr))
			windows.CloseHandle(parent)
			continue
		}
		go func(h windows.Handle, d time.Duration) {
			time.Sleep(d)
			windows.CloseHandle(h)
		}(blocker, hold)

		res, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW"), DefaultReplacePolicy())
		content := readOrErr(dst)
		windows.CloseHandle(parent)
		Report(t, fmt.Sprintf("P13.retry.hold%dms", hold.Milliseconds()), boolVerdict(werr == nil),
			"blocker released after %v: replace err=%s after %d attempt(s) over %v; destination now %q. "+
				"(2ms→256ms doubling, 10 attempts, 2s budget: 766ms of retrying in the worst case, so a veto lasting under "+
				"about three quarters of a second is ridden out and anything longer becomes an actionable error.)",
			hold, DescribeErr(werr), res.Attempts, res.Elapsed.Round(time.Millisecond), content)
		RequireProperty(t, fmt.Sprintf("P13.retry_integrity.hold%dms", hold.Milliseconds()),
			content == "OLD" || content == "NEW",
			"whatever the retry outcome, the destination must hold one COMPLETE version, never a partial one; found %q", content)
	}

	// Which status does a delete-pending destination actually produce? This
	// decides whether STATUS_ACCESS_DENIED belongs in the retryable set.
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)
	dst := filepath.Join(dir, "pending.json")
	mustWrite(t, dst, "OLD")
	// Legacy disposition on a handle we keep open leaves the name delete-pending.
	victim, verr := OpenForDeleteAt(parent, "pending.json", windows.FILE_NON_DIRECTORY_FILE)
	if verr != nil {
		Report(t, "M13.pending_status", NotMeasured, "OpenForDeleteAt: %s", DescribeErr(verr))
		return
	}
	if derr := DeleteByHandle(victim, false); derr != nil {
		windows.CloseHandle(victim)
		Report(t, "M13.pending_status", NotMeasured, "legacy disposition: %s", DescribeErr(derr))
		return
	}
	src, cerr := CreateFileAt(parent, "p.tmp")
	if cerr != nil {
		windows.CloseHandle(victim)
		Report(t, "M13.pending_status", NotMeasured, "CreateFileAt: %s", DescribeErr(cerr))
		return
	}
	windows.Write(src, []byte("NEW"))
	renameErr := RenameAtNT(src, parent, "pending.json", fileRenameInformationEx,
		fileRenameReplaceIfExists|fileRenamePosixSemantics)
	openErr := func() error {
		h, e := OpenRegularFileAt(parent, "pending.json")
		if e == nil {
			windows.CloseHandle(h)
		}
		return e
	}()
	DeleteByHandle(src, true)
	windows.CloseHandle(src)
	windows.CloseHandle(victim)
	Report(t, "M13.pending_status", Info,
		"with the destination legacy-delete-PENDING: NT rename -> %s ; a fresh open of the same name -> %s. "+
			"The NT layer keeps STATUS_DELETE_PENDING distinct (Win32 collapses it to ERROR_ACCESS_DENIED), which is why "+
			"STATUS_DELETE_PENDING is in the retryable set and STATUS_ACCESS_DENIED is NOT: at this layer ACCESS_DENIED can "+
			"only be an ACL denial, and retrying it would add the full budget of latency to a permanent failure.",
		DescribeErr(renameErr), DescribeErr(openErr))
}

// TestP13NeverRemoveThenRename is the regression test the task asks for: it
// must FAIL if someone later reimplements the replace as unlink+rename.
//
// It has two independent halves and, crucially, a NEGATIVE CONTROL for each.
// A guard that never fires against a broken implementation is not a guard, so
// the same instrument is pointed at removeThenRenameUNSAFE and must catch it.
func TestP13NeverRemoveThenRename(t *testing.T) {
	// --- half 1: the namespace-removal audit (white box, deterministic) ---
	t.Run("audit", func(t *testing.T) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		mustWrite(t, filepath.Join(dir, "notes.json"), "OLD")

		AuditStart()
		_, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW"), DefaultReplacePolicy())
		good := AuditStop()

		mustWrite(t, filepath.Join(dir, "control.json"), "OLD")
		AuditStart()
		cerr := removeThenRenameUNSAFE(parent, "control.json", []byte("NEW"))
		bad := AuditStop()

		names := func(es []AuditEntry, want string) bool {
			for _, e := range es {
				if strings.EqualFold(e.Name, want) {
					return true
				}
			}
			return false
		}
		goodRemoves := names(good, "notes.json")
		badRemoves := names(bad, "control.json")

		Report(t, "P13.audit_control", boolVerdict(badRemoves),
			"NEGATIVE CONTROL: the deliberately-wrong remove-then-rename implementation recorded %v (err=%v), and the audit "+
				"DID see it remove the destination = %v. If this were false the audit would be worthless and the guard below "+
				"would be vacuous.", bad, cerr, badRemoves)
		RequireProperty(t, "P13.audit", !goodRemoves && werr == nil,
			"AtomicWriteFile must perform zero namespace removals naming the destination (recorded %v, err=%v)", good, werr)
	})

	// --- half 2: the kernel's own change records (black box, deterministic) ---
	t.Run("change_records", func(t *testing.T) {
		observe := func(dir string, run func() error) ([]DirAction, error) {
			o, err := ObserveDir(dir)
			if err != nil {
				return nil, err
			}
			defer o.Close()
			// Prime: prove the read loop is live before the operation, so no
			// record can be lost at startup.
			mustWrite(t, filepath.Join(dir, ".prime"), "x")
			if !o.WaitFor(".prime", 10*time.Second) {
				return nil, fmt.Errorf("observer never saw the priming change (err %v)", o.Err())
			}
			o.Reset()
			runErr := run()
			mustWrite(t, filepath.Join(dir, ".sentinel"), "x")
			if !o.WaitFor(".sentinel", 10*time.Second) {
				return nil, fmt.Errorf("observer never saw the sentinel (err %v)", o.Err())
			}
			acts := o.Actions()
			if runErr != nil {
				return acts, fmt.Errorf("operation: %w", runErr)
			}
			return acts, nil
		}
		removedDest := func(acts []DirAction, dest string) bool {
			for _, a := range acts {
				if a.Action == windows.FILE_ACTION_REMOVED && strings.EqualFold(a.Name, dest) {
					return true
				}
			}
			return false
		}

		// The real implementation.
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		mustWrite(t, filepath.Join(dir, "notes.json"), "OLD")
		goodActs, gerr := observe(dir, func() error {
			_, e := AtomicWriteFile(parent, "notes.json", []byte("NEW"), DefaultReplacePolicy())
			return e
		})
		if gerr != nil {
			Report(t, "P13.change_records", NotMeasured, "observer: %v", gerr)
			return
		}

		// The negative control, in its own directory so the two record streams
		// cannot be confused.
		r2, dir2 := openScratchRoot(t)
		parent2, err := r2.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent2)
		mustWrite(t, filepath.Join(dir2, "notes.json"), "OLD")
		badActs, berr := observe(dir2, func() error {
			return removeThenRenameUNSAFE(parent2, "notes.json", []byte("NEW"))
		})
		if berr != nil {
			Report(t, "P13.change_records", NotMeasured, "control observer: %v", berr)
			return
		}

		goodRemoved := removedDest(goodActs, "notes.json")
		badRemoved := removedDest(badActs, "notes.json")
		same := fmt.Sprint(goodActs) == fmt.Sprint(badActs)

		Report(t, "P13.change_records", boolVerdict(!goodRemoved),
			"INSTRUMENT INSUFFICIENT, and the reason is a finding. AtomicWriteFile produced %v ; the deliberately-wrong "+
				"remove-then-rename produced %v ; identical apart from the temp name = %v (both contain "+
				"FILE_ACTION_REMOVED naming the destination = %v / %v). "+
				"A POSIX-semantics rename that REPLACES a destination makes the kernel emit FILE_ACTION_REMOVED for the "+
				"replaced file as part of the atomic rename, so ReadDirectoryChangesW CANNOT distinguish an atomic replace "+
				"from unlink+rename. The hypothesis that it could is withdrawn; the guard against the degradation is the "+
				"namespace-removal audit (P13.audit) and the continuous-existence observer (P13.continuous_existence).",
			goodActs, badActs, same, goodRemoved, badRemoved)

		Report(t, "P13.watch_sees_removed_on_replace", Info,
			"CONSEQUENCE FOR internal/watch: every notes save emits REMOVED(<doc>) immediately followed by "+
				"RENAMED_NEW_NAME(<doc>) and MODIFIED(<doc>). fsnotify maps those to Remove then Create then Write. A "+
				"watcher that reacts to Remove by dropping state for the document — or a UI that reacts by hiding it — will "+
				"flicker on every save. The 250ms debounce in internal/watch absorbs this today on Linux (where a rename "+
				"emits no unlink at all); on Windows the debounce is doing real work and must not be removed.")

	})

	// --- half 3: continuous resolvability (empirical corroboration) ---
	t.Run("continuous_existence", func(t *testing.T) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		mustWrite(t, filepath.Join(dir, "notes.json"), "OLD")

		poll := func(run func(i int) error) (polls, gaps int) {
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
					}
					polls++
					if _, e := StatAt(parent, "notes.json"); e != nil && isNotExist(e) {
						gaps++
					}
				}
			}()
			for i := 0; i < 200; i++ {
				_ = run(i)
			}
			close(stop)
			wg.Wait()
			return
		}

		goodPolls, goodGaps := poll(func(i int) error {
			_, e := AtomicWriteFile(parent, "notes.json", []byte(fmt.Sprintf("v%d", i)), DefaultReplacePolicy())
			return e
		})
		badPolls, badGaps := poll(func(i int) error {
			return removeThenRenameUNSAFE(parent, "notes.json", []byte(fmt.Sprintf("v%d", i)))
		})
		Report(t, "P13.continuous_existence", boolVerdict(goodGaps == 0),
			"a concurrent reader polled the destination name during 200 replaces: %d poll(s), %d observation(s) of "+
				"'does not exist'. NEGATIVE CONTROL (remove-then-rename, 200 replaces): %d poll(s), %d gap(s). "+
				"With the control failing on roughly half of all polls, this is the black-box discriminator that "+
				"ReadDirectoryChangesW turned out not to be: it is empirical rather than deterministic, but the margin is "+
				"not a close call.", goodPolls, goodGaps, badPolls, badGaps)
		if badGaps > 0 {
			RequireProperty(t, "P13.continuous_existence", goodGaps == 0,
				"the destination name must resolve at every instant during a replace; a concurrent reader saw it absent "+
					"%d time(s) out of %d polls (the same observer caught the wrong implementation %d time(s) out of %d)",
				goodGaps, goodPolls, badGaps, badPolls)
		} else {
			Report(t, "P13.continuous_existence", NotMeasured,
				"the negative control produced no gaps on this runner, so the observer cannot be shown to discriminate and "+
					"the assertion is withheld rather than reported as passing")
		}
	})
}

// TestP13Durability answers the flush question with a number rather than an
// opinion.
func TestP13Durability(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)
	_ = dir

	payload := make([]byte, 4096) // a realistic notes sidecar
	for i := range payload {
		payload[i] = byte('a' + i%26)
	}
	const n = 100

	bench := func(flush bool) time.Duration {
		pol := DefaultReplacePolicy()
		pol.Flush = flush
		start := time.Now()
		for i := 0; i < n; i++ {
			if _, err := AtomicWriteFile(parent, "bench.json", payload, pol); err != nil {
				t.Fatalf("AtomicWriteFile(flush=%v): %s", flush, DescribeErr(err))
			}
		}
		return time.Since(start) / n
	}

	noFlush := bench(false)
	withFlush := bench(true)
	Report(t, "P13.flush_cost", Info,
		"%d× (create temp + write 4 KiB + atomic replace): without FlushFileBuffers %v/op, with it %v/op (%.1f× ). "+
			"Recommendation: DO NOT flush by default. annotationfs_linux.go's writeFile does not fsync either, so flushing "+
			"would make Windows strictly MORE durable than the Linux backend rather than reaching parity, at this cost per "+
			"note save. What the atomic replace already guarantees without any flush is the property that matters: the "+
			"destination name always resolves to one COMPLETE version. What it does not guarantee is which version survives "+
			"a power loss — and a lost note revision is recoverable by the user, a torn one is not.",
		n, noFlush.Round(time.Microsecond), withFlush.Round(time.Microsecond),
		float64(withFlush)/float64(max(int64(noFlush), 1)))

	Report(t, "P13.flush_scope", Info,
		"there is no directory-fsync question on Windows: the rename is a metadata operation on the parent's index and "+
			"NTFS journals it, so no separate 'flush the directory' step exists to forget. FlushFileBuffers on the TEMP's "+
			"handle before the replace is the only durability knob, and it is a one-line policy flag (ReplacePolicy.Flush).")
}

// TestP13GoStdlibShareMode measures a fact that is easy to miss and expensive
// to discover late: Go's own os.Open on Windows does NOT grant
// FILE_SHARE_DELETE.
//
//	sharemode := uint32(FILE_SHARE_READ | FILE_SHARE_WRITE)
//	$GOROOT/src/syscall/syscall_windows.go:395
//
// R15 requires FILE_SHARE_READ|WRITE|DELETE everywhere. Any place the store
// hands out an *os.File opened by path — OpenDocument serving a document over
// HTTP is the obvious one — therefore vetoes concurrent replaces and deletes
// of that file for as long as the response is being written.
func TestP13GoStdlibShareMode(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	probe := func(open func(string) (func(), error)) (string, string) {
		dst := filepath.Join(dir, "doc.html")
		mustWrite(t, dst, "OLD")
		closer, oerr := open(dst)
		if oerr != nil {
			return "open-failed(" + DescribeErr(oerr) + ")", ""
		}
		_, werr := AtomicWriteFile(parent, "doc.html", []byte("NEW"), ReplacePolicy{MaxAttempts: 1, TotalBudget: time.Second})
		delErr := DeleteAt(parent, "doc.html", windows.FILE_NON_DIRECTORY_FILE, true)
		closer()
		_ = os.Remove(dst)
		return DescribeErr(werr), DescribeErr(delErr)
	}

	goReplace, goDelete := probe(func(p string) (func(), error) {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		return func() { f.Close() }, nil
	})
	ourReplace, ourDelete := probe(func(p string) (func(), error) {
		u, _ := windows.UTF16PtrFromString(p)
		h, err := windows.CreateFile(u, windows.GENERIC_READ, shareAll, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			return nil, err
		}
		return func() { windows.CloseHandle(h) }, nil
	})

	blocked := !strings.HasPrefix(goReplace, "nil")
	Report(t, "P13.go_share_mode", boolVerdict(blocked),
		"with the file held open by os.Open: atomic replace -> %s ; POSIX delete -> %s. "+
			"With the SAME file held by a CreateFile that grants FILE_SHARE_READ|WRITE|DELETE: replace -> %s ; delete -> %s. "+
			"Go's syscall.Open hard-codes FILE_SHARE_READ|FILE_SHARE_WRITE ($GOROOT/src/syscall/syscall_windows.go:395) and "+
			"omits FILE_SHARE_DELETE, so R15's \"FILE_SHARE_DELETE everywhere\" is NOT satisfied by using os.Open. "+
			"CONSEQUENCE: OpenDocument's Windows twin must not return an *os.File opened by os.Open — while the web server "+
			"is streaming a document, a concurrent notes replace or Delete of that document would fail with a sharing "+
			"violation and burn the whole retry bound. os.NewFile over a handle WE opened with the full share mode is the fix, "+
			"and it is the same wrapper the handle-anchored design already needs.",
		goReplace, goDelete, ourReplace, ourDelete)
	RequireProperty(t, "P13.share_all_reader_does_not_veto", strings.HasPrefix(ourReplace, "nil"),
		"a reader that grants the full share mode must NOT veto the atomic replace (got %s)", ourReplace)
}
