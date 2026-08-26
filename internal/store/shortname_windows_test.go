//go:build windows

package store

import (
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// shortNameOf returns the 8.3 alias NTFS holds for the entry (dir, name),
// and whether a DISTINCT alias exists at all. It asks the filesystem
// (GetShortPathNameW) rather than predicting the alias from NTFS's
// generation algorithm: predicting it is precisely the mistake P6.3 F1's
// fallback fix would have baked in, and the mistake P-5 already made once on
// this branch when it reasoned about EvalSymlinks from Go's source instead of
// measuring it.
//
// ok == false means "8.3 generation is off for this volume, or this name is
// already a valid 8.3 name" — in both cases there is no second spelling and
// nothing for the hazard to use.
func shortNameOf(t *testing.T, dir, name string) (string, bool) {
	t.Helper()
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("UTF16PtrFromString(%q): %v", name, err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		return "", false
	}
	short := filepath.Base(windows.UTF16ToString(buf))
	if short == "" || strings.EqualFold(short, name) {
		return "", false
	}
	return short, true
}

// TestShortNameAliasDoesNotBypassVisibility is P6.3 F1's regression test and,
// in its first section, the measurement F1 itself could not make: does a
// RootDirectory-relative NtCreateFile — the store's own traversal primitive,
// not a Win32 path open — resolve an NTFS 8.3 alias? That single question is
// the whole finding. If the answer is no, F1 collapses to a documentation
// note; if yes, `GET /p/ANNOTA~1` reached the annotations tree and every
// defaultIgnores/.scratchpadignore rule was one alias away from being
// bypassed, because ignore.go compares names as strings and neither
// EqualFold nor path.Match matches an alias against its long name.
//
// The test asserts four things, and the last one is the reason the fix is
// canonicalLookupName rather than a `~`-refusing lookup rule:
//
//  1. the reserved-name deny holds against the alias (.annotations);
//  2. the ignore rules hold against the alias, including the CONTENT routes
//     that F1's impact statement did not cover — a .md served by ResolveDoc
//     and an artifact served by ResolvePath, both living inside a directory
//     defaultIgnores hides;
//  3. the long-name behaviour is unchanged (baseline controls, asserted
//     before the capability gate so they run unconditionally);
//  4. NEGATIVE CONTROL — a perfectly ordinary, visible artifact addressed by
//     its OWN 8.3 alias is still reachable. A fix that refused the alias
//     shape outright would pass 1-3 and fail this, which is exactly the
//     over-breadth trade-off being rejected: the store normalises the
//     visibility DECISION, it does not narrow what a caller may address.
func TestShortNameAliasDoesNotBypassVisibility(t *testing.T) {
	root := testRoot(t)

	// The reserved name, with a sidecar in it so the tree is non-empty and
	// would genuinely list something if it were reachable.
	writeFile(t, root, AnnotationsDir+"/demo/index.html.json", `{"rev":1,"annotations":[]}`)
	// A defaultIgnores directory holding both servable content types.
	writeFile(t, root, "node_modules/pkg/index.html", "<h1>hidden artifact</h1>")
	writeFile(t, root, "node_modules/pkg/readme.md", "# hidden markdown")
	// The negative control: an ordinary, visible artifact whose name is long
	// enough to earn an 8.3 alias of its own.
	if _, err := Publish("", "my-long-artifact-name", map[string][]byte{"index.html": []byte("<h1>visible</h1>")}); err != nil {
		t.Fatalf("publish the control artifact: %v", err)
	}
	resetIgnoreCache()

	// Baseline controls run BEFORE the capability gate, so this test always
	// asserts something even on a volume with 8.3 generation switched off.
	if VisiblePath(AnnotationsDir) {
		t.Errorf("VisiblePath(%q) = true, want false — the pre-existing reserved-name deny regressed", AnnotationsDir)
	}
	if VisiblePath("node_modules") {
		t.Error(`VisiblePath("node_modules") = true, want false — the pre-existing defaultIgnores rule regressed`)
	}
	if !VisiblePath("my-long-artifact-name") {
		t.Error(`VisiblePath("my-long-artifact-name") = false, want true — an ordinary artifact stopped being visible`)
	}

	shortAnn, haveAnn := shortNameOf(t, root, AnnotationsDir)
	shortNM, haveNM := shortNameOf(t, root, "node_modules")
	shortArt, haveArt := shortNameOf(t, root, "my-long-artifact-name")
	if !haveAnn && !haveNM && !haveArt {
		t.Skip("SKIP(8dot3-capability): this volume generates no 8.3 aliases, so there is no second spelling to test. " +
			"The spike measured M6.enabled = ON for the runner's C: volume, which is where the USERPROFILE .scratchpad root lives, " +
			"so a skip here means the property is UNVERIFIED on this runner, not that it does not apply.")
	}
	t.Logf("8.3 aliases: %q=%q(%v) %q=%q(%v) %q=%q(%v)",
		AnnotationsDir, shortAnn, haveAnn, "node_modules", shortNM, haveNM, "my-long-artifact-name", shortArt, haveArt)

	// ---------------------------------------------------------------
	// The decisive measurement. Recorded whatever the answer, because a
	// "no" here would retire the finding rather than merely pass the test.
	// ---------------------------------------------------------------
	if haveAnn {
		rfs, err := openRootedFS(true)
		if err != nil {
			t.Fatalf("openRootedFS: %v", err)
		}
		rootFD := int(rfs.root.Fd())
		longFD, longErr := openRealDirAt(rootFD, AnnotationsDir)
		aliasFD, aliasErr := openRealDirAt(rootFD, shortAnn)
		resolved := false
		if longErr == nil && aliasErr == nil {
			longID, e1 := objectIDOf(longFD)
			aliasID, e2 := objectIDOf(aliasFD)
			resolved = e1 == nil && e2 == nil && longID == aliasID
		}
		if longErr == nil {
			closeFD(longFD)
		}
		if aliasErr == nil {
			closeFD(aliasFD)
		}
		rfs.close()
		t.Logf("P6.3.F1 MEASUREMENT: a RootDirectory-relative NtCreateFile %s the 8.3 alias %q to the same object as %q (alias open err=%v). "+
			"This is the step F1 could not execute; it is the whole finding.",
			map[bool]string{true: "RESOLVES", false: "does NOT resolve"}[resolved], shortAnn, AnnotationsDir, aliasErr)
		if !resolved {
			t.Logf("P6.3.F1 would collapse to a documentation note on this platform — but the assertions below are the durable " +
				"property either way, and must hold whether or not the kernel resolves the alias.")
		}
	}

	// --- 1. the reserved-name deny holds against the alias ---
	if haveAnn {
		if VisiblePath(shortAnn) {
			t.Errorf("VisiblePath(%q) = true: the 8.3 alias of %s bypassed Visible's reserved-name deny (P6.3 F1)", shortAnn, AnnotationsDir)
		}
		if _, ok := ResolveFolder(shortAnn); ok {
			t.Errorf("ResolveFolder(%q) succeeded: the annotations tree is browsable by its 8.3 alias", shortAnn)
		}
	}

	// --- 2. the ignore rules hold against the alias, content routes included ---
	if haveNM {
		if VisiblePath(shortNM) {
			t.Errorf("VisiblePath(%q) = true: the 8.3 alias of node_modules bypassed defaultIgnores", shortNM)
		}
		if _, ok := ResolveDoc([]string{shortNM, "pkg", "readme.md"}); ok {
			t.Errorf("ResolveDoc via %q served markdown from inside an ignore-hidden directory — this is CONTENT disclosure, "+
				"which F1's impact statement bounded out", shortNM)
		}
		if _, _, ok := ResolvePath([]string{shortNM, "pkg", "index.html"}); ok {
			t.Errorf("ResolvePath via %q resolved an artifact inside an ignore-hidden directory — this is CONTENT disclosure, "+
				"which F1's impact statement bounded out", shortNM)
		}
	}

	// --- 4. NEGATIVE CONTROL: normalise the decision, do not narrow addressing ---
	if haveArt {
		if !VisiblePath(shortArt) {
			t.Errorf("VisiblePath(%q) = false: an ordinary, VISIBLE artifact addressed by its own 8.3 alias became unreachable. "+
				"The fix must normalise the visibility decision to the on-disk name, not refuse alias-shaped segments outright "+
				"(that is the over-broad alternative this control exists to reject).", shortArt)
		}
		if _, _, ok := ResolvePath([]string{shortArt, "index.html"}); !ok {
			t.Errorf("ResolvePath(%q/index.html) failed: a visible artifact must stay servable through its 8.3 alias", shortArt)
		}
	}
}
