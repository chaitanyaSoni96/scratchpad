//go:build windows

package testutil

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// RequireNTFS skips t when the volume containing dir is not NTFS. The
// scratchpad security model is specified for NTFS local volumes only; tests
// asserting NTFS-dependent semantics call this before touching dir. When the
// filesystem cannot be determined at all, the test is skipped too, with the
// probe error in the message, so it cannot pass vacuously on an unknown
// filesystem.
func RequireNTFS(t testing.TB, dir string) {
	t.Helper()
	fsName, err := volumeFilesystem(dir)
	if err != nil {
		t.Skipf("SKIP(ntfs-required): cannot determine the filesystem of %q: %v", dir, err)
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
