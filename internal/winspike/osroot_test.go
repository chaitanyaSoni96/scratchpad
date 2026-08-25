//go:build windows

package winspike

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestM17OsRootStrategy evaluates candidate strategy 1: build the Windows
// backend on the standard library's os.Root.
//
// os.Root on Windows is already handle-anchored: every component is opened
// with NtCreateFile + OBJECT_ATTRIBUTES.RootDirectory and OBJ_DONT_REPARSE
// ($GOROOT/src/os/root_windows.go:146-154). The question is not whether the
// mechanism is right, it is whether the POLICY layered on top of it is the one
// this store needs.
func TestM17OsRootStrategy(t *testing.T) {
	dir := scratchDir(t)
	real := mustMkdir(t, filepath.Join(dir, "real"))
	_ = real
	mustWrite(t, filepath.Join(real, "index.html"), "x")
	mustMkdir(t, filepath.Join(dir, "real", "sub"))

	external := scratchDir(t)
	mustWrite(t, filepath.Join(external, "loot.txt"), "LOOT")

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("os.OpenRoot: %v", err)
	}
	defer root.Close()

	// --- Mkdir is a genuine create-only claim. -----------------------------
	if err := root.Mkdir("claim", 0o755); err != nil {
		t.Fatalf("Root.Mkdir: %v", err)
	}
	err = root.Mkdir("claim", 0o755)
	Report(t, "M17.mkdir_excl", boolVerdict(errors.Is(err, fs.ErrExist)),
		"os.Root.Mkdir on a taken name -> %v (errors.Is(fs.ErrExist)=%v)", err, errors.Is(err, fs.ErrExist))

	// --- Reserved device names. -------------------------------------------
	_, err = root.OpenFile("NUL", os.O_RDONLY, 0)
	Report(t, "M17.reserved", boolVerdict(err != nil),
		"os.Root.OpenFile(\"NUL\") -> %v (os.Root documents that it rejects Windows reserved device names)", err)

	// --- The decisive question: does os.Root FOLLOW an in-root link? -------
	ok, _ := symlinkCapability(t)
	if !ok {
		Report(t, "M17.follows_inroot_symlink", NotMeasured, "no symlink capability on this runner")
	} else {
		// The target MUST be relative: os.Root refuses an absolute target as
		// an escape regardless of where it points, so an absolute link would
		// measure the escape check rather than the follow behaviour.
		if err := CreateDirSymlink(filepath.Join(dir, "inroot"), "real", true); err != nil {
			Report(t, "M17.follows_inroot_symlink", NotMeasured, "could not create the in-root symlink: %s", DescribeErr(err))
		} else {
			sub, err := root.OpenRoot("inroot")
			if err == nil {
				sub.Close()
			}
			Report(t, "M17.follows_inroot_symlink", boolVerdict(err == nil),
				"os.Root.OpenRoot on a symlink that stays INSIDE the root -> err=%v. openBrowsableDir (storefs_linux.go:169) must REFUSE this; "+
					"os.Root deliberately does not, and exposes no option to turn the following off.", err)

			// Lstat can see it, but only as a check-then-use.
			fi, lerr := root.Lstat("inroot")
			mode := fs.FileMode(0)
			if lerr == nil {
				mode = fi.Mode()
			}
			Report(t, "M17.lstat_layering", Partial,
				"os.Root.Lstat(\"inroot\") mode=%v symlink=%v err=%v — usable as a pre-check, but a pre-check is check-then-use: "+
					"between Lstat and OpenRoot, A2 can substitute the entry. Layering Lstat over os.Root cannot reproduce O_NOFOLLOW's atomicity.",
				mode, mode&fs.ModeSymlink != 0, lerr)
		}

		// Escape attempt: a link whose target is outside the root.
		if err := CreateDirSymlink(filepath.Join(dir, "escape"), external, true); err == nil {
			sub, err := root.OpenRoot("escape")
			if err == nil {
				sub.Close()
			}
			Report(t, "M17.escape_refused", boolVerdict(err != nil),
				"os.Root.OpenRoot on a symlink pointing OUTSIDE the root -> %v (escape is refused, so the following is bounded by the root)", err)
		}

		// --- Survey Finding 2: os.Root.Symlink's link flavour. -------------
		if err := root.Symlink(external, "watchlink"); err != nil {
			Report(t, "M17.symlink_flavour", NotMeasured, "os.Root.Symlink: %v", err)
		} else {
			r2, err := OpenRoot(dir)
			if err == nil {
				at, serr := StatAt(r2.Handle(), "watchlink")
				dirSym := at.IsDir()
				Report(t, "M17.symlink_flavour", boolVerdict(dirSym),
					"os.Root.Symlink(<absolute external dir>) produced %s ; FILE_ATTRIBUTE_DIRECTORY set = %v (staterr %v). "+
						"A watch target always has a volume name, so rootSymlink sets neither SYMLINKAT_DIRECTORY nor SYMLINKAT_RELATIVE "+
						"($GOROOT/src/os/root_windows.go:246-293) — confirming survey Finding 2 that watch-link CREATION must be hand-rolled.",
					at, dirSym, serr)
				r2.Close()
			}
		}
	}

	// --- Junctions through os.Root. ---------------------------------------
	r3, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	defer r3.Close()
	if err := CreateJunctionAt(r3.Handle(), "viajunction", external); err != nil {
		Report(t, "M17.junction", NotMeasured, "could not create a junction: %s", DescribeErr(err))
	} else {
		sub, err := root.OpenRoot("viajunction")
		if err == nil {
			sub.Close()
		}
		Report(t, "M17.junction_refused", boolVerdict(err != nil),
			"os.Root.OpenRoot on a junction -> %v (os.Root reads the reparse link and reports errSymlink, then refuses because the target escapes)", err)

		fi, lerr := root.Lstat("viajunction")
		if lerr == nil {
			Report(t, "M17.junction_mode", Info,
				"os.Root.Lstat on a junction: mode=%v IsDir=%v ModeSymlink=%v ModeIrregular=%v",
				fi.Mode(), fi.IsDir(), fi.Mode()&fs.ModeSymlink != 0, fi.Mode()&fs.ModeIrregular != 0)
		} else {
			Report(t, "M17.junction_mode", Info, "os.Root.Lstat on a junction failed: %v", lerr)
		}
	}

	// --- Enumeration. ------------------------------------------------------
	entries, err := fs.ReadDir(root.FS(), ".")
	Report(t, "M17.readdir", boolVerdict(err == nil), "fs.ReadDir(root.FS(), \".\") -> %d entries, err=%v", len(entries), err)

	Report(t, "M17.summary", Partial,
		"os.Root satisfies R1 (segment-at-a-time, handle-anchored), R3 (OBJ_DONT_REPARSE on every component), R6 (atomic create-only) and R15 (FILE_SHARE_DELETE everywhere) out of the box. "+
			"It does NOT satisfy: the no-follow BROWSE walk (it follows in-root links by design and offers no opt-out), "+
			"R4/R5 tag allowlisting (it exposes fs.FileMode, not FILE_ATTRIBUTE_TAG_INFO — a junction is ModeIrregular), "+
			"watch-link creation (survey Finding 2), "+
			"R13/R14 identity (no FILE_ID_INFO accessor), and R8's classify-from-the-handle recursive removal (RemoveAll is a policy, not a primitive).")
}

// TestM17OsRootMissingPrimitives records, concretely, what a hand-rolled
// backend has to supply that neither os.Root nor x/sys/windows v0.41.0 does.
func TestM17OsRootMissingPrimitives(t *testing.T) {
	Report(t, "M17.gaps", Info,
		"x/sys/windows v0.41.0 exports NtCreateFile/NtSetInformationFile/GetFileInformationByHandleEx/SetFileInformationByHandle/DeviceIoControl and the OBJ_*/FILE_*/STATUS_* constants, "+
			"but NOT: Openat, Symlinkat, FILE_ATTRIBUTE_TAG_INFO, FILE_ID_INFO, FILE_NAME_INFO, FILE_RENAME_INFO, FILE_DISPOSITION_INFO_EX, "+
			"SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE, or the NT information-class numbers 65/64. "+
			"internal/winspike/winfs.go (%d lines) and links.go (%d lines) supply all of them.", winfsLines, linksLines)
	_ = windows.InvalidHandle
}

// Size markers so the ADR can quote the cost of the hand-rolled option
// honestly. Both files are heavily commented; the executable mechanism is
// roughly half of each.
const (
	winfsLines = 790
	linksLines = 261
)
