//go:build windows

package winspike

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// Run 32902343901 measured that a handle-relative Symlinkat (FILE_CREATE +
// FSCTL_SET_REPARSE_POINT with IO_REPARSE_TAG_SYMLINK) succeeds after
// SeCreateSymbolicLinkPrivilege has been REMOVED from the token — i.e. without
// going through CreateSymbolicLinkW's privilege check at all.
//
// That would mean `watch` never needs Developer Mode, which contradicts the
// spec's "Reparse points and watch semantics" and acceptance criterion 6. But
// the runner also has Developer Mode ENABLED, so the measurement cannot yet
// tell the two explanations apart. This probe turns Developer Mode off in a
// child process, removes the privilege there too, and re-measures.
//
// The registry write is safe here and only here: GitHub runners are ephemeral,
// the process is elevated, the key is restored on the way out, and no other
// job on this runner depends on it.

const devModeOffEnv = "WINSPIKE_DEVMODE_OFF"

const (
	appModelUnlockKey = `SOFTWARE\Microsoft\Windows\CurrentVersion\AppModelUnlock`
	devModeValue      = "AllowDevelopmentWithoutDevLicense"
)

func TestP14DeveloperModeDependency(t *testing.T) {
	if os.Getenv(devModeOffEnv) == "1" {
		runWithDeveloperModeOff(t)
		return
	}

	before, present := readDevMode()
	Report(t, "P14.devmode.initial", Info,
		"%s = %d (present=%v) before the probe", devModeValue, before, present)

	cmd := exec.Command(os.Args[0], "-test.run=^TestP14DeveloperModeDependency$", "-test.v")
	cmd.Env = append(os.Environ(), devModeOffEnv+"=1")
	out, err := cmd.CombinedOutput()
	relayed := 0
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<20)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "WINSPIKE|") {
			t.Log(strings.TrimSpace(sc.Text()))
			relayed++
		}
	}
	if relayed == 0 {
		Report(t, "P14.devmode", NotMeasured,
			"the Developer-Mode-off child produced no measurements (exit %v): %s", err, truncate(string(out), 800))
	}

	after, _ := readDevMode()
	Report(t, "P14.devmode.restored", boolVerdict(after == before),
		"%s = %d after the probe (was %d) — the machine state was restored", devModeValue, after, before)
}

func readDevMode() (uint64, bool) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, appModelUnlockKey, registry.QUERY_VALUE)
	if err != nil {
		return 0, false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue(devModeValue)
	if err != nil {
		return 0, false
	}
	return v, true
}

func writeDevMode(v uint32) error {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, appModelUnlockKey, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer k.Close()
	return k.SetDWordValue(devModeValue, v)
}

func runWithDeveloperModeOff(t *testing.T) {
	before, present := readDevMode()
	if !present {
		Report(t, "P14.devmode", NotMeasured, "%s is absent; cannot toggle it", devModeValue)
		return
	}
	if err := writeDevMode(0); err != nil {
		Report(t, "P14.devmode", NotMeasured, "could not clear %s (not elevated?): %s", devModeValue, DescribeErr(err))
		return
	}
	defer writeDevMode(uint32(before))

	now, _ := readDevMode()
	if err := RemovePrivilege(seCreateSymbolicLinkName); err != nil {
		Report(t, "P14.devmode", NotMeasured, "could not remove the privilege: %s", DescribeErr(err))
		return
	}
	held, _ := HasPrivilege(seCreateSymbolicLinkName)
	Report(t, "P14.devmode.child", Info,
		"child state: %s = %d, holds %s = %v", devModeValue, now, seCreateSymbolicLinkName, held)

	dir, err := os.MkdirTemp("", "winspike-devmode-")
	if err != nil {
		Report(t, "P14.devmode", NotMeasured, "MkdirTemp: %v", err)
		return
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "target")
	os.Mkdir(target, 0o755)

	noFlag := CreateDirSymlink(filepath.Join(dir, "a"), target, false)
	Report(t, "P14.devmode_off.symlink_noflag", boolVerdict(noFlag == nil),
		"Developer Mode OFF, privilege REMOVED: CreateSymbolicLinkW(DIRECTORY) -> %s", DescribeErr(noFlag))

	withFlag := CreateDirSymlink(filepath.Join(dir, "b"), target, true)
	Report(t, "P14.devmode_off.symlink_unprivflag", boolVerdict(withFlag == nil),
		"Developer Mode OFF, privilege REMOVED: CreateSymbolicLinkW(DIRECTORY|ALLOW_UNPRIVILEGED_CREATE) -> %s. "+
			"A failure here is the configuration the spec's acceptance criterion 6 describes; a success means the "+
			"Developer Mode state is cached or not consulted for this call.", DescribeErr(withFlag))

	r, oerr := OpenRoot(dir)
	if oerr != nil {
		Report(t, "P14.devmode", NotMeasured, "OpenRoot: %s", DescribeErr(oerr))
		return
	}
	defer r.Close()

	serr := SymlinkAt(r.Handle(), "c", target)
	Report(t, "P14.devmode_off.symlinkat", boolVerdict(serr == nil),
		"Developer Mode OFF, privilege REMOVED: handle-relative FSCTL_SET_REPARSE_POINT with IO_REPARSE_TAG_SYMLINK -> %s. "+
			"IF THIS SUCCEEDS the store can create a real directory SYMBOLIC LINK on a locked-down machine without Developer "+
			"Mode and without elevation, which removes the whole ERROR_PRIVILEGE_NOT_HELD story from `watch` and makes the "+
			"junction fallback unnecessary. That would INVALIDATE the spec's premise, so it needs independent confirmation "+
			"on a non-CI machine before it is relied on.", DescribeErr(serr))
	if serr == nil {
		at, _ := StatAt(r.Handle(), "c")
		li, _ := os.Lstat(filepath.Join(dir, "c"))
		mode := ""
		if li != nil {
			mode = li.Mode().String()
		}
		Report(t, "P14.devmode_off.symlinkat_result", Info,
			"the resulting entry classifies as %s and os.Lstat reports mode=%s (a real SYMLINK-tagged directory link, "+
				"visible to Watches/WatchLinkFor/Unwatch exactly as on Linux)", at, mode)
	}

	jerr := CreateJunctionAt(r.Handle(), "d", target)
	Report(t, "P14.devmode_off.junction", boolVerdict(jerr == nil),
		"Developer Mode OFF, privilege REMOVED: junction -> %s", DescribeErr(jerr))
}

// ---------------------------------------------------------------------------
// The RR1 vector run 2 uncovered: a NON-SURROGATE unknown reparse tag on a
// directory. Go sets ModeDir *and* ModeIrregular for it, because
// isReparseTagNameSurrogate() is false ($GOROOT/src/os/types_windows.go:190-204),
// so IsDir() is TRUE. The threat model assumed junctions were the whole
// problem; they are not.
// ---------------------------------------------------------------------------

func TestRR1UnknownTagDirectoryIsIsDirTrue(t *testing.T) {
	r, dir := openScratchRoot(t)
	if err := MkdirAt(r.Handle(), "unktree"); err != nil {
		t.Skip("mkdir")
	}
	// Give it a child BEFORE tagging it, so a descending walk would find one.
	mustWrite(t, filepath.Join(dir, "unktree", "inside.txt"), "reachable?")

	h, err := ntOpenAt(r.Handle(), "unktree", windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		t.Skip("reopen")
	}
	setErr := SetUnknownTag(h, 0x00001234)
	windows.CloseHandle(h)
	if setErr != nil {
		Report(t, "RR1.unknown_tag", NotMeasured, "SetUnknownTag: %s", DescribeErr(setErr))
		return
	}

	p := filepath.Join(dir, "unktree")
	li, lerr := os.Lstat(p)
	isDir := lerr == nil && li.IsDir()
	entries, rdErr := os.ReadDir(p)
	at, _ := StatAt(r.Handle(), "unktree")

	Report(t, "RR1.unknown_tag_isdir", boolVerdict(!isDir),
		"a NON-SURROGATE unknown reparse tag on a directory: os.Lstat.IsDir()=%v mode=%v (err %v) ; os.ReadDir -> %d entries, err=%v ; "+
			"tag view %s. Go sets ModeDir because IsReparseTagNameSurrogate is FALSE, so ANY walk that decides by IsDir() "+
			"treats it as an ordinary directory. This is a second RR1 vector the threat model did not enumerate: it assumed "+
			"the junction (surrogate) case.",
		isDir, modeOf(li), lerr, len(entries), rdErr, at)

	// Does the stdlib's recursive delete descend?
	rmErr := os.RemoveAll(p)
	_, gone := os.Stat(p)
	Report(t, "RR1.unknown_tag_removeall", Info,
		"os.RemoveAll on the unknown-tag directory -> err=%v ; entry gone = %v", rmErr, gone != nil)
	Report(t, "R4.allowlist_evidence", Info,
		"CONSEQUENCE for R4: the tag allowlist cannot be 'refuse name surrogates'. It must be 'refuse every tag that is not "+
			"explicitly allowed', because a non-surrogate third-party tag is reported by Go as a plain directory and, when a "+
			"filter driver IS present to service it, is genuinely traversable.")
}

func modeOf(fi os.FileInfo) string {
	if fi == nil {
		return "<nil>"
	}
	return fi.Mode().String()
}
