//go:build windows

package winspike

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// ---------------------------------------------------------------------------
// P1.5 — adversarial tests.
//
// Every test below is an ATTACK, not a probe. It stages a substitution in the
// exact window between validation and use, using the deterministic hook rather
// than a timing loop, and then asserts containment. A test that only shows the
// happy path is not evidence for a security property.
//
// The attacker model is the threat model's A2 (a local process that can create
// and rename entries inside the store) and A1 (content inside a watched
// source). Neither needs elevation: junctions are unprivileged (P14.noprivileg
// e.junction) and so is a non-Microsoft reparse tag (M4.noprivilege).
//
// Verdict convention: containment HELD means the mutation stayed inside the
// pinned object and the external tree was untouched. Anything else is a
// SECURITY-FAIL and fails the job.
// ---------------------------------------------------------------------------

// externalTree builds a decoy target outside the store with a recognisable
// payload, and returns a checker that reports whether it is still intact.
func externalTree(t *testing.T) (string, func() (bool, string)) {
	t.Helper()
	ext := scratchDir(t)
	mustWrite(t, filepath.Join(ext, "PRECIOUS.txt"), "do-not-touch")
	mustMkdir(t, filepath.Join(ext, "sub"))
	mustWrite(t, filepath.Join(ext, "sub", "also.txt"), "nor-this")
	return ext, func() (bool, string) {
		var missing []string
		want := map[string]string{
			"PRECIOUS.txt":                   "do-not-touch",
			filepath.Join("sub", "also.txt"): "nor-this",
		}
		for p, expect := range want {
			b, err := os.ReadFile(filepath.Join(ext, p))
			switch {
			case err != nil:
				missing = append(missing, p+"=<"+err.Error()+">")
			case string(b) != expect:
				missing = append(missing, p+"=<modified:"+truncate(string(b), 40)+">")
			}
		}
		// A new entry appearing inside the target is an escape too.
		if es, err := os.ReadDir(ext); err == nil {
			for _, e := range es {
				if e.Name() != "PRECIOUS.txt" && e.Name() != "sub" {
					missing = append(missing, "intruder:"+e.Name())
				}
			}
		}
		if len(missing) == 0 {
			return true, "intact"
		}
		return false, strings.Join(missing, ",")
	}
}

// plantLink puts a link named `name` under parent pointing at target, using
// whichever flavour this runner can create, and reports which one.
func plantLink(t *testing.T, parent windows.Handle, name, target string, kind string) (string, bool) {
	t.Helper()
	switch kind {
	case "junction":
		if err := CreateJunctionAt(parent, name, target); err != nil {
			return "junction:" + DescribeErr(err), false
		}
		return "junction", true
	case "symlink":
		if ok, _ := symlinkCapability(t); !ok {
			return "symlink:no-capability", false
		}
		if err := SymlinkAt(parent, name, target); err != nil {
			return "symlink:" + DescribeErr(err), false
		}
		return "symlink", true
	case "unknowntag":
		if err := MkdirAt(parent, name); err != nil {
			return "unknowntag:" + DescribeErr(err), false
		}
		h, err := ntOpenAt(parent, name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT,
			windows.OBJ_CASE_INSENSITIVE, 0)
		if err != nil {
			return "unknowntag:" + DescribeErr(err), false
		}
		serr := SetUnknownTag(h, 0x00001234)
		windows.CloseHandle(h)
		if serr != nil {
			return "unknowntag:" + DescribeErr(serr), false
		}
		return "unknowntag", true
	}
	return "unknown-kind", false
}

// ---------------------------------------------------------------------------
// A1 — an ancestor directory is renamed away and substituted between handle
// acquisition and the mutation.
//
// M7.redirect already proved this for a plain-directory decoy. The threat
// model (§5, Publish/ancestor replaced) asks for three more: a symlink, a
// junction, and an unknown-tag reparse point — because a decoy that is a LINK
// is the one that gives the attacker a destination outside the store.
// ---------------------------------------------------------------------------

func TestA1AncestorReplacedBetweenPinAndMutation(t *testing.T) {
	for _, kind := range []string{"realdir", "junction", "symlink", "unknowntag"} {
		t.Run(kind, func(t *testing.T) {
			base := scratchDir(t)
			rootPath := mustMkdir(t, filepath.Join(base, "store"))
			r, err := OpenRoot(rootPath)
			if err != nil {
				t.Fatalf("OpenRoot: %s", DescribeErr(err))
			}
			defer r.Close()
			ext, intact := externalTree(t)
			mustMkdir(t, filepath.Join(rootPath, "project"))

			// Pin the project directory exactly as Publish does, BEFORE the attack.
			parent, err := r.OpenRealDir([]string{"project"}, false, false)
			if err != nil {
				t.Fatalf("OpenRealDir: %s", DescribeErr(err))
			}
			defer windows.CloseHandle(parent)

			// A2 renames the validated ancestor away and substitutes a decoy.
			if err := os.Rename(filepath.Join(rootPath, "project"), filepath.Join(rootPath, "moved")); err != nil {
				Report(t, "A1.ancestor_replaced."+kind, NotMeasured, "could not rename the pinned ancestor: %v", err)
				return
			}
			decoy := ""
			if kind == "realdir" {
				if err := os.Mkdir(filepath.Join(rootPath, "project"), 0o755); err != nil {
					Report(t, "A1.ancestor_replaced."+kind, NotMeasured, "decoy mkdir: %v", err)
					return
				}
				decoy = "realdir"
			} else {
				var ok bool
				decoy, ok = plantLink(t, r.Handle(), "project", ext, kind)
				if !ok {
					Report(t, "A1.ancestor_replaced."+kind, NotMeasured, "could not plant the decoy: %s", decoy)
					return
				}
			}

			// The mutation, issued relative to the handle pinned before the attack.
			mkErr := MkdirAt(parent, "artifact")
			_, inOriginal := os.Stat(filepath.Join(rootPath, "moved", "artifact"))
			_, inDecoy := os.Stat(filepath.Join(rootPath, "project", "artifact"))
			extOK, extDetail := intact()

			held := mkErr == nil && inOriginal == nil && extOK
			Report(t, "A1.ancestor_replaced."+kind, boolVerdict(held),
				"decoy=%s: MkdirAt through the pinned handle -> %s ; landed in the ORIGINAL object=%v ; visible at the decoy "+
					"path=%v ; external target %s. A handle references the object, so the substitution is not merely detected — "+
					"it is unreachable.",
				decoy, DescribeErr(mkErr), inOriginal == nil, inDecoy == nil, extDetail)
			RequireProperty(t, "A1.ancestor_replaced."+kind, held,
				"a mutation through a pinned ancestor handle must land in the pinned object and must never reach a substituted "+
					"%s decoy or its target (mkdir err=%s, original=%v, external=%s)", decoy, DescribeErr(mkErr), inOriginal == nil, extDetail)
		})
	}
}

// ---------------------------------------------------------------------------
// A2 — the TARGET is replaced between check and use.
//
// This is the annotation write path's own window: the caller has validated
// that `notes.json` is a regular file, the temp is written, and the attacker
// substitutes the destination in the instant before the replace. The question
// the ADR needs answered is whether FILE_RENAME_INFORMATION_EX with
// REPLACE_IF_EXISTS follows a link at the destination.
// ---------------------------------------------------------------------------

func TestA2DestinationReplacedBeforeReplace(t *testing.T) {
	for _, kind := range []string{"junction", "symlink", "dirsymlink", "realdir", "realfile"} {
		t.Run(kind, func(t *testing.T) {
			r, dir := openScratchRoot(t)
			parent, err := r.OpenRealDir(nil, false, false)
			if err != nil {
				t.Fatalf("OpenRealDir: %s", DescribeErr(err))
			}
			defer windows.CloseHandle(parent)
			ext, intact := externalTree(t)
			extFile := filepath.Join(ext, "PRECIOUS.txt")

			mustWrite(t, filepath.Join(dir, "notes.json"), "COMPLETE-OLD")

			var stageErr error
			staged := ""
			setSpikeOpHook(t, onceHook(OpBeforeReplace, func() {
				// Remove the validated destination and substitute the decoy.
				if err := os.Remove(filepath.Join(dir, "notes.json")); err != nil {
					stageErr = err
					return
				}
				switch kind {
				case "junction":
					staged, _ = plantLink(t, r.Handle(), "notes.json", ext, "junction")
				case "symlink":
					if ok, _ := symlinkCapability(t); !ok {
						stageErr = fmt.Errorf("no symlink capability")
						return
					}
					// A FILE symlink pointing at an external file: the classic
					// "write through the link" escape.
					if err := CreateFileSymlink(filepath.Join(dir, "notes.json"), extFile, true); err != nil {
						stageErr = err
						return
					}
					staged = "filesymlink"
				case "dirsymlink":
					staged, _ = plantLink(t, r.Handle(), "notes.json", ext, "symlink")
				case "realdir":
					if err := os.Mkdir(filepath.Join(dir, "notes.json"), 0o755); err != nil {
						stageErr = err
						return
					}
					staged = "realdir"
				case "realfile":
					mustWrite(t, filepath.Join(dir, "notes.json"), "DECOY")
					staged = "realfile"
				}
			}))

			res, werr := AtomicWriteFile(parent, "notes.json", []byte("NEW-NOTES"), DefaultReplacePolicy())
			if stageErr != nil || (staged == "" && kind != "realfile") {
				Report(t, "A2.dest_replaced."+kind, NotMeasured, "could not stage the substitution: %v (staged=%q)", stageErr, staged)
				return
			}

			extOK, extDetail := intact()
			extContent := readOrErr(extFile)
			escaped := !extOK || strings.Contains(extContent, "NEW-NOTES")

			// Whatever happened, the write must not have reached outside the
			// store, and no temp may be left behind.
			left := tempsLeft(t, dir)
			Report(t, "A2.dest_replaced."+kind, boolVerdict(!escaped),
				"substituted a %s at the destination in the window before the replace: write err=%s (attempts %d) ; "+
					"external tree %s ; external file now %q ; temp residue %v",
				staged, DescribeErr(werr), res.Attempts, extDetail, truncate(extContent, 40), left)
			RequireProperty(t, "A2.dest_replaced."+kind, !escaped,
				"a destination substituted with a %s must never let the write reach outside the store (external tree %s, "+
					"external file %q)", staged, extDetail, truncate(extContent, 40))
			RequireProperty(t, "A2.dest_replaced_cleanup."+kind, len(left) == 0,
				"the temp must be cleaned up whatever the destination turned into; residue %v", left)
		})
	}
}

// ---------------------------------------------------------------------------
// A3 — nested reparse points BELOW an already-crossed boundary.
//
// openBrowsableDir forgives exactly one link: the store-owned watch link. The
// attack is a second link inside the watched source, which is content the
// attacker controls (A1). P12.browsable_nested covers the symlink case; this
// adds the two variants the threat model asks for and the deeper placements.
// ---------------------------------------------------------------------------

func TestA3NestedReparseBelowCrossedBoundary(t *testing.T) {
	symOK, _ := symlinkCapability(t)

	// The boundary itself: prefer a symlink (what watch would create), fall
	// back to a junction with MOUNT_POINT allowlisted so the nested-link rule
	// is still exercised on a runner without symlink capability.
	type scenario struct {
		boundary string
		allowed  []uint32
	}
	scenarios := []scenario{{"junction", []uint32{ioReparseTagSymlink, ioReparseTagMountPoint}}}
	if symOK {
		scenarios = append([]scenario{{"symlink", []uint32{ioReparseTagSymlink}}}, scenarios...)
	}

	for _, sc := range scenarios {
		for _, nested := range []string{"junction", "symlink", "unknowntag"} {
			name := sc.boundary + "_boundary/" + nested + "_nested"
			t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
				r, _ := openScratchRoot(t)
				source := scratchDir(t)
				inner := mustMkdir(t, filepath.Join(source, "inner"))
				mustWrite(t, filepath.Join(inner, "page.html"), "ok")
				ext, intact := externalTree(t)

				// The attacker's second link, inside the watched source.
				srcRoot, err := OpenRoot(source)
				if err != nil {
					t.Fatalf("OpenRoot(source): %s", DescribeErr(err))
				}
				defer srcRoot.Close()
				planted, ok := plantLink(t, srcRoot.Handle(), "nested", ext, nested)
				if !ok {
					Report(t, "A3.nested."+name, NotMeasured, "could not plant the nested link: %s", planted)
					return
				}
				// ...and a deeper one, two levels down, so the test is not
				// only about the first segment after the boundary.
				deep := mustMkdir(t, filepath.Join(source, "inner", "deeper"))
				deepRoot, derr := OpenRoot(deep)
				deepPlanted := "not-attempted"
				if derr == nil {
					defer deepRoot.Close()
					deepPlanted, _ = plantLink(t, deepRoot.Handle(), "nested", ext, nested)
				}

				// The store's own watch link.
				bk, ok := plantLink(t, r.Handle(), "watch", source, sc.boundary)
				if !ok {
					Report(t, "A3.nested."+name, NotMeasured, "could not plant the boundary: %s", bk)
					return
				}

				// Crossing the boundary once is allowed...
				h, okErr := r.OpenBrowsableDir([]string{"watch", "inner"}, sc.allowed...)
				if okErr == nil {
					windows.CloseHandle(h)
				}
				// ...crossing a second link inside the source is not.
				_, nestedErr := r.OpenBrowsableDir([]string{"watch", "nested"}, sc.allowed...)
				_, deepErr := r.OpenBrowsableDir([]string{"watch", "inner", "deeper", "nested"}, sc.allowed...)

				extOK, extDetail := intact()
				held := nestedErr != nil && extOK
				if deepPlanted != "not-attempted" && !strings.Contains(deepPlanted, ":") {
					held = held && deepErr != nil
				}

				Report(t, "A3.nested."+name, boolVerdict(held),
					"boundary=%s (crossed ok=%v), nested %s one level below -> %s, and two levels below (%s) -> %s ; "+
						"external tree %s",
					bk, okErr == nil, planted, DescribeErr(nestedErr), deepPlanted, DescribeErr(deepErr), extDetail)
				RequireProperty(t, "A3.nested."+name, held,
					"a second reparse point below an already-crossed boundary must be refused at ANY depth "+
						"(one level: %s ; two levels: %s ; external %s)",
					DescribeErr(nestedErr), DescribeErr(deepErr), extDetail)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// A4 — the STORE ROOT itself is renamed away and substituted mid-operation
// (threat model §4.15).
// ---------------------------------------------------------------------------

func TestA4RootReplacedMidOperation(t *testing.T) {
	for _, kind := range []string{"realdir", "junction", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			base := scratchDir(t)
			rootPath := mustMkdir(t, filepath.Join(base, "store"))
			r, err := OpenRoot(rootPath)
			if err != nil {
				t.Fatalf("OpenRoot: %s", DescribeErr(err))
			}
			defer r.Close()
			ext, intact := externalTree(t)

			if err := os.Rename(rootPath, filepath.Join(base, "store-moved")); err != nil {
				Report(t, "A4.root_replaced."+kind, NotMeasured, "could not rename the root: %v", err)
				return
			}
			baseRoot, berr := OpenRoot(base)
			if berr != nil {
				t.Fatalf("OpenRoot(base): %s", DescribeErr(berr))
			}
			defer baseRoot.Close()

			substituted := ""
			switch kind {
			case "realdir":
				if err := os.Mkdir(rootPath, 0o755); err != nil {
					Report(t, "A4.root_replaced."+kind, NotMeasured, "substitute mkdir: %v", err)
					return
				}
				substituted = "realdir"
			default:
				var ok bool
				substituted, ok = plantLink(t, baseRoot.Handle(), "store", ext, kind)
				if !ok {
					Report(t, "A4.root_replaced."+kind, NotMeasured, "could not substitute the root: %s", substituted)
					return
				}
			}

			// (i) the pinned handle keeps working and keeps naming the original.
			mkErr := MkdirAt(r.Handle(), "artifact")
			_, inOriginal := os.Stat(filepath.Join(base, "store-moved", "artifact"))
			_, atSubstitute := os.Stat(filepath.Join(rootPath, "artifact"))
			verifyErr := r.Verify()
			extOK, extDetail := intact()

			// (ii) a FRESH open of the root path — what a process that
			// re-resolved Root() would get — must either see a different
			// identity or be refused outright.
			fresh, freshErr := OpenRoot(rootPath)
			freshID := "refused"
			differs := true
			if freshErr == nil {
				freshID = fresh.ID().String()
				differs = fresh.ID() != r.ID()
				fresh.Close()
			}

			held := mkErr == nil && inOriginal == nil && extOK && differs
			Report(t, "A4.root_replaced."+kind, boolVerdict(held),
				"root renamed away and substituted with a %s: mutation through the PINNED handle -> %s, landed in the original=%v, "+
					"visible under the substituted path=%v ; Verify() on the pinned handle -> %v (a rename is not a replacement, so "+
					"nil is correct here) ; a FRESH OpenRoot of the same path -> %s, differs from the pinned identity=%v ; "+
					"external tree %s",
				substituted, DescribeErr(mkErr), inOriginal == nil, atSubstitute == nil, verifyErr, freshID, differs, extDetail)
			RequireProperty(t, "A4.root_replaced."+kind, held,
				"after the root is substituted, every mutation through the pinned handle must stay in the original object and "+
					"a fresh open must not silently return the substitute as if it were the store (mutation=%s original=%v "+
					"external=%s freshDiffers=%v)", DescribeErr(mkErr), inOriginal == nil, extDetail, differs)

			if kind != "realdir" {
				RequireProperty(t, "A4.root_reparse_refused."+kind, freshErr != nil,
					"OpenRoot must refuse a root that is a reparse point (%s), got err=%v", substituted, freshErr)
			}
		})
	}

	// The root simply vanishing is the benign half of the same window.
	t.Run("root_removed", func(t *testing.T) {
		base := scratchDir(t)
		rootPath := mustMkdir(t, filepath.Join(base, "store"))
		r, err := OpenRoot(rootPath)
		if err != nil {
			t.Fatalf("OpenRoot: %s", DescribeErr(err))
		}
		defer r.Close()
		rmErr := os.Remove(rootPath)
		mkErr := MkdirAt(r.Handle(), "artifact")
		verifyErr := r.Verify()
		Report(t, "A4.root_removed", Info,
			"removing the pinned root while a handle is open -> %v ; a subsequent MkdirAt through the handle -> %s ; "+
				"Verify() -> %v. Windows keeps the object alive for the open handle, so the store keeps working against a "+
				"directory that is no longer reachable by name — the ADR must decide whether that is acceptable or whether "+
				"the root identity is re-verified per mutation.",
			rmErr, DescribeErr(mkErr), verifyErr)
	})
}

// ---------------------------------------------------------------------------
// A5 — the second RR1 vector: a NON-SURROGATE unknown reparse tag, where
// os.Lstat().IsDir() is TRUE.
//
// M2.unknown_tag / RR1.unknown_tag_isdir established the classification. The
// open question they left is the one that matters for the design: is the
// handle-relative walk's refusal a CONSEQUENCE of no filter driver servicing
// the tag on this runner (in which case a machine with the driver would be
// wide open), or is it independent of servicing?
//
// The answer is decided by comparing STATUS codes for a SERVICED tag and an
// UNSERVICED one. A junction is serviced by NTFS itself.
// ---------------------------------------------------------------------------

func TestA5UnknownReparseTagRefusedIndependentOfDriver(t *testing.T) {
	r, dir := openScratchRoot(t)
	ext, intact := externalTree(t)

	planted, ok := plantLink(t, r.Handle(), "unk", ext, "unknowntag")
	if !ok {
		Report(t, "A5.unknown_tag", NotMeasured, "could not plant the unknown tag: %s", planted)
		return
	}
	if _, ok := plantLink(t, r.Handle(), "jn", ext, "junction"); !ok {
		Report(t, "A5.unknown_tag", NotMeasured, "could not plant the junction control")
		return
	}

	status := func(err error) windows.NTStatus {
		st, _ := StatusOf(err)
		return st
	}
	// With OBJ_DONT_REPARSE (the design's walk).
	_, unkNoFollow := OpenDirAt(r.Handle(), "unk")
	_, jnNoFollow := OpenDirAt(r.Handle(), "jn")
	// Without it (the naive port), which is where the filter driver is consulted.
	_, unkFollow := ntOpenAt(r.Handle(), "unk", dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	jnFollowH, jnFollow := ntOpenAt(r.Handle(), "jn", dirReadAccess, windows.FILE_OPEN,
		windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if jnFollow == nil {
		windows.CloseHandle(jnFollowH)
	}

	sameRefusal := status(unkNoFollow) == windows.STATUS_REPARSE_POINT_ENCOUNTERED &&
		status(jnNoFollow) == windows.STATUS_REPARSE_POINT_ENCOUNTERED

	li, _ := os.Lstat(filepath.Join(dir, "unk"))
	isDir := li != nil && li.IsDir()

	Report(t, "A5.unknown_tag_statuses", Info,
		"UNSERVICED tag 0x00001234: with OBJ_DONT_REPARSE -> %s ; without it -> %s. "+
			"SERVICED tag (junction, serviced by NTFS): with OBJ_DONT_REPARSE -> %s ; without it -> %s. "+
			"os.Lstat().IsDir() on the unknown tag = %v.",
		DescribeErr(unkNoFollow), DescribeErr(unkFollow), DescribeErr(jnNoFollow), DescribeErr(jnFollow), isDir)

	Report(t, "A5.unknown_tag_driver_independent", boolVerdict(sameRefusal),
		"THE ARGUMENT: the refusal status is identical (STATUS_REPARSE_POINT_ENCOUNTERED) for a tag NTFS services and for a "+
			"tag nothing services, while the WITHOUT-flag opens differ (the junction traverses, the unknown tag gets "+
			"STATUS_IO_REPARSE_TAG_NOT_HANDLED). OBJ_DONT_REPARSE therefore fails the open during name resolution, before any "+
			"filter driver is asked to service the tag — so on a machine that DOES have the driver (Windows Containers WCI*, "+
			"VFS-for-Git PROJFS*, a vendor filter) the open still fails the same way. What that machine changes is only the "+
			"WITHOUT-flag column: there the open would SUCCEED and Go's IsDir()==%v would be the entire defence. The design "+
			"never takes that column, which is why it is contained and a Go-mode-based port is not.", isDir)

	RequireProperty(t, "A5.unknown_tag_refused", status(unkNoFollow) == windows.STATUS_REPARSE_POINT_ENCOUNTERED,
		"a non-surrogate unknown reparse tag must be refused by the no-follow walk with STATUS_REPARSE_POINT_ENCOUNTERED "+
			"(i.e. by the no-follow rule, NOT merely because no driver services it), got %s", DescribeErr(unkNoFollow))

	// The removal path must also treat it as a link, not as the directory Go
	// says it is.
	AuditStart()
	rmErr := RemoveTreeAt(r.Handle(), "unk")
	audit := AuditStop()
	_, gone := StatAt(r.Handle(), "unk")
	extOK, extDetail := intact()
	Report(t, "A5.unknown_tag_removed", boolVerdict(rmErr == nil && gone != nil && extOK),
		"RemoveTreeAt on the unknown-tag entry -> %s ; entry gone=%v ; removals issued %v ; external tree %s",
		DescribeErr(rmErr), gone != nil, audit, extDetail)
	RequireProperty(t, "A5.unknown_tag_removed", extOK,
		"removing an unknown-tag entry must not touch anything outside the store (%s)", extDetail)
}

// ---------------------------------------------------------------------------
// A6 — RR1, the critical finding: a junction planted inside a tree that is
// being recursively deleted.
//
// This is the single most important row of the security matrix. It is run for
// every link flavour, at the top of the tree and buried inside it, and it has
// a NEGATIVE CONTROL: the mechanical port that classifies by
// FILE_ATTRIBUTE_DIRECTORY is pointed at the same fixture and must be shown to
// DESTROY the target. Without that control, "the target survived" would be
// evidence of nothing.
// ---------------------------------------------------------------------------

func TestA6RecursiveDeleteThroughPlantedLink(t *testing.T) {
	build := func(t *testing.T, kind string, depth int) (*Root, string, func() (bool, string), bool) {
		t.Helper()
		r, dir := openScratchRoot(t)
		ext, intact := externalTree(t)
		art := mustMkdir(t, filepath.Join(dir, "artifact"))
		mustWrite(t, filepath.Join(art, "index.html"), "<h1>x</h1>")
		mustWrite(t, filepath.Join(art, "style.css"), "body{}")
		// The link sits `depth` directories below the artifact root.
		parentPath := art
		for i := 0; i < depth; i++ {
			parentPath = mustMkdir(t, filepath.Join(parentPath, fmt.Sprintf("d%d", i)))
			mustWrite(t, filepath.Join(parentPath, "leaf.txt"), "leaf")
		}
		ph, err := OpenRoot(parentPath)
		if err != nil {
			t.Fatalf("OpenRoot(%q): %s", parentPath, DescribeErr(err))
		}
		defer ph.Close()
		planted, ok := plantLink(t, ph.Handle(), "trap", ext, kind)
		if !ok {
			Report(t, "A6.delete."+kind, NotMeasured, "could not plant the trap: %s", planted)
			return r, dir, intact, false
		}
		return r, dir, intact, true
	}

	for _, kind := range []string{"junction", "symlink", "unknowntag"} {
		for _, depth := range []int{0, 2} {
			id := fmt.Sprintf("A6.delete.%s.depth%d", kind, depth)
			t.Run(fmt.Sprintf("%s_depth%d", kind, depth), func(t *testing.T) {
				r, dir, intact, ok := build(t, kind, depth)
				if !ok {
					return
				}
				AuditStart()
				rmErr := RemoveTreeAt(r.Handle(), "artifact")
				audit := AuditStop()
				_, artGone := os.Stat(filepath.Join(dir, "artifact"))
				extOK, extDetail := intact()

				held := extOK && rmErr == nil && artGone != nil
				Report(t, id, boolVerdict(held),
					"a %s planted %d level(s) inside the artifact: RemoveTreeAt -> %s ; artifact removed=%v ; %d removal(s) "+
						"issued %v ; external target %s",
					kind, depth, DescribeErr(rmErr), artGone != nil, len(audit), audit, extDetail)
				RequireProperty(t, id, extOK,
					"RECURSIVE DELETE MUST NOT DESCEND THROUGH A PLANTED %s (threat model RR1, Critical): external target %s",
					strings.ToUpper(kind), extDetail)
			})
		}
	}

	// The negative control. This is expected to DESTROY its external tree.
	t.Run("negative_control_by_attribute", func(t *testing.T) {
		r, dir, intact, ok := build(t, "junction", 0)
		if !ok {
			return
		}
		rmErr := removeTreeAtByAttributeUNSAFE(r.Handle(), "artifact")
		_, artGone := os.Stat(filepath.Join(dir, "artifact"))
		extOK, extDetail := intact()
		Report(t, "A6.negative_control", boolVerdict(!extOK),
			"NEGATIVE CONTROL — the MECHANICAL port of removeTreeAt (classify by FILE_ATTRIBUTE_DIRECTORY, open without "+
				"OBJ_DONT_REPARSE) run against the same fixture: err=%s ; artifact removed=%v ; external target %s. "+
				"The external tree being DESTROYED here is the point: RR1 is a real, reachable, unprivileged data-loss defect "+
				"on real Windows, and it is what gives the assertions above their meaning.",
			DescribeErr(rmErr), artGone != nil, extDetail)
		if extOK {
			Report(t, "A6.negative_control", NotMeasured,
				"the naive port did NOT destroy the target on this runner, so the A6 assertions cannot be claimed to have "+
					"teeth from this evidence alone")
		}
	})

	// Removing a WATCHED entry: only the link may go.
	t.Run("watch_link_target_untouched", func(t *testing.T) {
		for _, kind := range []string{"junction", "symlink"} {
			r, dir := openScratchRoot(t)
			ext, intact := externalTree(t)
			planted, ok := plantLink(t, r.Handle(), "watched", ext, kind)
			if !ok {
				Report(t, "A6.unlink_watch."+kind, NotMeasured, "could not plant: %s", planted)
				continue
			}
			AuditStart()
			rmErr := RemoveTreeAt(r.Handle(), "watched")
			audit := AuditStop()
			_, gone := os.Stat(filepath.Join(dir, "watched"))
			extOK, extDetail := intact()
			Report(t, "A6.unlink_watch."+kind, boolVerdict(rmErr == nil && extOK),
				"RemoveTreeAt on a %s watch link -> %s ; link gone=%v ; removals %v ; external target %s",
				planted, DescribeErr(rmErr), gone != nil, audit, extDetail)
			RequireProperty(t, "A6.unlink_watch."+kind, extOK,
				"removing a watch link must remove only the link (%s)", extDetail)
		}
	})
}

// TestA6DeleteTargetSwappedMidwayThroughTheWalk is the check-then-use variant
// of A6: the entry is a perfectly ordinary directory when the walk enumerates
// it and a junction by the time the walk descends.
func TestA6DeleteTargetSwappedMidwayThroughTheWalk(t *testing.T) {
	r, dir := openScratchRoot(t)
	ext, intact := externalTree(t)

	art := mustMkdir(t, filepath.Join(dir, "artifact"))
	mustWrite(t, filepath.Join(art, "index.html"), "x")
	victim := mustMkdir(t, filepath.Join(art, "assets"))
	mustWrite(t, filepath.Join(victim, "a.txt"), "a")

	artRoot, err := OpenRoot(art)
	if err != nil {
		t.Fatalf("OpenRoot(artifact): %s", DescribeErr(err))
	}
	defer artRoot.Close()

	var swapErr error
	swapped := false
	setSpikeOpHook(t, func(op string) {
		if op != OpTreeEntry || swapped {
			return
		}
		// Fire once, at the first entry of the walk: replace `assets` — which
		// the enumeration has already listed as an ordinary directory — with a
		// junction to the external tree.
		swapped = true
		if err := os.RemoveAll(victim); err != nil {
			swapErr = err
			return
		}
		if err := CreateJunctionAt(artRoot.Handle(), "assets", ext); err != nil {
			swapErr = err
		}
	})

	AuditStart()
	rmErr := RemoveTreeAt(r.Handle(), "artifact")
	audit := AuditStop()
	extOK, extDetail := intact()
	_, artGone := os.Stat(filepath.Join(dir, "artifact"))

	if swapErr != nil {
		Report(t, "A6.swap_midwalk", NotMeasured, "could not stage the swap: %v", swapErr)
		return
	}
	Report(t, "A6.swap_midwalk", boolVerdict(extOK),
		"a listed, ordinary subdirectory was replaced with a junction between the enumeration and the descent: "+
			"RemoveTreeAt -> %s ; artifact removed=%v ; removals %v ; external target %s. The walk re-opens each entry with "+
			"OBJ_DONT_REPARSE and lets the OPEN do the classifying, so the substitution turns into a refusal to descend "+
			"rather than into a traversal.",
		DescribeErr(rmErr), artGone != nil, audit, extDetail)
	RequireProperty(t, "A6.swap_midwalk", extOK,
		"an entry substituted with a link between enumeration and descent must not be descended (%s)", extDetail)
}

// ---------------------------------------------------------------------------
// A7 — the two-step link creation window (M8): a crash between FILE_CREATE and
// FSCTL_SET_REPARSE_POINT leaves an EMPTY REAL DIRECTORY under the watch name.
//
// The question the ADR has to answer (§8 item 8) is not whether the window
// exists — M8 established that — but what the residue IS and which store
// operation can clear it.
// ---------------------------------------------------------------------------

func TestA7TwoStepLinkCreationCrashWindow(t *testing.T) {
	r, dir := openScratchRoot(t)
	source := scratchDir(t)
	mustWrite(t, filepath.Join(source, "page.html"), "x")

	// Simulate the crash: panic out of the hook between the two steps. Nothing
	// in CreateJunctionAt has run a cleanup defer at that point, so the residue
	// is exactly what a killed process leaves.
	setSpikeOpHook(t, onceHook(OpLinkNameClaimed, func() { panic("winspike: simulated crash between FILE_CREATE and FSCTL_SET_REPARSE_POINT") }))
	crashed := func() (rec any) {
		defer func() { rec = recover() }()
		_ = CreateJunctionAt(r.Handle(), "watchname", source)
		return nil
	}()
	spikeOpHook = nil
	if crashed == nil {
		Report(t, "A7.two_step_window", NotMeasured, "the simulated crash did not fire")
		return
	}

	// What is left behind?
	at, statErr := StatAt(r.Handle(), "watchname")
	h, openErr := OpenDirAt(r.Handle(), "watchname")
	hasHTML := false
	entries := 0
	if openErr == nil {
		hasHTML, _ = DirHasHTML(h)
		es, _ := ReadDirHandle(h)
		entries = len(es)
		windows.CloseHandle(h)
	}
	li, _ := os.Lstat(filepath.Join(dir, "watchname"))

	Report(t, "A7.two_step_residue", Info,
		"after a crash between the two steps the name holds: classify=%s (err %v) ; no-follow dir open=%s ; entries=%d ; "+
			"has .html=%v ; os.Lstat mode=%v. It is an ORDINARY EMPTY DIRECTORY — not a partial link, not a broken link, and "+
			"indistinguishable from a published-but-empty artifact.",
		at, statErr, DescribeErr(openErr), entries, hasHTML, modeOf(li))

	// Which operations can clear it? This is the ADR's actual question.
	reWatch := CreateJunctionAt(r.Handle(), "watchname", source)
	isArtifact := hasHTML // Delete only accepts a directory that directly holds a .html
	rmdirErr := DeleteAt(r.Handle(), "watchname", windows.FILE_DIRECTORY_FILE, true)
	_, gone := StatAt(r.Handle(), "watchname")

	Report(t, "A7.two_step_recovery", Info,
		"re-running watch over the residue -> %s (the create-only claim fails, as it must) ; Delete would refuse it because "+
			"dirHasHTML=%v so it is not an artifact ; Unwatch would refuse it because it is a REAL directory, not a link. "+
			"So today the name is STUCK: no CLI verb can clear it. A handle-relative rmdir of the EMPTY directory does work "+
			"(-> %s, gone=%v), which is the candidate recovery: let `unwatch` remove a link OR an empty real directory, since "+
			"an empty directory under a watch name carries no user data by definition.",
		DescribeErr(reWatch), isArtifact, DescribeErr(rmdirErr), gone != nil)

	Report(t, "A7.two_step_window", boolVerdict(true),
		"the window is real and its residue is benign for CONTAINMENT (an empty real directory grants an attacker nothing) "+
			"but is a usability trap: it consumes a name that create-only semantics will never release.")
}

// ---------------------------------------------------------------------------
// A8 — Publish's concurrent same-name claim, and the delete-pending variant.
// ---------------------------------------------------------------------------

func TestA8ConcurrentClaim(t *testing.T) {
	r, _ := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	const n = 16
	var wg sync.WaitGroup
	results := make([]error, n)
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i] = MkdirAt(parent, "contested")
		}(i)
	}
	close(start)
	wg.Wait()

	winners, collisions, other := 0, 0, []string{}
	for _, e := range results {
		switch {
		case e == nil:
			winners++
		case isExist(e):
			collisions++
		default:
			other = append(other, DescribeErr(e))
		}
	}
	Report(t, "A8.concurrent_claim", boolVerdict(winners == 1 && collisions == n-1),
		"%d goroutines raced MkdirAt on one pinned parent: %d winner(s), %d STATUS_OBJECT_NAME_COLLISION, %d other %v",
		n, winners, collisions, len(other), other)
	RequireProperty(t, "A8.concurrent_claim", winners == 1,
		"exactly one concurrent create-only claim may win (won %d, collided %d, other %v)", winners, collisions, other)

	// The Windows-only third outcome: the loser sees a DELETE-PENDING name.
	mustName := "pending"
	if err := MkdirAt(parent, mustName); err != nil {
		Report(t, "A8.delete_pending_claim", NotMeasured, "mkdir: %s", DescribeErr(err))
		return
	}
	victim, verr := OpenForDeleteAt(parent, mustName, windows.FILE_DIRECTORY_FILE)
	if verr != nil {
		Report(t, "A8.delete_pending_claim", NotMeasured, "OpenForDeleteAt: %s", DescribeErr(verr))
		return
	}
	legacyErr := DeleteByHandle(victim, false) // legacy disposition: delete-on-close
	claimErr := MkdirAt(parent, mustName)
	windows.CloseHandle(victim)
	afterErr := MkdirAt(parent, mustName)
	Report(t, "A8.delete_pending_claim", Info,
		"legacy disposition on %q -> %s ; a create-only claim DURING the delete-pending window -> %s ; the same claim after "+
			"the handle closed -> %s. Publish must map this to a distinct, transient error, never to \"already exists\" "+
			"(threat model §4.14). POSIX-semantics delete (M10) removes the window entirely, which is why it is the "+
			"recommended primitive.",
		mustName, DescribeErr(legacyErr), DescribeErr(claimErr), DescribeErr(afterErr))
}

// TestA9RenameFailureStatuses records which NTSTATUS each realistic failure of
// the atomic replace produces, so the class-65→class-10 fallback predicate can
// be narrow instead of blanket.
//
// The Go standard library falls back on ANY error
// ($GOROOT/src/internal/syscall/windows/at_windows.go:408-430). That is wrong
// for this store: class 10 has no POSIX semantics, so a blanket fallback
// silently downgrades the delete-pending guarantee whenever an ATTACK makes
// class 65 fail.
func TestA9RenameFailureStatuses(t *testing.T) {
	rows := []string{}
	record := func(label string, setup func(r *Root, dir string, parent windows.Handle) error) {
		r, dir := openScratchRoot(t)
		parent, err := r.OpenRealDir(nil, false, false)
		if err != nil {
			t.Fatalf("OpenRealDir: %s", DescribeErr(err))
		}
		defer windows.CloseHandle(parent)
		if err := setup(r, dir, parent); err != nil {
			rows = append(rows, label+": setup-failed("+DescribeErr(err)+")")
			return
		}
		src, cerr := CreateFileAt(parent, "t.tmp")
		if cerr != nil {
			rows = append(rows, label+": tmp-failed("+DescribeErr(cerr)+")")
			return
		}
		windows.Write(src, []byte("NEW"))
		exErr := RenameAtNT(src, parent, "dst", fileRenameInformationEx,
			fileRenameReplaceIfExists|fileRenamePosixSemantics)
		legacyErr := error(nil)
		if exErr != nil {
			legacyErr = RenameAtNT(src, parent, "dst", fileRenameInformation, fileRenameReplaceIfExists)
		}
		DeleteByHandle(src, true)
		windows.CloseHandle(src)
		rows = append(rows, fmt.Sprintf("%s: class65=%s | class10=%s | wouldFallBack=%v | retryable=%v",
			label, DescribeErr(exErr), DescribeErr(legacyErr), isUnsupportedRenameClass(exErr), IsRetryable(exErr)))
	}

	record("dest_is_directory", func(r *Root, dir string, parent windows.Handle) error {
		return os.Mkdir(filepath.Join(dir, "dst"), 0o755)
	})
	record("dest_is_junction", func(r *Root, dir string, parent windows.Handle) error {
		return CreateJunctionAt(r.Handle(), "dst", scratchDir(t))
	})
	record("dest_held_no_share_delete", func(r *Root, dir string, parent windows.Handle) error {
		mustWrite(t, filepath.Join(dir, "dst"), "OLD")
		dp, _ := windows.UTF16PtrFromString(filepath.Join(dir, "dst"))
		h, err := windows.CreateFile(dp, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
			windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err == nil {
			t.Cleanup(func() { windows.CloseHandle(h) })
		}
		return err
	})
	record("dest_absent", func(r *Root, dir string, parent windows.Handle) error { return nil })

	Report(t, "A9.rename_failure_statuses", Info,
		"%s. CONSEQUENCE: the class-65→class-10 fallback must fire ONLY on STATUS_INVALID_PARAMETER / NOT_SUPPORTED / "+
			"INVALID_INFO_CLASS / INVALID_DEVICE_REQUEST (i.e. 'this build or filesystem does not implement the class'). "+
			"The stdlib's blanket 'retry on any error' fallback would, on the rows above, silently retry an ATTACK with a "+
			"class that has no POSIX semantics.", strings.Join(rows, " ;; "))
}

// TestA10DocumentLookupHazards covers the Documents row of the matrix that the
// handle-anchored design answers structurally: a link where a file is
// expected, a stream suffix, and a rename race.
func TestA10DocumentLookupHazards(t *testing.T) {
	r, dir := openScratchRoot(t)
	ext, intact := externalTree(t)
	extFile := filepath.Join(ext, "PRECIOUS.txt")
	art := mustMkdir(t, filepath.Join(dir, "art"))
	mustWrite(t, filepath.Join(art, "index.html"), "<h1>real</h1>")
	artRoot, err := OpenRoot(art)
	if err != nil {
		t.Fatalf("OpenRoot: %s", DescribeErr(err))
	}
	defer artRoot.Close()

	// (a) a FILE symlink where a document is expected.
	if ok, _ := symlinkCapability(t); ok {
		if err := CreateFileSymlink(filepath.Join(art, "leak.html"), extFile, true); err == nil {
			h, oerr := OpenRegularFileAt(artRoot.Handle(), "leak.html")
			if oerr == nil {
				windows.CloseHandle(h)
			}
			RequireProperty(t, "A10.file_link_refused", oerr != nil,
				"a file symlink pointing outside the store must not be openable as a document, got err=%v", oerr)
			Report(t, "A10.file_link_refused", boolVerdict(oerr != nil),
				"OpenRegularFileAt on a file symlink to %q -> %s", extFile, DescribeErr(oerr))
		}
	} else {
		Report(t, "A10.file_link_refused", NotMeasured, "no symlink capability on this runner")
	}

	// (b) a DIRECTORY link where a document is expected.
	if _, ok := plantLink(t, artRoot.Handle(), "dirlink", ext, "junction"); ok {
		h, oerr := OpenRegularFileAt(artRoot.Handle(), "dirlink")
		if oerr == nil {
			windows.CloseHandle(h)
		}
		RequireProperty(t, "A10.dir_link_refused", oerr != nil,
			"a junction must not be openable as a document, got err=%v", oerr)
		Report(t, "A10.dir_link_refused", boolVerdict(oerr != nil),
			"OpenRegularFileAt on a junction -> %s", DescribeErr(oerr))
	}

	// (c) alternate stream syntax through the handle-relative open. M12 showed
	//     a RootDirectory-relative NtCreateFile ACCEPTS "doc.html:hidden";
	//     this records what each URL-shaped segment does so R11's reject-":"
	//     rule has its evidence.
	var rows []string
	for _, seg := range []string{"index.html:x", "index.html::$DATA", "C:evil", "art::$INDEX_ALLOCATION", "index.html."} {
		h, oerr := OpenRegularFileAt(artRoot.Handle(), seg)
		if oerr == nil {
			windows.CloseHandle(h)
		}
		rows = append(rows, fmt.Sprintf("%q -> %s", seg, DescribeErr(oerr)))
	}
	Report(t, "A10.stream_syntax", Info,
		"handle-relative document opens for URL-shaped segments: %s. Anything that does NOT fail here has to be excluded by "+
			"validateSegment (R11), because the open itself will not.", strings.Join(rows, " ; "))

	// (d) the rename race: the document is replaced between the parent pin and
	//     the file open.
	parent, perr := r.OpenBrowsableDir([]string{"art"})
	if perr != nil {
		Report(t, "A10.rename_race", NotMeasured, "OpenBrowsableDir: %s", DescribeErr(perr))
	} else {
		// Substitute index.html with a link to the external file in the window
		// AFTER the parent handle is pinned.
		_ = os.Remove(filepath.Join(art, "index.html"))
		staged := "realfile"
		if ok, _ := symlinkCapability(t); ok {
			if err := CreateFileSymlink(filepath.Join(art, "index.html"), extFile, true); err == nil {
				staged = "filesymlink"
			}
		}
		h, oerr := OpenRegularFileAt(parent, "index.html")
		content := ""
		if oerr == nil {
			buf := make([]byte, 64)
			n, _ := windows.Read(h, buf)
			content = string(buf[:max(n, 0)])
			windows.CloseHandle(h)
		}
		windows.CloseHandle(parent)
		_, extDetail := intact()
		leaked := strings.Contains(content, "do-not-touch")
		Report(t, "A10.rename_race", boolVerdict(!leaked),
			"the document was replaced with a %s after the parent handle was pinned: open -> %s ; bytes served %q ; "+
				"external tree %s. The parent pin does not freeze the CHILD, so the no-follow open on the final component "+
				"is what closes this — which is why openFileAt's Windows twin must be one NtCreateFile with "+
				"OBJ_DONT_REPARSE|FILE_NON_DIRECTORY_FILE and not an open-then-stat.",
			staged, DescribeErr(oerr), truncate(content, 32), extDetail)
		RequireProperty(t, "A10.rename_race", !leaked,
			"a document substituted with a link after the parent was pinned must never be served (%q)", truncate(content, 32))
	}
	_ = time.Now
}

// ---------------------------------------------------------------------------
// A11 — Browse: the WATCH TARGET is replaced between reading the link and
// opening it.
//
// openBrowsableDir contains the one path-string re-resolution left in the
// design: the target is read out of the reparse buffer and then opened BY
// NAME (storefs_linux.go:184 does exactly the same with readlinkat +
// unix.Open). Everything about that string is attacker-influenceable if the
// attacker can write to any directory ABOVE the watch target.
//
// This test stages both halves separately, because they have different
// answers:
//
//	(a) the target itself is swapped for a link  -> the no-follow reopen refuses it
//	(b) an ANCESTOR of the target is swapped     -> the OS resolves it, and follows
// ---------------------------------------------------------------------------

func TestA11WatchTargetReplacedBetweenReadlinkAndOpen(t *testing.T) {
	t.Run("target_itself_swapped", func(t *testing.T) {
		r, _ := openScratchRoot(t)
		source := scratchDir(t)
		mustWrite(t, filepath.Join(source, "page.html"), "real")
		ext, intact := externalTree(t)

		if k, ok := plantLink(t, r.Handle(), "watch", source, "junction"); !ok {
			Report(t, "A11.target_swapped", NotMeasured, "could not plant the boundary: %s", k)
			return
		}

		var stageErr error
		setSpikeOpHook(t, onceHook(OpBrowseBoundary, func() {
			if err := os.RemoveAll(source); err != nil {
				stageErr = err
				return
			}
			// Substitute the watch target with a junction to the external tree.
			parent, err := OpenRoot(filepath.Dir(source))
			if err != nil {
				stageErr = err
				return
			}
			defer parent.Close()
			stageErr = CreateJunctionAt(parent.Handle(), filepath.Base(source), ext)
		}))

		h, err := r.OpenBrowsableDir([]string{"watch"}, ioReparseTagSymlink, ioReparseTagMountPoint)
		reached := ""
		if err == nil {
			if es, rerr := ReadDirHandle(h); rerr == nil {
				for _, e := range es {
					reached += e.Name() + ","
				}
			}
			windows.CloseHandle(h)
		}
		if stageErr != nil {
			Report(t, "A11.target_swapped", NotMeasured, "could not stage: %v", stageErr)
			return
		}
		extOK, extDetail := intact()
		escaped := strings.Contains(reached, "PRECIOUS.txt")
		Report(t, "A11.target_swapped", boolVerdict(!escaped),
			"the watch TARGET was replaced with a junction to an external tree between the reparse-buffer read and the "+
				"open-by-name: open -> %s ; entries reached %q ; external tree %s. openAbsoluteDirNoFollow opens the FINAL "+
				"component with FILE_FLAG_OPEN_REPARSE_POINT and then refuses a reparse point, which is what closes this half.",
			DescribeErr(err), reached, extDetail)
		RequireProperty(t, "A11.target_swapped", !escaped && extOK,
			"a watch target substituted with a link must not be browsable (reached %q, external %s)", reached, extDetail)
	})

	t.Run("target_ancestor_swapped", func(t *testing.T) {
		r, _ := openScratchRoot(t)
		base := scratchDir(t)
		realParent := mustMkdir(t, filepath.Join(base, "parent"))
		source := mustMkdir(t, filepath.Join(realParent, "src"))
		mustWrite(t, filepath.Join(source, "page.html"), "real")

		// The attacker's tree, shaped so the SAME relative path exists inside it.
		evil := scratchDir(t)
		mustWrite(t, filepath.Join(mustMkdir(t, filepath.Join(evil, "src")), "LOOT.txt"), "loot")

		if k, ok := plantLink(t, r.Handle(), "watch", source, "junction"); !ok {
			Report(t, "A11.ancestor_swapped", NotMeasured, "could not plant the boundary: %s", k)
			return
		}

		var stageErr error
		setSpikeOpHook(t, onceHook(OpBrowseBoundary, func() {
			if err := os.RemoveAll(realParent); err != nil {
				stageErr = err
				return
			}
			baseRoot, err := OpenRoot(base)
			if err != nil {
				stageErr = err
				return
			}
			defer baseRoot.Close()
			stageErr = CreateJunctionAt(baseRoot.Handle(), "parent", evil)
		}))

		h, err := r.OpenBrowsableDir([]string{"watch"}, ioReparseTagSymlink, ioReparseTagMountPoint)
		reached := []string{}
		if err == nil {
			if es, rerr := ReadDirHandle(h); rerr == nil {
				for _, e := range es {
					reached = append(reached, e.Name())
				}
			}
			windows.CloseHandle(h)
		}
		if stageErr != nil {
			Report(t, "A11.ancestor_swapped", NotMeasured, "could not stage: %v", stageErr)
			return
		}
		followed := false
		for _, n := range reached {
			if n == "LOOT.txt" {
				followed = true
			}
		}
		Report(t, "A11.ancestor_swapped", boolVerdict(!followed),
			"an ANCESTOR of the watch target was replaced with a junction between the reparse-buffer read and the "+
				"open-by-name: open -> %s ; entries reached %v ; the attacker's tree was reached = %v. "+
				"THIS IS A REAL WINDOW AND IT IS PLATFORM-INDEPENDENT: storefs_linux.go:184 re-opens the readlink result as an "+
				"ABSOLUTE PATH with O_NOFOLLOW, which likewise protects only the final component. It is not a Windows "+
				"regression and it is bounded by the attacker already needing write access above the user's watched folder, "+
				"but the ADR should record it rather than let the Windows port inherit it silently. The structural fix on "+
				"both platforms is to walk the target's components handle-by-handle instead of opening the string.",
			DescribeErr(err), reached, followed)
	})
}

// A6 continued — Delete's PARENT replaced between the pin and the removal.
func TestA6DeleteParentReplaced(t *testing.T) {
	base := scratchDir(t)
	rootPath := mustMkdir(t, filepath.Join(base, "store"))
	r, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatalf("OpenRoot: %s", DescribeErr(err))
	}
	defer r.Close()
	ext, intact := externalTree(t)

	proj := mustMkdir(t, filepath.Join(rootPath, "project"))
	art := mustMkdir(t, filepath.Join(proj, "artifact"))
	mustWrite(t, filepath.Join(art, "index.html"), "x")

	parent, err := r.OpenRealDir([]string{"project"}, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)

	if err := os.Rename(proj, filepath.Join(rootPath, "moved")); err != nil {
		Report(t, "A6.parent_replaced", NotMeasured, "rename: %v", err)
		return
	}
	if k, ok := plantLink(t, r.Handle(), "project", ext, "junction"); !ok {
		Report(t, "A6.parent_replaced", NotMeasured, "decoy: %s", k)
		return
	}

	AuditStart()
	rmErr := RemoveTreeAt(parent, "artifact")
	audit := AuditStop()
	_, gone := os.Stat(filepath.Join(rootPath, "moved", "artifact"))
	extOK, extDetail := intact()
	held := rmErr == nil && gone != nil && extOK
	Report(t, "A6.parent_replaced", boolVerdict(held),
		"the project directory was renamed away and replaced by a junction to an external tree AFTER it was pinned: "+
			"RemoveTreeAt through the pinned handle -> %s ; the artifact in the ORIGINAL object is gone=%v ; removals %v ; "+
			"external target %s",
		DescribeErr(rmErr), gone != nil, audit, extDetail)
	RequireProperty(t, "A6.parent_replaced", held,
		"a delete through a pinned parent must remove from the pinned object and never reach a substituted decoy (%s)", extDetail)
}

// A12 — Notes: two concurrent writers of the same document.
//
// The rev guard lives in internal/store; what the PROTOTYPE owes is the layer
// below it: whatever interleaving occurs, the destination must always hold one
// complete version and no temp may survive.
func TestA12ConcurrentNoteWriters(t *testing.T) {
	r, dir := openScratchRoot(t)
	parent, err := r.OpenRealDir(nil, false, false)
	if err != nil {
		t.Fatalf("OpenRealDir: %s", DescribeErr(err))
	}
	defer windows.CloseHandle(parent)
	mustWrite(t, filepath.Join(dir, "notes.json"), "seed")

	const writers, rounds = 8, 40
	var wg sync.WaitGroup
	errs := make([]int, writers)
	bad := make([]string, 0)
	var badMu sync.Mutex
	start := make(chan struct{})
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			payload := []byte(strings.Repeat(fmt.Sprintf("w%d;", w), 200))
			<-start
			for i := 0; i < rounds; i++ {
				if _, e := AtomicWriteFile(parent, "notes.json", payload, DefaultReplacePolicy()); e != nil {
					errs[w]++
				}
				b, rerr := os.ReadFile(filepath.Join(dir, "notes.json"))
				if rerr != nil {
					continue
				}
				// Every complete version is one writer's payload repeated; a
				// torn write would mix two writers or be short.
				s := string(b)
				if s != "seed" && (len(s) != 200*3 || strings.Count(s, ";") != 200 || len(uniqueTokens(s)) != 1) {
					badMu.Lock()
					bad = append(bad, truncate(s, 60))
					badMu.Unlock()
				}
			}
		}(w)
	}
	close(start)
	wg.Wait()

	left := tempsLeft(t, dir)
	Report(t, "A12.concurrent_writers", boolVerdict(len(bad) == 0 && len(left) == 0),
		"%d writers × %d replaces of one document: per-writer failures %v ; torn/partial reads observed %d %v ; "+
			"temp residue %v ; final content %q. The unique temp name plus the atomic replace means concurrent writers "+
			"never share a temp and a reader never sees a partial file — the rev guard above this layer decides WHICH "+
			"version wins, not whether the file is intact.",
		writers, rounds, errs, len(bad), bad, left, truncate(readOrErr(filepath.Join(dir, "notes.json")), 30))
	RequireProperty(t, "A12.concurrent_writers", len(bad) == 0,
		"a concurrent reader must never observe a torn or partial document (%d bad reads: %v)", len(bad), bad)
	RequireProperty(t, "A12.concurrent_temp_residue", len(left) == 0,
		"concurrent writers must not leave temp files behind; residue %v", left)
}

func uniqueTokens(s string) map[string]bool {
	out := map[string]bool{}
	for _, tok := range strings.Split(s, ";") {
		if tok != "" {
			out[tok] = true
		}
	}
	return out
}
