//go:build windows

package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// setUnknownReparseTag stamps the empty directory dir with a non-Microsoft
// reparse tag, using the REPARSE_GUID_DATA_BUFFER form FSCTL_SET_REPARSE_POINT
// requires for a tag no filter driver on this machine claims. This
// reproduces, deterministically and without any real cloud-sync client or
// container runtime installed, the exact failure shape the ADR traces end to
// end as the "boot loop" (§6.11, finding F6): opening such a directory
// without FILE_FLAG_OPEN_REPARSE_POINT fails with
// STATUS_IO_REPARSE_TAG_NOT_HANDLED, which CreateFile surfaces as Win32
// ERROR_CANT_ACCESS_FILE (1920) — the same failure an APPEXECLINK, a
// OneDrive placeholder, or an unserviced ProjFS entry produces.
//
// The wire format mirrors the shape already measured working in this
// repo's Windows CI evidence (internal/winspike's SetUnknownTag /
// M4.noprivilege, A5.unknown_tag_refused). It is reimplemented here rather
// than imported from internal/winspike because that package is scheduled
// for deletion (ADR §11.1) and internal/watch must not take a dependency on
// it.
func setUnknownReparseTag(t *testing.T, dir string) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		t.Fatalf("UTF16PtrFromString(%q): %v", dir, err)
	}
	h, err := windows.CreateFile(p,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0)
	if err != nil {
		t.Fatalf("open %q to write a reparse point: %v", dir, err)
	}
	defer windows.CloseHandle(h)

	// REPARSE_GUID_DATA_BUFFER: ReparseTag(4) ReparseDataLength(2)
	// Reserved(2) ReparseGuid(16) DataBuffer[ReparseDataLength]. Bit 31
	// clear on the tag marks it third-party/non-Microsoft, which is what
	// makes no filter driver on the runner claim it.
	const tag = 0x00001234
	const dataLen = 8
	buf := make([]byte, 24+dataLen)
	putU32(buf, 0, tag)
	putU16(buf, 4, dataLen)
	putU16(buf, 6, 0)
	// An arbitrary fixed GUID; its value is never inspected by anything in
	// this test or in production code.
	guid := [16]byte{
		0x4a, 0x9b, 0x0e, 0x5c, 0x1d, 0x7f, 0x2e, 0x4d,
		0x9a, 0x3b, 0x0f, 0x6c, 0x8e, 0x2d, 0x1a, 0x77,
	}
	copy(buf[8:24], guid[:])

	var n uint32
	if err := windows.DeviceIoControl(h, windows.FSCTL_SET_REPARSE_POINT,
		&buf[0], uint32(len(buf)), nil, 0, &n, nil); err != nil {
		// Measured (M4.noprivilege/M4.nonempty) to succeed unprivileged on
		// an empty directory on the two evidence runners; skip rather than
		// fail if some other runner's policy differs, the same posture
		// testutil.RequireSymlinks takes for a missing OS capability.
		t.Skipf("cannot set a non-Microsoft reparse tag on this machine: %v", err)
	}
}

func putU16(b []byte, i int, v uint16) { b[i] = byte(v); b[i+1] = byte(v >> 8) }
func putU32(b []byte, i int, v uint32) {
	b[i], b[i+1], b[i+2], b[i+3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

// TestDesiredDirsSkipsUnservicedReparseTagInsteadOfFailing is F6/RW23's
// regression test: a single directory desiredDirs cannot open or classify
// must be skipped, not turned into an error that fails the whole walk.
func TestDesiredDirsSkipsUnservicedReparseTagInsteadOfFailing(t *testing.T) {
	root := t.TempDir()
	okDir := filepath.Join(root, "ok")
	if err := os.MkdirAll(okDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tagged := filepath.Join(root, "tagged")
	if err := os.Mkdir(tagged, 0o755); err != nil {
		t.Fatal(err)
	}
	setUnknownReparseTag(t, tagged)

	dirs, err := desiredDirs(root)
	if err != nil {
		t.Fatalf("desiredDirs returned a fatal error for one unserviced reparse-tagged directory, want a logged skip: %v", err)
	}

	rootCanonical, err := canonicalDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dirs[rootCanonical]; !ok {
		t.Fatal("root missing from the desired watch set")
	}
	okCanonical, err := canonicalDir(okDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dirs[okCanonical]; !ok {
		t.Fatal("sibling directory missing from the desired watch set — one bad entry must not take others down with it")
	}
	taggedCanonical, err := canonicalDir(tagged)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := dirs[taggedCanonical]; ok {
		t.Fatal("the unserviced reparse-tagged directory should have been skipped, not watched")
	}
}

// TestNewWatcherStartsDespiteUnservicedReparseTag is the end-to-end
// regression test for the "boot loop": reconcile() runs at startup
// (newWatcher), and per §6.11 item 3 startup must get the same triage as
// steady state. Before this fix, one such directory anywhere under the
// store root made newWatcher — and therefore the web server — fail to
// start, every time, with no user action able to recover it short of
// deleting the offending entry outside the running server.
func TestNewWatcherStartsDespiteUnservicedReparseTag(t *testing.T) {
	root := t.TempDir()
	tagged := filepath.Join(root, "tagged")
	if err := os.Mkdir(tagged, 0o755); err != nil {
		t.Fatal(err)
	}
	setUnknownReparseTag(t, tagged)

	b := newFakeBackend()
	w, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour)
	if err != nil {
		t.Fatalf("newWatcher failed at startup because of one unreadable entry (this is the F6 boot loop): %v", err)
	}
	if len(b.WatchList()) == 0 {
		t.Fatal("expected the root to be registered despite the unreadable sibling entry")
	}
	if err := w.backend.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestOpenWatchDirGrantsFileShareDelete is RW24's regression test:
// openWatchDir must not veto a concurrent rename/delete of a directory it
// has open, the way os.Open (FILE_SHARE_READ|FILE_SHARE_WRITE only,
// P13.go_share_mode) does.
func TestOpenWatchDirGrantsFileShareDelete(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "watched")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := openWatchDir(dir)
	if err != nil {
		t.Fatalf("openWatchDir: %v", err)
	}
	defer f.Close()

	if err := os.Rename(dir, filepath.Join(root, "renamed")); err != nil {
		t.Fatalf("rename of a directory held open by openWatchDir was vetoed (missing FILE_SHARE_DELETE): %v", err)
	}
}
