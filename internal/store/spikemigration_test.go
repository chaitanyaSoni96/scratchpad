package store

// Migration of the assertions that existed ONLY against internal/winspike's
// prototype, per ADR §11.1's inventory and the plan in P6.2 §11.4. Items 1-3
// here are the three AC5 matrix cells (Root/file, Root/link-or-reparse-point,
// Delete/parent-replaced); item 6 is M16.
//
// The distinction that makes this necessary: a prototype function is not the
// product function. winspike's 56 assertions overwhelmingly exercise
// winspike.OpenRoot / RemoveTreeAt / AtomicWriteFile, so they assert that the
// PROTOTYPE refuses a file as its root, never that openRootedFS does. The two
// were written from one design, which is exactly what made the substitution
// easy to miss for five phases.
//
// Everything in this file is untagged: each property is a property of the
// store on both backends, and the two backends reach the same refusal by
// different mechanisms (O_DIRECTORY/O_NOFOLLOW on Linux, an isDir/tag read
// from the pinned handle on Windows). Asserting them together is what proves
// the port did not quietly drop one.

// FALSIFICATION RECORD. Every test in this file and its two platform halves
// was run against a deliberately broken implementation before being trusted,
// because a migrated test that passes while asserting nothing is worse than
// the prototype coverage it replaces. Recorded here rather than in a commit
// message alone: the evidence is the point, and a year from now this file is
// where a reader will be standing.
//
// Falsified locally on Linux (each breakage reverted immediately after):
//
//	TestRootMustBeADirectory          drop O_DIRECTORY from openRootedFS
//	                                  -> "openRootedFS accepted a regular file as the store root"
//	TestRootMustNotBeALink            drop O_NOFOLLOW from openRootedFS
//	                                  -> "openRootedFS accepted a symlink as the store root"
//	TestDeleteIgnoresProjectSwap...   replace the handle-anchored removeTreeAt(parent, name)
//	                                  with a path-based os.RemoveAll(filepath.Join(root, project, name))
//	                                  -> "Delete did not remove the artifact from the pinned
//	                                     (renamed-away) directory", and with that assertion
//	                                     temporarily removed, the containment half fired too:
//	                                     "Delete followed the swapped ancestor into the external tree"
//	TestReadDirFDRestartsPerCall      drop readDirFD's unix.Seek(dup, 0, SEEK_SET)
//	                                  -> "readDirFD call 2 returned 0 entries ([]), want 3"
//
// Falsified on real Windows by CI run 32999441103 (commit 120838f on the
// scratch branch tmp/falsify-ac5, pushed for this purpose and deleted after;
// it tripped the repository's automated security review, as a branch carrying
// deliberately weakened containment guards should):
//
//	breakage A - the store-root guard made MODE-shaped instead of TAG-shaped,
//	             i.e. at.isReparse() && at.ReparseTag == IO_REPARSE_TAG_SYMLINK:
//	  TestRootMustNotBeAReparsePoint/junction
//	    -> "openRootedFS accepted a junction reparse point as the store root"
//	  TestRootMustNotBeAReparsePoint/unknown-tag
//	    -> "openRootedFS accepted a unknown-tag reparse point as the store root"
//	  Both flavours caught, which is the property: a mode-shaped guard misses
//	  BOTH, and the unknown-tag one is invisible to os.Lstat's mode bits
//	  entirely (plain ModeDir - RR1's second vector, ADR 5.2).
//
//	breakage B - annotationFS.openDir made to FOLLOW reparse points (a raw
//	             ntOpenAt with neither FILE_OPEN_REPARSE_POINT nor OBJ_DONT_REPARSE):
//	  TestAnnotationJunctionComponentsRejected failed on all four verbs
//	    -> "LoadNotes/SaveNotes/DeleteNotes/WalkNotes accepted a junction
//	       annotation component"
//	  and, the payoff, on the leak check:
//	    -> "the annotation backend wrote through the junction: [- index.html.json]"
//	  i.e. the broken build performed the actual escape the test exists to
//	  prevent, writing a sidecar into the external tree. The pre-existing
//	  TestAnnotationSymlinkComponentsRejected failed under the same breakage,
//	  so both link flavours discriminate.
//
// One test is not falsified by breaking production code because it carries the
// A/B control inside itself: TestOpenDocumentGrantsFileShareDelete opens the
// same file first with os.Open and REQUIRES the delete to be vetoed, then
// through OpenDocument and requires it to succeed. Both halves executed on run
// 32998775246. If the store's handle ever stops granting FILE_SHARE_DELETE the
// second half fails; if Go's os.Open ever stops vetoing, the control self-skips
// with an explicit marker rather than passing vacuously.

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"scratchpad/internal/testutil"
)

// ---------------------------------------------------------------------------
// §11.4 item 1 — MX.root_file. AC5 cell "Root / file".
// ---------------------------------------------------------------------------

// TestRootMustBeADirectory points SCRATCHPAD_ROOT at a regular file. Before
// this test the only coverage was winspike's MX.root_file against
// winspike.OpenRoot; grep-verified that no product test had ever set RootEnv
// to anything but a real t.TempDir().
//
// Linux refuses in unix.Open's O_DIRECTORY; Windows refuses on at.isDir()
// read from the pinned handle, because CreateFile with
// FILE_FLAG_BACKUP_SEMANTICS opens a regular file perfectly happily and the
// type check has to be explicit. Same outcome, different mechanism, which is
// why this is asserted on both rather than only where it was measured.
func TestRootMustBeADirectory(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "root-is-a-file")
	if err := os.WriteFile(file, []byte("not a store"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv(RootEnv, file)
	if rfs, err := openRootedFS(false); err == nil {
		rfs.close()
		t.Fatal("openRootedFS accepted a regular file as the store root")
	}
	if rfs, err := openRootedFS(true); err == nil {
		rfs.close()
		t.Fatal("openRootedFS(create) accepted a regular file as the store root")
	}
	// The user-facing halves, so this is not only a statement about an
	// unexported primitive.
	if _, err := List(); err == nil {
		t.Fatal("List succeeded with a regular file as the store root")
	}
	if _, err := Publish("", "art", map[string][]byte{"index.html": []byte("x")}); err == nil {
		t.Fatal("Publish succeeded with a regular file as the store root")
	}

	// Positive control: the identical calls against a real directory succeed,
	// so the four refusals above are caused by the root's TYPE and not by
	// something unrelated failing everything (the control discipline ADR
	// §11.1's migration rule requires).
	realRoot := filepath.Join(base, "root-is-a-dir")
	if err := os.Mkdir(realRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(RootEnv, realRoot)
	rfs, err := openRootedFS(false)
	if err != nil {
		t.Fatalf("control: openRootedFS refused a real directory root: %v", err)
	}
	rfs.close()
	if _, err := Publish("", "art", map[string][]byte{"index.html": []byte("x")}); err != nil {
		t.Fatalf("control: Publish failed against a real directory root: %v", err)
	}
}

// ---------------------------------------------------------------------------
// §11.4 item 2 — MX.root_reparse.* / A4.root_reparse_refused.*, shared half.
// AC5 cell "Root / link or reparse point". The Windows tag flavours (junction
// and an unrecognised non-Microsoft tag) are in the Windows twin, which is
// where the "refused on the TAG, not on fs.ModeSymlink" half can be shown.
// ---------------------------------------------------------------------------

func TestRootMustNotBeALink(t *testing.T) {
	testutil.RequireSymlinks(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "root-is-a-link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	t.Setenv(RootEnv, link)
	if rfs, err := openRootedFS(false); err == nil {
		rfs.close()
		t.Fatal("openRootedFS accepted a symlink as the store root")
	}

	// Positive control: the link's TARGET, opened directly, is a perfectly
	// good root. This is what makes the refusal above a statement about the
	// link rather than about the directory it happens to point at.
	t.Setenv(RootEnv, target)
	rfs, err := openRootedFS(false)
	if err != nil {
		t.Fatalf("control: openRootedFS refused the link's target directly: %v", err)
	}
	rfs.close()
}

// ---------------------------------------------------------------------------
// §11.4 item 3 — A6.parent_replaced. AC5 cell "Delete / parent replaced".
// ---------------------------------------------------------------------------

// TestDeleteIgnoresProjectSwapAfterParentPinned is the product-level twin of
// winspike's TestA6DeleteParentReplaced, which pinned a parent handle, renamed
// the project directory away, planted a junction to an external tree in its
// place, and asserted RemoveTreeAt removed from the PINNED object and never
// reached the decoy.
//
// The shape here is deliberately the same as TestPinnedMutationsIgnoreProjectSwap
// (store_test.go) with one difference: the swap fires on the "delete" hook
// rather than "publish-claim", so it lands in the window between Delete
// pinning its parent via openRealDir and Delete acting on it. §11.1 listed
// this property and it was simply never done — unlike items 1 and 2, which
// §11.1 never listed at all.
//
// The decoy is planted with plantDirLink, which is a junction on Windows and a
// symlink on Linux, so this runs on a Developer-Mode-off Windows box rather
// than skipping there — the correction commit e8dd8b5 made for the watch-link
// tests, applied to this one at birth.
func TestDeleteIgnoresProjectSwapAfterParentPinned(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := testRoot(t)
	if _, err := Publish("project", "victim", map[string][]byte{"index.html": []byte("victim")}); err != nil {
		t.Fatal(err)
	}

	// The external tree the decoy points at, with an entry of the same name
	// the delete is about to target. If Delete ever re-resolved "project" by
	// name it would land here and destroy this.
	outside := t.TempDir()
	decoy := filepath.Join(outside, "victim")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, "index.html"), []byte("decoy"), 0o644); err != nil {
		t.Fatal(err)
	}

	moved := filepath.Join(root, "project-moved")
	swapped := false
	setStoreOpHook(t, func(op string) {
		if op != "delete" {
			return
		}
		clearStoreOpHook()
		if err := os.Rename(filepath.Join(root, "project"), moved); err != nil {
			t.Errorf("staging: rename: %v", err)
			return
		}
		if err := plantDirLink(filepath.Join(root, "project"), outside); err != nil {
			t.Errorf("staging: plant decoy link: %v", err)
			return
		}
		swapped = true
	})

	if err := Delete("project", "victim"); err != nil {
		t.Fatalf("Delete through a pinned parent failed after the swap: %v", err)
	}
	if !swapped {
		t.Fatal("the delete hook never fired, so this test asserted nothing")
	}

	// It removed from the object it had pinned...
	if _, err := os.Stat(filepath.Join(moved, "victim")); !os.IsNotExist(err) {
		t.Fatalf("Delete did not remove the artifact from the pinned (renamed-away) directory: %v", err)
	}
	// ...and never reached the decoy through the substituted name.
	got, err := os.ReadFile(filepath.Join(decoy, "index.html"))
	if err != nil || string(got) != "decoy" {
		t.Fatalf("Delete followed the swapped ancestor into the external tree: content=%q err=%v", got, err)
	}
	if entries, err := os.ReadDir(outside); err == nil && len(entries) != 1 {
		t.Fatalf("the external tree was modified: %v", entries)
	}
}

// ---------------------------------------------------------------------------
// §11.4 item 6 — M16. Not an AC5 cell; adjacent debt of the same kind.
// ---------------------------------------------------------------------------

// TestReadDirFDRestartsPerCall pins M16: a duplicate of an already-open
// directory handle re-enumerates from the start, so repeated readDirFD on ONE
// pinned handle each returns the FULL listing.
//
// Every handle-anchored walk in the package relies on this and nothing
// asserted it. The two backends earn it differently and both are load-bearing:
// on Windows each DuplicateHandle restarts enumeration on its own (M16, the
// measured replacement for fdPath), while on Linux a dup(2) SHARES the
// original's file description INCLUDING the directory read offset, so
// readDirFD has to Seek(0) explicitly — without it the second call returns
// zero entries and, worse, silently, since an empty directory is a legitimate
// answer everywhere this is called.
func TestReadDirFDRestartsPerCall(t *testing.T) {
	root := testRoot(t)
	want := []string{"alpha", "beta", "gamma"}
	for _, name := range want {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		t.Fatal(err)
	}
	defer rfs.close()
	fd := int(rfs.root.Fd())

	// Three calls, not two: a cumulative-offset bug can survive a two-call
	// test if the first call happens to be the one that rewinds.
	for i := 1; i <= 3; i++ {
		entries, err := readDirFD(fd)
		if err != nil {
			t.Fatalf("readDirFD call %d: %v", i, err)
		}
		var got []string
		for _, e := range entries {
			got = append(got, e.Name())
		}
		sort.Strings(got)
		if len(got) != len(want) {
			t.Fatalf("readDirFD call %d returned %d entries (%v), want %d (%v) — "+
				"enumeration did not restart on the duplicated handle", i, len(got), got, len(want), want)
		}
		for j := range want {
			if got[j] != want[j] {
				t.Fatalf("readDirFD call %d = %v, want %v", i, got, want)
			}
		}
	}
}
