//go:build unix

package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"scratchpad/internal/testutil"
)

// TestDesiredDirsSkipsUnreadableEntryInsteadOfFailing is the Linux
// reproduction for P-3 (P4.7 semantic parity review). ADR §6.11's
// reconcile-error-triage clause — "an entry that cannot be read or
// classified is skipped and logged once, never fatal" — was implemented on
// Windows only: identity_windows.go's skipWalkError recognizes
// fs.ErrPermission (among others) and turns it into a logged skip.
// identity_unix.go's skipWalkError recognized only a disappeared entry
// (os.IsNotExist), so a directory the process cannot read anywhere under
// the store root made desiredDirs — and therefore newWatcher, and
// therefore cmd/scratchpad-web's startup call — return a hard error. Under
// systemd --user that is exactly the boot loop ADR §6.11 exists to
// prevent, reproduced on the one platform it was supposed to already cover.
//
// Root (and any account with CAP_DAC_OVERRIDE) ignores permission bits
// entirely, so mode 0o000 proves nothing there — testutil.RequireNotRoot
// turns that into an honest, greppable skip instead of a false pass.
func TestDesiredDirsSkipsUnreadableEntryInsteadOfFailing(t *testing.T) {
	testutil.RequireNotRoot(t)

	root := t.TempDir()
	denied := filepath.Join(root, "denied")
	if err := os.Mkdir(denied, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(denied, "index.html"), []byte("<p>hi</p>"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(denied, 0o000); err != nil {
		t.Fatal(err)
	}
	// t.TempDir's own cleanup needs to be able to walk back into denied to
	// remove it; restore permissions before the test ends.
	t.Cleanup(func() { _ = os.Chmod(denied, 0o755) })

	visible := filepath.Join(root, "visible")
	if err := os.Mkdir(visible, 0o755); err != nil {
		t.Fatal(err)
	}

	dirs, err := desiredDirs(root)
	if err != nil {
		t.Fatalf("desiredDirs must skip an unreadable entry, not fail the whole walk: %v", err)
	}

	rootCanonical := mustCanonical(t, root)
	visibleCanonical := mustCanonical(t, visible)
	deniedCanonical := mustCanonical(t, denied)
	if _, ok := dirs[rootCanonical]; !ok {
		t.Error("store root itself was dropped from the desired set")
	}
	if _, ok := dirs[visibleCanonical]; !ok {
		t.Error("sibling directory was dropped from the desired set")
	}
	if _, ok := dirs[deniedCanonical]; ok {
		t.Error("unreadable directory should have been skipped, not registered")
	}

	// The full Watcher construction path (newWatcher -> reconcile ->
	// desiredDirs) must also not be fatal — this is what actually gates
	// cmd/scratchpad-web's startup (main.go calls New, then log.Fatalf on
	// its error).
	b := newFakeBackend()
	if _, err := newWatcher(root, NewHub(), b, time.Hour, time.Hour); err != nil {
		t.Fatalf("newWatcher must not fail startup over one unreadable directory: %v", err)
	}
}
