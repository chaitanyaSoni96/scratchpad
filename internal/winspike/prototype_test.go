//go:build windows

package winspike

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestEnvironment prints the facts every other measurement has to be read
// against: OS build, filesystem, volume serial. Run it first.
func TestEnvironment(t *testing.T) {
	maj, min, build := osBuild()
	Report(t, "ENV.os", Info, "RtlGetVersion major=%d minor=%d build=%d GOARCH=%s", maj, min, build, os.Getenv("PROCESSOR_ARCHITECTURE"))

	r, dir := openScratchRoot(t)
	fsName, serial, flags, err := VolumeInfo(r.Handle())
	if err != nil {
		Report(t, "ENV.volume", NotMeasured, "GetVolumeInformationByHandle: %s", DescribeErr(err))
	} else {
		Report(t, "ENV.volume", Info, "tempdir=%q fs=%q serial=0x%08X flags=0x%08X", dir, fsName, serial, flags)
	}
	Report(t, "ENV.rootid", Info, "pinned root %s", r.ID())

	ok, symErr := symlinkCapability(t)
	Report(t, "ENV.symlink", boolVerdict(ok), "unprivileged directory symlink creation: %s", DescribeErr(symErr))

	if _, err := os.Stat(`D:\`); err == nil {
		Report(t, "ENV.seconddrive", Yes, `D:\ exists on this runner (usable for the volume-crossing probe M3)`)
	} else {
		Report(t, "ENV.seconddrive", No, `D:\ not present: %v`, err)
	}
}

func boolVerdict(b bool) string {
	if b {
		return Yes
	}
	return No
}

// TestP12Primitives demonstrates, operation for operation, the mechanisms
// storefs_linux.go relies on. Each subtest names its Linux counterpart.
func TestP12Primitives(t *testing.T) {
	r, dir := openScratchRoot(t)

	t.Run("root_pin", func(t *testing.T) {
		// storefs_linux.go:30 — O_RDONLY|O_DIRECTORY|O_NOFOLLOW.
		if err := r.Verify(); err != nil {
			t.Fatalf("Verify: %v", err)
		}
		Report(t, "P12.root", Yes, "root pinned by handle; FILE_ID_INFO %s recorded for R13 re-verification", r.ID())

		// Root must refuse a regular file (the O_DIRECTORY half).
		f := mustWrite(t, filepath.Join(dir, "notadir"), "x")
		if _, err := OpenRoot(f); err == nil {
			t.Errorf("OpenRoot on a regular file succeeded")
		} else {
			Report(t, "P12.root_file", Yes, "OpenRoot refuses a regular root: %v", err)
		}
	})

	t.Run("mkdir_create_only", func(t *testing.T) {
		// store.go:561 — unix.Mkdirat, create-only, EEXIST on collision.
		if err := MkdirAt(r.Handle(), "claim"); err != nil {
			t.Fatalf("first MkdirAt: %s", DescribeErr(err))
		}
		err := MkdirAt(r.Handle(), "claim")
		if err == nil {
			t.Fatalf("second MkdirAt succeeded; create-only is broken")
		}
		st, _ := StatusOf(err)
		Report(t, "P12.mkdir_excl", Yes,
			"NtCreateFile(FILE_CREATE|FILE_DIRECTORY_FILE) on a taken name -> %s (errno %v). This is the os.Mkdir/EEXIST analogue.",
			DescribeErr(err), ErrnoOf(err))
		RequireProperty(t, "P12.mkdir_excl", st == windows.STATUS_OBJECT_NAME_COLLISION,
			"a taken name must fail with STATUS_OBJECT_NAME_COLLISION, got %s", DescribeErr(err))
	})

	t.Run("open_regular_file_nofollow", func(t *testing.T) {
		// storefs_linux.go:128 — openat(O_RDONLY|O_NOFOLLOW) + S_IFREG.
		mustWrite(t, filepath.Join(dir, "doc.html"), "<h1>hi</h1>")
		h, err := OpenRegularFileAt(r.Handle(), "doc.html")
		if err != nil {
			t.Fatalf("OpenRegularFileAt: %s", DescribeErr(err))
		}
		windows.CloseHandle(h)

		mustMkdir(t, filepath.Join(dir, "adir"))
		_, err = OpenRegularFileAt(r.Handle(), "adir")
		Report(t, "P12.openfile_isdir", boolVerdict(err != nil),
			"FILE_NON_DIRECTORY_FILE on a directory -> %s (this replaces the separate S_IFREG fstat, closing the open-then-check window)", DescribeErr(err))
	})

	t.Run("delete_relative_to_parent", func(t *testing.T) {
		// store.go:787,844 — unix.Unlinkat relative to a pinned parent.
		mustWrite(t, filepath.Join(dir, "gone.txt"), "x")
		if err := DeleteAt(r.Handle(), "gone.txt", windows.FILE_NON_DIRECTORY_FILE, true); err != nil {
			t.Fatalf("DeleteAt(file): %s", DescribeErr(err))
		}
		if _, err := os.Stat(filepath.Join(dir, "gone.txt")); !os.IsNotExist(err) {
			t.Errorf("file still present after DeleteAt: %v", err)
		}
		mustMkdir(t, filepath.Join(dir, "gonedir"))
		if err := DeleteAt(r.Handle(), "gonedir", windows.FILE_DIRECTORY_FILE, true); err != nil {
			t.Fatalf("DeleteAt(dir): %s", DescribeErr(err))
		}
		Report(t, "P12.deleteat", Yes,
			"handle-relative delete works for files and directories via NtCreateFile(FILE_OPEN|FILE_OPEN_REPARSE_POINT)+FileDispositionInformationEx")
	})

	t.Run("readdir_through_retained_handle", func(t *testing.T) {
		// M16 / the fdPath replacement: storefs_linux.go:41 has no analogue.
		sub := mustMkdir(t, filepath.Join(dir, "listme"))
		mustWrite(t, filepath.Join(sub, "a.html"), "a")
		mustWrite(t, filepath.Join(sub, "b.txt"), "b")
		mustMkdir(t, filepath.Join(sub, "c"))

		h, err := OpenDirAt(r.Handle(), "listme")
		if err != nil {
			t.Fatalf("OpenDirAt: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(h)

		first, err := ReadDirHandle(h)
		if err != nil {
			Report(t, "M16", No, "ReadDir on a duplicated directory handle failed: %s", DescribeErr(err))
			t.Fatalf("ReadDirHandle: %v", err)
		}
		second, err2 := ReadDirHandle(h)
		names := func(es []os.DirEntry) []string {
			out := make([]string, 0, len(es))
			for _, e := range es {
				out = append(out, e.Name())
			}
			return out
		}
		Report(t, "M16", Yes,
			"os.NewFile(dup(dirhandle)).ReadDir works and RESTARTS per duplicate: first=%v (n=%d) second=%v (n=%d, err=%v). This is the fdPath (/proc/self/fd) replacement.",
			names(first), len(first), names(second), len(second), err2)
		RequireProperty(t, "M16", len(first) == 3 && len(second) == 3,
			"a retained directory handle must be enumerable more than once without re-resolving a path (got %d then %d)", len(first), len(second))

		hasHTML, err := DirHasHTML(h)
		Report(t, "P12.dirHasHTML", boolVerdict(hasHTML && err == nil),
			"dirHasHTMLFD analogue through the handle: %v (err %v)", hasHTML, err)
	})

	t.Run("nested_walk_by_handle", func(t *testing.T) {
		// storefs_linux.go:59 — openRealDir, one segment per handle.
		mustMkdir(t, filepath.Join(dir, "p1", "p2", "p3"))
		h, err := r.OpenRealDir([]string{"p1", "p2", "p3"}, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(h)
		name, nerr := NameInfo(h)
		Report(t, "P12.openrealdir", Yes, "walked p1/p2/p3 handle-relative; FILE_NAME_INFO=%q (err %v)", name, nerr)
	})

	t.Run("reject_artifact_ancestor", func(t *testing.T) {
		// storefs_linux.go:81 — rejectArtifacts.
		art := mustMkdir(t, filepath.Join(dir, "artdir"))
		mustWrite(t, filepath.Join(art, "index.html"), "x")
		mustMkdir(t, filepath.Join(art, "child"))
		_, err := r.OpenRealDir([]string{"artdir", "child"}, false, true)
		Report(t, "P12.reject_artifact", boolVerdict(err != nil),
			"publishing under an artifact ancestor is refused: %v", err)
	})
}

// TestP12AncestorRenameCannotRedirect is the Windows twin of
// TestPinnedMutationsIgnoreProjectSwap (store_test.go:403-432) and of
// measurement M7. It is the single property that justifies the whole
// handle-anchored design.
func TestP12AncestorRenameCannotRedirect(t *testing.T) {
	r, dir := openScratchRoot(t)
	mustMkdir(t, filepath.Join(dir, "project"))

	// Pin the project directory the way Publish does, BEFORE the attack.
	parent, err := r.OpenRealDir([]string{"project"}, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	// A2 renames the validated ancestor away and drops a decoy in its place.
	renameErr := os.Rename(filepath.Join(dir, "project"), filepath.Join(dir, "moved"))
	if renameErr != nil {
		Report(t, "M7", No,
			"Windows REFUSED to rename a directory while a handle to it is open (FILE_SHARE_DELETE was granted by the opener): %s. "+
				"The A2 ancestor-substitution race is therefore harder to stage on Windows than on Linux, but this must NOT be relied on: "+
				"the opener's share mode is the only thing preventing it, and any handle the store does not own can be opened differently.",
			DescribeErr(renameErr))
		return
	}
	Report(t, "M7", Yes, "a directory with an open handle CAN be renamed on Windows (handle granted FILE_SHARE_DELETE); the substitution race is live")

	if err := os.Mkdir(filepath.Join(dir, "project"), 0o755); err != nil {
		t.Fatalf("decoy mkdir: %v", err)
	}

	// The mutation is issued relative to the handle pinned before the rename.
	if err := MkdirAt(parent, "artifact"); err != nil {
		t.Fatalf("MkdirAt through pinned parent: %s", DescribeErr(err))
	}

	_, inMoved := os.Stat(filepath.Join(dir, "moved", "artifact"))
	_, inDecoy := os.Stat(filepath.Join(dir, "project", "artifact"))
	landedInOriginal := inMoved == nil
	landedInDecoy := inDecoy == nil

	Report(t, "M7.redirect", boolVerdict(landedInOriginal && !landedInDecoy),
		"after renaming the pinned ancestor and substituting a decoy, the create landed in the ORIGINAL object=%v, in the decoy=%v",
		landedInOriginal, landedInDecoy)
	RequireProperty(t, "M7.redirect", landedInOriginal && !landedInDecoy,
		"a handle-relative mutation must follow the pinned object through a rename and must never reach a same-named replacement (original=%v decoy=%v)",
		landedInOriginal, landedInDecoy)

	// The handle keeps working and reports the NEW path — proof the handle
	// references the object, not the name.
	if name, err := NameInfo(parent); err == nil {
		Report(t, "M7.namefollows", Info, "FILE_NAME_INFO on the retained handle after the rename: %q", name)
	}
	if final, err := FinalPath(parent, volumeNameDOS); err == nil {
		Report(t, "M6.finalpath_after_rename", Info, "GetFinalPathNameByHandleW after the rename: %q", final)
	}
}

// TestP12ReparseRefusedOnTraversal proves OBJ_DONT_REPARSE gives the walk its
// O_NOFOLLOW, for every link flavour the attacker can plant.
func TestP12ReparseRefusedOnTraversal(t *testing.T) {
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	mustWrite(t, filepath.Join(external, "secret.txt"), "SECRET")
	mustMkdir(t, filepath.Join(external, "deep"))

	// Junction: unprivileged, and the highest-value attacker primitive.
	if err := CreateJunctionAt(r.Handle(), "viajunction", external); err != nil {
		Report(t, "P12.junction_create", No, "CreateJunctionAt: %s", DescribeErr(err))
	} else {
		Report(t, "P12.junction_create", Yes, "created a MOUNT_POINT junction unprivileged, handle-relative")
		_, err := r.OpenRealDir([]string{"viajunction"}, false, false)
		RequireProperty(t, "P12.junction_traverse", err != nil,
			"a junction must never be traversable by the project walk (got err=%v)", err)
		Report(t, "P12.junction_traverse", boolVerdict(err != nil), "OpenRealDir through a junction: %v", err)

		// And the deeper form: junction as an INTERMEDIATE component.
		_, err = r.OpenRealDir([]string{"viajunction", "deep"}, false, false)
		RequireProperty(t, "P12.junction_intermediate", err != nil,
			"a junction must never be traversable as an intermediate component (got err=%v)", err)
	}

	if ok, _ := symlinkCapability(t); ok {
		if err := CreateDirSymlink(filepath.Join(dir, "viasymlink"), external, true); err != nil {
			Report(t, "P12.symlink_create", No, "CreateDirSymlink: %s", DescribeErr(err))
		} else {
			_, err := r.OpenRealDir([]string{"viasymlink"}, false, false)
			RequireProperty(t, "P12.symlink_traverse", err != nil,
				"a directory symlink must never be traversable by the project walk (got err=%v)", err)
			Report(t, "P12.symlink_traverse", boolVerdict(err != nil), "OpenRealDir through a directory symlink: %v", err)
		}
	} else {
		Report(t, "P12.symlink_traverse", NotMeasured, "no symlink capability on this runner")
	}
}

// TestP12BrowsableOneBoundary ports openBrowsableDir's single-boundary rule:
// exactly one store-owned link may be crossed, and only for an allowlisted tag.
func TestP12BrowsableOneBoundary(t *testing.T) {
	ok, _ := symlinkCapability(t)
	if !ok {
		Report(t, "P12.browsable", NotMeasured, "no symlink capability on this runner")
		t.Skip("SKIP(symlink-capability)")
	}
	r, dir := openScratchRoot(t)
	external := scratchDir(t)
	inner := mustMkdir(t, filepath.Join(external, "inner"))
	mustWrite(t, filepath.Join(inner, "page.html"), "x")

	// A second, attacker-planted link INSIDE the watched source.
	nested := scratchDir(t)
	mustWrite(t, filepath.Join(nested, "loot.txt"), "LOOT")
	nestedLinkOK := CreateDirSymlink(filepath.Join(external, "nested"), nested, true) == nil

	if err := CreateDirSymlink(filepath.Join(dir, "watch"), external, true); err != nil {
		t.Fatalf("watch link: %s", DescribeErr(err))
	}

	h, err := r.OpenBrowsableDir([]string{"watch", "inner"})
	if err != nil {
		t.Fatalf("OpenBrowsableDir across the approved boundary: %s", DescribeErr(err))
	}
	windows.CloseHandle(h)
	Report(t, "P12.browsable_boundary", Yes, "exactly one approved SYMLINK boundary is crossed and the target reopened no-follow")

	if nestedLinkOK {
		_, err = r.OpenBrowsableDir([]string{"watch", "nested"})
		RequireProperty(t, "P12.browsable_nested", err != nil,
			"a second link inside the watched source must be refused (invariant 5); got err=%v", err)
		Report(t, "P12.browsable_nested", boolVerdict(err != nil), "second link inside the watched source: %v", err)
	} else {
		Report(t, "P12.browsable_nested", NotMeasured, "could not plant the nested link")
	}

	// A junction as the watch boundary must be refused by the allowlist.
	if err := CreateJunctionAt(r.Handle(), "watchj", external); err == nil {
		_, err = r.OpenBrowsableDir([]string{"watchj", "inner"})
		Report(t, "P12.browsable_tag_allowlist", boolVerdict(err != nil),
			"a MOUNT_POINT boundary is refused when the allowlist holds only SYMLINK: %v", err)
		// ...and accepted when the ADR chooses to allow junctions.
		h, err2 := r.OpenBrowsableDir([]string{"watchj", "inner"}, ioReparseTagSymlink, ioReparseTagMountPoint)
		if err2 == nil {
			windows.CloseHandle(h)
		}
		Report(t, "P12.browsable_tag_allowlist_junction", boolVerdict(err2 == nil),
			"the same boundary is crossed when MOUNT_POINT is added to the allowlist: err=%v (so the junction fallback is a one-line policy change, not a mechanism change)", err2)
	}
}
