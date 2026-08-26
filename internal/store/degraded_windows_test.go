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

	// --- watch: the property acceptance criterion 6 is actually about ---

	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "index.html"), []byte("<h1>src</h1>"), 0o644); err != nil {
		t.Fatalf("test setup: %v", err)
	}
	link, watchErr := Watch("", "wsrc", source)
	if watchErr == nil {
		rfs, openErr := openRootedFS(false)
		if openErr != nil {
			t.Fatalf("openRootedFS to classify the new link: %v", openErr)
		}
		tag, tagErr := readLinkTagAt(int(rfs.root.Fd()), "wsrc")
		rfs.close()
		fmt.Printf("DEGRADED|watch|outcome=succeeded|link=%s|flavour_tag=0x%08X|tagErr=%v\n", link, tag, tagErr)
		if tagErr != nil || tag != windows.IO_REPARSE_TAG_MOUNT_POINT {
			t.Fatalf("watch succeeded with the symlink privilege genuinely revoked, but the link is NOT a junction (tag=0x%08X, err=%v) — a real directory symbolic link should be impossible in this state", tag, tagErr)
		}
		// A successful watch must not have cost publish-only operation
		// anything: prove it again, after the watch.
		if _, err := Publish("", "pub2", map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
			t.Fatalf("Publish after a successful watch failed: %v", err)
		}
		fmt.Println("DEGRADED|publish_after_watch|ok")
	} else {
		msg := watchErr.Error()
		fmt.Printf("DEGRADED|watch|outcome=failed|err=%s\n", msg)
		lower := strings.ToLower(msg)
		if !strings.Contains(lower, "developer mode") {
			t.Fatalf("watch failure message does not name Developer Mode as remediation: %q", msg)
		}
		if strings.Contains(lower, "elevated is not") || strings.Contains(lower, "not the recommended fix") || strings.Contains(lower, "do not run") {
			// explicitly de-prioritizes elevation — good.
		} else if strings.Contains(lower, "elevat") {
			t.Fatalf("watch failure message mentions elevation without clearly de-prioritizing it as the default fix: %q", msg)
		}
		// Watch failure must never degrade publish-only operation.
		if _, err := Publish("", "pub2", map[string][]byte{"index.html": []byte("<p>hi</p>")}); err != nil {
			t.Fatalf("Publish after a FAILED watch must still work: %v", err)
		}
		fmt.Println("DEGRADED|publish_after_failed_watch|ok")
	}

	if err := Delete("", "pub2"); err != nil {
		t.Fatalf("Delete of the second artifact failed with the link privilege revoked: %v", err)
	}
	fmt.Println("DEGRADED|final_delete|ok")
}
