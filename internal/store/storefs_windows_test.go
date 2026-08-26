//go:build windows

package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"scratchpad/internal/testutil"
)

// TestValidateAbsoluteWindowsPath is the direct test for the ONE validator
// §4.1 (Root()) and §3.3 (readlinkAt's target) share — before this file it
// was reached only indirectly through Watch/ResolvePath (P4.7
// semantic-parity finding P-6). Table-driven because it is a pure string
// function: no filesystem, no privilege, nothing platform-fragile.
func TestValidateAbsoluteWindowsPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute drive-rooted", `C:\foo\bar`, false},
		{"lowercase drive letter", `c:\foo`, false},
		{"bare drive root", `C:\`, false},
		{"relative", `foo\bar`, true},
		{"drive-relative", `C:foo`, true},
		{"current-drive-relative", `\foo`, true},
		{"UNC path", `\\server\share`, true},
		{`device namespace \\?\`, `\\?\C:\foo`, true},
		{`device namespace \\.\`, `\\.\C:\foo`, true},
		{"too short", `C:`, true},
		{"empty", "", true},
		{"non-letter drive", `1:\foo`, true},
		{"missing backslash after colon", `C:foo\bar`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAbsoluteWindowsPath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateAbsoluteWindowsPath(%q) = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

// TestOpenVolumeRootNoFollowOpensRealRoot is the direct test for the
// trusted, fixed anchor openAbsoluteDirNoFollowWin walks every absolute
// target from — before this file it was reached only indirectly (P-6). A
// handle genuinely anchored at the drive root must be able to open a real
// child by name; t.TempDir()'s own drive is used rather than a hardcoded
// "C:\" so this does not assume where the runner's temp directory lives.
func TestOpenVolumeRootNoFollowOpensRealRoot(t *testing.T) {
	dir := t.TempDir()
	drive := dir[:3] // e.g. "C:\"
	fd, err := openVolumeRootNoFollow(drive)
	if err != nil {
		t.Fatalf("openVolumeRootNoFollow(%q): %v", drive, err)
	}
	defer closeFD(fd)

	rel := strings.TrimPrefix(dir, drive)
	first := strings.SplitN(rel, `\`, 2)[0]
	sub, err := openRealDirAt(fd, first)
	if err != nil {
		t.Fatalf("openRealDirAt(volume root, %q) = %v, want success from a genuine volume-root handle", first, err)
	}
	closeFD(sub)
}

// TestOpenVolumeRootNoFollowRefusesMalformedDrive proves the failure side:
// "0" is never a valid drive letter, so this must be refused rather than
// silently opened as if it named something.
func TestOpenVolumeRootNoFollowRefusesMalformedDrive(t *testing.T) {
	if fd, err := openVolumeRootNoFollow(`0:\`); err == nil {
		closeFD(fd)
		t.Fatal(`openVolumeRootNoFollow("0:\\") succeeded, want refusal`)
	}
}

// TestOpenAbsoluteDirNoFollowWinRefusesRelativeTarget is the Windows
// counterpart of storefs_linux_test.go's
// TestOpenAbsoluteDirNoFollowRefusesRelativeTarget (P-6): a relative path
// has no trusted anchor to walk it from.
func TestOpenAbsoluteDirNoFollowWinRefusesRelativeTarget(t *testing.T) {
	if fd, err := openAbsoluteDirNoFollowWin(`relative\path`); err == nil {
		closeFD(fd)
		t.Fatal("openAbsoluteDirNoFollowWin(relative path) = nil error, want a refusal")
	}
}

// TestOpenAbsoluteDirNoFollowWinRefusesUNCTarget confirms the walk itself
// also refuses what validateAbsoluteWindowsPath already refuses (it calls
// that validator first), so the containment guarantee does not silently
// depend only on callers validating first.
func TestOpenAbsoluteDirNoFollowWinRefusesUNCTarget(t *testing.T) {
	if fd, err := openAbsoluteDirNoFollowWin(`\\server\share\dir`); err == nil {
		closeFD(fd)
		t.Fatal("openAbsoluteDirNoFollowWin(UNC path) = nil error, want a refusal")
	}
}

// TestOpenAbsoluteDirNoFollowWinOpensRealTarget is the plain success case:
// a genuine absolute, drive-rooted directory must open cleanly.
func TestOpenAbsoluteDirNoFollowWinOpensRealTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	fd, err := openAbsoluteDirNoFollowWin(target)
	if err != nil {
		t.Fatalf("openAbsoluteDirNoFollowWin(%q): %v", target, err)
	}
	closeFD(fd)
}

// TestOpenAbsoluteDirNoFollowWinRefusesJunctionAncestor is the Windows
// counterpart of storefs_linux_test.go's
// TestOpenAbsoluteDirNoFollowRefusesSymlinkAncestor: an ancestor swapped
// for a link partway through the walk must be refused at that component,
// not silently followed (A11.ancestor_swapped). Junction, not symlink,
// because junction creation needs no privilege at all
// (testutil.RequireWatchLinks), and openRealDirAt refuses ANY reparse tag
// regardless of flavour (openStrictAt, storefs_windows.go), so this proves
// the same property the Linux test does without needing symlink privilege.
func TestOpenAbsoluteDirNoFollowWinRefusesJunctionAncestor(t *testing.T) {
	testutil.RequireWatchLinks(t)
	base := t.TempDir()
	subdir := filepath.Join(base, "subdir")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(subdir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	// Before the swap, the walk succeeds cleanly.
	fd, err := openAbsoluteDirNoFollowWin(target)
	if err != nil {
		t.Fatalf("openAbsoluteDirNoFollowWin(%q) = %v, want success", target, err)
	}
	closeFD(fd)

	// Swap the ancestor "subdir" for a junction into an attacker tree.
	attacker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(attacker, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}
	if err := testutil.MakeJunction(subdir, attacker); err != nil {
		t.Fatal(err)
	}

	if fd, err := openAbsoluteDirNoFollowWin(target); err == nil {
		closeFD(fd)
		t.Fatalf("openAbsoluteDirNoFollowWin(%q) succeeded after an ancestor became a junction, want refusal", target)
	}
}
