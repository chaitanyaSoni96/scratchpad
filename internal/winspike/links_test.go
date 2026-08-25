//go:build windows

package winspike

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestP14SymlinkPrivilege measures unprivileged directory symlink creation
// with and without SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE, and the exact
// error when the privilege is absent (spec: "Detect and explain
// ERROR_PRIVILEGE_NOT_HELD").
func TestP14SymlinkPrivilege(t *testing.T) {
	dir := scratchDir(t)
	target := mustMkdir(t, filepath.Join(dir, "target"))

	withoutFlag := CreateDirSymlink(filepath.Join(dir, "l-noflag"), target, false)
	Report(t, "P14.symlink_noflag", boolVerdict(withoutFlag == nil),
		"CreateSymbolicLinkW(SYMBOLIC_LINK_FLAG_DIRECTORY) with NO unprivileged flag -> %s", DescribeErr(withoutFlag))

	withFlag := CreateDirSymlink(filepath.Join(dir, "l-flag"), target, true)
	Report(t, "P14.symlink_unprivflag", boolVerdict(withFlag == nil),
		"CreateSymbolicLinkW(SYMBOLIC_LINK_FLAG_DIRECTORY|ALLOW_UNPRIVILEGED_CREATE) -> %s", DescribeErr(withFlag))

	privNotHeld := withoutFlag == windows.ERROR_PRIVILEGE_NOT_HELD || withFlag == windows.ERROR_PRIVILEGE_NOT_HELD
	Report(t, "P14.privilege_not_held", boolVerdict(privNotHeld),
		"ERROR_PRIVILEGE_NOT_HELD (%d) observed on this runner: %v. On a runner where symlinks work, this cell is only reachable in the degraded-mode job.",
		uint32(windows.ERROR_PRIVILEGE_NOT_HELD), privNotHeld)

	// M8 — is CreateSymbolicLinkW a true O_EXCL analogue? Watch's create-only
	// atomicity (store.go:637, unix.Symlinkat -> EEXIST) depends on it.
	if withFlag == nil {
		collide := CreateDirSymlink(filepath.Join(dir, "l-flag"), target, true)
		isExist := collide != nil && os.IsExist(collide)
		Report(t, "M8.createsymlink_excl", boolVerdict(collide != nil),
			"CreateSymbolicLinkW over an EXISTING name -> %s (os.IsExist=%v). Watch relies on this being a create-only failure, not a silent replace.",
			DescribeErr(collide), isExist)
		RequireProperty(t, "M8.createsymlink_excl", collide != nil,
			"CreateSymbolicLinkW must fail when the name is taken; it must never replace an existing entry")

		// And over an existing REAL directory, which is the dangerous case.
		mustMkdir(t, filepath.Join(dir, "realdir"))
		overReal := CreateDirSymlink(filepath.Join(dir, "realdir"), target, true)
		Report(t, "M8.over_real_dir", boolVerdict(overReal != nil),
			"CreateSymbolicLinkW over an existing REAL directory -> %s", DescribeErr(overReal))
	}

	// The handle-relative form we would actually use (Symlinkat analogue).
	r, rdir := openScratchRoot(t)
	ext := mustMkdir(t, filepath.Join(rdir, "ext"))
	err := SymlinkAt(r.Handle(), "hlink", ext)
	Report(t, "M8.symlinkat", boolVerdict(err == nil),
		"handle-relative Symlinkat (FILE_CREATE directory + FSCTL_SET_REPARSE_POINT) -> %s", DescribeErr(err))
	if err == nil {
		collide := SymlinkAt(r.Handle(), "hlink", ext)
		st, _ := StatusOf(collide)
		Report(t, "M8.symlinkat_excl", boolVerdict(st == windows.STATUS_OBJECT_NAME_COLLISION),
			"handle-relative Symlinkat over a taken name -> %s. The NAME CLAIM is atomic (FILE_CREATE); "+
				"the reparse tag is applied in a SECOND step, so a crash between the two leaves an EMPTY REAL DIRECTORY under the watch name, "+
				"not a partial link. That intermediate state is indistinguishable from a published-but-empty artifact and must be handled by the ADR.",
			DescribeErr(collide))
		at, _ := StatAt(r.Handle(), "hlink")
		Report(t, "M8.symlinkat_result", Info, "resulting entry: %s", at)
	}
}

// TestP14JunctionUnprivileged measures whether an unprivileged process can
// create a junction, and whether it can set a NON-Microsoft reparse tag (M4) —
// the latter gates the whole "unknown tag" cell of the security test matrix.
func TestP14JunctionUnprivileged(t *testing.T) {
	r, dir := openScratchRoot(t)
	target := mustMkdir(t, filepath.Join(dir, "jtarget"))
	mustWrite(t, filepath.Join(target, "inside.txt"), "x")

	err := CreateJunctionAt(r.Handle(), "j", target)
	Report(t, "P14.junction_unprivileged", boolVerdict(err == nil),
		"FSCTL_SET_REPARSE_POINT with IO_REPARSE_TAG_MOUNT_POINT, unprivileged -> %s", DescribeErr(err))
	if err == nil {
		tag, sub, rerr := ReadLinkAt(r.Handle(), "j")
		Report(t, "P14.junction_target", boolVerdict(rerr == nil),
			"FSCTL_GET_REPARSE_POINT tag=0x%08X(%s) substitute=%q err=%v", tag, TagName(tag), sub, rerr)
	}

	// M4 — a non-Microsoft tag needs the REPARSE_GUID_DATA_BUFFER form.
	if err := MkdirAt(r.Handle(), "unknowntag"); err != nil {
		Report(t, "M4", NotMeasured, "could not create the carrier directory: %s", DescribeErr(err))
		return
	}
	h, oerr := ntOpenAt(r.Handle(), "unknowntag",
		windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if oerr != nil {
		Report(t, "M4", NotMeasured, "could not reopen the carrier directory: %s", DescribeErr(oerr))
		return
	}
	defer windows.CloseHandle(h)
	const nonMicrosoftTag = 0x00001234 // bit 31 clear => non-Microsoft
	uerr := SetUnknownTag(h, nonMicrosoftTag)
	Report(t, "M4", boolVerdict(uerr == nil),
		"FSCTL_SET_REPARSE_POINT with a NON-Microsoft tag 0x%08X (REPARSE_GUID_DATA_BUFFER), unprivileged -> %s",
		uint32(nonMicrosoftTag), DescribeErr(uerr))
	if uerr == nil {
		at, _ := StatAt(r.Handle(), "unknowntag")
		Report(t, "M4.result", Info, "unknown-tag entry classifies as %s", at)
		_, terr := r.OpenRealDir([]string{"unknowntag"}, false, false)
		RequireProperty(t, "M4.traverse", terr != nil,
			"an unknown reparse tag must never be traversable (got err=%v)", terr)
	} else {
		Report(t, "M4.matrix", Info,
			"the security matrix's \"unknown tag\" Watch cell is a DOCUMENTED EXCLUSION: an unprivileged process cannot plant one, "+
				"so it can only arrive from an already-privileged writer or a third-party filter driver.")
	}
}

// TestP14LinkClassification is the confirm-or-refute of the threat model's
// critical claim: Go sets ModeSymlink only for IO_REPARSE_TAG_SYMLINK, so a
// junction reads as ModeIrregular — which decides whether Delete would recurse
// through a junction (RR1, the beta blocker).
func TestP14LinkClassification(t *testing.T) {
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	mustWrite(t, filepath.Join(external, "loot.txt"), "LOOT")
	mustWrite(t, filepath.Join(dir, "plain.txt"), "p")
	mustMkdir(t, filepath.Join(dir, "plaindir"))

	kinds := map[string]string{}
	if err := CreateJunctionAt(r.Handle(), "junction", external); err == nil {
		kinds["junction"] = "junction"
	} else {
		Report(t, "P14.classify", NotMeasured, "junction: %s", DescribeErr(err))
	}
	if ok, _ := symlinkCapability(t); ok {
		if CreateDirSymlink(filepath.Join(dir, "dirsymlink"), external, true) == nil {
			kinds["dirsymlink"] = "directory symlink"
		}
		if CreateFileSymlink(filepath.Join(dir, "filesymlink"), filepath.Join(external, "loot.txt"), true) == nil {
			kinds["filesymlink"] = "file symlink"
		}
	}
	kinds["plaindir"] = "plain directory"
	kinds["plain.txt"] = "plain file"

	// The parent listing, which is what store.go:316 entryIsDir consumes.
	parentEntries := map[string]fs.DirEntry{}
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			parentEntries[e.Name()] = e
		}
	}

	junctionIsSymlink := true
	junctionIsDir := true
	for name, what := range kinds {
		p := filepath.Join(dir, name)
		li, lerr := os.Lstat(p)
		si, serr := os.Stat(p)
		at, aerr := StatAt(r.Handle(), name)
		ev, everr := filepath.EvalSymlinks(p)
		_, rderr := os.ReadDir(p)

		lmode := fs.FileMode(0)
		if lerr == nil {
			lmode = li.Mode()
		}
		smode := fs.FileMode(0)
		if serr == nil {
			smode = si.Mode()
		}
		entryType := fs.FileMode(0)
		entryIsDir := false
		if e, ok := parentEntries[name]; ok {
			entryType = e.Type()
			entryIsDir = e.IsDir()
		}

		Report(t, "P14.classify."+name, Info,
			"%s: Lstat.Mode=%v(symlink=%v dir=%v irregular=%v err=%v) Stat.Mode=%v(err=%v) "+
				"DirEntry.Type=%v DirEntry.IsDir=%v tag[%s] (staterr=%v) EvalSymlinks=%q(err=%v) ReadDir.err=%v",
			what, lmode, lmode&fs.ModeSymlink != 0, lmode.IsDir(), lmode&fs.ModeIrregular != 0, lerr,
			smode, serr, entryType, entryIsDir, at, aerr, ev, everr, rderr)

		if name == "junction" && lerr == nil {
			junctionIsSymlink = lmode&fs.ModeSymlink != 0
			junctionIsDir = lmode.IsDir()
		}
	}

	if _, ok := kinds["junction"]; ok {
		Report(t, "P14.junction_modesymlink", boolVerdict(!junctionIsSymlink && !junctionIsDir),
			"THREAT MODEL CLAIM (§3.2, §3.4, §4.2): a junction is neither fs.ModeSymlink nor fs.ModeDir. "+
				"Measured: ModeSymlink=%v ModeDir=%v. If both are false the claim is CONFIRMED, and store.go:316 entryIsDir, "+
				"store.go:417 List's link test, store.go:722 Watches and store.go:691 WatchLinkFor all misclassify junctions today.",
			junctionIsSymlink, junctionIsDir)
		RequireProperty(t, "P14.junction_not_dir", !junctionIsDir,
			"a junction must not report fs.ModeDir, otherwise a Go-mode-based recursive delete descends into the target (RR1). ModeDir=%v", junctionIsDir)
	}

	// The destruction primitive itself: does a handle-based walk descend?
	if _, ok := kinds["junction"]; ok {
		at, err := StatAt(r.Handle(), "junction")
		if err == nil {
			descend := at.IsDir() && !at.IsReparse()
			Report(t, "P14.delete_descend", boolVerdict(!descend),
				"tag-based classification of the junction says descend=%v (%s). The correct rule is: FILE_ATTRIBUTE_DIRECTORY alone is NOT enough — "+
					"FILE_ATTRIBUTE_REPARSE_POINT is set on a junction, so a walk keyed on the directory attribute alone WOULD descend and destroy the target.",
				descend, at)
			RequireProperty(t, "P14.delete_descend", !descend,
				"a reparse-tagged entry must be unlinked, never descended")
			Report(t, "P14.delete_attr_trap", Info,
				"trap check: FILE_ATTRIBUTE_DIRECTORY on the junction = %v (this is the bit a naive port would test, and it is SET)", at.IsDir())
		}
		// Removing the junction must leave the target intact.
		if err := DeleteAt(r.Handle(), "junction", windows.FILE_DIRECTORY_FILE, true); err != nil {
			Report(t, "P14.unlink_junction", No, "DeleteAt(junction): %s", DescribeErr(err))
		} else {
			_, statErr := os.Stat(filepath.Join(external, "loot.txt"))
			RequireProperty(t, "P14.unlink_junction", statErr == nil,
				"removing a junction must not touch its target (target readable afterwards = %v)", statErr == nil)
			Report(t, "P14.unlink_junction", Yes,
				"handle-relative delete of a junction removed only the link; the target tree is intact")
		}
	}
}
