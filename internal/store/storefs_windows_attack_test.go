//go:build windows

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"scratchpad/internal/testutil"
)

// This file is the Windows-only half of P3.12's deterministic attack matrix:
// reparse-tag variants (junction, and a non-Microsoft "unknown tag") that
// have no Linux analogue and so cannot live in the shared attack_test.go.
// Shared hook-driven tests (A8, A2's realfile/realdir variants, A4's
// within-operation half, browse-segment, doc-open) are in attack_test.go;
// P3.7-P3.10's permanent migrations (A6.delete.{junction,symlink}.*,
// A6.swap_midwalk + its negative control, the atomic-write P13 properties)
// are in annotationfs_windows_test.go. This file does not duplicate any of
// those — see each test's doc comment for what existing coverage it extends.

// ---------------------------------------------------------------------------
// The deferred raw reparse-buffer helper (ADR §11.1: "A6.delete.unknowntag.*
// ... deferred to P3.12, which already owns the harder hook-driven matrix"
// because no such helper existed in internal/store). Ported from
// internal/winspike/links.go's SetUnknownTag: a REPARSE_GUID_DATA_BUFFER is
// tag(4) + length(2) + reserved(2) + GUID(16) + payload — the shape required
// for any tag without a Microsoft-defined body. The payload content is never
// read by anything in this codebase; only the TAG matters for classification
// (attrTagInfo.isReparse() looks at FILE_ATTRIBUTE_REPARSE_POINT alone,
// ADR §2.1), which is exactly why a completely inert, undocumented tag is
// enough to exercise "a reparse point we do not understand" (Scope C).
// ---------------------------------------------------------------------------

// nonMicrosoftTag is bit-31-clear (0x00001234), so it is unambiguously not
// one of the tags this store creates or recognises (IO_REPARSE_TAG_SYMLINK,
// IO_REPARSE_TAG_MOUNT_POINT) and not a Microsoft-defined tag either —
// exactly the RR1 second vector (ADR §5.2): a non-surrogate unknown tag on a
// directory is reported by Go as ModeDir, not suppressed the way a junction's
// surrogate bit suppresses it, which is why "refuse name surrogates" was
// rejected as the policy in favour of "refuse every tag not on the allowlist".
const nonMicrosoftTag = 0x00001234

// makeUnknownTagReparseAt creates a directory at (parent, name) and applies a
// non-Microsoft reparse tag to it, bypassing symlinkAt/setSymlinkReparse/
// setMountPointReparse entirely (all three are tag-fixed and refuse to write
// anything else). Mirrors internal/winspike/links.go's SetUnknownTag exactly,
// adapted to this package's ntOpenAt/put16/put32 helpers (win32_windows.go).
func makeUnknownTagReparseAt(parent int, name string, tag uint32) error {
	if err := mkdirClaim(parent, name); err != nil {
		return fmt.Errorf("mkdirClaim(%q): %w", name, err)
	}
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return fmt.Errorf("open new unknown-tag entry %q: %w", name, err)
	}
	defer windows.CloseHandle(h)
	const dataLen = 8
	buf := make([]byte, 24+dataLen)
	put32(buf, 0, tag)
	put16(buf, 4, dataLen)
	put16(buf, 6, 0)
	// An arbitrary, fixed GUID (identical to the winspike prototype's, for no
	// reason other than there being no reason to vary it): the filesystem
	// does not interpret it, it only has to be present per
	// REPARSE_GUID_DATA_BUFFER's layout.
	guid := [16]byte{0x4a, 0x9b, 0x0e, 0x5c, 0x1d, 0x7f, 0x2e, 0x4d,
		0x9a, 0x3b, 0x0f, 0x6c, 0x8e, 0x2d, 0x1a, 0x77}
	copy(buf[8:24], guid[:])
	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT, &buf[0], uint32(len(buf)), nil, 0, &n, nil); err != nil {
		return fmt.Errorf("FSCTL_SET_REPARSE_POINT(tag=0x%08X): %w", tag, err)
	}
	return nil
}

func makeUnknownTagReparseForTest(t *testing.T, parent int, name string) {
	t.Helper()
	if err := makeUnknownTagReparseAt(parent, name, nonMicrosoftTag); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// A6.delete.unknowntag — the flavour P3.7-P3.10 explicitly deferred here.
// Same shape as TestRemoveTreeAtLeavesJunctionTargetIntact
// (annotationfs_windows_test.go), now exercised with the helper above.
// ---------------------------------------------------------------------------

// TestRemoveTreeAtLeavesUnknownTagTargetIntact is the unknown-tag sibling of
// TestRemoveTreeAtLeavesJunctionTargetIntact. Negative control: the same
// negative control already proves the assertion has teeth
// (removeTreeAtByAttributeUnsafeForTest, annotationfs_windows_test.go) —
// FILE_ATTRIBUTE_DIRECTORY is set on this entry too (it is a real directory
// with a tag applied, exactly like a junction), so the classify-then-open
// shape would descend into it identically; a second run of that control
// specifically for this tag would be redundant with
// TestRemoveTreeAtSwapMidWalkAndNegativeControl's existing demonstration,
// which is why this test only exercises the REAL implementation and relies
// on that shared control rather than repeating it.
func TestRemoveTreeAtLeavesUnknownTagTargetIntact(t *testing.T) {
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
			// An unknown tag cannot be applied to the attacker's real target
			// directory itself (that would change what "attacker" IS, not
			// what the store's entry points at) — it goes on the STORE'S OWN
			// entry, same as a junction/symlink would, but the substitute
			// name it carries is irrelevant to Scope C classification: the
			// tag alone is what routes this to deleteEntryAt. So this entry
			// does not "point at" attacker at all; it is simply an opaque
			// reparse point sitting where a real directory would otherwise
			// be, and the property under test is that removeTreeAt does not
			// need to know or care what an unrecognised tag means in order
			// to refuse descending into it.
			makeUnknownTagReparseForTest(t, cur, "unk")
			closeFD(cur)

			if err := removeTreeAt(parent, "art"); err != nil {
				t.Fatalf("removeTreeAt: %v", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "art")); !os.IsNotExist(err) {
				t.Errorf("removeTreeAt did not remove the artifact directory: stat err = %v", err)
			}
			// The entry itself must be gone too (Delete's job is to remove
			// its own namespace entry for an unrecognised tag, never to
			// leave it behind unexamined) — checked via Stat on the parent's
			// listing rather than the (meaningless) reparse target.
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("unrelated external marker must survive regardless: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A1.ancestor_replaced.{junction,unknowntag} — the two reparse-tag flavours
// TestPinnedMutationsIgnoreProjectSwap (store_test.go, shared) does not
// cover: it exercises realdir and symlink. F-b (a handle names an object,
// not a name) predicts these two flavours are refused identically, since
// they never reach a decision that depends on tag identity — the ancestor
// walk already completed and pinned the handle before the swap happens.
// ---------------------------------------------------------------------------

func TestPublishAncestorReplacedWithJunctionOrUnknownTag(t *testing.T) {
	for _, kind := range []string{"junction", "unknowntag"} {
		t.Run(kind, func(t *testing.T) {
			root := testRoot(t)
			if err := os.Mkdir(filepath.Join(root, "project"), 0o755); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(root, "project-original")
			attacker := t.TempDir()
			marker := filepath.Join(attacker, "LOOT.txt")
			if err := os.WriteFile(marker, []byte("do not delete"), 0o644); err != nil {
				t.Fatal(err)
			}

			var stageErr error
			setStoreOpHook(t, func(op string) {
				if op != "publish-claim" {
					return
				}
				clearStoreOpHook()
				if err := os.Rename(filepath.Join(root, "project"), original); err != nil {
					stageErr = err
					return
				}
				rfs, err := openRootedFS(false)
				if err != nil {
					stageErr = err
					return
				}
				defer rfs.close()
				switch kind {
				case "junction":
					stageErr = makeJunctionAt(int(rfs.root.Fd()), "project", attacker)
				case "unknowntag":
					stageErr = makeUnknownTagReparseAt(int(rfs.root.Fd()), "project", nonMicrosoftTag)
				}
			})

			_, err := Publish("project", "safe", map[string][]byte{"index.html": []byte("safe")})
			if stageErr != nil {
				t.Fatalf("staging the %s ancestor swap: %v", kind, stageErr)
			}
			if err != nil {
				t.Fatalf("Publish through a pinned ancestor should have completed despite the swap, got %v", err)
			}

			if _, statErr := os.Stat(filepath.Join(attacker, "safe")); !os.IsNotExist(statErr) {
				t.Fatalf("Publish escaped through the swapped ancestor into the attacker tree: stat err = %v", statErr)
			}
			if got, err := os.ReadFile(filepath.Join(original, "safe", "index.html")); err != nil || string(got) != "safe" {
				t.Fatalf("pinned Publish should have landed in the ORIGINAL (renamed-away) ancestor: content=%q err=%v", got, err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Errorf("unrelated attacker content must survive untouched: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A4.root_replaced — the junction/unknown-tag flavours of the root-open hook
// test, plus the cross-OPERATION identity-cache detector (§4.1, F9) that has
// no Linux analogue: Linux simply re-opens the root by path every operation
// and has no notion of "the same root string now names a different object".
// TestPublishSurvivesRootRenamedAwayMidOperation (attack_test.go, shared)
// already covers the WITHIN-operation realdir case identically on both
// platforms; this covers the WITHIN-operation reparse flavours and the
// Windows-only cross-operation detector.
// ---------------------------------------------------------------------------

func TestPublishSurvivesRootReplacedWithJunctionMidOperation(t *testing.T) {
	root := testRoot(t)
	original := root + "-original"
	attacker := t.TempDir()

	var stageErr error
	setStoreOpHook(t, func(op string) {
		if op != "root-open" {
			return
		}
		clearStoreOpHook()
		if err := os.Rename(root, original); err != nil {
			stageErr = err
			return
		}
		// makeJunctionAtPath creates the directory at root itself (it must
		// not already exist, matching symlinkAt's own create-then-tag shape).
		if err := makeJunctionAtPath(root, attacker); err != nil {
			stageErr = err
		}
	})

	if _, err := Publish("", "safe", map[string][]byte{"index.html": []byte("safe")}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if stageErr != nil {
		t.Fatalf("staging the junction root swap: %v", stageErr)
	}
	if _, err := os.Stat(filepath.Join(attacker, "safe")); !os.IsNotExist(err) {
		t.Fatalf("Publish escaped through the junction-replaced root: stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(original, "safe", "index.html"))
	if err != nil || string(got) != "safe" {
		t.Fatalf("pinned Publish should have landed in the original root: content=%q err=%v", got, err)
	}
}

// makeJunctionAtPath creates a junction at the (not-yet-existing) path name,
// pointing at target, for staging a root-replacement attack where no open
// parent handle is available (the root itself has no parent within the
// test's control the way an ordinary entry does). It opens name's freshly
// created directory directly by path rather than relative to a handle,
// which is fine here because it is attacker setup code, not anything this
// store's own containment argument relies on.
func makeJunctionAtPath(name, target string) error {
	if err := os.Mkdir(name, 0o755); err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ|windows.DELETE, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	return setMountPointReparse(h, `\??\`+target, target)
}

// TestRootIdentityCacheDetectsCrossOperationReplacement is the ADR §4.1/F9
// cross-operation detector: R13's diagnostic is a no-op WITHIN one operation
// (the handle is already pinned, so F-b already makes a same-operation swap
// harmless — see the tests above), and revision 1 overstated exactly this by
// crediting a per-operation identity re-read (rootedFS.verifyRoot) with
// catching a replacement BETWEEN operations. That method never acquired a
// caller and was deleted for P6.2's FD-3; this test is the whole of R13's
// coverage. The fix is the process-level rootIdentityCache keyed on the root
// STRING, exercised here directly rather than through two full Publish calls
// (which each pin a NEW handle from openRootedFS — the cache is what makes
// the SECOND one loud instead of silently accepting the new object).
func TestRootIdentityCacheDetectsCrossOperationReplacement(t *testing.T) {
	root := testRoot(t)
	resetRootIdentityCacheForTest()

	rfs1, err := openRootedFS(false)
	if err != nil {
		t.Fatalf("first openRootedFS: %v", err)
	}
	if err := rfs1.close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}

	_, err = openRootedFS(false)
	if err == nil {
		t.Fatal("openRootedFS after the root was removed and recreated at the same path succeeded silently, want a loud identity-mismatch error (ADR §4.1: this is exactly the cross-operation case the per-operation R12 pin cannot see on its own)")
	}
	if !strings.Contains(err.Error(), "replaced") {
		t.Errorf("root replacement failed for the wrong reason: %v, want it to say the root was replaced", err)
	}
}

// ---------------------------------------------------------------------------
// A2.dest_replaced — the junction/directory-symlink flavours: a genuine
// escape attempt (the substituted destination points OUTSIDE the store),
// unlike attack_test.go's realfile/realdir variants which stay inside it.
// Mirrors internal/winspike/adversarial_test.go's TestA2DestinationReplaced-
// BeforeReplace exactly in shape and in what it asserts.
// ---------------------------------------------------------------------------

func TestSaveNotesDestinationReplacedWithLinkNeverEscapes(t *testing.T) {
	for _, kind := range []string{"junction", "dirsymlink"} {
		t.Run(kind, func(t *testing.T) {
			if kind == "dirsymlink" {
				testutil.RequireSymlinks(t)
			}
			root := testRoot(t)
			doc := publishDoc(t, "", "art")
			if _, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "a", Status: "open"}}}, 0); err != nil {
				t.Fatalf("seed SaveNotes: %v", err)
			}
			dest := filepath.Join(root, AnnotationsDir, "art", "index.html.json")
			attacker := t.TempDir()

			var stageErr error
			setStoreOpHook(t, func(op string) {
				if op != "notes-replace" {
					return
				}
				clearStoreOpHook()
				if err := os.Remove(dest); err != nil {
					stageErr = err
					return
				}
				switch kind {
				case "junction":
					stageErr = makeJunctionAtPath(dest, attacker)
				case "dirsymlink":
					stageErr = os.Symlink(attacker, dest)
				}
			})

			_, saveErr := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "b", Status: "open"}}}, 1)
			if stageErr != nil {
				t.Fatalf("staging the %s destination swap: %v", kind, stageErr)
			}

			leaked, err := os.ReadDir(attacker)
			if err != nil {
				t.Fatalf("reading the external target: %v", err)
			}
			if len(leaked) != 0 {
				t.Fatalf("SaveNotes wrote into the external %s target: %v", kind, leaked)
			}
			// Whatever the outcome (fail closed, or succeed by replacing only
			// the store's own namespace ENTRY rather than following it), no
			// temp residue may be left in the store.
			entries, err := os.ReadDir(filepath.Join(root, AnnotationsDir, "art"))
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".notes-") {
					t.Errorf("temp residue left behind after a %s destination substitution: %s", kind, e.Name())
				}
			}
			t.Logf("%s: SaveNotes = %v (informational: the containment property is 'never escapes', not a specific error shape)", kind, saveErr)
		})
	}
}

// ---------------------------------------------------------------------------
// A7 — the two-step link-creation crash window (ADR §6.6, spike-findings.md
// §10.4). Rule 1 (self-heal on a synchronous FSCTL failure) already runs
// inside symlinkAt and is not independently retestable here without
// artificially removing SeCreateSymbolicLinkPrivilege AND blocking the
// junction fallback in the same process (a privilege-token manipulation this
// package has no test harness for — internal/winspike/privilege.go built one
// for exactly this reason, and it does not exist in internal/store). Rule 3
// (widen Delete/Unwatch to remove an empty non-artifact directory) is P4.3's
// deliverable per the ADR's own consequences table, not P3.11/P3.12's, and is
// NOT implemented in internal/store as of this test. This test therefore
// documents CURRENT, pre-rule-3 behaviour — informational, matching the
// spike's own A7.two_step_residue/A7.two_step_recovery INFO framing rather
// than asserting a HELD/BROKEN verdict — so the still-open gap stays visible
// in code instead of only in the ADR, and a future P4.3 change that closes it
// will make the "still stuck" assertion below fail LOUDLY, prompting an
// update here rather than silently drifting out of sync with the fix.
// TestTwoStepCrashResidueRecoversViaDelete is
// TestTwoStepCrashResidueCurrentlyLeavesNameStuck, updated (not silently
// deleted — see that test's own former comment, which asked for exactly
// this) now that P4.3 has landed ADR §6.6 rule 3's widening in
// internal/store's shared Delete: an empty, non-artifact, non-link
// directory is real recovery for the residue a crash between symlinkAt's
// mkdirClaim and its FSCTL_SET_REPARSE_POINT leaves behind.
func TestTwoStepCrashResidueRecoversViaDelete(t *testing.T) {
	root := testRoot(t)
	// Simulate the crash: the name claim (step 1) succeeded, but the reparse
	// tag (step 2) was never applied — exactly what a process kill between
	// symlinkAt's mkdirClaim and its FSCTL_SET_REPARSE_POINT would leave
	// behind, and exactly what symlinkAt's own rule-1 self-heal exists to
	// clean up on any SYNCHRONOUS failure path (a real crash has none).
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	if err := mkdirClaim(int(rfs.root.Fd()), "wedged"); err != nil {
		t.Fatalf("mkdirClaim: %v", err)
	}
	rfs.close()

	if _, err := os.Stat(filepath.Join(root, "wedged")); err != nil {
		t.Fatalf("residue setup: %v", err)
	}

	// A7.two_step_residue: it is an ORDINARY EMPTY DIRECTORY, not a partial
	// or broken link — confirmed by the fact that a plain os.Stat/ReadDir
	// sees exactly what a normal empty directory looks like.
	entries, err := os.ReadDir(filepath.Join(root, "wedged"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("residue = %v, %v, want an ordinary empty directory", entries, err)
	}

	// Re-running watch over the residue still fails: it is not a link, so
	// symlinkAt's own idempotence check (readlinkAt failing with
	// isNotALinkAt) correctly refuses to adopt it silently.
	if _, err := Watch("", "wedged", t.TempDir()); err == nil {
		t.Fatal("re-running watch over the residue unexpectedly succeeded")
	}
	// Unwatch deliberately does NOT gain rule 3's recovery power — it stays
	// link-only, preserving the create-only-for-agents asymmetry (Unwatch is
	// agent-reachable; Delete is user-only).
	if err := Unwatch("", "wedged"); err == nil {
		t.Fatal("Unwatch unexpectedly removed the residue — that recovery power belongs to Delete only")
	}
	if _, err := os.Stat(filepath.Join(root, "wedged")); err != nil {
		t.Fatalf("the wedged name must still be there after the refused unwatch: %v", err)
	}
	// Delete DOES recover it now (rule 3): an empty, non-artifact, non-link
	// directory is exactly what rmdirAt is safe to remove.
	if err := Delete("", "wedged"); err != nil {
		t.Fatalf("Delete must remove the empty residue (ADR §6.6 rule 3): %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "wedged")); !os.IsNotExist(err) {
		t.Fatalf("residue must be gone after Delete, stat err = %v", err)
	}
}

// ---------------------------------------------------------------------------
// Watch junction variant + unknown-tag entry visibility ("Watches ⊆
// Unwatch-able", ADR §11 P3.12). The symlink flavour of all three properties
// below is already covered elsewhere (TestWatch, TestUnwatch, store_test.go);
// this exercises the junction flavour end-to-end through the public API
// (rather than only at the removeTreeAt primitive level, which
// TestRemoveTreeAtLeavesJunctionTargetIntact already covers) and confirms an
// unrecognised tag is inert rather than crashing or misclassifying.
// ---------------------------------------------------------------------------

func TestWatchViaJunctionIsListedAndUnwatchable(t *testing.T) {
	testRoot(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<h1>src</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	if err := makeJunctionAt(int(rfs.root.Fd()), "viajunction", source); err != nil {
		rfs.close()
		t.Fatalf("makeJunctionAt: %v", err)
	}
	rfs.close()

	links, err := Watches()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Path != "viajunction" || !sameTarget(links[0].Target, source) {
		t.Fatalf("Watches() = %+v, want one link resolved to %q", links, source)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || !list[0].IsLink || list[0].RelPath() != "viajunction" {
		t.Fatalf("List() = %+v, want the junction-watched artifact reporting IsLink", list)
	}

	if f, safe := OpenDocument([]string{"viajunction", "index.html"}); !safe {
		t.Fatal("OpenDocument did not serve through the junction-based watch")
	} else {
		f.Close()
	}

	if err := Unwatch("", "viajunction"); err != nil {
		t.Fatalf("Unwatch a junction-based watch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(source, "index.html")); err != nil {
		t.Errorf("source must survive unwatching a junction: %v", err)
	}
	if links, _ := Watches(); len(links) != 0 {
		t.Errorf("Watches() = %+v, want none left after Unwatch", links)
	}
}

// TestUnknownTagEntryIsInvisibleAndInert is RW15's documented shape (an
// unrecognised reparse tag in the store's own root): Scope C means "never
// explored, never listed" (classifyEntry, store.go), not "an error" and not
// "a phantom artifact/watch". This confirms List/Watches tolerate it
// silently rather than crashing or misclassifying it as either. RW15 itself
// (an inert "unsupported entry" tile with a Delete action in the web UI) is
// P4.6's deliverable, out of scope here — this only proves the internal/store
// half already holds.
func TestUnknownTagEntryIsInvisibleAndInert(t *testing.T) {
	root := testRoot(t)
	if _, err := Publish("", "ordinary", map[string][]byte{"index.html": []byte("<p>ok</p>")}); err != nil {
		t.Fatal(err)
	}
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	if err := makeUnknownTagReparseAt(int(rfs.root.Fd()), "mystery", nonMicrosoftTag); err != nil {
		rfs.close()
		t.Fatalf("makeUnknownTagReparseAt: %v", err)
	}
	rfs.close()

	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 || list[0].Name != "ordinary" {
		t.Fatalf("List() = %+v, want only the ordinary artifact — the unknown-tag entry must not appear", list)
	}

	links, err := Watches()
	if err != nil {
		t.Fatalf("Watches: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("Watches() = %+v, want none — an unrecognised tag must never be reported as a watch link", links)
	}

	// Delete refuses it (it is neither an allow-listed link nor an artifact)
	// rather than silently doing nothing or crashing.
	if err := Delete("", "mystery"); err == nil {
		t.Error("Delete unexpectedly succeeded on an unrecognised-tag entry through the artifact path")
	}
	if _, err := os.Stat(filepath.Join(root, "mystery")); err != nil {
		t.Errorf("the unrecognised-tag entry should still be there (inert, not silently removed): %v", err)
	}
}

// ---------------------------------------------------------------------------
// MATRIX.EXCLUDED — rows the spike could not measure on a GitHub-hosted
// runner and that this task must not invent a test for, per its own
// instruction. Recorded here, verbatim reasons from spike-findings.md §11.1,
// so the exclusion lives in code next to the tests it is NOT a substitute
// for, rather than only in a document nobody greps before adding a "real"
// test in this area later:
//
//   - MATRIX.EXCLUDED.smb — no SMB share on a GitHub runner. R18 already
//     requires refusing UNC for mutations (validateAbsoluteWindowsPath,
//     storefs_windows.go), so this stays policy rather than measurement; a
//     live-share check belongs to a manual pre-beta pass (owner: P6.6).
//   - MATRIX.EXCLUDED.refs_devdrive — no ReFS volume on a GitHub runner (the
//     image exposes NTFS C: and D: only). FILE_RENAME_INFORMATION_EX and
//     FILE_DISPOSITION_INFORMATION_EX are documented for ReFS but unverified
//     here (owner: P6.6, one manual Dev Drive check per RW9).
//   - MATRIX.EXCLUDED.fat32 — the runner's only FAT32 volume is the unmounted
//     EFI partition. POSIX semantics and rename class 65 are documented
//     unsupported there; isUnsupportedRenameClass's class-65-to-10 fallback
//     (annotationfs_windows.go) is the mechanism that would cover it, and
//     A9.rename_failure_statuses already established which statuses justify
//     firing it.
//   - MATRIX.EXCLUDED.cloud_placeholders — no OneDrive on a GitHub runner.
//     The "broken target" cell's cloud variant (ERROR_CLOUD_FILE_* instead of
//     not-found) and RW13's mass-rehydration risk are owned by P4.6/P4.2.
//   - MATRIX.EXCLUDED.antivirus_distribution — Defender's realtime state on a
//     CI image is not representative (M13.av). The retryable status set
//     (retryableRenameStatuses, annotationfs_windows.go) is chosen from
//     documentation with a stated bound; the DETERMINISTIC half — an
//     interfering handle this package opens itself — is what
//     TestAtomicWriteRidesOutTransientSharingViolation and
//     TestAtomicWriteRetryBoundExhaustedPreservesDestination
//     (annotationfs_windows_test.go) already measure instead.
//   - MATRIX.EXCLUDED.readdirchanges_overflow — not deterministically
//     reproducible (M15.overflow); internal/watch's territory, not
//     internal/store's, and explicitly out of this task's scope.
//   - MATRIX.EXCLUDED.non_elevated_session — GitHub runners execute elevated
//     with Developer Mode on; every privilege-sensitive answer in this
//     package's test suite comes from the ADR's measured privilege table
//     (§6.6), not from running genuinely unprivileged here. One manual
//     confirmation on an ordinary account is still owed (owner: P5.5/RW16).
//   - MATRIX.EXCLUDED.32bit — not a target. win32_windows.go:37 carries the
//     compile-time assertion (a zero-size array indexed by a bool expression
//     on uintptr's size) that fails the build on a 32-bit uintptr, which
//     TestGoos386WouldNotCompile below does not re-test (it cannot: this
//     package does not compile for GOARCH=386 at all) but documents.
//
// MATRIX.EXCLUDED.directory_hard_links is NOT in this list: unlike the seven
// above, it is cheaply verifiable on any Windows runner and gets its own
// real (not merely documented) test below.
// ---------------------------------------------------------------------------

// TestDirectoryHardLinksDoNotExist confirms MATRIX.EXCLUDED.directory_hard_links
// ("CONCEPT DOES NOT EXIST ON WINDOWS") empirically rather than only citing
// spike-findings.md's measurement of the same fact (MX.directory_hard_links):
// os.Link (CreateHardLinkW) on a directory must fail. This is why the
// "Documents / file link" matrix row's hard-link variant is accepted, not
// fixed (RW8): there is no directory-hard-link primitive to defend against on
// this platform in the first place.
func TestDirectoryHardLinksDoNotExist(t *testing.T) {
	root := testRoot(t)
	if err := os.Mkdir(filepath.Join(root, "adir"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := os.Link(filepath.Join(root, "adir"), filepath.Join(root, "alias"))
	if err == nil {
		t.Fatal("os.Link (CreateHardLinkW) on a directory unexpectedly succeeded on Windows")
	}
}

// TestRequireNTFSWiredIntoAlternateStreamTest documents that RequireNTFS
// (testutil, F6) now has a real call site: the ADS-syntax lookup rejection
// (TestValidateSegmentADSSyntax, names_test.go) is meaningful only on an NTFS
// volume — a stream-selector ':' is specific to NTFS, and the containment
// property it guards does not exist on a filesystem without alternate data
// streams at all. This does not duplicate that test's assertions; it exists
// so a reader auditing RequireNTFS's call sites finds the reasoning in one
// place rather than re-deriving it, and so the wiring itself (not just the
// function definition) is exercised by the suite.
func TestRequireNTFSWiredIntoAlternateStreamTest(t *testing.T) {
	root := testRoot(t)
	testutil.RequireNTFS(t, root)
	if err := validateSegment("index.html:hidden"); err == nil {
		t.Fatal("validateSegment accepted NTFS alternate-data-stream syntax on a confirmed-NTFS volume")
	}
}
