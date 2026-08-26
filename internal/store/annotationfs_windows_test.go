//go:build windows

package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P3.7-P3.10 permanent tests, migrating the required properties ADR §11.1
// assigns to these tasks out of internal/winspike before it was deleted
// (P3.11/P3.12 own the rest of the inventory; see EXECUTION.md for the
// scoped-down items this file does not attempt — an "unknown tag" watch
// flavour needs raw reparse-buffer helpers this package does not expose).
// ---------------------------------------------------------------------------

// openScratchDir creates and opens a fresh, plain project-style directory
// directly under a throwaway test root, for exercising atomicWriteFileAt/
// removeTreeAt in isolation from the annotation tree's own locking — the
// same shape as internal/winspike/atomicwrite_test.go's openScratchRoot.
func openScratchDir(t *testing.T) (parent int, dir string) {
	t.Helper()
	root := testRoot(t)
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	t.Cleanup(func() { rfs.close() })
	if err := mkdirClaim(int(rfs.root.Fd()), "scratch"); err != nil {
		t.Fatalf("mkdirClaim(scratch): %v", err)
	}
	fd, err := openRealDirAt(int(rfs.root.Fd()), "scratch")
	if err != nil {
		t.Fatalf("openRealDirAt(scratch): %v", err)
	}
	t.Cleanup(func() { closeFD(fd) })
	return fd, filepath.Join(root, "scratch")
}

// tempResidue lists the .notes-*.tmp entries left in dir. The write path
// must leave none, on success or on any failure path.
func tempResidue(t *testing.T, dir string) []string {
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

// makeJunctionAt creates a JUNCTION (not a symlink) at (parent, name)
// pointing at target, bypassing symlinkAt's symlink-first attempt so the
// junction flavour is exercised deterministically regardless of the
// runner's privilege/Developer-Mode state (§6.6's measured privilege table:
// junctions are the only flavour available with Developer Mode off). It
// returns an error rather than calling testing.T directly so it is safe to
// call from inside a deterministic-race hook invoked deep within
// production recursion (calling t.Fatal there would runtime.Goexit mid
// walk, which is not what the hook is meant to simulate).
func makeJunctionAt(parent int, name, target string) error {
	if err := mkdirClaim(parent, name); err != nil {
		return fmt.Errorf("mkdirClaim(%q): %w", name, err)
	}
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return fmt.Errorf("open new junction %q: %w", name, err)
	}
	defer windows.CloseHandle(h)
	if err := setMountPointReparse(h, `\??\`+target, target); err != nil {
		return fmt.Errorf("setMountPointReparse(%q): %w", name, err)
	}
	return nil
}

// makeJunctionForTest is makeJunctionAt for call sites on the test's own
// goroutine, where t.Fatalf is safe.
func makeJunctionForTest(t *testing.T, parent int, name, target string) {
	t.Helper()
	if err := makeJunctionAt(parent, name, target); err != nil {
		t.Fatal(err)
	}
}

// removeThenRenameUnsafeForTest is the deliberately-wrong decomposition
// atomicWriteFileAt must never become: unlink the destination BY NAME, then
// rename the temp into place. It exists ONLY to prove the namespace-removal
// audit and the continuous-existence observer below have teeth — a guard
// that never fires against a broken implementation is not a guard
// (migrated from internal/winspike/atomicwrite.go's removeThenRenameUNSAFE).
func removeThenRenameUnsafeForTest(parent int, name string, data []byte) error {
	tmp, err := newAnnotationTempName()
	if err != nil {
		return err
	}
	h, err := ntOpenAt(windows.Handle(parent), tmp, windows.FILE_GENERIC_WRITE|windows.DELETE,
		windows.FILE_CREATE, windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(h), tmp)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	// THE DEFECT: the destination leaves the namespace HERE, by name, for
	// the duration of the rename below.
	if err := deleteEntryAt(parent, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		f.Close()
		return err
	}
	err = renameAtNT(windows.Handle(f.Fd()), windows.Handle(parent), name, fileRenameInformationEx, fileRenamePosixSemantics)
	f.Close()
	return err
}

// removeTreeAtByAttributeUnsafeForTest is the NEGATIVE CONTROL for
// removeTreeAt's operation-as-classification shape (ADR §4.5, F3): it is the
// mechanical translation of "classify from FILE_ATTRIBUTE_DIRECTORY alone,
// then open" — the shape revision 1 of the ADR specified and the prototype's
// own A6.negative_control (removeTreeAtByAttributeUNSAFE) exists to refute.
// FILE_ATTRIBUTE_DIRECTORY is SET on a junction (P14.delete_attr_trap), and
// the follow-up open below carries neither OBJ_DONT_REPARSE nor
// FILE_OPEN_REPARSE_POINT, so it TRAVERSES the junction transparently. This
// function must never be used for anything but proving
// TestRemoveTreeAtSwapMidWalkAndNegativeControl has teeth.
func removeTreeAtByAttributeUnsafeForTest(parent int, name string) error {
	h0, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		if errors.Is(translateOpen("stat", err), fs.ErrNotExist) {
			return nil
		}
		return err
	}
	var basic windows.ByHandleFileInformation
	attrErr := windows.GetFileInformationByHandle(h0, &basic)
	windows.CloseHandle(h0)
	if attrErr != nil {
		return attrErr
	}
	if basic.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return deleteEntryAt(parent, name)
	}
	// THE TRAP: opened with FILE_DIRECTORY_FILE only — no no-follow flag at
	// all — so a junction here is followed straight into its target.
	wh, err := ntOpenAt(windows.Handle(parent), name, dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return err
	}
	h := int(wh)
	entries, readErr := readDirFD(h)
	if readErr == nil {
		for _, e := range entries {
			if err := removeTreeAtByAttributeUnsafeForTest(h, e.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
				readErr = err
				break
			}
		}
	}
	closeFD(h)
	if readErr != nil {
		return readErr
	}
	return rmdirAt(parent, name)
}

// ---------------------------------------------------------------------------
// P3.8: the atomic write.
// ---------------------------------------------------------------------------

func TestAtomicWriteFileHappyPath(t *testing.T) {
	parent, dir := openScratchDir(t)

	if err := atomicWriteFileAt(parent, "notes.json", []byte(`{"rev":1}`)); err != nil {
		t.Fatalf("first write: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "notes.json"))
	if err != nil || string(got) != `{"rev":1}` {
		t.Fatalf("after first write: content=%q err=%v", got, err)
	}

	if err := atomicWriteFileAt(parent, "notes.json", []byte(`{"rev":2}`)); err != nil {
		t.Fatalf("replace: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(dir, "notes.json"))
	if err != nil || string(got) != `{"rev":2}` {
		t.Fatalf("after replace: content=%q err=%v", got, err)
	}

	if left := tempResidue(t, dir); len(left) != 0 {
		t.Fatalf("temp residue after successful writes: %v", left)
	}

	seen := map[string]bool{}
	for i := 0; i < 64; i++ {
		n, err := newAnnotationTempName()
		if err != nil {
			t.Fatalf("newAnnotationTempName: %v", err)
		}
		if seen[n] {
			t.Fatalf("duplicate temp name %q out of 64 generated", n)
		}
		seen[n] = true
		if !strings.HasPrefix(n, ".notes-") || !strings.HasSuffix(n, ".tmp") {
			t.Fatalf("temp name %q has the wrong shape", n)
		}
	}
}

// Namespace-removal audit state (P3.14 red-team L5). Owns the mutex/log this
// package's production recordNamespaceRemoval used to carry directly;
// annotationfs_windows.go now only ever compares namespaceRemovalHook to nil
// on the production path, and this init wires the hook to the state below
// for the one test that needs it.
var (
	writeAuditMu  sync.Mutex
	writeAuditOn  bool
	writeAuditLog []string
)

func init() {
	namespaceRemovalHook = func(name string) {
		writeAuditMu.Lock()
		if writeAuditOn {
			writeAuditLog = append(writeAuditLog, name)
		}
		writeAuditMu.Unlock()
	}
}

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

// TestAtomicWriteNeverRemovesDestination migrates P13.no_dest_removal/
// P13.audit, with P13.audit_control as its own negative control.
func TestAtomicWriteNeverRemovesDestination(t *testing.T) {
	parent, dir := openScratchDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	writeAuditStart()
	writeErr := atomicWriteFileAt(parent, "notes.json", []byte("NEW"))
	good := writeAuditStop()
	if writeErr != nil {
		t.Fatalf("replace: %v", writeErr)
	}
	for _, name := range good {
		if strings.EqualFold(name, "notes.json") {
			t.Fatalf("P13.no_dest_removal: atomicWriteFileAt recorded a namespace removal naming the destination: %v", good)
		}
	}

	// The negative control: a deliberately-wrong remove-then-rename
	// decomposition MUST be caught by the same instrument, or the assertion
	// above would be vacuous.
	if err := os.WriteFile(filepath.Join(dir, "control.json"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeAuditStart()
	controlErr := removeThenRenameUnsafeForTest(parent, "control.json", []byte("NEW"))
	bad := writeAuditStop()
	if controlErr != nil {
		t.Fatalf("negative control setup failed: %v", controlErr)
	}
	sawControlRemoval := false
	for _, name := range bad {
		if strings.EqualFold(name, "control.json") {
			sawControlRemoval = true
		}
	}
	if !sawControlRemoval {
		t.Fatal("P13.audit_control: the audit did NOT see the deliberately-wrong remove-then-rename implementation remove the destination — the instrument is not proven to have teeth, so the assertion above is not trustworthy")
	}
}

// TestAtomicWriteRetryBoundExhaustedPreservesDestination migrates
// P13.bound_preserves_dest and P13.sharing_never_truncates: a destination
// held without FILE_SHARE_DELETE for the whole retry bound must terminate
// in an actionable error, well inside a request timeout, with the
// destination still holding its COMPLETE previous content.
func TestAtomicWriteRetryBoundExhaustedPreservesDestination(t *testing.T) {
	parent, dir := openScratchDir(t)
	dst := filepath.Join(dir, "notes.json")
	if err := os.WriteFile(dst, []byte("COMPLETE-OLD"), 0o644); err != nil {
		t.Fatal(err)
	}

	p, err := windows.UTF16PtrFromString(dst)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("could not open the blocking handle: %v", err)
	}
	defer windows.CloseHandle(blocker)

	start := time.Now()
	writeErr := atomicWriteFileAt(parent, "notes.json", []byte("NEW"))
	elapsed := time.Since(start)

	var re *replaceError
	if !errors.As(writeErr, &re) {
		t.Fatalf("atomicWriteFileAt with a destination held WITHOUT FILE_SHARE_DELETE for the whole bound = %v, want a *replaceError", writeErr)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("retry loop took %v, want well under a request-timeout ballpark (the budget is 2s)", elapsed)
	}
	got, err := os.ReadFile(dst)
	if err != nil || string(got) != "COMPLETE-OLD" {
		t.Fatalf("destination after a vetoed replace = %q, %v, want the untouched COMPLETE-OLD content", got, err)
	}
	if left := tempResidue(t, dir); len(left) != 0 {
		t.Fatalf("a bound-exhausted replace must still remove its temp file; residue %v", left)
	}
}

// TestAtomicWriteRidesOutTransientSharingViolation migrates
// P13.retry_integrity.*: whatever the retry outcome, the destination must
// hold one COMPLETE version, never a partial one, and a veto shorter than
// the bound's worst case (766ms) should be ridden out rather than reported.
func TestAtomicWriteRidesOutTransientSharingViolation(t *testing.T) {
	for _, hold := range []time.Duration{20 * time.Millisecond, 200 * time.Millisecond} {
		t.Run(hold.String(), func(t *testing.T) {
			parent, dir := openScratchDir(t)
			dst := filepath.Join(dir, "notes.json")
			if err := os.WriteFile(dst, []byte("OLD"), 0o644); err != nil {
				t.Fatal(err)
			}
			p, err := windows.UTF16PtrFromString(dst)
			if err != nil {
				t.Fatal(err)
			}
			blocker, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
				windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
			if err != nil {
				t.Fatalf("blocking handle: %v", err)
			}
			go func(h windows.Handle, d time.Duration) {
				time.Sleep(d)
				windows.CloseHandle(h)
			}(blocker, hold)

			writeErr := atomicWriteFileAt(parent, "notes.json", []byte("NEW"))
			got, rerr := os.ReadFile(dst)
			if rerr != nil {
				t.Fatalf("read destination: %v", rerr)
			}
			if string(got) != "OLD" && string(got) != "NEW" {
				t.Fatalf("destination = %q, want a COMPLETE OLD or NEW version, never a partial one", got)
			}
			if hold < 700*time.Millisecond && writeErr != nil {
				t.Errorf("a %v veto should be ridden out by the retry bound (766ms worst case), got error: %v", hold, writeErr)
			}
		})
	}
}

// TestAtomicWriteConcurrentWritersNoTornReadsNoResidue migrates
// A12.concurrent_writers and A12.concurrent_temp_residue: 8 writers x 25
// replaces of one document must produce zero per-writer failures, zero torn
// reads (the final content is exactly one writer's complete payload) and no
// leftover temp files.
func TestAtomicWriteConcurrentWritersNoTornReadsNoResidue(t *testing.T) {
	parent, dir := openScratchDir(t)
	const writers = 8
	const replacesPerWriter = 25
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < replacesPerWriter; i++ {
				payload := fmt.Sprintf("writer=%d seq=%d", w, i)
				if err := atomicWriteFileAt(parent, "shared.json", []byte(payload)); err != nil {
					errs <- fmt.Errorf("writer %d replace %d: %w", w, i, err)
					return
				}
			}
			errs <- nil
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	got, err := os.ReadFile(filepath.Join(dir, "shared.json"))
	if err != nil {
		t.Fatalf("final read: %v", err)
	}
	var w, i int
	if _, err := fmt.Sscanf(string(got), "writer=%d seq=%d", &w, &i); err != nil {
		t.Fatalf("final content %q is not a COMPLETE write from any single writer (a torn read): %v", got, err)
	}
	if left := tempResidue(t, dir); len(left) != 0 {
		t.Fatalf("temp residue after %d concurrent writers x %d replaces: %v", writers, replacesPerWriter, left)
	}
}

// TestAtomicWriteContinuousExistence migrates P13.continuous_existence and
// its remove-then-rename negative control: a concurrent reader polling the
// destination across 200 replaces must never observe it absent.
func TestAtomicWriteContinuousExistence(t *testing.T) {
	poll := func(parent int, run func(i int) error) (polls, gaps int) {
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
				if _, err := statAt(parent, "notes.json"); err != nil && errors.Is(err, fs.ErrNotExist) {
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

	parent, dir := openScratchDir(t)
	if err := os.WriteFile(filepath.Join(dir, "notes.json"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, goodGaps := poll(parent, func(i int) error {
		return atomicWriteFileAt(parent, "notes.json", []byte(fmt.Sprintf("v%d", i)))
	})
	if goodGaps != 0 {
		t.Errorf("P13.continuous_existence: destination observed absent %d time(s) during 200 replaces, want 0", goodGaps)
	}

	parent2, dir2 := openScratchDir(t)
	if err := os.WriteFile(filepath.Join(dir2, "notes.json"), []byte("OLD"), 0o644); err != nil {
		t.Fatal(err)
	}
	badPolls, badGaps := poll(parent2, func(i int) error {
		return removeThenRenameUnsafeForTest(parent2, "notes.json", []byte(fmt.Sprintf("v%d", i)))
	})
	if badGaps == 0 {
		t.Skipf("negative control produced no gaps out of %d polls on this runner, so the observer cannot be shown to discriminate here; the assertion above is withheld rather than reported as passing", badPolls)
	}
	t.Logf("negative control (remove-then-rename) observed %d gap(s) out of %d polls, confirming the observer discriminates", badGaps, badPolls)
}

// ---------------------------------------------------------------------------
// P3.9: safe recursive removal — the release gate.
// ---------------------------------------------------------------------------

// TestRemoveTreeAtLeavesJunctionTargetIntact migrates the core of RW1's
// coverage matrix (A6.delete.junction.depth{0,2}): removing an artifact that
// contains a junction, at the top level and nested two levels deep, must
// remove the junction ENTRY and leave the junction's TARGET byte-intact.
func TestRemoveTreeAtLeavesJunctionTargetIntact(t *testing.T) {
	for _, depth := range []int{0, 2} {
		t.Run(fmt.Sprintf("depth%d", depth), func(t *testing.T) {
			parent, dir := openScratchDir(t)
			attacker := t.TempDir()
			marker := filepath.Join(attacker, "LOOT.txt")
			if err := os.WriteFile(marker, []byte("do not delete"), 0o644); err != nil {
				t.Fatal(err)
			}

			if err := mkdirClaim(parent, "art"); err != nil {
				t.Fatal(err)
			}
			cur, err := openRealDirAt(parent, "art")
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < depth; i++ {
				name := fmt.Sprintf("sub%d", i)
				if err := mkdirClaim(cur, name); err != nil {
					t.Fatal(err)
				}
				next, err := openRealDirAt(cur, name)
				if err != nil {
					t.Fatal(err)
				}
				closeFD(cur)
				cur = next
			}
			makeJunctionForTest(t, cur, "link", attacker)
			closeFD(cur)

			if err := removeTreeAt(parent, "art"); err != nil {
				t.Fatalf("removeTreeAt: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "art")); !os.IsNotExist(err) {
				t.Errorf("removeTreeAt did not remove the artifact directory: stat err = %v", err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("removeTreeAt destroyed the junction's TARGET at depth %d: marker gone, stat err = %v", depth, err)
			}
		})
	}
}

// TestRemoveTreeAtSwapMidWalkAndNegativeControl is the binding-constraint-1
// test: P3.9 is open-then-classify-from-the-handle, NEVER classify-then-open.
// It has two halves, and — per ADR §11.1's rule that "a property is migrated
// only when its negative control is migrated with it" — the second half is
// not optional.
func TestRemoveTreeAtSwapMidWalkAndNegativeControl(t *testing.T) {
	build := func(t *testing.T) (parent int, dir, attacker, marker string) {
		t.Helper()
		parent, dir = openScratchDir(t)
		attacker = t.TempDir()
		marker = filepath.Join(attacker, "LOOT.txt")
		if err := os.WriteFile(marker, []byte("do not delete"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := mkdirClaim(parent, "art"); err != nil {
			t.Fatal(err)
		}
		artFD, err := openRealDirAt(parent, "art")
		if err != nil {
			t.Fatal(err)
		}
		if err := mkdirClaim(artFD, "victim"); err != nil {
			t.Fatal(err)
		}
		closeFD(artFD)
		return parent, dir, attacker, marker
	}

	// --- the REAL implementation: A6.swap_midwalk must HOLD ---
	t.Run("real_implementation_refuses_the_swap", func(t *testing.T) {
		parent, dir, attacker, marker := build(t)
		var once sync.Once
		var swapErr error
		// The hook runs on the test's own goroutine but DEEP inside
		// removeTreeAt's recursion, so it reports failures through swapErr
		// (checked below) rather than calling t.Fatal directly, which would
		// runtime.Goexit mid-walk and skip the deferred handle cleanups the
		// production code relies on.
		setStoreOpHook(t, func(op string) {
			if op != "annotation-tree-entry" {
				return
			}
			once.Do(func() {
				// Swap "victim" for a junction into the attacker's tree
				// BETWEEN enumeration and the recursive descent — the exact
				// window a classify-then-open shape would reopen.
				victim := filepath.Join(dir, "art", "victim")
				if err := os.RemoveAll(victim); err != nil {
					swapErr = fmt.Errorf("remove real victim dir: %w", err)
					return
				}
				artFD, err := openRealDirAt(parent, "art")
				if err != nil {
					swapErr = fmt.Errorf("reopen art: %w", err)
					return
				}
				swapErr = makeJunctionAt(artFD, "victim", attacker)
				closeFD(artFD)
			})
		})

		if err := removeTreeAt(parent, "art"); err != nil {
			t.Fatalf("removeTreeAt: %v", err)
		}
		if swapErr != nil {
			t.Fatalf("mid-walk swap setup failed: %v", swapErr)
		}
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("A6.swap_midwalk: removeTreeAt must refuse to descend through a directory swapped for a junction mid-walk, but the target was destroyed: %v", err)
		}
	})

	// --- the NEGATIVE CONTROL: the classify-then-open shape MUST destroy the target ---
	t.Run("negative_control_destroys_the_target", func(t *testing.T) {
		parent, dir, attacker, marker := build(t)
		victim := filepath.Join(dir, "art", "victim")
		if err := os.RemoveAll(victim); err != nil {
			t.Fatalf("remove real victim dir: %v", err)
		}
		artFD, err := openRealDirAt(parent, "art")
		if err != nil {
			t.Fatalf("reopen art: %v", err)
		}
		makeJunctionForTest(t, artFD, "victim", attacker)
		closeFD(artFD)

		_ = removeTreeAtByAttributeUnsafeForTest(parent, "art")
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("A6.negative_control FAILED TO FIRE: the deliberately-wrong classify-then-open implementation left the attacker's marker intact (stat err = %v) — if this ever passes, the real_implementation_refuses_the_swap subtest above is not proven to have teeth", err)
		}
		t.Logf("negative control confirmed: the classify-then-open shape destroyed the external target (marker gone), proving the real implementation's refusal above is a meaningful assertion and not a vacuous one")
	})
}

// ---------------------------------------------------------------------------
// P3.7: the annotation rendezvous rework.
// ---------------------------------------------------------------------------

func TestAnnotationLockRendezvousSharedAndExclusive(t *testing.T) {
	testRoot(t)
	a1, err := openAnnotationFS()
	if err != nil {
		t.Fatalf("openAnnotationFS (1): %v", err)
	}
	defer a1.close()
	a2, err := openAnnotationFS()
	if err != nil {
		t.Fatalf("openAnnotationFS (2): %v", err)
	}
	defer a2.close()

	if err := lockRendezvous(a1, false); err != nil {
		t.Fatalf("first shared lock: %v", err)
	}
	defer unlockRendezvous(a1)
	if err := lockRendezvous(a2, false); err != nil {
		t.Fatalf("second shared lock while the first is held: %v", err)
	}
	defer unlockRendezvous(a2)

	a3, err := openAnnotationFS()
	if err != nil {
		t.Fatalf("openAnnotationFS (3): %v", err)
	}
	defer a3.close()
	if err := lockRendezvous(a3, true); err == nil {
		unlockRendezvous(a3)
		t.Fatal("an exclusive lock succeeded while two shared locks were held, want a conflict (Windows byte-range locks are mandatory, M14.mandatory)")
	}
}

// TestAnnotationLockIdentitySwapDetected exercises the DETECTOR §6.7 adds,
// not a control: it cannot prevent mutual exclusion from being lost across a
// delete-and-recreate of the rendezvous object between two PROCESSES, but it
// must turn the same swap between two operations IN THIS PROCESS into a
// loud error.
func TestAnnotationLockIdentitySwapDetected(t *testing.T) {
	root := testRoot(t)
	resetLockIdentityCacheForTest()
	resetRootIdentityCacheForTest()

	a1, err := openAnnotationFS()
	if err != nil {
		t.Fatalf("first openAnnotationFS: %v", err)
	}
	if err := a1.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	lockPath := filepath.Join(root, lockFileName)
	if err := os.Remove(lockPath); err != nil {
		t.Fatalf("remove lock file: %v", err)
	}
	if err := os.WriteFile(lockPath, nil, 0o644); err != nil {
		t.Fatalf("recreate lock file: %v", err)
	}

	if _, err := openAnnotationFS(); err == nil {
		t.Fatal("openAnnotationFS after the lock file was deleted and recreated at the same path succeeded silently, want a loud identity-mismatch error (ADR §6.7: this is a detector, not a control)")
	}
}

func TestVisibleHidesLockFileName(t *testing.T) {
	root := testRoot(t)
	resetIgnoreCache()
	writeFile(t, root, ".scratchpadignore", "!"+lockFileName+"\n")
	if Visible(root, lockFileName, false) {
		t.Errorf("Visible(root, %q, false) = true, want false even with a ! override", lockFileName)
	}
	// NTFS folds case (M11), and this is a deny rule where over-breadth is
	// safe, so a folded spelling must be hidden too.
	if Visible(root, strings.ToUpper(lockFileName), false) {
		t.Errorf("Visible(root, %q, false) = true, want false (nameEquals must fold case on Windows)", strings.ToUpper(lockFileName))
	}
}
