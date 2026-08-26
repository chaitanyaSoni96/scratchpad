//go:build linux

package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"

	"scratchpad/internal/testutil"
)

// TestOpenAbsoluteDirNoFollowRefusesRelativeTarget guards the "no safe
// anchor" refusal: a relative path has no trusted directory to walk it
// from, so it must be refused outright rather than resolved against some
// ambient default (the process's current working directory — exactly what
// re-opening the string as-is, the old code's approach, would have done).
func TestOpenAbsoluteDirNoFollowRefusesRelativeTarget(t *testing.T) {
	if fd, err := openAbsoluteDirNoFollow("relative/path"); err == nil {
		unix.Close(fd)
		t.Fatal("openAbsoluteDirNoFollow(relative path) = nil error, want a refusal")
	}
}

// TestOpenAbsoluteDirNoFollowRefusesSymlinkAncestor is the unit-level
// version of the A11.ancestor_swapped regression in store_test.go: it
// exercises openAbsoluteDirNoFollow directly rather than through the full
// Watch/ResolvePath stack, so a failure here points straight at the walk.
func TestOpenAbsoluteDirNoFollowRefusesSymlinkAncestor(t *testing.T) {
	testutil.RequireSymlinks(t)
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
	fd, err := openAbsoluteDirNoFollow(target)
	if err != nil {
		t.Fatalf("openAbsoluteDirNoFollow(%q) = %v, want success", target, err)
	}
	unix.Close(fd)

	// Swap the ancestor "subdir" for a symlink into an attacker tree.
	attacker := t.TempDir()
	if err := os.MkdirAll(filepath.Join(attacker, "target"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(subdir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(attacker, subdir); err != nil {
		t.Fatal(err)
	}

	fd, err = openAbsoluteDirNoFollow(target)
	if err == nil {
		unix.Close(fd)
		t.Fatalf("openAbsoluteDirNoFollow(%q) succeeded after an ancestor became a symlink, want refusal", target)
	}
	// openDirAt's O_DIRECTORY|O_NOFOLLOW on a symlink surfaces as either
	// errno depending on kernel version/path shape — openBrowsableDir's own
	// crossing check already treats both as "hit a symlink" for the same
	// reason (storefs_linux.go).
	if !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("openAbsoluteDirNoFollow error = %v, want ELOOP or ENOTDIR", err)
	}
}

// TestOpenAbsoluteDirNoFollowRefusesFinalComponentSymlink reconfirms
// A11.target_swapped (the target itself, not an ancestor, replaced with a
// symlink) is still refused by the rewritten walk — this case already held
// before the fix (O_NOFOLLOW on the final unix.Open protected it), and must
// keep holding.
func TestOpenAbsoluteDirNoFollowRefusesFinalComponentSymlink(t *testing.T) {
	testutil.RequireSymlinks(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	fd, err := openAbsoluteDirNoFollow(link)
	if err == nil {
		unix.Close(fd)
		t.Fatalf("openAbsoluteDirNoFollow(%q) followed a symlink final component, want refusal", link)
	}
	if !errors.Is(err, unix.ELOOP) && !errors.Is(err, unix.ENOTDIR) {
		t.Errorf("openAbsoluteDirNoFollow error = %v, want ELOOP or ENOTDIR", err)
	}
}
