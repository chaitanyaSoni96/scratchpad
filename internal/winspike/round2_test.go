//go:build windows

package winspike

import (
	"bufio"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P1.4, corrected: the GitHub runner is ELEVATED with Developer Mode on, so a
// bare attempt cannot answer "can an UNPRIVILEGED process do this". This probe
// re-executes the test binary in a child that has removed
// SeCreateSymbolicLinkPrivilege from its own token, and relays the child's
// measurement lines into this log.
// ---------------------------------------------------------------------------

const dropPrivEnv = "WINSPIKE_DROP_SYMLINK_PRIVILEGE"

func TestP14WithoutSymlinkPrivilege(t *testing.T) {
	if os.Getenv(dropPrivEnv) == "1" {
		runWithoutPrivilege(t)
		return
	}

	held, herr := HasPrivilege(seCreateSymbolicLinkName)
	Report(t, "P14.token", Info,
		"this process holds %s = %v (err %v); elevated CI runners hold it, ordinary interactive users do not — "+
			"every 'unprivileged' answer below comes from a child process that REMOVED it",
		seCreateSymbolicLinkName, held, herr)

	cmd := exec.Command(os.Args[0], "-test.run=^TestP14WithoutSymlinkPrivilege$", "-test.v")
	cmd.Env = append(os.Environ(), dropPrivEnv+"=1")
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
		Report(t, "P14.noprivilege", NotMeasured,
			"the privilege-dropping child produced no measurements (exit %v); output: %s", err, truncate(string(out), 800))
	}
}

func runWithoutPrivilege(t *testing.T) {
	if err := RemovePrivilege(seCreateSymbolicLinkName); err != nil {
		Report(t, "P14.noprivilege", NotMeasured, "could not remove %s: %s", seCreateSymbolicLinkName, DescribeErr(err))
		return
	}
	held, _ := HasPrivilege(seCreateSymbolicLinkName)
	Report(t, "P14.noprivilege.token", boolVerdict(!held),
		"child process token after SE_PRIVILEGE_REMOVED: holds %s = %v", seCreateSymbolicLinkName, held)

	dir, err := os.MkdirTemp("", "winspike-nopriv-")
	if err != nil {
		Report(t, "P14.noprivilege", NotMeasured, "MkdirTemp: %v", err)
		return
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "target")
	os.Mkdir(target, 0o755)

	noFlag := CreateDirSymlink(filepath.Join(dir, "l-noflag"), target, false)
	Report(t, "P14.noprivilege.symlink_noflag", boolVerdict(noFlag == nil),
		"WITHOUT the privilege: CreateSymbolicLinkW(DIRECTORY), no unprivileged flag -> %s "+
			"(ERROR_PRIVILEGE_NOT_HELD=1314 is the error the CLI must explain)", DescribeErr(noFlag))

	withFlag := CreateDirSymlink(filepath.Join(dir, "l-flag"), target, true)
	Report(t, "P14.noprivilege.symlink_unprivflag", boolVerdict(withFlag == nil),
		"WITHOUT the privilege: CreateSymbolicLinkW(DIRECTORY|ALLOW_UNPRIVILEGED_CREATE) -> %s "+
			"(this is the Developer-Mode path the spec relies on)", DescribeErr(withFlag))

	r, oerr := OpenRoot(dir)
	if oerr != nil {
		Report(t, "P14.noprivilege", NotMeasured, "OpenRoot: %s", DescribeErr(oerr))
		return
	}
	defer r.Close()

	jerr := CreateJunctionAt(r.Handle(), "j", target)
	Report(t, "P14.noprivilege.junction", boolVerdict(jerr == nil),
		"WITHOUT the privilege: FSCTL_SET_REPARSE_POINT with IO_REPARSE_TAG_MOUNT_POINT -> %s "+
			"(if this succeeds, junctions are the cheapest attacker primitive on a locked-down machine and the "+
			"one link the store could always create — the P1.4 fallback question)", DescribeErr(jerr))

	serr := SymlinkAt(r.Handle(), "hl", target)
	Report(t, "P14.noprivilege.symlinkat", boolVerdict(serr == nil),
		"WITHOUT the privilege: handle-relative Symlinkat (FILE_CREATE + FSCTL_SET_REPARSE_POINT with IO_REPARSE_TAG_SYMLINK) -> %s. "+
			"Note this route does NOT go through CreateSymbolicLinkW, so the ALLOW_UNPRIVILEGED_CREATE flag has no effect on it.",
		DescribeErr(serr))

	// M4 without the privilege.
	if err := MkdirAt(r.Handle(), "unk"); err == nil {
		h, oerr := ntOpenAt(r.Handle(), "unk", windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
			windows.OBJ_CASE_INSENSITIVE, 0)
		if oerr == nil {
			uerr := SetUnknownTag(h, 0x00001234)
			windows.CloseHandle(h)
			Report(t, "M4.noprivilege", boolVerdict(uerr == nil),
				"WITHOUT the privilege: FSCTL_SET_REPARSE_POINT with a NON-Microsoft tag -> %s. "+
					"This is the authoritative answer for the security matrix's \"unknown tag\" cell.", DescribeErr(uerr))
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// ---------------------------------------------------------------------------
// The create-only error map. Run 32901703383 showed that claiming a name that
// is already a reparse point yields STATUS_REPARSE_POINT_ENCOUNTERED, NOT
// STATUS_OBJECT_NAME_COLLISION — because OBJ_DONT_REPARSE fires during name
// resolution, before the collision is detected. Publish and Watch both depend
// on "name taken" having exactly one error, so this table is load-bearing (R6).
// ---------------------------------------------------------------------------

func TestM8ClaimOverExistingEntry(t *testing.T) {
	r, dir := openScratchRoot(t)
	external := scratchDir(t)

	type row struct{ name, what string }
	rows := []row{}
	mustMkdir(t, filepath.Join(dir, "realdir"))
	rows = append(rows, row{"realdir", "plain directory"})
	mustWrite(t, filepath.Join(dir, "realfile"), "x")
	rows = append(rows, row{"realfile", "plain file"})
	if CreateJunctionAt(r.Handle(), "junction", external) == nil {
		rows = append(rows, row{"junction", "junction (MOUNT_POINT)"})
	}
	if ok, _ := symlinkCapability(t); ok {
		if CreateDirSymlink(filepath.Join(dir, "dirsym"), external, true) == nil {
			rows = append(rows, row{"dirsym", "directory symlink"})
		}
	}

	reparseSeen := false
	for _, rw := range rows {
		withNoFollow := MkdirAt(r.Handle(), rw.name)
		// The same claim WITHOUT OBJ_DONT_REPARSE, to isolate the cause.
		hWeak, weakErr := ntOpenAt(r.Handle(), rw.name, dirReadAccess, windows.FILE_CREATE,
			windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, windows.FILE_ATTRIBUTE_NORMAL)
		if weakErr == nil {
			windows.CloseHandle(hWeak)
		}
		stNoFollow, _ := StatusOf(withNoFollow)
		if stNoFollow == windows.STATUS_REPARSE_POINT_ENCOUNTERED {
			reparseSeen = true
		}
		Report(t, "M8.claim_over."+rw.name, Info,
			"claiming a name already held by a %s: with OBJ_DONT_REPARSE -> %s | without it -> %s",
			rw.what, DescribeErr(withNoFollow), DescribeErr(weakErr))
	}

	Report(t, "M8.claim_error_map", boolVerdict(!reparseSeen),
		"does 'name already taken' have ONE error on Windows? reparse-point collisions produced "+
			"STATUS_REPARSE_POINT_ENCOUNTERED rather than STATUS_OBJECT_NAME_COLLISION = %v. "+
			"If true, Publish must map BOTH statuses to \"already exists\" (R6), and Watch's idempotence relaxation "+
			"(store.go:642, read the existing link and compare) must be reached from the REPARSE status, not from EEXIST — "+
			"a direct port of the Linux errors.Is(EEXIST) branch would turn every repeat `watch` into a hard error.",
		reparseSeen)
}

// ---------------------------------------------------------------------------
// RR1 measured against the ACTUAL Go functions the store uses today, not
// against a hypothetical port: os.RemoveAll and filepath.WalkDir.
// ---------------------------------------------------------------------------

func TestRR1StdlibWalkAndRemoveThroughJunction(t *testing.T) {
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	mustWrite(t, filepath.Join(external, "PRECIOUS.txt"), "do not delete")
	mustMkdir(t, filepath.Join(external, "sub"))
	mustWrite(t, filepath.Join(external, "sub", "also.txt"), "nor this")

	if err := CreateJunctionAt(r.Handle(), "art", external); err != nil {
		Report(t, "RR1", NotMeasured, "junction: %s", DescribeErr(err))
		return
	}

	// (a) filepath.WalkDir — store.go:380's loadArtifact size/mtime walk.
	seen := []string{}
	_ = filepath.WalkDir(filepath.Join(dir, "art"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		seen = append(seen, filepath.Base(p))
		return nil
	})
	descended := false
	for _, s := range seen {
		if s == "PRECIOUS.txt" {
			descended = true
		}
	}
	Report(t, "RR1.walkdir", boolVerdict(!descended),
		"filepath.WalkDir starting AT a junction saw %v -> descended into the target = %v. "+
			"loadArtifact (store.go:380) walks an artifact this way, so a junction planted as an artifact "+
			"makes it read an arbitrary external tree.", seen, descended)

	// (b) os.RemoveAll — the destruction primitive.
	rmErr := os.RemoveAll(filepath.Join(dir, "art"))
	_, precious := os.Stat(filepath.Join(external, "PRECIOUS.txt"))
	_, also := os.Stat(filepath.Join(external, "sub", "also.txt"))
	targetIntact := precious == nil && also == nil
	Report(t, "RR1.removeall", boolVerdict(targetIntact),
		"os.RemoveAll on a junction -> err=%v ; the junction target survived intact = %v (PRECIOUS.txt err=%v, sub/also.txt err=%v). "+
			"Go's RemoveAll is handle-based since 1.21 and removes the link without descending; "+
			"a hand-rolled recursive delete that tests FILE_ATTRIBUTE_DIRECTORY instead WOULD descend (P14.delete_attr_trap).",
		rmErr, targetIntact, precious, also)
	RequireProperty(t, "RR1.removeall", targetIntact,
		"removing a junction must never destroy its target tree")
}

// ---------------------------------------------------------------------------
// R13 — root replacement detection from the pinned handle's identity.
// ---------------------------------------------------------------------------

func TestR13RootReplacement(t *testing.T) {
	base := scratchDir(t)
	rootPath := mustMkdir(t, filepath.Join(base, "store"))
	r, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot: %s", DescribeErr(err))
	}
	defer r.Close()

	if err := r.Verify(); err != nil {
		t.Fatalf("Verify on an untouched root: %v", err)
	}

	// A rename must NOT be reported as replacement: the handle follows the object.
	if err := os.Rename(rootPath, filepath.Join(base, "store-moved")); err != nil {
		Report(t, "R13.rename", NotMeasured, "could not rename the root: %v", err)
	} else {
		Report(t, "R13.rename", boolVerdict(r.Verify() == nil),
			"after RENAMING the pinned root, FILE_ID_INFO is unchanged (Verify err=%v) — a rename is not a replacement", r.Verify())
	}

	// A replacement MUST be detected.
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		Report(t, "R13.replace", NotMeasured, "could not create the replacement root: %v", err)
		return
	}
	r2, err := OpenRoot(rootPath)
	if err != nil {
		Report(t, "R13.replace", NotMeasured, "OpenRoot on the replacement: %s", DescribeErr(err))
		return
	}
	defer r2.Close()
	same := r2.ID() == r.ID()
	Report(t, "R13.replace", boolVerdict(!same),
		"a NEW directory created under the same name has a different FILE_ID_INFO (old %s, new %s, identical=%v) — "+
			"so R13's pre-mutation identity check is implementable and is the only reliable replacement detector",
		r.ID(), r2.ID(), same)
	RequireProperty(t, "R13.replace", !same,
		"a replaced root must be distinguishable by FILE_ID_INFO")
}

// ---------------------------------------------------------------------------
// M3, completed: a real VOLUME mount point, built with the same tag a junction
// uses but a \??\Volume{GUID}\ substitute name.
// ---------------------------------------------------------------------------

func TestM3VolumeMountPoint(t *testing.T) {
	r, _ := openScratchRoot(t)
	buf := make([]uint16, 128)
	if err := windows.GetVolumeNameForVolumeMountPoint(windows.StringToUTF16Ptr(`C:\`), &buf[0], uint32(len(buf))); err != nil {
		Report(t, "M3.volume_mount_point", NotMeasured, "GetVolumeNameForVolumeMountPoint: %s", DescribeErr(err))
		return
	}
	guidPath := windows.UTF16ToString(buf) // \\?\Volume{...}\
	sub := `\??\` + strings.TrimPrefix(guidPath, `\\?\`)

	if err := MkdirAt(r.Handle(), "vmp"); err != nil {
		Report(t, "M3.volume_mount_point", NotMeasured, "mkdir: %s", DescribeErr(err))
		return
	}
	h, err := ntOpenAt(r.Handle(), "vmp", windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		Report(t, "M3.volume_mount_point", NotMeasured, "reopen: %s", DescribeErr(err))
		return
	}
	setErr := SetMountPointRaw(h, sub, guidPath)
	windows.CloseHandle(h)
	if setErr != nil {
		Report(t, "M3.volume_mount_point", NotMeasured, "FSCTL_SET_REPARSE_POINT: %s", DescribeErr(setErr))
		return
	}
	tag, got, _ := ReadLinkAt(r.Handle(), "vmp")
	at, _ := StatAt(r.Handle(), "vmp")
	isVolume := strings.HasPrefix(got, `\??\Volume{`)
	Report(t, "M3.volume_mount_point", boolVerdict(isVolume),
		"a VOLUME mount point carries tag 0x%08X(%s) — IDENTICAL to a junction — and is distinguishable ONLY by its substitute name %q "+
			"(begins \\??\\Volume{ = %v); classification %s. Any allowlist keyed on MOUNT_POINT therefore admits volume crossings "+
			"unless the reparse DATA is inspected (threat model §4.3).",
		tag, TagName(tag), got, isVolume, at)
	_ = DeleteAt(r.Handle(), "vmp", windows.FILE_DIRECTORY_FILE, true)
}

// ---------------------------------------------------------------------------
// Follow-ups the first run showed were needed.
// ---------------------------------------------------------------------------

func TestRound2Followups(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	// M9 control: does the Win32 wrapper work AT ALL, i.e. is the failure
	// specifically about RootDirectory rather than about the buffer?
	t.Run("M9_win32_control", func(t *testing.T) {
		mustWrite(t, filepath.Join(dir, "w32-dest.json"), "OLD")
		h, err := CreateFileAt(parent, "w32-src.tmp")
		if err != nil {
			t.Skip("create")
		}
		windows.Write(h, []byte("NEW"))
		err = RenameAtWin32(h, 0, filepath.Join(dir, "w32-dest.json"), win32FileRenameInfoEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		windows.CloseHandle(h)
		content, _ := os.ReadFile(filepath.Join(dir, "w32-dest.json"))
		Report(t, "M9.win32_control_nullroot", boolVerdict(err == nil && string(content) == "NEW"),
			"CONTROL: the SAME Win32 call with RootDirectory=NULL and a fully-qualified FileName -> err=%s, destination now %q. "+
				"Success here proves the buffer layout is correct and that SetFileInformationByHandle specifically REFUSES a "+
				"non-NULL RootDirectory, which is the M9 answer.", DescribeErr(err), string(content))
	})

	// M10: the LEGACY disposition against a memory-mapped file, where
	// STATUS_CANNOT_DELETE is documented.
	t.Run("M10_mapped_legacy", func(t *testing.T) {
		p := filepath.Join(dir, "mapped2.bin")
		mustWrite(t, p, strings.Repeat("m", 4096))
		mp, _ := windows.UTF16PtrFromString(p)
		fh, err := windows.CreateFile(mp, windows.GENERIC_READ|windows.GENERIC_WRITE, shareAll, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			t.Skip("open")
		}
		defer windows.CloseHandle(fh)
		mh, err := windows.CreateFileMapping(fh, nil, windows.PAGE_READONLY, 0, 4096, nil)
		if err != nil {
			t.Skip("mapping")
		}
		defer windows.CloseHandle(mh)
		addr, err := windows.MapViewOfFile(mh, windows.FILE_MAP_READ, 0, 0, 4096)
		if err != nil {
			t.Skip("view")
		}
		defer windows.UnmapViewOfFile(addr)

		dh, derr := OpenForDeleteAt(r.Handle(), "mapped2.bin", windows.FILE_NON_DIRECTORY_FILE)
		var legacyErr error
		if derr == nil {
			legacyErr = DeleteByHandle(dh, false)
			windows.CloseHandle(dh)
		}
		_, still := StatAt(r.Handle(), "mapped2.bin")
		Report(t, "M10.mapped_legacy", Info,
			"LEGACY FileDispositionInfo on a MEMORY-MAPPED file -> open=%s delete=%s ; name still present = %v. "+
				"Compare M10.mapped (POSIX semantics), which succeeded: POSIX delete unlinks the NAME while the mapping keeps the "+
				"data alive, so it is also the right answer for liveness, not only for the delete-pending window.",
			DescribeErr(derr), DescribeErr(legacyErr), still == nil)
	})

	// M12: an artifact that really does contain a file named "C".
	t.Run("M12_real_C_file", func(t *testing.T) {
		mustWrite(t, filepath.Join(dir, "C"), "plain")
		f, err := os.OpenFile(filepath.Join(dir, "C:evil"), os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			Report(t, "M12.C_stream", NotMeasured, "could not create the stream: %v", err)
			return
		}
		f.WriteString("EVIL")
		f.Close()
		h, oerr := ntOpenAt(r.Handle(), "C:evil", fileReadAccess, windows.FILE_OPEN,
			windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
		if oerr == nil {
			windows.CloseHandle(h)
		}
		Report(t, "M12.C_stream", boolVerdict(oerr == nil),
			"with a file literally named \"C\" present, the URL segment \"C:evil\" opens stream \"evil\" of it -> %s. "+
				"validateSegment (store.go:107-120) permits ':' today, so this is reachable over HTTP (RR8).", DescribeErr(oerr))
	})

	// M18: a device name as a NON-final segment, and CON.txt.
	t.Run("M18_nonfinal_and_extension", func(t *testing.T) {
		for _, n := range []string{`COM1\x.html`, "CON.txt", "NUL.txt"} {
			h, err := ntOpenAt(r.Handle(), n, fileReadAccess, windows.FILE_OPEN,
				windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
			if err == nil {
				windows.CloseHandle(h)
			}
			Report(t, "M18.relative."+strings.ReplaceAll(n, `\`, "_"), Info,
				"handle-relative open of %q -> %s", n, DescribeErr(err))
		}
		// Can a file named CON.txt actually be CREATED on this build?
		err := MkdirAt(r.Handle(), "CON.txt")
		Report(t, "M18.create_CON.txt", boolVerdict(err == nil),
			"creating a directory named \"CON.txt\" relative to a handle -> %s. RtlIsDosDeviceName_U says it is NOT a device on "+
				"build 26100, so a store created here can hold a name that older builds cannot address (threat model §4.12).",
			DescribeErr(err))
	})

	// M2, completed: the no-reparse open against an UNKNOWN tag.
	t.Run("M2_unknown_tag", func(t *testing.T) {
		if err := MkdirAt(r.Handle(), "unk2"); err != nil {
			t.Skip("mkdir")
		}
		h, err := ntOpenAt(r.Handle(), "unk2", windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
			windows.OBJ_CASE_INSENSITIVE, 0)
		if err != nil {
			t.Skip("reopen")
		}
		setErr := SetUnknownTag(h, 0x00001234)
		windows.CloseHandle(h)
		if setErr != nil {
			Report(t, "M2.unknown_tag", NotMeasured, "could not set an unknown tag: %s", DescribeErr(setErr))
			return
		}
		_, dirErr := OpenDirAt(r.Handle(), "unk2")
		_, fileErr := OpenRegularFileAt(r.Handle(), "unk2")
		at, _ := StatAt(r.Handle(), "unk2")
		li, _ := os.Lstat(filepath.Join(dir, "unk2"))
		si, sErr := os.Stat(filepath.Join(dir, "unk2"))
		lm, sm := fs.FileMode(0), fs.FileMode(0)
		if li != nil {
			lm = li.Mode()
		}
		if si != nil {
			sm = si.Mode()
		}
		Report(t, "M2.unknown_tag", Info,
			"NON-Microsoft tag 0x00001234: OpenDirAt(no-reparse)=%s | OpenRegularFileAt(no-reparse)=%s | classify=%s | "+
				"os.Lstat.Mode=%v os.Stat.Mode=%v (err %v). Note surrogate=false, so the kernel does NOT treat it as a name "+
				"surrogate and Go still reports ModeDir alongside ModeIrregular — an allowlist keyed on the surrogate bit would "+
				"admit it (R4).",
			DescribeErr(dirErr), DescribeErr(fileErr), at, lm, sm, sErr)
		_ = DeleteAt(r.Handle(), "unk2", windows.FILE_DIRECTORY_FILE, true)
	})

	// The fsnotify event path after a rename, stated precisely.
	t.Run("summary", func(t *testing.T) {
		maj, minv, build := osBuild()
		Report(t, "SUMMARY.instrument", Info,
			"harness: internal/winspike, every file //go:build windows, no import of internal/store; runner OS %d.%d build %d",
			maj, minv, build)
	})
}
