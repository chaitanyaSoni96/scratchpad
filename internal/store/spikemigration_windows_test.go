//go:build windows

package store

// The Windows half of the internal/winspike migration (ADR §11.1, plan in
// P6.2 §11.4): the properties whose whole point is a Windows mechanism —
// refusing a store root on the reparse TAG rather than on fs.ModeSymlink, the
// junction flavour of the annotation-component refusal, and the share mode a
// served document is opened with.

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"

	"scratchpad/internal/testutil"
)

// plantDirLink is the Windows half of the pair described in
// spikemigration_linux_test.go. A JUNCTION, deliberately, not a symlink:
// junction creation needs no privilege at all, so a shared test that plants
// its decoy through this runs on an ordinary Developer-Mode-off Windows box
// instead of skipping — which is the difference between covering a matrix
// cell and appearing to.
func plantDirLink(link, target string) error { return makeJunctionAtPath(link, target) }

// openDirHandleForTest opens dir by path as a directory handle, for staging
// only: the reparse-planting helpers are handle-relative, and a candidate
// store root has no parent inside the store for a test to have pinned.
func openDirHandleForTest(t *testing.T, dir string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.FILE_GENERIC_WRITE, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		t.Fatalf("open staging directory %q: %v", dir, err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
	return h
}

// ---------------------------------------------------------------------------
// §11.4 item 2, Windows half — MX.root_reparse.* / A4.root_reparse_refused.*.
// ---------------------------------------------------------------------------

// TestRootMustNotBeAReparsePoint is the AC5 "Root / link or reparse point"
// cell, and specifically the half that can only be shown on Windows: that
// openRootedFS refuses on the reparse TAG read from the pinned handle, not on
// a mode bit.
//
// The unknown-tag case is what discriminates. A junction and a directory
// symlink would both be caught by a lazy fs.ModeSymlink-shaped check on a
// good day (a junction actually would not — it is ModeIrregular — which is
// the other half of the same point), but a NON-MICROSOFT tag on a directory is
// reported by Go as plain ModeDir with no link bit anywhere: RR1's second
// vector, ADR §5.2. A root guard written against os.Lstat's mode would wave it
// straight through. This asserts the shipped guard names the tag it refused,
// which is only possible if the decision came from FILE_ATTRIBUTE_TAG_INFO.
func TestRootMustNotBeAReparsePoint(t *testing.T) {
	testutil.RequireWatchLinks(t)
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	staging := openDirHandleForTest(t, base)

	cases := []struct {
		name    string
		plant   func(t *testing.T, path string)
		wantTag string
	}{
		{
			name:    "junction",
			plant:   func(t *testing.T, path string) { mustPlantJunction(t, path, target) },
			wantTag: "MOUNT_POINT",
		},
		{
			name: "unknown-tag",
			plant: func(t *testing.T, path string) {
				if err := makeUnknownTagReparseAt(int(staging), filepath.Base(path), nonMicrosoftTag); err != nil {
					t.Fatal(err)
				}
			},
			wantTag: "0x00001234",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetRootIdentityCacheForTest()
			path := filepath.Join(base, "root-"+tc.name)
			tc.plant(t, path)

			t.Setenv(RootEnv, path)
			rfs, err := openRootedFS(false)
			if err == nil {
				rfs.close()
				t.Fatalf("openRootedFS accepted a %s reparse point as the store root", tc.name)
			}
			// Refused ON THE TAG: the message names the tag that was read from
			// the handle. A mode-based guard could not produce this string, and
			// for the unknown-tag case could not have refused at all.
			if !strings.Contains(err.Error(), tc.wantTag) {
				t.Errorf("openRootedFS refused a %s but did not name the tag %s it read from the handle: %v", tc.name, tc.wantTag, err)
			}
			if !strings.Contains(err.Error(), "reparse point") {
				t.Errorf("openRootedFS refusal does not read as a reparse-point refusal: %v", err)
			}
		})
	}

	// Positive control: the junction's TARGET, used directly as the root,
	// works. The refusals above are therefore about the link and not about the
	// directory underneath it.
	resetRootIdentityCacheForTest()
	t.Setenv(RootEnv, target)
	rfs, err := openRootedFS(false)
	if err != nil {
		t.Fatalf("control: openRootedFS refused the reparse point's target directly: %v", err)
	}
	rfs.close()
}

func mustPlantJunction(t *testing.T, link, target string) {
	t.Helper()
	if err := makeJunctionAtPath(link, target); err != nil {
		t.Fatalf("plant junction at %q: %v", link, err)
	}
}

// ---------------------------------------------------------------------------
// §11.4 item 4 — the junction flavour of MX.notes_{root,intermediate}_link.*.
// ---------------------------------------------------------------------------

// TestAnnotationJunctionComponentsRejected is the junction sibling of
// TestAnnotationSymlinkComponentsRejected (annotations_test.go). That test is
// gated on RequireSymlinks, so on an ordinary Developer-Mode-off Windows box
// the annotation backend's component refusal had NO coverage at all — the same
// P-4 gap that was found and fixed for internal/watch and internal/web and
// missed for annotations.
//
// A junction is the flavour that matters here for a second reason: it is what
// symlinkAt itself falls back to when a directory symlink cannot be created,
// so it is the link shape a real Windows store is most likely to contain.
func TestAnnotationJunctionComponentsRejected(t *testing.T) {
	testutil.RequireWatchLinks(t)
	root := testRoot(t)
	doc := publishDoc(t, "", "art")
	control := publishDoc(t, "", "control")
	outside := t.TempDir()

	annRoot := filepath.Join(root, AnnotationsDir)
	if err := os.Mkdir(annRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	mustPlantJunction(t, filepath.Join(annRoot, "art"), outside)

	// Positive control first: an identically shaped document whose mirrored
	// path holds a REAL directory must work, so the refusals below are caused
	// by the junction and not by the backend being broken outright.
	if _, err := SaveNotes(control, NotesFile{Annotations: []Annotation{{ID: "c"}}}, 0); err != nil {
		t.Fatalf("control: SaveNotes on a doc with no junction in its path should succeed, got %v", err)
	}
	if f, err := LoadNotes(control); err != nil || len(f.Annotations) != 1 {
		t.Fatalf("control: LoadNotes = %+v, %v, want the one annotation just saved", f, err)
	}

	if _, err := LoadNotes(doc); err == nil {
		t.Error("LoadNotes accepted a junction annotation component")
	}
	if _, err := SaveNotes(doc, NotesFile{Annotations: []Annotation{{ID: "n"}}}, 0); err == nil {
		t.Error("SaveNotes accepted a junction annotation component")
	}
	if err := DeleteNotes(doc); err == nil {
		t.Error("DeleteNotes accepted a junction annotation component")
	}
	if _, err := WalkNotes("art"); err == nil {
		t.Error("WalkNotes accepted a junction annotation component")
	}

	// The payoff a real attack wants: nothing was written through the junction
	// into the external tree.
	leaked, err := os.ReadDir(outside)
	if err != nil {
		t.Fatalf("reading the external target: %v", err)
	}
	if len(leaked) != 0 {
		t.Fatalf("the annotation backend wrote through the junction: %v", leaked)
	}
}

// ---------------------------------------------------------------------------
// §11.4 item 5 — P13.go_share_mode, the DOCUMENT half.
// ---------------------------------------------------------------------------

// TestOpenDocumentGrantsFileShareDelete pins the reason documents are never
// served through os.Open (R15, P13.go_share_mode). §11.1 assigned this to
// P3.5; only the watcher half (TestOpenWatchDirGrantsFileShareDelete) was ever
// written.
//
// The negative control is the entire point and is why this test is worth
// having: Go's own os.Open hard-codes FILE_SHARE_READ|FILE_SHARE_WRITE and
// omits FILE_SHARE_DELETE, so a document served that way VETOES a concurrent
// delete or rename of itself — which on this store would mean an open preview
// blocking the atomic replace of a notes file, or a user's own delete from
// Explorer. Asserting only that our open permits deletion would pass against
// an implementation that had never thought about share modes at all; asserting
// that os.Open does NOT is what gives the first half teeth.
func TestOpenDocumentGrantsFileShareDelete(t *testing.T) {
	root := testRoot(t)
	if _, err := Publish("", "art", map[string][]byte{
		"index.html": []byte("entry"),
		"page.html":  []byte("served"),
	}); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(root, "art", "page.html")

	// Negative control: the standard-library open holds the file against
	// deletion. If this ever starts succeeding, Go changed its share mode and
	// the assertion below stops proving anything.
	if f, err := os.Open(victim); err == nil {
		delErr := os.Remove(victim)
		f.Close()
		if delErr == nil {
			t.Skip("SKIP(share-mode-control): os.Open no longer vetoes deletion on this platform/toolchain, " +
				"so the assertion below would not discriminate; P13.go_share_mode needs re-measuring")
		}
	} else {
		t.Fatalf("control: could not open the document with os.Open: %v", err)
	}

	// The real assertion: a document pinned by the store's own primitive does
	// NOT veto deletion, because openStrictAt passes shareAll.
	f, ok := OpenDocument([]string{"art", "page.html"})
	if !ok {
		t.Fatal("OpenDocument refused a published top-level page")
	}
	defer f.Close()
	if err := os.Remove(victim); err != nil {
		t.Fatalf("a document served by OpenDocument vetoed its own deletion — "+
			"the handle is missing FILE_SHARE_DELETE (R15, P13.go_share_mode): %v", err)
	}
	// The handle was, and still is, a live handle on the document. Without
	// this the assertion above could pass vacuously against an OpenDocument
	// that returned something already closed — deleting a file nothing holds
	// open trivially succeeds, and would look exactly like the property under
	// test.
	body, err := io.ReadAll(f)
	if err != nil || string(body) != "served" {
		t.Fatalf("reading through the pinned document handle after its name was removed = %q, %v; want %q — "+
			"the handle under test was not a live one", body, err, "served")
	}
}
