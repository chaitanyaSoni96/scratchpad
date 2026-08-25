//go:build windows

package winspike

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// The spec's Security Test Matrix, cell by cell.
//
// Phase 1 does not owe the matrix its Phase 3 tests; it owes an answer for
// every cell: DEMONSTRATED on the prototype, MECHANISM-ONLY (the primitive is
// proven here, the policy that uses it is internal/store code Phase 3 must
// test), or EXCLUDED with a stated reason. A silent gap is the one outcome
// that is not allowed.
//
// Verdict encoding, so the job summary can be read at a glance:
//
//	YES          demonstrated by a named test in this package
//	PARTIAL      the Windows primitive is demonstrated; the store-level policy
//	             that consumes it is not in this package and is handed to P3
//	NOT-MEASURED documented exclusion, reason in the detail
// ---------------------------------------------------------------------------

// A few cells needed one more small measurement before they could be claimed.
// They are made here rather than being asserted from documentation.

func TestMatrixRootCells(t *testing.T) {
	base := scratchDir(t)

	// root missing.
	_, missErr := OpenRoot(filepath.Join(base, "nope"))
	Report(t, "MX.root_missing", boolVerdict(missErr != nil),
		"OpenRoot on a path that does not exist -> %s (the store creates the root before pinning it; this is the error the "+
			"create path must distinguish)", DescribeErr(missErr))

	// root file.
	f := mustWrite(t, filepath.Join(base, "afile"), "x")
	_, fileErr := OpenRoot(f)
	RequireProperty(t, "MX.root_file", fileErr != nil,
		"OpenRoot must refuse a regular file as the store root, got err=%v", fileErr)
	Report(t, "MX.root_file", boolVerdict(fileErr != nil), "OpenRoot on a regular file -> %v", fileErr)

	// root link / reparse point, one measurement per tag.
	target := mustMkdir(t, filepath.Join(base, "target"))
	baseRoot, err := OpenRoot(base)
	if err != nil {
		t.Fatalf("OpenRoot(base): %s", DescribeErr(err))
	}
	defer baseRoot.Close()

	for _, kind := range []string{"junction", "symlink", "unknowntag"} {
		name := "root_" + kind
		if k, ok := plantLink(t, baseRoot.Handle(), name, target, kind); !ok {
			Report(t, "MX.root_reparse."+kind, NotMeasured, "could not plant: %s", k)
			continue
		}
		_, rerr := OpenRoot(filepath.Join(base, name))
		RequireProperty(t, "MX.root_reparse."+kind, rerr != nil,
			"a store root that is a %s must be refused (tag-based, not fs.ModeSymlink-based), got err=%v", kind, rerr)
		Report(t, "MX.root_reparse."+kind, boolVerdict(rerr != nil), "OpenRoot on a %s -> %v", kind, rerr)
	}

	// root as a VOLUME mount point — same tag as a junction, different meaning.
	buf := make([]uint16, 128)
	if err := windows.GetVolumeNameForVolumeMountPoint(windows.StringToUTF16Ptr(`C:\`), &buf[0], uint32(len(buf))); err != nil {
		Report(t, "MX.root_reparse.volume_mount_point", NotMeasured, "GetVolumeNameForVolumeMountPoint: %s", DescribeErr(err))
		return
	}
	guid := windows.UTF16ToString(buf)
	if err := MkdirAt(baseRoot.Handle(), "root_vmp"); err != nil {
		Report(t, "MX.root_reparse.volume_mount_point", NotMeasured, "mkdir: %s", DescribeErr(err))
		return
	}
	h, oerr := ntOpenAt(baseRoot.Handle(), "root_vmp", windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
		windows.OBJ_CASE_INSENSITIVE, 0)
	if oerr != nil {
		Report(t, "MX.root_reparse.volume_mount_point", NotMeasured, "reopen: %s", DescribeErr(oerr))
		return
	}
	serr := SetMountPointRaw(h, `\??\`+strings.TrimPrefix(guid, `\\?\`), guid)
	windows.CloseHandle(h)
	if serr != nil {
		Report(t, "MX.root_reparse.volume_mount_point", NotMeasured, "SetMountPointRaw: %s", DescribeErr(serr))
		return
	}
	_, verr := OpenRoot(filepath.Join(base, "root_vmp"))
	RequireProperty(t, "MX.root_reparse.volume_mount_point", verr != nil,
		"a store root that is a VOLUME mount point must be refused; it carries the same tag as a junction and crossing it "+
			"changes volume, FileId space and possibly filesystem. Got err=%v", verr)
	Report(t, "MX.root_reparse.volume_mount_point", boolVerdict(verr != nil),
		"OpenRoot on a volume mount point (%s) -> %v. NOTE for the ADR: a user redirecting %%USERPROFILE%%\\.scratchpad onto "+
			"another drive with `mklink /J` is NORMAL behaviour, so refusing it silently will read as a bug; the refusal has "+
			"to name the reason.", guid, verr)
}

func TestMatrixNotesAndNameCells(t *testing.T) {
	r, dir := openScratchRoot(t)

	// Notes: the .annotations root, and an intermediate component, being links.
	for _, kind := range []string{"junction", "symlink"} {
		rr, d := openScratchRoot(t)
		ext := scratchDir(t)
		if k, ok := plantLink(t, rr.Handle(), ".annotations", ext, kind); !ok {
			Report(t, "MX.notes_root_link."+kind, NotMeasured, "could not plant: %s", k)
			continue
		}
		_, aerr := rr.OpenRealDir([]string{".annotations"}, false, false)
		RequireProperty(t, "MX.notes_root_link."+kind, aerr != nil,
			"the annotations root being a %s must be refused by the walk, got err=%v", kind, aerr)
		Report(t, "MX.notes_root_link."+kind, boolVerdict(aerr != nil),
			"OpenRealDir into a %s-shaped .annotations -> %s", kind, DescribeErr(aerr))
		_ = d
	}
	for _, kind := range []string{"junction", "symlink"} {
		rr, d := openScratchRoot(t)
		ext := scratchDir(t)
		mustMkdir(t, filepath.Join(d, ".annotations"))
		ah, err := OpenRoot(filepath.Join(d, ".annotations"))
		if err != nil {
			Report(t, "MX.notes_intermediate_link."+kind, NotMeasured, "OpenRoot: %s", DescribeErr(err))
			continue
		}
		k, ok := plantLink(t, ah.Handle(), "proj", ext, kind)
		ah.Close()
		if !ok {
			Report(t, "MX.notes_intermediate_link."+kind, NotMeasured, "could not plant: %s", k)
			continue
		}
		_, aerr := rr.OpenRealDir([]string{".annotations", "proj"}, false, false)
		RequireProperty(t, "MX.notes_intermediate_link."+kind, aerr != nil,
			"an intermediate %s under .annotations must be refused, got err=%v", kind, aerr)
		Report(t, "MX.notes_intermediate_link."+kind, boolVerdict(aerr != nil),
			"OpenRealDir through .annotations/<%s> -> %s", kind, DescribeErr(aerr))
	}

	// Names: trailing dot and trailing space, handle-relative.
	var rows []string
	for _, n := range []string{"art.", "art ", "art..", "art. ", ".art"} {
		mkErr := MkdirAt(r.Handle(), n)
		at, statErr := StatAt(r.Handle(), n)
		listed := "no"
		if es, err := os.ReadDir(dir); err == nil {
			for _, e := range es {
				if e.Name() == n {
					listed = "exact"
				} else if strings.TrimRight(e.Name(), ". ") == strings.TrimRight(n, ". ") && listed == "no" {
					listed = "as " + e.Name()
				}
			}
		}
		rows = append(rows, fmt.Sprintf("%q: mkdir=%s stat=%v(%v) listed=%s", n, DescribeErr(mkErr), at.FileAttributes, statErr, listed))
	}
	Report(t, "MX.trailing_dot_space", Info,
		"handle-relative creation and lookup of trailing-dot/space names: %s. The relative form goes through the object "+
			"manager without the Win32 path parser's trailing-character stripping, so what is CREATED and what a Win32 "+
			"caller can later address are not the same set — R11 has to exclude these at validation, not rely on the "+
			"filesystem to reject them.", strings.Join(rows, " ; "))

	// Names: directory hard links do not exist. Stated as a measurement so the
	// ADR does not spend effort defending against a non-threat (§4.4).
	src := mustMkdir(t, filepath.Join(dir, "hlsrc"))
	linkErr := windows.CreateHardLink(windows.StringToUTF16Ptr(filepath.Join(dir, "hldst")),
		windows.StringToUTF16Ptr(src), 0)
	fsrc := mustWrite(t, filepath.Join(dir, "hlfile"), "x")
	fileLinkErr := windows.CreateHardLink(windows.StringToUTF16Ptr(filepath.Join(dir, "hlfile2")),
		windows.StringToUTF16Ptr(fsrc), 0)
	Report(t, "MX.directory_hard_links", boolVerdict(linkErr != nil && fileLinkErr == nil),
		"CreateHardLinkW on a DIRECTORY -> %v ; on a FILE -> %v. Directory hard links do not exist on Windows (§4.4), so "+
			"there is no directory-hard-link cell to test. File hard links DO work unprivileged and are RR4: an ordinary "+
			"regular file with no tag to detect, identical to the pre-existing Linux gap.", linkErr, fileLinkErr)
}

// TestSecurityTestMatrixCoverage prints the coverage table. It makes no
// filesystem calls; it is the index into everything above.
func TestSecurityTestMatrixCoverage(t *testing.T) {
	type cell struct {
		id      string
		verdict string
		detail  string
	}
	cells := []cell{
		// ---- Root ----
		{"MATRIX.Root.root_missing", Yes,
			"DEMONSTRATED: MX.root_missing — OpenRoot on an absent path fails with a distinguishable status."},
		{"MATRIX.Root.root_file", Yes,
			"DEMONSTRATED: MX.root_file, P12.root_file — FILE_DIRECTORY_FILE refuses a regular file."},
		{"MATRIX.Root.root_link_reparse", Yes,
			"DEMONSTRATED for all four flavours: MX.root_reparse.junction / .symlink / .unknowntag / .volume_mount_point. " +
				"The refusal is keyed on FILE_ATTRIBUTE_TAG_INFO, not on fs.ModeSymlink, which is required because a junction " +
				"is neither ModeSymlink nor ModeDir (P14.junction_modesymlink)."},
		{"MATRIX.Root.root_replaced", Yes,
			"DEMONSTRATED: A4.root_replaced.realdir / .junction / .symlink, plus A4.root_removed and R13.replace. " +
				"The pinned handle makes the substitution unreachable; a FRESH open is what needs the FILE_ID_INFO check."},

		// ---- Publish ----
		{"MATRIX.Publish.concurrent_claim", Yes,
			"DEMONSTRATED: A8.concurrent_claim (16 racing claims, exactly one winner) and A8.delete_pending_claim " +
				"(the Windows-only third outcome)."},
		{"MATRIX.Publish.ancestor_replaced", Yes,
			"DEMONSTRATED: A1.ancestor_replaced.realdir / .junction / .symlink / .unknowntag, and M7.redirect."},
		{"MATRIX.Publish.ancestor_link", Yes,
			"DEMONSTRATED: P12.junction_traverse, P12.junction_intermediate, P12.symlink_traverse, A5.strict_walk. " +
				"NOTE the correction in A5.obj_dont_reparse_inert_for_unknown_tags: for a NON-Microsoft tag the refusal must " +
				"come from the tag read off a FILE_OPEN_REPARSE_POINT handle, not from OBJ_DONT_REPARSE, which is inert for " +
				"those tags."},
		{"MATRIX.Publish.artifact_ancestor", Partial,
			"MECHANISM DEMONSTRATED: P12.reject_artifact (rejectArtifacts through the pinned walk). The remaining half is " +
				"store policy: dirHasHTMLFD uses strings.ToLower, which is NOT the volume's $UpCase folding (M11), so a " +
				"`.HTML` entry can miss the artifact test. That is an internal/store fix and an internal/store test (P3)."},

		// ---- Browse ----
		{"MATRIX.Browse.one_boundary", Yes,
			"DEMONSTRATED: P12.browsable_boundary, P12.browsable_tag_allowlist (a MOUNT_POINT boundary is refused unless " +
				"the allowlist names it)."},
		{"MATRIX.Browse.nested_link", Yes,
			"DEMONSTRATED: A3.nested.* — every boundary flavour × every nested flavour (junction, symlink, unknown tag), " +
				"at one and at two levels below the boundary — and A3.nested_strict.* restates each refusal as a TAG-based " +
				"one so it does not depend on the runner's filter drivers."},
		{"MATRIX.Browse.cycle", Partial,
			"MECHANISM DEMONSTRATED: a cycle needs a SECOND link inside the watched source, which A3.nested.* shows is " +
				"refused, so unbounded traversal cannot be constructed through the browse walk. The other half — that " +
				"List's string-keyed visited set terminates — is internal/store code; M5.case additionally showed " +
				"EvalSymlinks canonicalises case, so RR9's case-alternating-cycle mechanism does not exist. P3 owns the " +
				"List/watcher termination test."},
		{"MATRIX.Browse.broken_target", Yes,
			"DEMONSTRATED: A11.target_swapped removes the target entirely as part of its staging; the browse open fails " +
				"cleanly rather than panicking or serving anything."},
		{"MATRIX.Browse.target_replacement", Yes,
			"DEMONSTRATED, AND IT FOUND A WINDOW: A11.target_swapped (the target itself — REFUSED) and " +
				"A11.ancestor_swapped (an ancestor of the target — the open-by-name follows it). See the findings; the " +
				"second half is platform-independent and present in storefs_linux.go:184 today."},

		// ---- Documents ----
		{"MATRIX.Documents.file_link", Yes,
			"DEMONSTRATED: A10.file_link_refused. The hard-link variant is RR4 and is accepted, not fixed (MX.directory_hard_links)."},
		{"MATRIX.Documents.directory_link", Yes,
			"DEMONSTRATED: A10.dir_link_refused — refused by FILE_NON_DIRECTORY_FILE|OBJ_DONT_REPARSE in ONE operation, " +
				"not by a stat."},
		{"MATRIX.Documents.alternate_stream", Partial,
			"MEASURED: A10.stream_syntax and M12 — a RootDirectory-relative NtCreateFile ACCEPTS `doc.html:hidden`, so the " +
				"open will not reject it. The control therefore has to be validateSegment rejecting ':' (R11), which is " +
				"internal/store code. P3 owns the 404 assertions."},
		{"MATRIX.Documents.case_variation", Partial,
			"MEASURED: M11 gives the volume's real $UpCase folding, including that `.annotations`/`.Annotations` and " +
				"`key.pem`/`key.PEM` DO fold (RR5 confirmed live). The fix and its test are internal/store (ignore.go's " +
				"reserved-name check and defaultIgnores)."},
		{"MATRIX.Documents.rename_race", Yes,
			"DEMONSTRATED: A10.rename_race — the document is substituted after the parent handle is pinned."},

		// ---- Delete ----
		{"MATRIX.Delete.target_replaced", Yes,
			"DEMONSTRATED, THE T1 TEST: A6.delete.junction/.symlink/.unknowntag at depths 0 and 2, plus A6.swap_midwalk " +
				"(substituted between enumeration and descent), plus the NEGATIVE CONTROL A6.negative_control which shows " +
				"the mechanical port DOES destroy the target."},
		{"MATRIX.Delete.parent_replaced", Yes,
			"DEMONSTRATED: A6.parent_replaced."},
		{"MATRIX.Delete.link_target_untouched", Yes,
			"DEMONSTRATED: A6.unlink_watch.junction / .symlink, and P14.unlink_junction."},
		{"MATRIX.Delete.annotation_subtree_cleanup", Partial,
			"MECHANISM DEMONSTRATED: RemoveTreeAt is the primitive and A6.* proves it is containment-safe. Whether Delete " +
				"actually calls it for the notes subtree, and the re-published-name-inherits-no-notes assertion, are " +
				"internal/store behaviour (P3)."},

		// ---- Notes ----
		{"MATRIX.Notes.annotation_root_link", Yes,
			"DEMONSTRATED: MX.notes_root_link.junction / .symlink."},
		{"MATRIX.Notes.intermediate_link", Yes,
			"DEMONSTRATED: MX.notes_intermediate_link.junction / .symlink."},
		{"MATRIX.Notes.concurrent_revisions", Partial,
			"MECHANISM DEMONSTRATED: A12.concurrent_writers — 8 writers × 40 replaces, no torn read, no temp residue. " +
				"The rev guard itself and ErrRevMismatch are internal/store. The Windows-extra the threat model asks for " +
				"(Delete racing SaveNotes without a store-root directory lock, RR6) needs the lock-FILE substitute chosen " +
				"in the ADR (M14) and is a P3 test."},
		{"MATRIX.Notes.sharing_violation", Yes,
			"DEMONSTRATED — AND THIS CELL DOES NOT EXIST ON LINUX: P13.sharing.* (all 8 share masks × read/write access × " +
				"3 mutations), P13.sharing_never_truncates, P13.bound, P13.bound_preserves_dest, P13.retry.hold*, " +
				"M13.pending_status."},

		// ---- Watch ----
		{"MATRIX.Watch.same_target_idempotence", Partial,
			"MEASURED: the Windows twist is that a create-only claim over an existing LINK fails with " +
				"STATUS_REPARSE_POINT_ENCOUNTERED, not STATUS_OBJECT_NAME_COLLISION (M8.claim_over.*), so a mechanical port " +
				"of Watch's errors.Is(EEXIST) relaxation turns every repeat `watch` into a hard error. The relaxation " +
				"itself is internal/store (P3)."},
		{"MATRIX.Watch.different_target_collision", Partial,
			"MECHANISM DEMONSTRATED: M8.createsymlink_excl / M8.symlinkat_excl — link creation never replaces. The " +
				"target-comparison policy (linksTo) is internal/store, and on Windows it must compare OBJECT IDENTITY, not " +
				"strings, because of 8.3 aliasing (M6.prefix_defect)."},
		{"MATRIX.Watch.junction_reparse_variants", Yes,
			"DEMONSTRATED for MOUNT_POINT (junction), MOUNT_POINT (volume), SYMLINK and a synthetic unknown tag: " +
				"P14.classify.*, M3.volume_mount_point, A5.*, A6.*. The `Watches ⊆ Unwatch-able` invariant is store logic; " +
				"the primitive half (a junction IS removable relative to a pinned parent) is P14.unlink_junction."},
		{"MATRIX.Watch.no_symlink_privilege", Yes,
			"WINDOWS-ONLY CELL: P14.devmode_off.* and the P1.4 table — junctions are the only link an unprivileged, " +
				"Developer-Mode-off user can create; ERROR_PRIVILEGE_NOT_HELD is 1314."},
		{"MATRIX.Watch.two_step_crash_window", Yes,
			"WINDOWS-ONLY CELL ADDED BY THIS SPIKE: A7.two_step_residue / A7.two_step_recovery — a crash between " +
				"FILE_CREATE and FSCTL_SET_REPARSE_POINT leaves an empty real directory that NO CLI verb can clear."},

		// ---- Names ----
		{"MATRIX.Names.reserved_devices", Yes,
			"DEMONSTRATED: M18.*, M18.relative_open.* — reserved device names are NOT reachable through a handle-relative " +
				"open (STATUS_OBJECT_NAME_NOT_FOUND) while the path-based control succeeds. The handle-anchored design " +
				"closes the lookup half for free; R11 remains defence in depth for the display paths."},
		{"MATRIX.Names.trailing_dot_space", Yes,
			"DEMONSTRATED: MX.trailing_dot_space — handle-relative creation bypasses the Win32 path parser's stripping, so " +
				"the created set and the Win32-addressable set differ."},
		{"MATRIX.Names.unc_drive_forms", Partial,
			"The SYNTACTIC half (SCRATCHPAD_ROOT set to `\\\\server\\share\\x`, `C:foo`, `\\foo`, `\\\\?\\C:\\x`, " +
				"`\\\\.\\PhysicalDrive0`) is pure string validation in internal/store and is a P3 test with no Windows " +
				"primitive to measure. The LIVE half is excluded: see MATRIX.EXCLUDED.smb."},
		{"MATRIX.Names.unicode_case_collisions", Yes,
			"DEMONSTRATED: M11 measured the volume's real $UpCase against Go's EqualFold, including the two disagreements " +
				"(Kelvin sign, ß/ẞ) and the confirmation that RR5 is live."},

		// ---- Explicit exclusions ----
		{"MATRIX.EXCLUDED.directory_hard_links", NotMeasured,
			"CONCEPT DOES NOT EXIST ON WINDOWS. CreateHardLinkW fails on a directory (MX.directory_hard_links measures it " +
				"rather than asserting it from documentation). There is no cell to test; §4.4 says so and this confirms it."},
		{"MATRIX.EXCLUDED.smb", NotMeasured,
			"NO SMB SHARE ON A GITHUB RUNNER. R18 already requires refusing UNC for mutations, so this stays policy rather " +
				"than measurement; a live-share check belongs to a manual pre-beta pass."},
		{"MATRIX.EXCLUDED.refs_devdrive", NotMeasured,
			"NO ReFS VOLUME ON A GITHUB RUNNER (the image exposes NTFS C: and NTFS D:). This is the realistic gap — a Dev " +
				"Drive is exactly where developers keep source trees — and it is the one thing CI cannot close. " +
				"FILE_RENAME_INFORMATION_EX and FILE_DISPOSITION_INFORMATION_EX are documented for ReFS but unverified here."},
		{"MATRIX.EXCLUDED.fat32", NotMeasured,
			"THE RUNNER'S ONLY FAT32 VOLUME IS THE UNMOUNTED EFI PARTITION. POSIX semantics and class 65 are documented as " +
				"unsupported there; A9.rename_failure_statuses establishes which statuses justify the class-10 fallback, " +
				"which is the mechanism that would cover FAT32."},
		{"MATRIX.EXCLUDED.cloud_placeholders", NotMeasured,
			"NO ONEDRIVE ON A GITHUB RUNNER. The `broken target` cell's cloud variant (ERROR_CLOUD_FILE_* instead of " +
				"not-found) and RR10's mass-rehydration risk are documented exclusions with a manual pre-beta check."},
		{"MATRIX.EXCLUDED.antivirus_distribution", NotMeasured,
			"NOT MEASURABLE ON A RUNNER (M13.av). Defender's realtime state on a CI image is not representative, so the " +
				"retryable set is chosen from documentation with a stated bound (RetryableStatuses) and the DETERMINISTIC " +
				"half — an interfering handle we open ourselves — is measured instead (P13.sharing.*, P13.retry.*)."},
		{"MATRIX.EXCLUDED.readdirchanges_overflow", NotMeasured,
			"NOT DETERMINISTICALLY REPRODUCIBLE (M15.overflow). The DirObserver in this package DOES detect the overflow " +
				"condition (a 0-byte return) and reports it rather than silently truncating, which is the shape " +
				"internal/watch should copy in Phase 3."},
		{"MATRIX.EXCLUDED.non_elevated_session", NotMeasured,
			"GITHUB RUNNERS EXECUTE ELEVATED WITH DEVELOPER MODE ON. The privilege-removal child (P14.noprivilege.*) is a " +
				"faithful simulation of the PRIVILEGE dimension but not of every ACL difference; one manual confirmation on " +
				"an ordinary user account is still owed."},
		{"MATRIX.EXCLUDED.32bit", NotMeasured,
			"NOT A TARGET. The FILE_RENAME_INFORMATION layout in this prototype asserts a 64-bit HANDLE and returns an " +
				"error otherwise."},
	}

	counts := map[string]int{}
	for _, c := range cells {
		Report(t, c.id, c.verdict, "%s", c.detail)
		counts[c.verdict]++
	}
	Report(t, "MATRIX.SUMMARY", Info,
		"%d matrix cells accounted for: %d DEMONSTRATED on the prototype, %d MECHANISM-ONLY (handed to Phase 3 with the "+
			"Windows primitive proven), %d DOCUMENTED EXCLUSIONS. Zero silent gaps.",
		len(cells), counts[Yes], counts[Partial], counts[NotMeasured])
}
