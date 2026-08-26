//go:build windows

package store

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// ---------------------------------------------------------------------------
// P4.4 — degraded mode, measured rather than simulated.
//
// The evidence gap this file closes: testutil.WatchLinkCapable/RequireWatchLinks
// only change what a TEST SKIPS on — they never touch the process's actual
// privilege state. The CI "degraded mode" job sets a test-harness env var, but
// the runner underneath is still elevated with Developer Mode on, so nothing
// in the existing suite has ever run against a genuinely privilege-incapable
// process. This file brings internal/winspike's own answer to exactly that
// problem — a child process that removes SeCreateSymbolicLinkPrivilege from
// its own token with SE_PRIVILEGE_REMOVED (chosen there, and here, because
// CreateSymbolicLinkW/the reparse FSCTLs enable the privilege on demand, so
// merely disabling it would prove nothing) — into internal/store's own test
// suite, against the REAL store package, not a prototype.
//
// hasPrivilege/removePrivilege below are a direct, minimal port of
// internal/winspike/privilege.go's HasPrivilege/RemovePrivilege (that
// package is read-only reference material for this task, per its own
// instructions — not imported, since internal/winspike is Phase 1 spike
// scaffolding slated for deletion and internal/store must not depend on it).
// ---------------------------------------------------------------------------

const (
	dropWatchPrivilegeEnv    = "SCRATCHPAD_DROP_WATCH_PRIVILEGE"
	seCreateSymbolicLinkName = "SeCreateSymbolicLinkPrivilege"
)

// hasPrivilege reports whether the current process token holds the named
// privilege at all (enabled or not). Ported from internal/winspike/privilege.go.
func hasPrivilege(name string) (bool, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false, err
	}
	defer token.Close()

	var want windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &want); err != nil {
		return false, err
	}
	var size uint32
	windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size)
	if size == 0 {
		return false, fmt.Errorf("degraded_windows_test: GetTokenInformation returned no size")
	}
	buf := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &buf[0], size, &size); err != nil {
		return false, err
	}
	tp := (*windows.Tokenprivileges)(unsafe.Pointer(&buf[0]))
	for _, p := range tp.AllPrivileges() {
		if p.Luid == want {
			return true, nil
		}
	}
	return false, nil
}

// removePrivilege removes the named privilege from the current process
// token, permanently for the life of the process (SE_PRIVILEGE_REMOVED).
// Ported from internal/winspike/privilege.go.
func removePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(),
		windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()

	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
		return err
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_REMOVED}
	return windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil)
}

// TestDegradedModeWithPrivilegeGenuinelyRevoked is the parent half: it
// re-executes this same test binary, filtered to this one test, in a child
// process that removes the privilege before doing anything else, and treats
// the child's exit code as the verdict — this is a real go test invocation,
// so every t.Fatalf inside the child fails this test for real, not merely a
// logged observation. DEGRADED| lines are relayed into this test's own log
// for visibility in the CI job's output.
func TestDegradedModeWithPrivilegeGenuinelyRevoked(t *testing.T) {
	if os.Getenv(dropWatchPrivilegeEnv) == "1" {
		runDegradedModeChild(t)
		return
	}

	held, _ := hasPrivilege(seCreateSymbolicLinkName)
	t.Logf("parent process holds %s = %v (CI runners execute elevated with Developer Mode on, so true is expected here — the child below removes it for real)", seCreateSymbolicLinkName, held)

	cmd := exec.Command(os.Args[0], "-test.run=^TestDegradedModeWithPrivilegeGenuinelyRevoked$", "-test.v")
	cmd.Env = append(os.Environ(), dropWatchPrivilegeEnv+"=1")
	out, runErr := cmd.CombinedOutput()

	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	var markers []string
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "DEGRADED|") {
			t.Log(strings.TrimSpace(line))
			markers = append(markers, line)
		}
	}
	if runErr != nil {
		t.Fatalf("privilege-revoked child failed (%v) — full output:\n%s", runErr, out)
	}
	if len(markers) == 0 {
		t.Fatalf("privilege-revoked child produced no DEGRADED| markers — full output:\n%s", out)
	}
	foundWatch := false
	for _, m := range markers {
		if strings.Contains(m, "DEGRADED|watch|") {
			foundWatch = true
			t.Logf("RESULT (measured, privilege genuinely revoked): %s", strings.TrimSpace(m))
		}
	}
	if !foundWatch {
		t.Fatal("child never reported a watch outcome marker")
	}
}

// runDegradedModeChild is the child half. It removes the privilege first,
// confirms the removal took, then exercises Publish/List/notes/Delete and
// finally Watch, printing one DEGRADED|... line per step so the parent's
// log carries the evidence even though the assertions themselves live here
// (in the child's own *testing.T, which is what makes a broken invariant a
// real test failure and not just an unread log line).
func runDegradedModeChild(t *testing.T) {
	beforeHeld, beforeErr := hasPrivilege(seCreateSymbolicLinkName)
	fmt.Printf("DEGRADED|token_before|held=%v|err=%v\n", beforeHeld, beforeErr)

	if err := removePrivilege(seCreateSymbolicLinkName); err != nil {
		t.Fatalf("could not remove %s from this process's own token: %v", seCreateSymbolicLinkName, err)
	}
	afterHeld, _ := hasPrivilege(seCreateSymbolicLinkName)
	fmt.Printf("DEGRADED|token_after|held=%v\n", afterHeld)
	if afterHeld {
		t.Fatal("privilege still held after SE_PRIVILEGE_REMOVED — the simulation did not take effect, so nothing measured below is trustworthy")
	}

	root := testRoot(t)
	fmt.Printf("DEGRADED|root|%s\n", root)

	// --- publish/list/delete/notes: must all work without any link privilege ---

	if _, err := Publish("", "pub1", map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
		t.Fatalf("Publish failed with the link privilege revoked: %v", err)
	}
	fmt.Println("DEGRADED|publish|ok")

	list, err := List()
	if err != nil || len(list) != 1 || list[0].Name != "pub1" {
		t.Fatalf("List failed with the link privilege revoked: %+v, %v", list, err)
	}
	fmt.Println("DEGRADED|list|ok")

	doc := "pub1/index.html"
	f := NotesFile{Annotations: []Annotation{{ID: "n1", Status: "open", Body: "check this", Target: Target{Type: "element", Selector: "#x"}}}}
	if _, err := SaveNotes(doc, f, 0); err != nil {
		t.Fatalf("SaveNotes failed with the link privilege revoked: %v", err)
	}
	if _, err := ResolveNote(doc, "n1", "looks fine"); err != nil {
		t.Fatalf("ResolveNote failed with the link privilege revoked: %v", err)
	}
	docsNotes, err := WalkNotes("")
	if err != nil || len(docsNotes) != 1 {
		t.Fatalf("WalkNotes failed with the link privilege revoked: %+v, %v", docsNotes, err)
	}
	report := FormatReport(docsNotes, ReportOptions{All: true})
	if !strings.Contains(report, "n1") {
		t.Fatalf("FormatReport did not include the resolved note: %s", report)
	}
	fmt.Println("DEGRADED|notes|ok")

	if err := Delete("", "pub1"); err != nil {
		t.Fatalf("Delete failed with the link privilege revoked: %v", err)
	}
	fmt.Println("DEGRADED|delete|ok")

	// --- watch: acceptance criterion 6, and the actual three-row reality ---
	//
	// The token privilege and Developer Mode are TWO INDEPENDENT dimensions
	// (ADR §6.6's measured privilege table): with the privilege removed but
	// Developer Mode ON (this runner's
	// ambient state), SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE still
	// succeeds, so symlinkAt correctly produces a real directory SYMBOLIC
	// LINK, not a junction — that is row 2, and it is the primary path for
	// every Developer-Mode user, so a test must not treat it as failure.
	// Only row 3 (privilege AND Developer Mode both absent) forces the
	// junction fallback, and Developer Mode is machine policy
	// (AllowDevelopmentWithoutDevLicense), not a per-process token bit —
	// reaching it needs the registry toggle below, which only an elevated
	// process (this runner) can perform.
	devModeBefore, devModePresent := readDevMode()
	fmt.Printf("DEGRADED|devmode_before|value=%d|present=%v\n", devModeBefore, devModePresent)

	// Row 2: privilege removed, Developer Mode left exactly as this runner
	// already has it.
	row2Want := uint32(windows.IO_REPARSE_TAG_SYMLINK)
	if devModePresent && devModeBefore == 0 {
		row2Want = windows.IO_REPARSE_TAG_MOUNT_POINT
	}
	assertWatchFlavour(t, "row2", "wsrc-row2", row2Want)

	// Row 3: ALSO turn Developer Mode off, matching internal/winspike's
	// devmode_test.go technique exactly (registry write, deferred restore —
	// safe here because this runner is elevated, ephemeral, and no other
	// job depends on the value). If this process cannot write the key (not
	// elevated), row 3 is simply not reachable here — report that plainly
	// rather than failing on an environment this task cannot control.
	if !devModePresent {
		fmt.Println("DEGRADED|devmode_toggle|skipped|reason=AllowDevelopmentWithoutDevLicense absent")
	} else if err := writeDevMode(0); err != nil {
		fmt.Printf("DEGRADED|devmode_toggle|skipped|reason=%v\n", err)
	} else {
		defer func() {
			restoreErr := writeDevMode(devModeBefore)
			fmt.Printf("DEGRADED|devmode_restored|value=%d|err=%v\n", devModeBefore, restoreErr)
		}()
		now, _ := readDevMode()
		fmt.Printf("DEGRADED|devmode_toggle|ok|value=%d\n", now)
		// Row 3: privilege AND Developer Mode both unavailable — the ONLY
		// state the ADR's table says forces the junction fallback.
		assertWatchFlavour(t, "row3", "wsrc-row3", windows.IO_REPARSE_TAG_MOUNT_POINT)
	}

	// One more Delete, on whatever assertWatchFlavour's row2 case published
	// (always created regardless of which rows ran), so Delete is proven a
	// second time after a watch — not just before one.
	if err := Delete("", "pub-after-wsrc-row2"); err != nil {
		t.Fatalf("Delete after a watch failed with the link privilege revoked: %v", err)
	}
	fmt.Println("DEGRADED|final_delete|ok")
}

// assertWatchFlavour runs Watch(name, a fresh source directory) and asserts
// the resulting link carries wantTag, printing a DEGRADED|watch|... marker
// either way. A successful watch must never have cost publish-only
// operation anything, so it re-proves Publish immediately afterward — this
// is "watch failure does not disable publish-only workflows" run in
// reverse (a watch SUCCESS must not disable it either).
func assertWatchFlavour(t *testing.T, label, name string, wantTag uint32) {
	t.Helper()
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<h1>src</h1>"), 0o644); err != nil {
		t.Fatalf("[%s] test setup: %v", label, err)
	}
	link, watchErr := Watch("", name, source)
	if watchErr != nil {
		msg := watchErr.Error()
		fmt.Printf("DEGRADED|watch|label=%s|outcome=failed|err=%s\n", label, msg)
		lower := strings.ToLower(msg)
		if !strings.Contains(lower, "developer mode") {
			t.Fatalf("[%s] watch failure message does not name Developer Mode as remediation: %q", label, msg)
		}
		if strings.Contains(lower, "elevated is not") || strings.Contains(lower, "not the recommended fix") || strings.Contains(lower, "do not run") {
			// explicitly de-prioritizes elevation — good.
		} else if strings.Contains(lower, "elevat") {
			t.Fatalf("[%s] watch failure message mentions elevation without clearly de-prioritizing it as the default fix: %q", label, msg)
		}
		if _, err := Publish("", "pub-after-"+name, map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
			t.Fatalf("[%s] Publish after a FAILED watch must still work: %v", label, err)
		}
		fmt.Printf("DEGRADED|publish_after_watch|label=%s|ok\n", label)
		return
	}

	rfs, openErr := openRootedFS(false)
	if openErr != nil {
		t.Fatalf("[%s] openRootedFS to classify the new link: %v", label, openErr)
	}
	tag, tagErr := readLinkTagAt(int(rfs.root.Fd()), name)
	rfs.close()
	fmt.Printf("DEGRADED|watch|label=%s|outcome=succeeded|link=%s|flavour_tag=0x%08X|tagErr=%v\n", label, link, tag, tagErr)
	if tagErr != nil || tag != wantTag {
		t.Fatalf("[%s] watch succeeded but produced tag 0x%08X (err=%v), want 0x%08X", label, tag, tagErr, wantTag)
	}
	if _, err := Publish("", "pub-after-"+name, map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
		t.Fatalf("[%s] Publish after a successful watch failed: %v", label, err)
	}
	fmt.Printf("DEGRADED|publish_after_watch|label=%s|ok\n", label)
}

// readDevMode/writeDevMode are a direct port of
// internal/winspike/devmode_test.go's identically-named functions: the same
// registry key, the same safety reasoning (elevated, ephemeral CI runner;
// always restored). Duplicated rather than imported because internal/store
// must not depend on internal/winspike (Phase 1 spike scaffolding, slated
// for deletion).
const (
	appModelUnlockKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock`
	devModeValue      = "AllowDevelopmentWithoutDevLicense"
)

func readDevMode() (uint32, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, appModelUnlockKey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(devModeValue)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

func writeDevMode(v uint32) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, appModelUnlockKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(devModeValue, v)
}
