//go:build windows

package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P4.3 — Windows watch links. Four properties, verified with tests rather
// than by reading the mechanism (native-windows-support.md, P4.3):
//
//   1. same-target re-watch is a no-op, driven off
//      STATUS_REPARSE_POINT_ENCOUNTERED rather than a mechanical port of
//      Linux's errors.Is(EEXIST) — TestTranslateClaimReparseStatusesAreExists
//      (the mechanism, in isolation) and TestWatch/TestWatchOverExistingJunctionIsNoOp
//      (the same-target no-op reached through the public API, both link
//      flavours: TestWatch already covers the symlink flavour with
//      testutil.RequireSymlinks).
//   2. a different-target collision is refused —
//      TestWatchCreateOnly (store_test.go, pre-existing).
//   3. an unknown reparse tag under a watch name is refused, not silently
//      adopted — TestWatchRefusesUnknownTagCollision.
//   4. Unwatch removes only the link and never touches the target, for both
//      flavours — TestUnwatch/TestWatch (symlink, store_test.go, pre-existing)
//      and TestWatchViaJunctionIsListedAndUnwatchable (junction,
//      storefs_windows_attack_test.go, pre-existing).
//
// Plus the ADR §6.6 gap-closure evidence: TestSymlinkAtSelfHealsOnFSCTLFailure
// proves the two-step creation window's rule 1 (self-heal through the SAME
// handle, which requires the post-claim reopen to hold DELETE access) for
// BOTH link flavours, deterministically — no privilege manipulation needed,
// because checkedNameLen (win32_windows.go) rejects an oversized target
// before either flavour's DeviceIoControl call, which is a synchronous
// failure symlinkAt's rule 1 must clean up regardless of why the FSCTL step
// failed.
// ---------------------------------------------------------------------------

// TestTranslateClaimReparseStatusesAreExists is the direct, mechanism-level
// test for property 1: M8.claim_error_map (spike-findings.md §4.2, ADR
// §6.6) found that a taken name occupied by a junction or directory symlink
// fails a FILE_CREATE claim with STATUS_REPARSE_POINT_ENCOUNTERED, not
// STATUS_OBJECT_NAME_COLLISION — the status a mechanical port of Linux's
// errors.Is(err, unix.EEXIST) would be built to recognise and this one would
// not. translateClaim (win32_windows.go) is the fix: it maps BOTH statuses
// (plus STATUS_IO_REPARSE_TAG_NOT_HANDLED, for an unrecognised tag on the
// claim path) to errExistsReparse, which wraps errExists — so Watch's
// `errors.Is(err, errExists)` collision check (store.go) catches every one
// of them, and this test asserts that mapping directly rather than only
// observing its effect through Watch.
func TestTranslateClaimReparseStatusesAreExists(t *testing.T) {
	reparse := []windows.NTStatus{
		windows.STATUS_REPARSE_POINT_ENCOUNTERED,
		windows.STATUS_IO_REPARSE_TAG_NOT_HANDLED,
	}
	for _, st := range reparse {
		err := translateClaim("mkdir", st)
		if !errors.Is(err, errExists) {
			t.Errorf("translateClaim(%v) = %v, want errors.Is(_, errExists)", st, err)
		}
		if !errors.Is(err, errExistsReparse) {
			t.Errorf("translateClaim(%v) = %v, want errors.Is(_, errExistsReparse) specifically — a mechanical EEXIST-only port would have missed this status entirely (M8.claim_error_map)", st, err)
		}
	}
	// The negative control this test needs teeth: an ordinary collision
	// (plain directory or file already there) is a DIFFERENT status and
	// must land on errExists WITHOUT the errExistsReparse wrapping, so a
	// test asserting only "errors.Is(_, errExists)" above cannot be
	// satisfied vacuously by every NTSTATUS mapping to errExists.
	plain := translateClaim("mkdir", windows.STATUS_OBJECT_NAME_COLLISION)
	if !errors.Is(plain, errExists) {
		t.Fatalf("translateClaim(STATUS_OBJECT_NAME_COLLISION) = %v, want errors.Is(_, errExists)", plain)
	}
	if errors.Is(plain, errExistsReparse) {
		t.Fatalf("translateClaim(STATUS_OBJECT_NAME_COLLISION) = %v, unexpectedly wrapped errExistsReparse — the two claim shapes must stay distinguishable", plain)
	}
	// A status that means neither must not be swept into errExists at all.
	other := translateClaim("mkdir", windows.STATUS_ACCESS_DENIED)
	if errors.Is(other, errExists) {
		t.Fatalf("translateClaim(STATUS_ACCESS_DENIED) = %v, unexpectedly errors.Is(_, errExists)", other)
	}
}

// TestWatchOverExistingJunctionIsNoOp is property 1 through the public API,
// junction flavour (TestWatch, store_test.go, already covers the symlink
// flavour via testutil.RequireSymlinks). Re-watching the exact same target
// under a junction-backed link must succeed as a no-op, not fail — the
// regression a mechanical errors.Is(EEXIST) port would introduce.
func TestWatchOverExistingJunctionIsNoOp(t *testing.T) {
	testRoot(t)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<h1>src</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}

	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	if err := makeJunctionAt(int(rfs.root.Fd()), "viajunction2", source); err != nil {
		rfs.close()
		t.Fatalf("makeJunctionAt: %v", err)
	}
	rfs.close()

	if _, err := Watch("", "viajunction2", source); err != nil {
		t.Fatalf("re-watching the same target over an existing junction should be a no-op, got %v", err)
	}
	links, err := Watches()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].Path != "viajunction2" || !sameTarget(links[0].Target, source) {
		t.Fatalf("Watches() = %+v, want exactly one link still resolved to %q", links, source)
	}
}

// TestWatchRefusesUnknownTagCollision is property 3: an entry tagged with a
// reparse point scratchpad does not recognise must never be silently
// adopted as if it were the store's own watch link. Watch's collision
// branch (store.go) must refuse it, Watches() must never report it, and the
// entry itself must be left untouched — not overwritten, not upgraded into
// a real link.
func TestWatchRefusesUnknownTagCollision(t *testing.T) {
	root := testRoot(t)
	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	if err := makeUnknownTagReparseAt(int(rfs.root.Fd()), "mystery2", nonMicrosoftTag); err != nil {
		rfs.close()
		t.Fatalf("makeUnknownTagReparseAt: %v", err)
	}
	rfs.close()

	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "index.html"), []byte("<p>ok</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Watch("", "mystery2", target); err == nil {
		t.Fatal("Watch unexpectedly succeeded over an unrecognised-tag collision")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("Watch collision error = %v, want it to say the name already exists", err)
	}

	if links, _ := Watches(); len(links) != 0 {
		t.Errorf("Watches() = %+v, want none — the unrecognised-tag entry must never be reported as a watch link", links)
	}
	// The entry itself must be exactly what it was — not silently replaced,
	// upgraded, or removed by the failed attempt.
	rfs2, err := openRootedFS(false)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	defer rfs2.close()
	tag, err := readLinkTagAt(int(rfs2.root.Fd()), "mystery2")
	if err != nil {
		t.Fatalf("readLinkTagAt(mystery2): %v", err)
	}
	if tag != nonMicrosoftTag {
		t.Errorf("mystery2's tag = 0x%08X after the refused watch, want unchanged 0x%08X", tag, nonMicrosoftTag)
	}
	_ = root
}

// TestSymlinkAtSelfHealsOnFSCTLFailure is the direct evidence for both of
// ADR §6.6's recorded gaps being closed: "CreateJunctionAt's missing cleanup
// on failure" and "the missing DELETE access on the reopen". There is no
// longer a separate CreateJunctionAt in this package — symlinkAt tries a
// directory symbolic link, then a junction, through the SAME reopened
// handle, and cleans up through that handle on ANY failure of either
// flavour's DeviceIoControl call (link_windows.go). This test forces that
// failure deterministically, without touching privilege at all:
// checkedNameLen (win32_windows.go) rejects a target whose UTF-16 byte
// length would overflow the reparse buffer's uint16 fields BEFORE either
// setSymlinkReparse or setMountPointReparse ever calls DeviceIoControl — a
// synchronous failure of exactly the kind rule 1 must self-heal, on every
// machine regardless of Developer Mode or privilege state.
func TestSymlinkAtSelfHealsOnFSCTLFailure(t *testing.T) {
	root := testRoot(t)
	// 0x10000 UTF-16 code units (2 bytes each) guarantees checkedNameLen's
	// 0xFFFF-byte ceiling is exceeded for BOTH the substitute and print
	// names symlinkAt/setSymlinkReparse/setMountPointReparse construct, so
	// neither flavour's FSCTL is ever reached.
	oversizedTarget := `C:\` + strings.Repeat("a", 0x10000)

	rfs, err := openRootedFS(true)
	if err != nil {
		t.Fatalf("openRootedFS: %v", err)
	}
	defer rfs.close()
	parent := int(rfs.root.Fd())

	if err := symlinkAt(parent, oversizedTarget, "toolong"); err == nil {
		t.Fatal("symlinkAt with an oversized target unexpectedly succeeded")
	}

	// Rule 1's whole point: no wedged empty directory left behind. If either
	// the cleanup were missing (revision 1's bug) or the reopen lacked
	// DELETE access (the other half of the same gap), this Stat would
	// succeed and find the leftover directory instead of ErrNotExist.
	if _, statErr := os.Stat(filepath.Join(root, "toolong")); !os.IsNotExist(statErr) {
		t.Fatalf("residue left behind after a synchronous FSCTL failure (ADR §6.6 rule 1 not holding): stat err = %v", statErr)
	}

	// The name must be fully free again — not merely absent from disk but
	// unclaimed from the store's point of view — so a real watch can use it
	// immediately afterward.
	if _, err := Watch("", "toolong", t.TempDir()); err != nil {
		t.Fatalf("watch of the freed name after cleanup should succeed, got %v", err)
	}
}
