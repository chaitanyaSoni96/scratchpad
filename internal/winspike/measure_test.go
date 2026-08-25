//go:build windows

package winspike

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// M1 — OBJ_DONT_REPARSE: availability, and whether it covers INTERMEDIATE
// components or only the final one. R3's falsifier is "a design that relies
// solely on FILE_FLAG_OPEN_REPARSE_POINT", so this is the measurement that
// says whether the stronger primitive actually exists.
// ---------------------------------------------------------------------------

func TestM1ObjDontReparse(t *testing.T) {
	maj, min, build := osBuild()
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	deep := mustMkdir(t, filepath.Join(external, "deep"))
	mustWrite(t, filepath.Join(deep, "leaf.txt"), "LEAF")

	if err := CreateJunctionAt(r.Handle(), "j", external); err != nil {
		Report(t, "M1", NotMeasured, "could not create a junction: %s", DescribeErr(err))
		return
	}
	_ = dir

	// (a) Final component is the reparse point.
	_, errFinal := ntOpenAt(r.Handle(), "j", dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, noFollowAttrs, 0)
	stFinal, _ := StatusOf(errFinal)
	Report(t, "M1.final", boolVerdict(stFinal == windows.STATUS_REPARSE_POINT_ENCOUNTERED),
		"OBJ_DONT_REPARSE, reparse point as the FINAL component -> %s [OS %d.%d build %d]",
		DescribeErr(errFinal), maj, min, build)

	// (b) Reparse point as an INTERMEDIATE component of a relative name.
	_, errMid := ntOpenAt(r.Handle(), `j\deep`, dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, noFollowAttrs, 0)
	stMid, _ := StatusOf(errMid)
	Report(t, "M1.intermediate", boolVerdict(errMid != nil),
		"OBJ_DONT_REPARSE, reparse point as an INTERMEDIATE component of %q -> %s", `j\deep`, DescribeErr(errMid))
	RequireProperty(t, "M1.intermediate", errMid != nil,
		"OBJ_DONT_REPARSE must fail a path whose intermediate component is a reparse point (R3); got err=%v", errMid)

	// (c) The control: FILE_OPEN_REPARSE_POINT without OBJ_DONT_REPARSE is
	//     documented as affecting the final component only. If this SUCCEEDS,
	//     the threat model's §3.2 claim is confirmed by demonstration.
	h, errWeak := ntOpenAt(r.Handle(), `j\deep`, dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if errWeak == nil {
		windows.CloseHandle(h)
	}
	Report(t, "M1.weak_flag_traverses", boolVerdict(errWeak == nil),
		"CONTROL: FILE_OPEN_REPARSE_POINT *without* OBJ_DONT_REPARSE on %q -> err=%s. "+
			"Success here demonstrates that FILE_FLAG_OPEN_REPARSE_POINT protects only the FINAL component, "+
			"so a port relying on it alone traverses junctions silently (threat model §3.2, R3).",
		`j\deep`, DescribeErr(errWeak))

	Report(t, "M1.status_values", Info,
		"STATUS_REPARSE_POINT_ENCOUNTERED=0x%08X (final=0x%08X intermediate=0x%08X)",
		uint32(windows.STATUS_REPARSE_POINT_ENCOUNTERED), uint32(stFinal), uint32(stMid))
	Report(t, "M1.refs_smb", NotMeasured,
		"ReFS and SMB behaviour of OBJ_DONT_REPARSE is NOT measured: the GitHub runner exposes one NTFS volume and no SMB share. "+
			"Dev Drive (ReFS) is the realistic gap and needs a manual check before the beta (threat model §9.8).")
}

// ---------------------------------------------------------------------------
// M2 — the exact NT error a no-reparse open returns for each tag class, and
// how Go maps it.
// ---------------------------------------------------------------------------

func TestM2ErrorPerTagClass(t *testing.T) {
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	mustWrite(t, filepath.Join(external, "loot.txt"), "LOOT")

	type probe struct {
		name string
		what string
	}
	var probes []probe

	if CreateJunctionAt(r.Handle(), "j", external) == nil {
		probes = append(probes, probe{"j", "MOUNT_POINT (junction)"})
	}
	if ok, _ := symlinkCapability(t); ok {
		if CreateDirSymlink(filepath.Join(dir, "ds"), external, true) == nil {
			probes = append(probes, probe{"ds", "SYMLINK (directory)"})
		}
		if CreateFileSymlink(filepath.Join(dir, "fs"), filepath.Join(external, "loot.txt"), true) == nil {
			probes = append(probes, probe{"fs", "SYMLINK (file)"})
		}
	}
	mustMkdir(t, filepath.Join(dir, "realdir"))
	probes = append(probes, probe{"realdir", "plain directory (control)"})
	mustWrite(t, filepath.Join(dir, "realfile"), "x")
	probes = append(probes, probe{"realfile", "plain file (control)"})

	for _, p := range probes {
		_, dirErr := OpenDirAt(r.Handle(), p.name)
		_, fileErr := OpenRegularFileAt(r.Handle(), p.name)
		at, atErr := StatAt(r.Handle(), p.name)
		Report(t, "M2."+p.name, Info,
			"%s: OpenDirAt(no-reparse)=%s | OpenRegularFileAt(no-reparse)=%s | classify=%s (err %v)",
			p.what, DescribeErr(dirErr), DescribeErr(fileErr), at, atErr)
	}

	// APPEXECLINK, if the runner has one: these are real, common, and break
	// naive size/open accounting (threat model §4.2).
	appdir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "WindowsApps")
	if entries, err := os.ReadDir(appdir); err == nil {
		found := false
		for _, e := range entries {
			if e.Type()&os.ModeIrregular == 0 && e.Type() != 0 {
				continue
			}
			p := filepath.Join(appdir, e.Name())
			li, lerr := os.Lstat(p)
			if lerr != nil {
				continue
			}
			ar, aerr := OpenRoot(appdir)
			if aerr != nil {
				break
			}
			at, serr := StatAt(ar.Handle(), e.Name())
			ar.Close()
			if serr == nil && at.IsReparse() {
				Report(t, "M2.appexeclink", Info,
					"real reparse point in WindowsApps: %q Lstat.Mode=%v classify=%s", e.Name(), li.Mode(), at)
				found = true
				break
			}
		}
		if !found {
			Report(t, "M2.appexeclink", NotMeasured, "no reparse point found under %q", appdir)
		}
	} else {
		Report(t, "M2.appexeclink", NotMeasured, "%q not readable: %v", appdir, err)
	}
	Report(t, "M2.cloud", NotMeasured,
		"cloud placeholder tags (CLOUD_*, ONEDRIVE, STORAGE_SYNC, HSM*) cannot be produced on a CI runner without OneDrive; "+
			"they remain a documented exclusion and a manual pre-beta check (threat model §4.2, RR10).")
}

// ---------------------------------------------------------------------------
// M3 — can a volume mount point be told apart from a junction without parsing
// reparse data?
// ---------------------------------------------------------------------------

func TestM3VolumeCrossing(t *testing.T) {
	r, _ := openScratchRoot(t)
	rootID, err := FileIDOf(r.Handle())
	if err != nil {
		t.Fatalf("FileIDOf(root): %v", err)
	}

	second := `D:\`
	if _, err := os.Stat(second); err != nil {
		Report(t, "M3", NotMeasured, "no second volume on this runner (%v); the junction-vs-volume-mount-point distinction is untested", err)
		return
	}
	target := filepath.Join(second, fmt.Sprintf("winspike-%d", os.Getpid()))
	if err := os.MkdirAll(target, 0o755); err != nil {
		Report(t, "M3", NotMeasured, "cannot write to %s: %v", second, err)
		return
	}
	defer os.RemoveAll(target)

	if err := CreateJunctionAt(r.Handle(), "xvol", target); err != nil {
		Report(t, "M3", NotMeasured, "cross-volume junction: %s", DescribeErr(err))
		return
	}
	// Open THROUGH the junction (reparse allowed) and compare volume serials.
	h, err := ntOpenAt(r.Handle(), "xvol", dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		Report(t, "M3", NotMeasured, "open through the cross-volume junction: %s", DescribeErr(err))
		return
	}
	defer windows.CloseHandle(h)
	throughID, err := FileIDOf(h)
	if err != nil {
		Report(t, "M3", NotMeasured, "FILE_ID_INFO through the junction: %s", DescribeErr(err))
		return
	}
	differs := throughID.VolumeSerialNumber != rootID.VolumeSerialNumber
	tag, sub, _ := ReadLinkAt(r.Handle(), "xvol")
	Report(t, "M3", boolVerdict(differs),
		"a MOUNT_POINT that crosses a volume is detectable from FILE_ID_INFO alone: root %s vs through-link %s (differs=%v). "+
			"Tag is identical to a same-volume junction (0x%08X %s, substitute %q), so the TAG cannot distinguish them — "+
			"the volume serial can, and a volume mount point's substitute name would begin \\??\\Volume{.",
		rootID, throughID, differs, tag, TagName(tag), sub)
}

// ---------------------------------------------------------------------------
// M5 — does filepath.EvalSymlinks canonicalise CASE on Windows? List's cycle
// guard (store.go:407) keys its visited map on the answer.
// ---------------------------------------------------------------------------

func TestM5EvalSymlinksCase(t *testing.T) {
	dir := scratchDir(t)
	mustMkdir(t, filepath.Join(dir, "MiXeDcAsE", "Inner"))

	lower := filepath.Join(dir, "mixedcase", "inner")
	upper := filepath.Join(dir, "MIXEDCASE", "INNER")
	exact := filepath.Join(dir, "MiXeDcAsE", "Inner")

	el, errL := filepath.EvalSymlinks(lower)
	eu, errU := filepath.EvalSymlinks(upper)
	ee, errE := filepath.EvalSymlinks(exact)

	canon := errL == nil && errU == nil && errE == nil && el == eu && eu == ee
	Report(t, "M5.case", boolVerdict(canon),
		"EvalSymlinks(%q)=%q err=%v | EvalSymlinks(%q)=%q err=%v | EvalSymlinks(%q)=%q err=%v -> all three agree = %v. "+
			"If they agree, toNorm/normBase (FindFirstFile per component) DOES canonicalise case and List's visited map is sound against case-varying cycles; "+
			"if not, a case-alternating cycle produces a fresh key per iteration and List recurses until the web process dies (threat model §3.3a, RR9).",
		lower, el, errL, upper, eu, errU, exact, ee, errE, canon)

	// EvalSymlinks across a junction.
	r, _ := openScratchRoot(t)
	rd, _ := FinalPath(r.Handle(), volumeNameDOS)
	external := scratchDir(t)
	mustMkdir(t, filepath.Join(external, "sub"))
	if err := CreateJunctionAt(r.Handle(), "j", external); err == nil {
		base := strings.TrimPrefix(rd, `\\?\`)
		viaFinal, errF := filepath.EvalSymlinks(filepath.Join(base, "j"))
		viaMid, errM := filepath.EvalSymlinks(filepath.Join(base, "j", "sub"))
		Report(t, "M5.junction", Info,
			"EvalSymlinks with a junction as the FINAL component -> %q err=%v ; as an INTERMEDIATE component -> %q err=%v. "+
				"walkSymlinks only follows fs.ModeSymlink ($GOROOT/src/path/filepath/symlink.go), so a junction is not resolved; "+
				"an error in the intermediate case means A1 controls whether a subtree appears in List at all.",
			viaFinal, errF, viaMid, errM)
	} else {
		Report(t, "M5.junction", NotMeasured, "junction: %s", DescribeErr(err))
	}
}

// ---------------------------------------------------------------------------
// M6 — 8.3 short-name aliasing.
// ---------------------------------------------------------------------------

func TestM6ShortNames(t *testing.T) {
	dir := scratchDir(t)
	long := mustMkdir(t, filepath.Join(dir, "AVeryLongDirectoryName.Extension"))
	mustWrite(t, filepath.Join(long, "index.html"), "x")

	p, err := windows.UTF16PtrFromString(long)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetShortPathName(p, &buf[0], uint32(len(buf)))
	if err != nil || n == 0 {
		Report(t, "M6", NotMeasured, "GetShortPathName: %v", err)
		return
	}
	short := windows.UTF16ToString(buf[:n])
	aliased := !strings.EqualFold(short, long)
	Report(t, "M6.enabled", boolVerdict(aliased),
		"8.3 alias generation on this volume: long=%q short=%q aliased=%v", long, short, aliased)

	if !aliased {
		Report(t, "M6", NotMeasured, "8.3 generation is disabled on this volume; the aliasing hazard is volume-dependent and must not be assumed absent elsewhere")
		return
	}

	sp, _ := windows.UTF16PtrFromString(short)
	h, err := windows.CreateFile(sp, windows.GENERIC_READ, shareAll, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		Report(t, "M6", NotMeasured, "open via the short name: %s", DescribeErr(err))
		return
	}
	defer windows.CloseHandle(h)

	finalDOS, errD := FinalPath(h, volumeNameDOS)
	finalGUID, errG := FinalPath(h, volumeNameGUID)
	nameInfo, errN := NameInfo(h)
	longWins := strings.Contains(strings.ToLower(finalDOS), strings.ToLower(filepath.Base(long)))
	Report(t, "M6.resolution", boolVerdict(longWins),
		"handle opened via the 8.3 alias: GetFinalPathNameByHandleW(VOLUME_NAME_DOS)=%q (err %v) VOLUME_NAME_GUID=%q (err %v) FILE_NAME_INFO=%q (err %v) -> long name recovered = %v. "+
			"Both are DISPLAY primitives; the security answer is FILE_ID_INFO comparison, not string comparison (R2).",
		finalDOS, errD, finalGUID, errG, nameInfo, errN, longWins)

	// The concrete defect: Watch's "already inside the scratchpad" check is a
	// string prefix test (store.go:610-616).
	Report(t, "M6.prefix_defect", Info,
		"strings.HasPrefix(%q, %q) = %v — this is why Watch's containment guard must become a FILE_ID_INFO comparison (threat model §4.7, RR12).",
		short, long, strings.HasPrefix(short, long))
}

// ---------------------------------------------------------------------------
// M9 — the single most important measurement: does
// SetFileInformationByHandle(FileRenameInfoEx) honour
// FILE_RENAME_INFO.RootDirectory for a relative FileName?
// ---------------------------------------------------------------------------

func TestM9HandleRelativeRename(t *testing.T) {
	_, _, build := osBuild()
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	type variant struct {
		id    string
		call  func(src, destParent windows.Handle, name string) error
		label string
	}
	variants := []variant{
		{"M9.win32_ex_rootdir", func(src, dp windows.Handle, name string) error {
			return RenameAtWin32(src, dp, name, win32FileRenameInfoEx, fileRenameReplaceIfExists|fileRenamePosixSemantics)
		}, "Win32 SetFileInformationByHandle(FileRenameInfoEx=22), RootDirectory=parent handle, relative FileName, REPLACE|POSIX"},
		{"M9.win32_ex_rootdir_noposix", func(src, dp windows.Handle, name string) error {
			return RenameAtWin32(src, dp, name, win32FileRenameInfoEx, fileRenameReplaceIfExists)
		}, "Win32 SetFileInformationByHandle(FileRenameInfoEx=22), RootDirectory=parent handle, REPLACE only"},
		{"M9.win32_plain_rootdir", func(src, dp windows.Handle, name string) error {
			return RenameAtWin32(src, dp, name, win32FileRenameInfo, 1)
		}, "Win32 SetFileInformationByHandle(FileRenameInfo=3), RootDirectory=parent handle, ReplaceIfExists=TRUE"},
		{"M9.nt_ex_rootdir", func(src, dp windows.Handle, name string) error {
			return RenameAtNT(src, dp, name, fileRenameInformationEx, fileRenameReplaceIfExists|fileRenamePosixSemantics)
		}, "NtSetInformationFile(FileRenameInformationEx=65), RootDirectory=parent handle, REPLACE|POSIX (what the Go stdlib uses)"},
		{"M9.nt_plain_rootdir", func(src, dp windows.Handle, name string) error {
			return RenameAtNT(src, dp, name, fileRenameInformation, 1)
		}, "NtSetInformationFile(FileRenameInformation=10), RootDirectory=parent handle, ReplaceIfExists=TRUE"},
	}

	anyWorks := false
	for i, v := range variants {
		src := fmt.Sprintf("tmp-%d.tmp", i)
		dst := fmt.Sprintf("dest-%d.json", i)
		mustWrite(t, filepath.Join(dir, dst), "OLD")
		h, err := CreateFileAt(parent, src)
		if err != nil {
			Report(t, v.id, NotMeasured, "could not create the temp file: %s", DescribeErr(err))
			continue
		}
		if _, err := windows.Write(h, []byte("NEW")); err != nil {
			Report(t, v.id, NotMeasured, "write: %s", DescribeErr(err))
			windows.CloseHandle(h)
			continue
		}
		renameErr := v.call(h, parent, dst)
		windows.CloseHandle(h)

		content, readErr := os.ReadFile(filepath.Join(dir, dst))
		replaced := readErr == nil && string(content) == "NEW"
		_, srcLeft := os.Stat(filepath.Join(dir, src))
		if renameErr == nil && replaced {
			anyWorks = true
		}
		Report(t, v.id, boolVerdict(renameErr == nil && replaced),
			"%s -> err=%s ; destination content after = %q (read err %v) ; temp still present = %v [build %d]",
			v.label, DescribeErr(renameErr), string(content), readErr, srcLeft == nil, build)
	}

	RequireProperty(t, "M9", anyWorks,
		"at least one handle-relative atomic replace must work, otherwise annotationfs_linux.go:135's Renameat has NO Windows equivalent "+
			"and the atomic annotation write cannot be made handle-relative at all")

	// Control: the same call with RootDirectory=0 and a full path, which is
	// what a string-path port would do.
	mustWrite(t, filepath.Join(dir, "ctl-dest.json"), "OLD")
	h, err := CreateFileAt(parent, "ctl-src.tmp")
	if err == nil {
		windows.Write(h, []byte("NEW"))
		err = RenameAtNT(h, 0, filepath.Join(dir, "ctl-dest.json"), fileRenameInformationEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		windows.CloseHandle(h)
		Report(t, "M9.control_fullpath", Info,
			"CONTROL: RootDirectory=NULL with a fully-qualified FileName -> err=%s. This form works but RE-RESOLVES the destination path, "+
				"which is exactly the TOCTOU the pinned parent removes (R1/R3).", DescribeErr(err))
	}
}

// ---------------------------------------------------------------------------
// M10 — FILE_DISPOSITION_POSIX_SEMANTICS: availability, and whether it removes
// the delete-pending window that would make Publish's create-only contract
// report the wrong error (threat model §4.14).
// ---------------------------------------------------------------------------

func TestM10PosixDelete(t *testing.T) {
	_, _, build := osBuild()
	r, dir := openScratchRoot(t)

	// POSIX semantics: the name must leave the namespace at once.
	mustWrite(t, filepath.Join(dir, "posix.txt"), "x")
	h, err := OpenForDeleteAt(r.Handle(), "posix.txt", windows.FILE_NON_DIRECTORY_FILE)
	if err != nil {
		t.Fatalf("OpenForDeleteAt: %s", DescribeErr(err))
	}
	ntErr := DeleteByHandle(h, true)
	_, statErr := StatAt(r.Handle(), "posix.txt")
	reuse := MkdirAt(r.Handle(), "posix.txt") // claim the name while the handle is STILL OPEN
	windows.CloseHandle(h)
	Report(t, "M10.posix_nt", boolVerdict(ntErr == nil),
		"NtSetInformationFile(FileDispositionInformationEx=64, DELETE|POSIX_SEMANTICS) -> %s ; name visible afterwards = %v ; "+
			"re-claiming the SAME name while the handle is still open -> %s [build %d]",
		DescribeErr(ntErr), statErr == nil, DescribeErr(reuse), build)
	RequireProperty(t, "M10.posix_nt", ntErr == nil && statErr != nil,
		"POSIX-semantics delete must remove the name from the namespace immediately, otherwise Publish's create-only contract "+
			"reports ERROR_ACCESS_DENIED (delete pending) instead of \"already exists\" (§4.14)")
	_ = DeleteAt(r.Handle(), "posix.txt", windows.FILE_DIRECTORY_FILE, true)

	// The Win32 wrapper form.
	mustWrite(t, filepath.Join(dir, "posix2.txt"), "x")
	if h2, err := OpenForDeleteAt(r.Handle(), "posix2.txt", windows.FILE_NON_DIRECTORY_FILE); err == nil {
		werr := DeleteByHandleWin32(h2, true)
		windows.CloseHandle(h2)
		Report(t, "M10.posix_win32", boolVerdict(werr == nil),
			"Win32 SetFileInformationByHandle(FileDispositionInfoEx=21) -> %s", DescribeErr(werr))
	}

	// The legacy form, to show the delete-pending window is real.
	mustWrite(t, filepath.Join(dir, "legacy.txt"), "x")
	h3, err := OpenForDeleteAt(r.Handle(), "legacy.txt", windows.FILE_NON_DIRECTORY_FILE)
	if err == nil {
		lerr := DeleteByHandle(h3, false)
		_, still := StatAt(r.Handle(), "legacy.txt")
		reclaim := MkdirAt(r.Handle(), "legacy.txt")
		windows.CloseHandle(h3)
		Report(t, "M10.legacy_pending", Info,
			"legacy FileDispositionInfo (delete-on-last-close) -> %s ; name still resolvable while the handle is open = %v (%v) ; "+
				"re-claiming it -> %s. This is the delete-pending error that must NOT be surfaced as \"already exists\" (R6).",
			DescribeErr(lerr), still == nil, still, DescribeErr(reclaim))
	}
	_ = DeleteAt(r.Handle(), "legacy.txt", 0, true)

	// Memory-mapped: documented to fail with STATUS_CANNOT_DELETE.
	mapped := filepath.Join(dir, "mapped.bin")
	mustWrite(t, mapped, strings.Repeat("m", 4096))
	mp, _ := windows.UTF16PtrFromString(mapped)
	fh, err := windows.CreateFile(mp, windows.GENERIC_READ|windows.GENERIC_WRITE, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		Report(t, "M10.mapped", NotMeasured, "open for mapping: %s", DescribeErr(err))
		return
	}
	mh, err := windows.CreateFileMapping(fh, nil, windows.PAGE_READONLY, 0, 4096, nil)
	if err != nil {
		windows.CloseHandle(fh)
		Report(t, "M10.mapped", NotMeasured, "CreateFileMapping: %s", DescribeErr(err))
		return
	}
	addr, err := windows.MapViewOfFile(mh, windows.FILE_MAP_READ, 0, 0, 4096)
	if err != nil {
		windows.CloseHandle(mh)
		windows.CloseHandle(fh)
		Report(t, "M10.mapped", NotMeasured, "MapViewOfFile: %s", DescribeErr(err))
		return
	}
	dh, derr := OpenForDeleteAt(r.Handle(), "mapped.bin", windows.FILE_NON_DIRECTORY_FILE)
	var mapErr error
	if derr == nil {
		mapErr = DeleteByHandle(dh, true)
		windows.CloseHandle(dh)
	}
	windows.UnmapViewOfFile(addr)
	windows.CloseHandle(mh)
	windows.CloseHandle(fh)
	Report(t, "M10.mapped", Info,
		"POSIX delete of a MEMORY-MAPPED file -> open=%s delete=%s (STATUS_CANNOT_DELETE=0xC0000121 expected). "+
			"Relevant because the web binary serves artifact files and any mapping by another process blocks removal.",
		DescribeErr(derr), DescribeErr(mapErr))
}

// ---------------------------------------------------------------------------
// M11 — the volume's real $UpCase folding. Reported, never assumed.
// ---------------------------------------------------------------------------

func TestM11CaseFolding(t *testing.T) {
	r, _ := openScratchRoot(t)
	cases := []struct {
		id, a, b string
	}{
		{"ascii_i_I", "i", "I"},
		{"dotless_i_U+0131_vs_I", "\u0131", "I"},
		{"dotless_i_U+0131_vs_i", "\u0131", "i"},
		{"dotted_I_U+0130_vs_i", "\u0130", "i"},
		{"dotted_I_U+0130_vs_I", "\u0130", "I"},
		{"kelvin_U+212A_vs_K", "\u212a", "K"},
		{"kelvin_U+212A_vs_k", "\u212a", "k"},
		{"sharp_s_vs_capital_sharp_s", "\u00df", "\u1e9e"},
		{"sharp_s_vs_ss", "\u00df", "ss"},
		{"fullwidth_A_vs_A", "\uff21", "A"},
		{"fullwidth_A_vs_fullwidth_a", "\uff21", "\uff41"},
		{"annotations_case", ".annotations", ".Annotations"},
		{"pem_case", "key.pem", "key.PEM"},
		{"ssh_case", ".ssh", ".SSH"},
	}
	for i, c := range cases {
		sub := fmt.Sprintf("fold%02d", i)
		if err := MkdirAt(r.Handle(), sub); err != nil {
			Report(t, "M11."+c.id, NotMeasured, "mkdir: %s", DescribeErr(err))
			continue
		}
		h, err := OpenDirAt(r.Handle(), sub)
		if err != nil {
			Report(t, "M11."+c.id, NotMeasured, "open: %s", DescribeErr(err))
			continue
		}
		firstErr := MkdirAt(h, c.a)
		secondErr := MkdirAt(h, c.b)
		windows.CloseHandle(h)
		if firstErr != nil {
			Report(t, "M11."+c.id, NotMeasured, "could not create %q: %s", c.a, DescribeErr(firstErr))
			continue
		}
		st, _ := StatusOf(secondErr)
		folded := st == windows.STATUS_OBJECT_NAME_COLLISION
		goFold := strings.EqualFold(c.a, c.b)
		agree := folded == goFold
		Report(t, "M11."+c.id, boolVerdict(folded),
			"NTFS folds %q == %q : %v (create of the second name -> %s) | Go strings.EqualFold says %v | AGREE=%v",
			c.a, c.b, folded, DescribeErr(secondErr), goFold, agree)
	}
	Report(t, "M11.consequence", Info,
		"Every row where NTFS folds but the Go check is case-SENSITIVE is a live bypass: ignore.go:378's `.annotations` reserved-name check "+
			"and defaultIgnores' credential rules go through path.Match, which is case-sensitive (threat model §4.10, RR5).")
}

// ---------------------------------------------------------------------------
// M12 — can an alternate-data-stream open be detected after the fact?
// ---------------------------------------------------------------------------

func TestM12AlternateDataStreams(t *testing.T) {
	r, dir := openScratchRoot(t)
	base := filepath.Join(dir, "doc.html")
	mustWrite(t, base, "<h1>page</h1>")

	streamPath := base + ":hidden"
	f, err := os.OpenFile(streamPath, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		Report(t, "M12", NotMeasured, "could not create an ADS (non-NTFS volume?): %v", err)
		return
	}
	f.WriteString("STREAMBYTES")
	f.Close()

	// Does the HANDLE-RELATIVE open accept the colon form at all? This is the
	// question that decides whether rejecting ':' in validateSegment is the
	// whole fix or only half of it.
	h, err := ntOpenAt(r.Handle(), "doc.html:hidden", fileReadAccess, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
	Report(t, "M12.relative_open", boolVerdict(err == nil),
		"NtCreateFile with a RootDirectory-relative ObjectName %q -> %s (so the store's own handle-relative opens are equally exposed to ADS syntax)",
		"doc.html:hidden", DescribeErr(err))
	if err == nil {
		name, nerr := NameInfo(h)
		final, ferr := FinalPath(h, volumeNameDOS)
		visible := strings.Contains(name, ":") || strings.Contains(final, ":hidden")
		Report(t, "M12.detect", boolVerdict(visible),
			"from the STREAM handle: FILE_NAME_INFO=%q (err %v) GetFinalPathNameByHandleW=%q (err %v) -> stream visible after the fact = %v. "+
				"If false, there is no defence in depth behind rejecting ':' and the rejection must be complete (R11).",
			name, nerr, final, ferr, visible)
		windows.CloseHandle(h)
	}

	entries, _ := os.ReadDir(dir)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	fi, _ := os.Stat(base)
	size := int64(-1)
	if fi != nil {
		size = fi.Size()
	}
	Report(t, "M12.invisible", Info,
		"os.ReadDir of the artifact shows %v and os.Stat reports size=%d — the stream bytes are invisible to loadArtifact's walk "+
			"(store.go:380-386) and therefore to maxPreviewBytes (threat model §4.6).", names, size)

	// The drive-letter-looking segment.
	h2, err2 := ntOpenAt(r.Handle(), "C:evil", fileReadAccess, windows.FILE_OPEN,
		windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
	if err2 == nil {
		windows.CloseHandle(h2)
	}
	Report(t, "M12.drive_looking", Info,
		"a URL segment %q opened relative to the root -> %s (NTFS parses it as stream \"evil\" of a file named \"C\", not as a drive)",
		"C:evil", DescribeErr(err2))
}

// ---------------------------------------------------------------------------
// M13 — sharing violations. The AV distribution is not measurable here; the
// deterministic half is.
// ---------------------------------------------------------------------------

func TestM13SharingViolations(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	dst := filepath.Join(dir, "notes.json")
	mustWrite(t, dst, "OLD")

	// An interfering opener that does NOT grant FILE_SHARE_DELETE — an editor,
	// Explorer's preview handler, or an AV scanner.
	dp, _ := windows.UTF16PtrFromString(dst)
	blocker, err := windows.CreateFile(dp, windows.GENERIC_READ,
		windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		Report(t, "M13", NotMeasured, "could not open the blocking handle: %s", DescribeErr(err))
		return
	}

	src, err := CreateFileAt(parent, "tmp.tmp")
	if err != nil {
		windows.CloseHandle(blocker)
		t.Fatalf("CreateFileAt: %s", DescribeErr(err))
	}
	windows.Write(src, []byte("NEW"))

	blockedErr := RenameAtNT(src, parent, "notes.json", fileRenameInformationEx,
		fileRenameReplaceIfExists|fileRenamePosixSemantics)
	content, _ := os.ReadFile(dst)
	Report(t, "M13.blocked", boolVerdict(blockedErr != nil),
		"atomic replace while the destination is held WITHOUT FILE_SHARE_DELETE -> %s ; destination still holds %q (R9: never truncated, never absent)",
		DescribeErr(blockedErr), string(content))

	// Bounded retry: release the blocker and measure how quickly it succeeds.
	windows.CloseHandle(blocker)
	start := time.Now()
	var retryErr error
	attempts := 0
	for i := 0; i < 20; i++ {
		attempts++
		retryErr = RenameAtNT(src, parent, "notes.json", fileRenameInformationEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		if retryErr == nil {
			break
		}
		time.Sleep(time.Duration(1<<uint(min(i, 6))) * time.Millisecond)
	}
	windows.CloseHandle(src)
	after, _ := os.ReadFile(dst)
	Report(t, "M13.retry", boolVerdict(retryErr == nil),
		"after the blocker closed, the replace succeeded in %d attempt(s) over %v (err %s); destination now holds %q",
		attempts, time.Since(start).Round(time.Millisecond), DescribeErr(retryErr), string(after))

	// The same for a recursive delete meeting a held child.
	sub := mustMkdir(t, filepath.Join(dir, "artifact"))
	child := mustWrite(t, filepath.Join(sub, "held.txt"), "x")
	cp, _ := windows.UTF16PtrFromString(child)
	held, err := windows.CreateFile(cp, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err == nil {
		sh, oerr := OpenDirAt(r.Handle(), "artifact")
		var delErr error
		if oerr == nil {
			delErr = DeleteAt(sh, "held.txt", windows.FILE_NON_DIRECTORY_FILE, true)
			windows.CloseHandle(sh)
		}
		windows.CloseHandle(held)
		Report(t, "M13.delete_blocked", Info,
			"deleting a child held open WITHOUT FILE_SHARE_DELETE -> %s. Delete has no partial-failure story today "+
				"(removeTreeAt aborts mid-tree, annotationfs_linux.go:183-186) and this is the error that will trigger it.",
			DescribeErr(delErr))
	}

	Report(t, "M13.av", NotMeasured,
		"the observed distribution of transient errors under a real antivirus and Windows Search is NOT measured: "+
			"Defender's realtime protection state on a GitHub runner is not representative of a developer machine. "+
			"The retryable set must therefore be chosen from documentation (ERROR_SHARING_VIOLATION, ERROR_LOCK_VIOLATION, "+
			"ERROR_ACCESS_DENIED-from-delete-pending, ERROR_DIR_NOT_EMPTY) with a bound, not from a measured distribution.")
}

// ---------------------------------------------------------------------------
// M14 — LockFileEx on a directory handle. The store-root flock is what makes
// Delete and SaveNotes mutually exclusive (annotations.go:119-136).
// ---------------------------------------------------------------------------

func TestM14LockDirectoryHandle(t *testing.T) {
	r, dir := openScratchRoot(t)

	ov := new(windows.Overlapped)
	errRead := windows.LockFileEx(r.Handle(),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ov)
	Report(t, "M14.dir_readhandle", boolVerdict(errRead == nil),
		"LockFileEx(EXCLUSIVE|FAIL_IMMEDIATELY) on the pinned root DIRECTORY handle (GENERIC_READ) -> %s", DescribeErr(errRead))
	if errRead == nil {
		windows.UnlockFileEx(r.Handle(), 0, 1, 0, new(windows.Overlapped))
	}

	// Again with write access, in case the failure was an access-mask problem.
	dp, _ := windows.UTF16PtrFromString(dir)
	wh, err := windows.CreateFile(dp, windows.GENERIC_READ|windows.GENERIC_WRITE, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		Report(t, "M14.dir_writehandle", Info, "a directory cannot even be opened GENERIC_WRITE: %s", DescribeErr(err))
	} else {
		ov2 := new(windows.Overlapped)
		errWrite := windows.LockFileEx(wh, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ov2)
		Report(t, "M14.dir_writehandle", boolVerdict(errWrite == nil),
			"LockFileEx on a directory handle opened GENERIC_READ|GENERIC_WRITE -> %s", DescribeErr(errWrite))
		if errWrite == nil {
			windows.UnlockFileEx(wh, 0, 1, 0, new(windows.Overlapped))
		}
		windows.CloseHandle(wh)
	}

	// Control: a regular file, which is what a lock-FILE substitute would use.
	lockPath := mustWrite(t, filepath.Join(dir, ".lock"), "")
	lp, _ := windows.UTF16PtrFromString(lockPath)
	fh, err := windows.CreateFile(lp, windows.GENERIC_READ|windows.GENERIC_WRITE, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err == nil {
		ov3 := new(windows.Overlapped)
		errFile := windows.LockFileEx(fh, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, ov3)
		Report(t, "M14.file_control", boolVerdict(errFile == nil),
			"CONTROL: LockFileEx on a regular FILE handle -> %s", DescribeErr(errFile))

		// Mandatory, not advisory: a second opener's read must actually fail.
		fh2, err2 := windows.CreateFile(lp, windows.GENERIC_READ, shareAll, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err2 == nil {
			var buf [1]byte
			var n uint32
			readErr := windows.ReadFile(fh2, buf[:], &n, nil)
			Report(t, "M14.mandatory", Info,
				"a byte-range lock is MANDATORY on Windows: a second handle's ReadFile over the locked range -> %s "+
					"(unlike flock, a hung holder blocks other processes' I/O outright)", DescribeErr(readErr))
			windows.CloseHandle(fh2)
		}
		if errFile == nil {
			windows.UnlockFileEx(fh, 0, 1, 0, new(windows.Overlapped))
		}
		windows.CloseHandle(fh)
	}

	Report(t, "M14.consequence", Info,
		"If the directory lock fails, the store-root rendezvous of annotations.go:119-136 has no direct Windows equivalent. "+
			"The lock file has to live somewhere, and the comment at annotations.go:124-126 chose the root INODE precisely so the "+
			"rendezvous survives `.annotations` being renamed away. A lock file pinned by HANDLE at process start (opened once, "+
			"never re-resolved) preserves that property: the handle keeps naming the same object after any rename. That is the "+
			"design the ADR should evaluate (RR6).")
}

// ---------------------------------------------------------------------------
// M18 — RtlIsDosDeviceName_U across the reserved-name forms.
// ---------------------------------------------------------------------------

func TestM18ReservedDeviceNames(t *testing.T) {
	maj, min, build := osBuild()
	names := []string{"NUL", "nul", "CON", "CON.txt", "NUL.txt", "NUL.tar.gz", "COM1", "COM0", "COM9", "LPT1", "AUX", "PRN", "CONIN$", "NUL ", "NUL.", "index.html"}
	for _, n := range names {
		v := rtlIsDosDeviceName(n)
		Report(t, "M18."+strings.ReplaceAll(n, " ", "_SPACE"), boolVerdict(v != 0),
			"RtlIsDosDeviceName_U(%q) = 0x%08X (non-zero = the OS treats it as a DOS device) [OS %d.%d build %d]", n, v, maj, min, build)
	}

	// And the reachable consequence: what happens when a lookup segment names
	// a device relative to a pinned directory handle?
	r, _ := openScratchRoot(t)
	for _, n := range []string{"NUL", "CON", "COM1"} {
		h, err := ntOpenAt(r.Handle(), n, fileReadAccess, windows.FILE_OPEN,
			windows.FILE_NON_DIRECTORY_FILE, noFollowAttrs, 0)
		if err == nil {
			windows.CloseHandle(h)
		}
		Report(t, "M18.relative_open."+n, boolVerdict(err != nil),
			"opening %q RELATIVE TO A DIRECTORY HANDLE -> %s. A handle-relative open resolves in the object namespace of the "+
				"directory, so device names may not be reachable this way at all — which would be a containment BONUS of the "+
				"handle-anchored design that the threat model (§4.12) did not anticipate.", n, DescribeErr(err))
	}
	// The path-based control, which is what a string-path port would issue.
	dir := scratchDir(t)
	f, err := os.OpenFile(filepath.Join(dir, "NUL"), os.O_RDONLY, 0)
	if err == nil {
		f.Close()
	}
	Report(t, "M18.path_open_control", Info,
		"CONTROL: os.OpenFile(<dir>/NUL, O_RDONLY) -> %v (a path-based port serves an empty 200 here instead of a 404)", err)
}
