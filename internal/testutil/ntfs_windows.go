//go:build windows

package testutil

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// RequireNTFS skips t when the volume containing dir is confirmed to be a
// filesystem OTHER than NTFS, and calls t.Fatalf — not Skipf — when the
// filesystem cannot be determined at all.
//
// P0.5/P2.7 finding F6: an earlier version of this function skipped on "cannot
// determine", with a doc comment claiming that made the test "unable to pass
// vacuously". That claim was true only at the level of one test function: at
// the level of a CI job, a skip IS a vacuous pass, because no job here counts
// skips as failures. If GetVolumePathName/GetVolumeInformation ever failed for
// any reason — an unusual mount, a permission quirk, a stale handle — every
// NTFS-dependent security test in the suite would silently disappear and the
// required job would stay green. A probe failure is not evidence of a
// non-NTFS volume; it is evidence that this helper could not do its job, and
// that must be loud, not a quiet skip. The Windows CI runners this matters on
// are NTFS by construction, so a real probe failure here should never fire in
// practice — which is exactly why, if it ever does, it needs to fail the
// build rather than be absorbed as one more skip line nobody reads.
func RequireNTFS(t testing.TB, dir string) {
	t.Helper()
	fsName, err := volumeFilesystem(dir)
	if err != nil {
		t.Fatalf("RequireNTFS: could not determine the filesystem of %q: %v (this is a harness failure, not a reason to skip an NTFS-dependent test)", dir, err)
		return
	}
	if !strings.EqualFold(fsName, "NTFS") {
		t.Skipf("SKIP(ntfs-required): %q is on %s, not NTFS; this test asserts NTFS-only semantics", dir, fsName)
	}
}

// volumeFilesystem returns the filesystem name (e.g. "NTFS", "FAT32", "ReFS")
// of the volume containing dir.
func volumeFilesystem(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	absP, err := windows.UTF16PtrFromString(abs)
	if err != nil {
		return "", err
	}
	// Resolve the volume mount point governing abs, then ask that root for
	// its filesystem name.
	root := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumePathName(absP, &root[0], uint32(len(root))); err != nil {
		return "", err
	}
	fsName := make([]uint16, windows.MAX_PATH+1)
	if err := windows.GetVolumeInformation(&root[0], nil, 0, nil, nil, nil, &fsName[0], uint32(len(fsName))); err != nil {
		return "", err
	}
	return windows.UTF16ToString(fsName), nil
}
