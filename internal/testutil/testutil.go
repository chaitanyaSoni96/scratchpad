// Package testutil provides OS-capability helpers for the test suites.
//
// The helpers keep platform assumptions out of individual tests: a test that
// needs a capability calls the matching Require* function first and is skipped
// with a greppable reason when the environment cannot provide it. Skip reasons
// carry stable markers so CI can count and audit them:
//
//	SKIP(symlink-capability)  the process cannot create symlinks
//	SKIP(ntfs-required)       the volume under test is not NTFS (Windows only)
//	SKIP(unix-only)           the test asserts genuinely Unix-specific semantics
//
// The package is internal and imported only by tests; it must never be pulled
// into production code paths.
package testutil

import (
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// SymlinkTestEnv overrides symlink-capability detection in tests when set:
// "0" reports incapable, "1" reports capable. Any other value (or unset)
// falls back to probing the OS once per process.
const SymlinkTestEnv = "SCRATCHPAD_TEST_SYMLINKS"

var (
	symlinkOnce    sync.Once
	symlinkCapable bool
)

// RequireSymlinks skips t when the OS or this process cannot create symbolic
// links. On non-Windows systems it never skips. On Windows, unprivileged
// symlink creation requires Developer Mode (or SeCreateSymbolicLinkPrivilege);
// the skip message names that remediation.
func RequireSymlinks(t testing.TB) {
	t.Helper()
	if SymlinkCapable() {
		return
	}
	t.Skipf("SKIP(symlink-capability): this process cannot create symlinks on %s; "+
		"enable Windows Developer Mode (Settings > System > For developers) or grant "+
		"SeCreateSymbolicLinkPrivilege, or set %s=1 to override detection",
		runtime.GOOS, SymlinkTestEnv)
}

// SymlinkCapable reports whether this process can create symbolic links.
// Non-Windows systems are always considered capable. On Windows the
// SCRATCHPAD_TEST_SYMLINKS environment variable wins when set to "0" or "1";
// otherwise the answer comes from a single cached os.Symlink probe in a
// process-wide temporary directory.
func SymlinkCapable() bool {
	if runtime.GOOS != "windows" {
		return true
	}
	switch os.Getenv(SymlinkTestEnv) {
	case "0":
		return false
	case "1":
		return true
	}
	symlinkOnce.Do(func() { symlinkCapable = probeSymlink() })
	return symlinkCapable
}

// probeSymlink attempts to create a directory symlink in a fresh temporary
// directory. It deliberately does not use testing.T temp dirs so the cached
// result is independent of any single test's lifecycle.
func probeSymlink() bool {
	dir, err := os.MkdirTemp("", "scratchpad-symlink-probe-")
	if err != nil {
		return false
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		return false
	}
	return os.Symlink(target, filepath.Join(dir, "link")) == nil
}

// RequireUnix skips t on non-Unix systems. Use it only for tests whose
// assertions (not merely their setup) depend on genuinely Unix-specific
// semantics, such as FIFO special files or Unix permission-bit behavior.
func RequireUnix(t testing.TB) {
	t.Helper()
	if !isUnix {
		t.Skipf("SKIP(unix-only): this test asserts Unix-specific semantics that do not exist on %s", runtime.GOOS)
	}
}
