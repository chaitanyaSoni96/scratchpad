package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"scratchpad/internal/testutil"
)

// This file is P3.12's hook-driven attack matrix, shared across GOOS per the
// task's own instruction ("Windows-only attack tests belong behind //go:build
// windows; shared ones should run on both. Prefer shared."). Every test here
// exercises a hook that now exists identically on both platforms (root-open,
// browse-segment, doc-open, notes-replace, notes-remove — ADR §11/R17) or a
// property (mkdirClaim's atomicity, the annotation rendezvous lock) that both
// backends implement to the same policy. Windows-only reparse-tag variants
// (junction, unknown tag) that have no Linux analogue live in
// storefs_windows_attack_test.go.
//
// Per the ADR §11.1 migration rule, a property is only worth adding when its
// NEGATIVE CONTROL is meaningful too. Where the attack is expected to be
// contained (the common case), the corresponding "this would have escaped
// without the guard" control is either an existing regression test elsewhere
// in this package (named in the comment) or, where none exists, spelled out
// inline.

// ---------------------------------------------------------------------------
// A8.concurrent_claim — Publish's create-only claim under real concurrency.
// ---------------------------------------------------------------------------

// TestPublishConcurrentClaimExactlyOneWins migrates A8.concurrent_claim: N
// goroutines racing to Publish the identical name must produce exactly one
// winner and N-1 "already exists" losers — never a corrupted/partial
// artifact, and never more than one goroutine believing it won. mkdirClaim's
// atomicity (FILE_CREATE / O_EXCL-via-Mkdirat) is the primitive under test;
// TestPublishCreateOnly already proves the SEQUENTIAL half (a second, later
// Publish fails), so this is the concurrent half that a naive
// check-then-create implementation (stat, see nothing, then mkdir) would
// fail — that mechanical alternative is this test's negative control,
// reasoned rather than run: if Publish is ever rewritten to check for
// existence before claiming, `racers` goroutines observing "not there yet"
// simultaneously would all attempt to write index.html concurrently, and the
// content on disk afterward would not reliably be any single goroutine's
// complete payload.
func TestPublishConcurrentClaimExactlyOneWins(t *testing.T) {
	testRoot(t)
	const racers = 16
	var wg sync.WaitGroup
	errs := make(chan error, racers)
	start := make(chan struct{})
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			payload := []byte(fmt.Sprintf("<p>racer %d</p>", i))
			_, err := Publish("", "same-name", map[string][]byte{"index.html": payload})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	wins, losses := 0, 0
	for err := range errs {
		switch {
		case err == nil:
			wins++
		case strings.Contains(err.Error(), "already exists"):
			losses++
		default:
			t.Fatalf("unexpected concurrent Publish error (not a clean already-exists loss): %v", err)
		}
	}
	if wins != 1 || losses != racers-1 {
		t.Fatalf("wins=%d losses=%d, want exactly 1 winner and %d losers", wins, losses, racers-1)
	}

	a, ok, err := Resolve("", "same-name")
	if err != nil || !ok {
		t.Fatalf("Resolve(same-name) = %+v, %v, %v, want the winner's artifact", a, ok, err)
	}
	if a.Pages == nil || len(a.Pages) != 1 {
		t.Fatalf("winning artifact = %+v, want exactly one page (never a merge of two writers)", a)
	}
}

// ---------------------------------------------------------------------------
// RW5/RW6 — Delete racing SaveNotes without the annotation rendezvous lock
// serializing them would let a concurrent notes save recreate a sidecar file
// underneath (or immediately after) a Delete that is trying to remove it.
// The rendezvous lock (lockAnnotations: shared for normal work, exclusive for
// Delete/Unwatch) is the guard; this races the two operations for real using
// the "notes-remove"/"notes-replace" hooks to line up the interleaving
// deterministically instead of hoping a timing window is hit.
// ---------------------------------------------------------------------------

// TestDeleteRacingSaveNotesNeverLeavesOrphanedNotes migrates the ADR §6.7/RW5
// "racing Delete-vs-SaveNotes test" P3.12 owns. It uses the "notes-remove"
// hook (fires inside removeSubtree, after Delete has already taken the
// EXCLUSIVE rendezvous lock and pinned the annotation parent, before the
// removal itself) to release a concurrent SaveNotes goroutine that is
// blocked acquiring its own SHARED lock — proving the two operations
// actually serialize rather than merely usually not colliding. Whichever one
// the lock lets go second must see a clean, non-corrupted world: either the
// notes file is gone (Delete fully won) or it holds exactly the concurrent
// writer's complete content (SaveNotes fully won, after Delete's exclusive
// hold released) — never a half-written file, and never a save silently
// discarded without the caller learning about it.
//
// Negative control: without lockAnnotations serializing the two paths
// (i.e. if Delete's removeSubtree and SaveNotes's writeFile each opened
// annotationFS independently with no shared rendezvous, exactly revision 1's
// pre-ADR gap RW5 documents), the hook below would let SaveNotes's temp file
// get created and renamed into place WHILE removeTreeAt is still walking the
// same subtree, and the outcome would depend on the walk's exact timing —
// sometimes leaving the just-recreated sidecar deleted moments after
// SaveNotes returned success, silently. The lock is what turns that into the
// deterministic two-outcome result asserted below.
func TestDeleteRacingSaveNotesNeverLeavesOrphanedNotes(t *testing.T) {
	testRoot(t)
	doc := publishDoc(t, "", "art")
	if _, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "a", Status: "open"}}}, 0); err != nil {
		t.Fatalf("seed SaveNotes: %v", err)
	}

	saveDone := make(chan error, 1)
	var once sync.Once
	setStoreOpHook(t, func(op string) {
		if op != "notes-remove" {
			return
		}
		once.Do(func() {
			// Delete now holds the EXCLUSIVE rendezvous lock (by this point
			// it has already removed the artifact directory itself — see
			// store.go's Delete: removeTreeAt runs before removeNotesFor) and
			// is about to remove the annotation subtree. Kick off a
			// concurrent SaveNotes, which must block acquiring its own
			// (shared) rendezvous lock until Delete's removeSubtree — and the
			// whole Delete call — finishes and releases it.
			go func() {
				_, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "b", Status: "open"}}}, 1)
				saveDone <- err
			}()
			// Sleep on THIS goroutine (Delete's own call stack, since the
			// hook runs synchronously inside removeSubtree) before returning,
			// so Delete cannot release the exclusive lock until the
			// goroutine above has had a real chance to reach the lock
			// acquisition and start blocking on it — making this a genuine
			// contention test rather than a coincidence of scheduling.
			time.Sleep(20 * time.Millisecond)
		})
	})

	deleteErr := Delete("", "art")
	saveErr := <-saveDone

	if deleteErr != nil {
		t.Fatalf("Delete: %v", deleteErr)
	}

	// After Delete has fully returned (lock released), the world must be one
	// of exactly two clean states — never a partial/torn one.
	if saveErr == nil {
		// SaveNotes ran after Delete's exclusive hold released, against a doc
		// Delete just removed — DocExists(doc) is now false, so SaveNotes
		// must itself have refused (see TestSaveNotesRequiresDocExists) and
		// this branch should be unreachable; if it is ever reached, a stale
		// sidecar would be exactly the RW5 orphan this test exists to catch.
		f, err := loadNotesRaw(mustOpenAnnotationFSForTest(t), doc)
		t.Fatalf("SaveNotes unexpectedly succeeded after the artifact was deleted (rev-file=%+v err=%v) — this is the RW5 orphan", f, err)
	}
	if !errors.Is(saveErr, ErrRevMismatch) && !strings.Contains(saveErr.Error(), "no such document") {
		t.Fatalf("SaveNotes after a concurrent Delete failed for an unexpected reason: %v, want ErrRevMismatch or \"no such document\"", saveErr)
	}

	root, _ := Root()
	if _, err := os.Stat(filepath.Join(root, AnnotationsDir, "art")); !os.IsNotExist(err) {
		t.Errorf("annotation subtree for a deleted artifact must not survive: stat err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "art")); !os.IsNotExist(err) {
		t.Errorf("artifact directory must not survive Delete: stat err = %v", err)
	}
}

// mustOpenAnnotationFSForTest is a t.Fatal-on-error convenience for the one
// diagnostic-only call above; it is not itself part of the assertion.
func mustOpenAnnotationFSForTest(t *testing.T) *annotationFS {
	t.Helper()
	ann, err := openAnnotationFS()
	if err != nil {
		t.Fatalf("openAnnotationFS: %v", err)
	}
	t.Cleanup(func() { ann.close() })
	return ann
}

// ---------------------------------------------------------------------------
// A2.dest_replaced — the annotation write path's destination substituted in
// the window between the temp file being fully written and the atomic
// rename that replaces the destination. The junction/directory-symlink
// variants (a genuine escape attempt: redirecting the write outside the
// store) are Windows-only (storefs_windows_attack_test.go) because there is
// no Linux analogue of a junction and a Linux symlink-as-destination is
// already covered by A10.dir_link_refused-style document tests; this file
// covers the two variants that exist identically on both platforms: the
// destination becomes a real, unrelated FILE (an ordinary concurrent
// overwrite — must succeed cleanly) or a real DIRECTORY (must fail cleanly,
// with no residue).
// ---------------------------------------------------------------------------

func TestSaveNotesDestinationReplacedBeforeReplace(t *testing.T) {
	for _, kind := range []string{"realfile", "realdir"} {
		t.Run(kind, func(t *testing.T) {
			root := testRoot(t)
			doc := publishDoc(t, "", "art")
			if _, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "a", Status: "open"}}}, 0); err != nil {
				t.Fatalf("seed SaveNotes: %v", err)
			}
			dest := filepath.Join(root, AnnotationsDir, "art", "index.html.json")
			if _, err := os.Stat(dest); err != nil {
				t.Fatalf("seed destination missing: %v", err)
			}

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
				case "realfile":
					stageErr = os.WriteFile(dest, []byte("DECOY"), 0o644)
				case "realdir":
					stageErr = os.Mkdir(dest, 0o755)
				}
			})

			saveErr := func() error {
				_, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "b", Status: "open"}}}, 1)
				return err
			}()
			if stageErr != nil {
				t.Fatalf("staging %s: %v", kind, stageErr)
			}

			switch kind {
			case "realfile":
				// An ordinary destination replacement is exactly what an
				// atomic write is FOR: it must succeed and the new content
				// must win cleanly over the decoy.
				if saveErr != nil {
					t.Fatalf("replacing a plain decoy file should succeed, got %v", saveErr)
				}
				got, err := os.ReadFile(dest)
				if err != nil {
					t.Fatalf("read destination: %v", err)
				}
				if strings.Contains(string(got), "DECOY") {
					t.Fatalf("destination still holds the decoy content: %q", got)
				}
			case "realdir":
				// Replacing a FILE onto a DIRECTORY name must fail closed —
				// never silently descend into or delete the directory.
				if saveErr == nil {
					t.Fatal("replacing a directory at the destination unexpectedly succeeded")
				}
				fi, err := os.Stat(dest)
				if err != nil || !fi.IsDir() {
					t.Fatalf("the substituted directory must survive a failed replace untouched: stat = %+v, %v", fi, err)
				}
			}

			// Whatever happened, no temp file may be left behind (R9's
			// cleanup-through-the-handle guarantee, exercised here via the
			// public API rather than atomicWriteFileAt directly).
			entries, err := os.ReadDir(filepath.Join(root, AnnotationsDir, "art"))
			if err != nil {
				t.Fatalf("readdir: %v", err)
			}
			for _, e := range entries {
				if strings.HasPrefix(e.Name(), ".notes-") {
					t.Errorf("temp residue left behind after a %s destination substitution: %s", kind, e.Name())
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// A4.root_replaced — the store root substituted mid-operation, after the
// "root-open" hook fires (the root handle is pinned) but before the caller
// does anything else with it. F-b (a handle names an object, not a name)
// predicts the operation completes against the ORIGINAL root regardless.
// ---------------------------------------------------------------------------

// TestPublishSurvivesRootRenamedAwayMidOperation is the "root-open" hook's
// realdir variant: the root directory itself is renamed away (and an empty
// decoy takes its place at the old path) between when Publish pins the root
// handle and when it proceeds to claim the artifact name. Negative control:
// a hypothetical implementation that re-resolved SCRATCHPAD_ROOT by path a
// second time inside the same operation (instead of reusing the one already-
// open handle) would land the new artifact in the DECOY, not the original —
// exactly the class of bug F-b exists to rule out structurally, and exactly
// what TestPinnedMutationsIgnoreProjectSwap already proves for a PROJECT
// ancestor one level in; this is the same property one level higher, at the
// root itself.
func TestPublishSurvivesRootRenamedAwayMidOperation(t *testing.T) {
	root := testRoot(t)
	original := root + "-original"
	setStoreOpHook(t, func(op string) {
		if op != "root-open" {
			return
		}
		clearStoreOpHook()
		if err := os.Rename(root, original); err != nil {
			t.Errorf("rename root away: %v", err)
			return
		}
		if err := os.Mkdir(root, 0o755); err != nil {
			t.Errorf("recreate decoy at the old root path: %v", err)
		}
	})

	if _, err := Publish("", "safe", map[string][]byte{"index.html": []byte("safe")}); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "safe")); !os.IsNotExist(err) {
		t.Fatalf("Publish landed in the DECOY root instead of the pinned original: stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(original, "safe", "index.html"))
	if err != nil || string(got) != "safe" {
		t.Fatalf("pinned Publish should have landed in the original (renamed-away) root: content=%q err=%v", got, err)
	}
}

// ---------------------------------------------------------------------------
// browse-segment — a REAL directory ancestor, already resolved past the
// store's own project tree, swapped for a link between the moment it is
// pinned and the moment the walk proceeds to the next segment. This extends
// the existing static A3 coverage (TestOpenBrowsableDirStillRefusesNested-
// SymlinkAfterFix, TestListDoesNotFollowSymlinksInsideWatch, which plant the
// link before the walk starts) with a mid-walk TIMING variant: the ancestor
// is real and innocent when the walk begins, and only becomes a link while
// the walk is already in progress.
// ---------------------------------------------------------------------------

// TestBrowseSegmentSwappedForLinkMidWalkStillRefused requires symlink
// capability because both the legitimate boundary and the mid-walk decoy are
// built from real symlinks. Negative control: TestWatch and
// TestListDoesNotFollowSymlinksInsideWatch already show that if the SECOND
// link were reached, list/browse would produce content from the attacker's
// tree (that is the whole point of "exactly one crossing"); this test's
// value is proving the crossed-flag also holds under a hook-driven RACE, not
// only against a link planted before the walk starts.
func TestBrowseSegmentSwappedForLinkMidWalkStillRefused(t *testing.T) {
	testutil.RequireSymlinks(t)
	testRoot(t)

	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "tree", source); err != nil {
		t.Fatal(err)
	}

	attacker := t.TempDir()
	if err := os.WriteFile(filepath.Join(attacker, "LOOT.txt"), []byte("classified"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stageErr error
	setStoreOpHook(t, func(op string) {
		if op != "browse-segment" {
			return
		}
		clearStoreOpHook()
		// "tree" (the ONE permitted boundary) has just been crossed; "sub" —
		// still a real, empty directory at this instant — is about to be
		// opened next. Swap it for a second link now.
		sub := filepath.Join(source, "sub")
		if err := os.Remove(sub); err != nil {
			stageErr = err
			return
		}
		stageErr = os.Symlink(attacker, sub)
	})

	_, _, ok := ResolvePath([]string{"tree", "sub", "LOOT.txt"})
	if stageErr != nil {
		t.Fatalf("staging the mid-walk swap: %v", stageErr)
	}
	if ok {
		t.Fatal("ResolvePath crossed a SECOND link planted mid-walk after the one permitted boundary")
	}
	if f, safe := OpenDocument([]string{"tree", "sub", "LOOT.txt"}); safe {
		f.Close()
		t.Fatal("OpenDocument served the attacker's file through a second, mid-walk link")
	}
}

// ---------------------------------------------------------------------------
// doc-open — the final document substituted after its parent directory is
// pinned but before the file itself is opened. This is A10.rename_race at
// the OpenDocument layer specifically (as opposed to the browse-boundary
// layer TestBrowseRefusesWatchAncestorSymlinkSwap already covers).
// ---------------------------------------------------------------------------

// TestDocOpenSubstitutedAfterParentPinnedStillRefused requires symlink
// capability. Negative control: TestOpenDocumentRejectsArtifactAssetSymlink
// already shows a symlink planted BEFORE the open is refused; this proves
// the same refusal holds when the substitution happens in the exact window
// between parent-pin and file-open, which a path-based "validate the whole
// path, then open the whole path" implementation would miss.
func TestDocOpenSubstitutedAfterParentPinnedStillRefused(t *testing.T) {
	testutil.RequireSymlinks(t)
	root := testRoot(t)
	if _, err := Publish("", "art", map[string][]byte{
		"index.html": []byte("<p>ok</p>"),
		"asset.txt":  []byte("original"),
	}); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stageErr error
	setStoreOpHook(t, func(op string) {
		if op != "doc-open" {
			return
		}
		clearStoreOpHook()
		asset := filepath.Join(root, "art", "asset.txt")
		if err := os.Remove(asset); err != nil {
			stageErr = err
			return
		}
		stageErr = os.Symlink(outside, asset)
	})

	f, ok := OpenDocument([]string{"art", "asset.txt"})
	if stageErr != nil {
		t.Fatalf("staging the mid-open swap: %v", stageErr)
	}
	if ok {
		f.Close()
		t.Fatal("OpenDocument opened a symlink substituted after its parent directory was pinned")
	}
}

// ---------------------------------------------------------------------------
// Publish artifact-ancestor rejection, closing the spike's "Partial" matrix
// row: "dirHasHTMLFD uses strings.ToLower, not the volume's $UpCase folding
// (M11), so a .HTML entry can miss the artifact test." dirHasHTMLFD's own
// suffix check (strings.ToLower(name) has suffix ".html") folds a plain
// ASCII extension identically to $UpCase regardless of platform — the M11
// concern is about non-ASCII case-folding disagreements in the REST of a
// filename, which this specific suffix probe never looks at — so this test
// closes the row with a passing demonstration rather than assuming a defect
// the code does not actually have.
// ---------------------------------------------------------------------------

func TestRejectArtifactAncestorCaseInsensitiveHTMLExtension(t *testing.T) {
	testRoot(t)
	if _, err := Publish("", "UPPER", map[string][]byte{"INDEX.HTML": []byte("<p>shout</p>")}); err != nil {
		t.Fatalf("publish with an uppercase .HTML entry: %v", err)
	}
	if _, err := Publish("UPPER", "inner", map[string][]byte{"index.html": []byte("<p>nested</p>")}); err == nil {
		t.Fatal("publishing under an artifact whose html file has an uppercase extension should fail")
	} else if !strings.Contains(err.Error(), "UPPER") || !strings.Contains(err.Error(), "artifact") {
		t.Errorf("nesting-under-uppercase-html rejection failed for the wrong reason: %v", err)
	}
}
