//go:build windows

package store

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows"
)

// This file is the Windows port of storefs_linux.go, per the ADR's §3.2
// backend API. It is a mechanical (not literal) port of
// internal/winspike/winfs.go's Root/OpenRealDir/OpenBrowsableDir/strict-open
// shapes, adapted to this package's int-handle convention (§3.1: a Win32
// HANDLE is pointer-sized and round-trips through int on amd64/arm64,
// windows.InvalidHandle == int(-1), matching the sentinel Linux already
// uses) and to the containment fixes the ADR's revision 2 requires that the
// prototype's earliest commits did not yet have (the strict open, §2.1; the
// handle-by-handle browse-boundary walk, §4.3).
//
// P3.7-P3.10 (annotationfs_windows.go) landed the last stubs this package
// had — openAnnotationFS, removeTreeAt, flockFile/funlockFile/
// openLockFileAt and the atomic write path — so the errWindowsUnimplemented
// sentinel this file used to define for them is gone; every function in
// this package is now a real implementation.

// objectID is FILE_ID_INFO on Windows: VolumeSerialNumber plus a 128-bit
// FileId. Comparable, opaque to shared code, never rendered into a path
// (R13/R14). ByHandleFileInformation's 64-bit file index is NOT used here —
// it is insufficient on ReFS (survey Finding 6).
type objectID struct {
	vol uint64
	id  [16]byte
}

func objectIDOf(fd int) (objectID, error) {
	info, err := fileIDOf(windows.Handle(fd))
	if err != nil {
		return objectID{}, err
	}
	return objectID{vol: info.VolumeSerialNumber, id: info.FileID}, nil
}

// rootIdentityCache is the process-level last-seen root identity keyed on
// the resolved root STRING (ADR §4.1, closing finding F9). R12 is overridden
// to a per-operation pin (so t.Setenv(store.RootEnv, ...) keeps working —
// each distinct root string gets its own cache entry), which means
// verifyRoot() alone can only compare against a value recorded microseconds
// earlier in the SAME operation — a case the handle chain already makes
// harmless (F-b). This cache is what restores the cross-OPERATION property
// R13 exists for: the same root string resolving to a DIFFERENT object
// between two calls to openRootedFS is the "silently operating on the wrong
// store" case R13 was written to catch.
var (
	rootIdentityMu    sync.Mutex
	rootIdentityCache = map[string]objectID{}
)

func checkRootIdentity(path string, id objectID) error {
	rootIdentityMu.Lock()
	defer rootIdentityMu.Unlock()
	if prev, ok := rootIdentityCache[path]; ok {
		if prev != id {
			return fmt.Errorf("scratchpad: the store root %q was replaced with a different directory since this process last used it — refusing to operate on it (identity was %v, now %v)", path, prev, id)
		}
		return nil
	}
	rootIdentityCache[path] = id
	return nil
}

// resetRootIdentityCacheForTest clears the process-level cache. Exported
// (lower-case, so package-internal only — a _test.go file in this package
// can still call it) for tests that reuse a root path across subtests with
// SCRATCHPAD_ROOT pointed at a fresh temp directory each time, which would
// otherwise trip the cache's own replacement detector.
func resetRootIdentityCacheForTest() {
	rootIdentityMu.Lock()
	defer rootIdentityMu.Unlock()
	rootIdentityCache = map[string]objectID{}
}

// rootedFS keeps operations anchored to the handle opened for
// SCRATCHPAD_ROOT. path is the exact string this handle was opened from —
// shared code uses THAT string for every advisory path computation in the
// same operation instead of re-reading Root() (§7.3).
type rootedFS struct {
	root   *os.File
	path   string
	id     objectID
	volume string // filesystem name, from GetVolumeInformationByHandle (R18)
}

var warnNonNTFSOnce sync.Once

func openRootedFS(create bool) (*rootedFS, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if err := validateAbsoluteWindowsPath(root); err != nil {
		return nil, fmt.Errorf("scratchpad: %s: %w", RootEnv, err)
	}
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	p, err := windows.UTF16PtrFromString(root)
	if err != nil {
		return nil, err
	}
	// FILE_FLAG_OPEN_REPARSE_POINT (not OBJ_DONT_REPARSE — this is a Win32
	// CreateFile call, not ntOpenAt) so a root that is itself a symlink,
	// junction or volume mount point is opened as the reparse point, not
	// followed, and refused below on the TAG (never on fs.ModeSymlink,
	// which misses junctions entirely — threat model §3.4).
	h, err := windows.CreateFile(p, windows.GENERIC_READ, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, translateOpen("open store root", err)
	}
	at, err := attrTagOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if at.isReparse() {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("scratchpad: store root %q is a %s reparse point — refusing to use a symlink, junction or volume mount point as the store root", root, tagName(at.ReparseTag))
	}
	if !at.isDir() {
		windows.CloseHandle(h)
		return nil, fmt.Errorf("scratchpad: store root %q is not a directory", root)
	}
	fid, err := fileIDOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	id := objectID{vol: fid.VolumeSerialNumber, id: fid.FileID}
	if err := checkRootIdentity(root, id); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	fsName, _, _ := volumeInfo(h)
	// R18: warn once, before the first MUTATION (create == true is Publish's
	// and Watch's entry point; read paths pass create == false and never
	// warn) — the filesystem cannot be known before a handle exists, so this
	// is the earliest point the gate can fire (threat model §9.8).
	if create && fsName != "" && !strings.EqualFold(fsName, "NTFS") {
		warnNonNTFSOnce.Do(func() {
			fmt.Fprintf(os.Stderr, "warning: the scratchpad store root is on a %s volume, not NTFS — atomic rename and POSIX delete are unsupported there and mutations may fail loudly\n", fsName)
		})
	}
	// Deterministic-race hook (ADR §11/R17, "root-open"): fires after the root
	// handle is pinned but before the caller does anything else with it, so a
	// test can substitute the root out from under an in-flight operation
	// (A4.root_replaced.*-style attacks) without a timing loop. Mirrors the
	// call site in storefs_linux.go's openRootedFS exactly, so a shared test
	// can install the hook once and run on both platforms.
	runStoreOpHook("root-open")
	return &rootedFS{root: os.NewFile(uintptr(h), root), path: root, id: id, volume: fsName}, nil
}

func (r *rootedFS) close() error { return r.root.Close() }

// nameEquals is the platform pair Visible's reserved-name check (ignore.go)
// uses to compare a candidate entry name against AnnotationsDir/lockFileName
// (ADR §7.4). NTFS folds case (M11: `.annotations`/`.Annotations` fold
// together), and this is a DENY rule, so the over-breadth of EqualFold vs.
// NTFS's actual $UpCase table is safe — the cost is that a Windows user
// cannot have a top-level directory whose name folds to one of the two
// reserved names, which is correct behaviour anyway. Never used for
// identity (identity is FILE_ID_INFO, everywhere, without exception).
func nameEquals(a, b string) bool { return strings.EqualFold(a, b) }

// canonicalLookupName is the third member of the §7.4 name-comparison
// platform pair (nameEquals above, matchName in names_windows.go), and it
// closes the half of that pair those two cannot reach: NTFS gives a name a
// second, requester-typeable SPELLING, not merely a second case.
//
// P6.3 F1. nameEquals folds case, so `.Annotations` no longer bypasses
// Visible's reserved-name deny (ignore.go) and matchName no longer lets
// `.SSH` slip past defaultIgnores. Neither helps against an 8.3 short name:
// with 8.3 generation on — the Windows default for the system volume, where
// `~\.scratchpad` lives, and measured ON for the runner's C: in the spike's
// M6.enabled — NTFS stores `ANNOTA~1` alongside `.annotations` and resolves
// both, including through an OBJECT_ATTRIBUTES.RootDirectory-relative
// NtCreateFile. EqualFold("ANNOTA~1", ".annotations") is false and
// path.Match("node_modules", "node_m~1") is false, so a requester-supplied
// URL segment spelled as the alias passed every string comparison in
// ignore.go while the kernel happily resolved it to the hidden object.
//
// Three fixes were considered (the trade-off is argued in
// reviews/P6.2-threat-model-audit.md §10):
//
//   - Refusing the 8.3 SHAPE in checkLookupSegmentPlatform (`FOO~1`-like) is
//     one line, but it is a string guess at NTFS's generation algorithm — the
//     exact class of reasoning P-5 already falsified once on this branch —
//     and it makes an entry a watched repo literally named `FOO~1.TXT`
//     unaddressable.
//   - Comparing objectIDOf against `.annotations`' own identity after the
//     open is sound but fixes only the two RESERVED names; every
//     defaultIgnores and .scratchpadignore rule — the half that carries the
//     content-disclosure routes — would stay bypassable, and it would put a
//     policy decision inside openBrowsableDir, a mechanism function.
//   - This: ask the filesystem what the entry is called and decide on THAT.
//     It is a fact obtained from the filesystem rather than a guess about it,
//     it fixes the reserved names and every ignore rule in one place, and it
//     generalises to any future aliasing mechanism rather than to 8.3 alone.
//
// It normalises the DECISION only, never the request: a caller may still
// address a visible entry by any spelling NTFS accepts, so case-variant URLs
// (`/a/ART/index.html`) keep working exactly as docs/windows.md documents.
// What changes is that the ignore rules and the reserved-name deny now run
// against the name on disk.
//
// The returned value is used for a string comparison and to extend the
// advisory path walk — never to open anything — so GetLongPathName's
// whole-path resolution (it follows reparse points, and this is the
// path-based advisory layer, ADR §6.9 row 2) cannot redirect an operation.
// Any failure — most commonly "the entry does not exist" — falls back to the
// requested spelling, which is correct: an entry that is not there has
// nothing to hide.
func canonicalLookupName(dir, name string) string {
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, name))
	if err != nil {
		return name
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	if err == nil && int(n) > len(buf) {
		buf = make([]uint16, n+1)
		n, err = windows.GetLongPathName(p, &buf[0], uint32(len(buf)))
	}
	if err != nil || n == 0 {
		return name
	}
	base := filepath.Base(windows.UTF16ToString(buf))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return name
	}
	return base
}

// verifyRoot re-reads FILE_ID_INFO on the pinned handle and compares it
// against the identity recorded at pin time (R13). It distinguishes a
// rename (handle follows the object, F-b) from a replacement (different
// identity). This is a diagnostic within one operation, not a control — see
// the ADR §4.1: because the name is never re-resolved, a replaced root
// cannot redirect this operation regardless. Cross-operation replacement is
// what the rootIdentityCache above catches.
func (r *rootedFS) verifyRoot() error {
	fid, err := fileIDOf(windows.Handle(r.root.Fd()))
	if err != nil {
		return err
	}
	id := objectID{vol: fid.VolumeSerialNumber, id: fid.FileID}
	if id != r.id {
		return fmt.Errorf("scratchpad: the store root %q was replaced during this operation (pinned %v, now %v)", r.path, r.id, id)
	}
	return nil
}

func dupFD(fd int) (int, error) {
	dup, err := dupHandle(windows.Handle(fd))
	if err != nil {
		return -1, err
	}
	return int(dup), nil
}

func closeFD(fd int) { windows.CloseHandle(windows.Handle(fd)) }

// ---------------------------------------------------------------------------
// The strict primitives (ADR §2.1). This is the single most important
// correction from revision 1: FILE_OPEN_REPARSE_POINT plus a
// FILE_ATTRIBUTE_TAG_INFO read from the SAME handle, refusing by tag value —
// not OBJ_DONT_REPARSE, which A5.obj_dont_reparse_inert_for_unknown_tags
// proved does nothing for a non-Microsoft tag on a machine with no filter
// driver servicing it (the refusal on CI is "no driver claimed this tag",
// not "we asked not to reparse" — on a machine WITH the driver the same open
// would be serviced and traverse).
// ---------------------------------------------------------------------------

// reparseRefusal is returned when a strict open found a reparse point where
// a real directory or file is required. It carries the tag so callers can
// render an actionable message and apply an allowlist themselves (Scope A)
// rather than have one buried in the primitive. Unwrap chains to errReparse
// so errors.Is(err, errReparse) works from anywhere the error is wrapped.
type reparseRefusal struct {
	Name  string
	Tag   uint32
	Attrs uint32
}

func (e *reparseRefusal) Error() string {
	return fmt.Sprintf("%q is a %s reparse point (tag 0x%08X), not a real directory entry", e.Name, tagName(e.Tag), e.Tag)
}
func (e *reparseRefusal) Unwrap() error { return errReparse }

// openStrictAt is the shared body of openRealDirAt/openRealFileAt/statAt's
// sibling. name must be exactly one component — enforced by ntOpenAt at
// runtime, not by comment.
func openStrictAt(parent int, name string, access, options uint32) (int, error) {
	h, err := ntOpenAt(windows.Handle(parent), name, access, windows.FILE_OPEN,
		options|windows.FILE_OPEN_REPARSE_POINT, noFollowAttrs, 0)
	if err != nil {
		return -1, translateOpen("open", err)
	}
	at, tagErr := attrTagOf(h)
	if tagErr != nil {
		windows.CloseHandle(h)
		return -1, tagErr
	}
	if at.isReparse() {
		windows.CloseHandle(h)
		return -1, &reparseRefusal{Name: name, Tag: at.ReparseTag, Attrs: at.FileAttributes}
	}
	return int(h), nil
}

// openRealDirAt is the openat(O_DIRECTORY|O_NOFOLLOW) twin, hardened: it
// opens the entry itself and refuses ANY reparse tag from the handle, so its
// guarantee does not depend on which filter drivers are installed on the
// machine (A5.strict_open, A5.unknown_tag_refused, A5.strict_walk,
// A5.strict_open_admits_real_dirs — all REQUIRED in run 9).
func openRealDirAt(parent int, name string) (int, error) {
	return openStrictAt(parent, name, dirReadAccess, windows.FILE_DIRECTORY_FILE)
}

// openRealFileAt is the openat(O_NOFOLLOW)+S_IFREG twin: one open, one tag
// read, no fstat window.
func openRealFileAt(parent int, name string) (int, error) {
	return openStrictAt(parent, name, fileReadAccess, windows.FILE_NON_DIRECTORY_FILE)
}

// openDirAt is the name store.go's shared code calls directly (Publish's
// post-mkdirClaim reopen, Delete's classification reopen). It is the SAME
// strict primitive as openRealDirAt: Scope B requires the strict open
// everywhere a mutation walk descends, and these two call sites are exactly
// that, not merely a "the linux name happens to exist" alias.
func openDirAt(parent int, name string) (int, error) { return openRealDirAt(parent, name) }

// mkdirClaim is the create-only claim: FILE_CREATE is the O_EXCL analogue.
// STATUS_OBJECT_NAME_COLLISION and, from a claim that resolves onto an
// existing reparse point, STATUS_REPARSE_POINT_ENCOUNTERED, BOTH mean "the
// name is taken" (M8.claim_error_map) — translateClaim is what makes
// Watch's same-target idempotence branch (store.go) hang off the right
// status instead of a mechanical errors.Is(err, unix.EEXIST) port that would
// turn every repeat `watch` into a hard error.
//
// FILE_OPEN_REPARSE_POINT (M3, win32_windows.go's noFollowAttrs comment):
// this is called both directly (Publish's directory claim) and through
// symlinkAt (a watch link's name claim). Pre-existing compensating control,
// per caller:
//   - Publish: caught one line later by openDirAt's own strict open
//     (store.go), which fails and writes no content if a traversed claim
//     landed on a driver-serviced reparse target — the residue is an empty
//     directory at the driver's target, cleaned up by Publish's rollback
//     removing the attacker's link, not that directory.
//   - symlinkAt: its own post-claim reopen (link_windows.go) already passes
//     FILE_OPEN_REPARSE_POINT (a FILE_OPEN, not a create disposition, so it
//     was never in scope for this finding) and so never traverses through to
//     a driver's target either — it opens whatever is actually at the name.
//     Both callers therefore had a real backstop; this flag closes the gap
//     at its source instead of relying on either one.
func mkdirClaim(parent int, name string) error {
	h, err := ntOpenAt(windows.Handle(parent), name, dirReadAccess, windows.FILE_CREATE,
		windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return translateClaim("mkdir", err)
	}
	return windows.CloseHandle(h)
}

// rmdirAt removes the single empty directory entry name relative to parent —
// Publish's claim-rollback, and (per ADR §6.6 rule 3) Delete's widened
// "remove an empty non-artifact directory left behind by an interrupted
// watch" recovery. It opens the entry as a LINK (FILE_OPEN_REPARSE_POINT,
// not OBJ_DONT_REPARSE — removing an entry must classify what IS there, not
// refuse to look) and POSIX-disposes it; a non-empty directory surfaces as
// errNotEmpty (STATUS_DIRECTORY_NOT_EMPTY), so this can never destroy
// content and never silently follows a link into a delete.
func rmdirAt(parent int, name string) error {
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return translateOpen("rmdir", err)
	}
	defer windows.CloseHandle(h)
	return translateOpen("rmdir", deleteByHandlePosix(h))
}

// dirHasHTMLFD reports whether fd directly contains a regular *.html file.
// See storefs_linux.go's twin for why the error return exists (fail-open vs
// fail-closed is the caller's decision, ADR §4.3).
func dirHasHTMLFD(fd int) (bool, error) {
	entries, err := readDirFD(fd)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if !strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
			continue
		}
		m, err := statAt(fd, e.Name())
		if err == nil && m.IsRegular {
			return true, nil
		}
	}
	return false, nil
}

// openRealDir walks only real directory entries, one strict open per
// segment against the previous handle (F-a). A reparse point at ANY
// position — serviced by a filter driver or not — fails the walk with a
// reparseRefusal carrying the tag (R1, R3, R6).
func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	for i, seg := range segs {
		next, openErr := openRealDirAt(fd, seg)
		if openErr != nil && create && errors.Is(openErr, fs.ErrNotExist) {
			if mkErr := mkdirClaim(fd, seg); mkErr != nil && !errors.Is(mkErr, errExists) {
				closeFD(fd)
				return -1, mkErr
			}
			next, openErr = openRealDirAt(fd, seg)
		}
		closeFD(fd)
		if openErr != nil {
			var rr *reparseRefusal
			if errors.As(openErr, &rr) {
				return -1, fmt.Errorf("project ancestor %q is a link or reparse point (tag %s)", strings.Join(segs[:i+1], "/"), tagName(rr.Tag))
			}
			return -1, openErr
		}
		fd = next
		if rejectArtifacts {
			// Fail-open on a read error: this only gates directory
			// creation, not a security boundary (mirrors storefs_linux.go).
			if hasHTML, _ := dirHasHTMLFD(fd); hasHTML {
				closeFD(fd)
				return -1, fmt.Errorf("%q is an artifact, not a project", strings.Join(segs[:i+1], "/"))
			}
		}
	}
	return fd, nil
}

// openBrowsableDir permits exactly one link boundary: the store-owned watch
// crossing (invariant 5). Every other segment is a strict open. Unlike
// openRealDir, the boundary guard fails CLOSED on a dirHasHTMLFD read error
// (ADR §4.3, "one place the prototype is stricter than Linux — keep it").
func (r *rootedFS) openBrowsableDir(segs []string) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	crossed := false
	for _, seg := range segs {
		next, openErr := openRealDirAt(fd, seg)
		if openErr != nil && !crossed {
			var rr *reparseRefusal
			if errors.As(openErr, &rr) {
				hasHTML, htmlErr := dirHasHTMLFD(fd)
				if htmlErr == nil && !hasHTML {
					next, openErr = crossWatchBoundary(fd, seg)
					if openErr == nil {
						crossed = true
					}
				}
			}
		}
		closeFD(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
		// Deterministic-race hook ("browse-segment"): fires once per resolved
		// path component, after this segment is pinned and before the next is
		// opened, so a test can substitute an already-passed ancestor mid-walk.
		// Mirrors storefs_linux.go's openBrowsableDir call site exactly.
		runStoreOpHook("browse-segment")
	}
	return fd, nil
}

// ---------------------------------------------------------------------------
// The A11.ancestor_swapped fix (ADR §4.3): crossing the one permitted
// boundary never consumes the reparse-buffer string as a path handed
// straight to CreateFile. Instead the target is walked component-by-
// component, anchored at the volume root, under the strict primitive — the
// Windows twin of storefs_linux.go's openFilesystemRootNoFollow /
// openAbsoluteDirNoFollow (commit 113cbb2).
// ---------------------------------------------------------------------------

// crossWatchBoundary reads and validates the link at (parent, name) —
// readlinkAt (link_windows.go) enforces the Scope A tag allowlist, the
// SYMLINK_FLAG_RELATIVE refusal and the absolute/drive-rooted validator, and
// the \??\Volume{ refusal (§5.3) — then reaches the target by the
// handle-by-handle walk below. It is the single reusable mechanism
// openBrowsableDir (one path) and store.go's handle-anchored List/Watches
// (a whole tree) both call for the one crossing invariant 5 permits.
func crossWatchBoundary(parent int, name string) (int, error) {
	target, err := readlinkAt(parent, name)
	if err != nil {
		return -1, err
	}
	return openAbsoluteDirNoFollowWin(target)
}

// openAbsoluteDirNoFollowWin opens target — already validated absolute and
// drive-rooted by readlinkAt — by walking its components one at a time from
// the volume root, refusing a reparse point (or anything not a plain
// directory) at ANY component. No path string survives past the volume-root
// open: from there it is a chain of pinned handles, so a later ancestor
// substitution has nothing left to redirect (closing A11.ancestor_swapped,
// the only measured NO in run 9 that lands on a containment question).
func openAbsoluteDirNoFollowWin(target string) (int, error) {
	if err := validateAbsoluteWindowsPath(target); err != nil {
		return -1, err
	}
	fd, err := openVolumeRootNoFollow(target[:3]) // "C:\"
	if err != nil {
		return -1, err
	}
	for _, seg := range strings.Split(target[3:], `\`) {
		if seg == "" {
			continue
		}
		next, err := openRealDirAt(fd, seg)
		closeFD(fd)
		if err != nil {
			return -1, err
		}
		fd = next
	}
	return fd, nil
}

// openVolumeRootNoFollow opens "C:\" (or whichever drive) as the trusted,
// fixed anchor for openAbsoluteDirNoFollowWin — the Windows counterpart of
// Linux's openFilesystemRootNoFollow("/"). It refuses a reparse point (a
// drive root cannot itself be a mount point in the ordinary case, but the
// tag is still checked rather than assumed) and requires a real directory.
func openVolumeRootNoFollow(drive string) (int, error) {
	p, err := windows.UTF16PtrFromString(drive)
	if err != nil {
		return -1, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return -1, translateOpen("open volume root", err)
	}
	at, err := attrTagOf(h)
	if err != nil {
		windows.CloseHandle(h)
		return -1, err
	}
	if at.isReparse() || !at.isDir() {
		windows.CloseHandle(h)
		return -1, fmt.Errorf("scratchpad: volume root %q is not a plain directory", drive)
	}
	return int(h), nil
}

// validateAbsoluteWindowsPath is the ONE validator §4.1 (Root()) and §3.3
// (readlinkAt's target) share: absolute, drive-letter-rooted; refuses
// drive-relative (C:foo), current-drive-relative (\foo), UNC
// (\\server\share), and device-namespace (\\?\, \\.\) forms — all of which
// start with a `\\` prefix or lack a `X:\` prefix, checked in that order so
// the UNC/device case gets its own clearer message.
func validateAbsoluteWindowsPath(p string) error {
	if strings.HasPrefix(p, `\\`) {
		return fmt.Errorf("%q must not be a UNC or device-namespace path", p)
	}
	if len(p) < 3 || p[1] != ':' || p[2] != '\\' {
		return fmt.Errorf("%q must be an absolute, drive-letter-rooted path", p)
	}
	c := p[0]
	if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
		return fmt.Errorf("%q must be an absolute, drive-letter-rooted path", p)
	}
	return nil
}

// canonicalizeWatchTarget is Watch's platform-pair twin to
// filepath.EvalSymlinks (ADR §4.3/§4.7, §10.3 exception 2): EvalSymlinks
// cannot do this job on Windows because it does not resolve junctions and
// errors when one is an intermediate component (M5.junction). It opens the
// target FOLLOWING reparse points (an ordinary FILE_FLAG_BACKUP_SEMANTICS
// open, no FILE_FLAG_OPEN_REPARSE_POINT) and reads
// GetFinalPathNameByHandleW(VOLUME_NAME_DOS) from that handle — a narrow,
// named exception to the "never re-resolve a path" rule, because the output
// is written into the link as a durable record and every later use of it is
// re-validated (validateAbsoluteWindowsPath) and then walked handle-by-
// handle (openAbsoluteDirNoFollowWin), never consumed directly.
func canonicalizeWatchTarget(target string) (string, error) {
	p, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return "", err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, shareAll, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", translateOpen("open watch target", err)
	}
	defer windows.CloseHandle(h)
	at, err := attrTagOf(h)
	if err != nil {
		return "", err
	}
	if !at.isDir() {
		return "", fmt.Errorf("scratchpad: %s is not a directory", target)
	}
	final, err := finalPathDOS(h)
	if err != nil {
		return "", err
	}
	if err := validateAbsoluteWindowsPath(final); err != nil {
		return "", fmt.Errorf("scratchpad: watch target resolved to an unsupported path %q: %w", final, err)
	}
	return final, nil
}

// alreadyInsideRoot is the "already inside the scratchpad" guard (ADR §7.1):
// target IS the root is sound (FILE_ID_INFO identity, via a no-follow
// handle-by-handle open of the already-canonicalized target — R13.replace
// shows FILE_ID_INFO discriminates objects); target is INSIDE the root is
// advisory (case-insensitive canonical-path comparison, both sides handle-
// derived so both are long-name canonical per M6.resolution). The hard
// backstop for a bypass is the identity-keyed cycle guard in List/Watches
// (RR12, rated Low: "a confusing recursive listing, bounded by the cycle
// guard, not an escape").
func alreadyInsideRoot(target, root string) bool {
	rfs, err := openRootedFS(false)
	if err != nil {
		return false
	}
	defer rfs.close()
	if fd, err := openAbsoluteDirNoFollowWin(target); err == nil {
		id, idErr := objectIDOf(fd)
		closeFD(fd)
		if idErr == nil && id == rfs.id {
			return true
		}
	}
	rootFinal, err := finalPathDOS(windows.Handle(rfs.root.Fd()))
	if err != nil {
		return false
	}
	tl, rl := strings.ToLower(target+`\`), strings.ToLower(rootFinal+`\`)
	return strings.EqualFold(target, rootFinal) || strings.HasPrefix(tl, rl)
}

// ---------------------------------------------------------------------------
// Classification (statAt/statSelf/statLinkTarget) and directory reads.
// ---------------------------------------------------------------------------

// statAt is fstatat(parent, name, AT_SYMLINK_NOFOLLOW)'s Windows twin: it
// opens name — one component, enforced by ntOpenAt — with
// FILE_OPEN_REPARSE_POINT (NOT OBJ_DONT_REPARSE: classifying or removing a
// link must open the LINK), then reads FILE_ATTRIBUTE_TAG_INFO and
// BY_HANDLE_FILE_INFORMATION from that SAME handle. Final component and
// whole path coincide here (a single name relative to a pinned parent), so
// FILE_OPEN_REPARSE_POINT's final-component-only guarantee is sufficient —
// and only here.
func statAt(parent int, name string) (entryMeta, error) {
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return entryMeta{}, translateOpen("stat", err)
	}
	defer windows.CloseHandle(h)
	return classifyHandle(h)
}

// statSelf classifies the handle itself — used for a directory's own
// ModTime baseline (loadArtifactAt), where there is no parent/name pair.
func statSelf(fd int) (entryMeta, error) { return classifyHandle(windows.Handle(fd)) }

func classifyHandle(h windows.Handle) (entryMeta, error) {
	at, err := attrTagOf(h)
	if err != nil {
		return entryMeta{}, err
	}
	m := entryMeta{Tag: at.ReparseTag}
	switch {
	case at.isReparse():
		m.IsLink = isWatchTag(at.ReparseTag) // Scope A/B/C: everything else is "a reparse point we do not understand" (R4)
	case at.isDir():
		m.IsDir = true
	default:
		m.IsRegular = true
	}
	var bhfi windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &bhfi); err == nil {
		m.Size = int64(bhfi.FileSizeHigh)<<32 | int64(bhfi.FileSizeLow)
		m.ModTime = time.Unix(0, bhfi.LastWriteTime.Nanoseconds())
	}
	return m, nil
}

// statLinkTarget is statAt reduced to "is this a real directory, was the
// answer obtained" — never follows, never takes a path. A reparse-tagged
// entry answers isDir=false, ok=true: the fail-closed answer, which stops
// descent. There is no path argument and no follow, so there is nothing to
// redirect (ADR §3.2 resolves this the safe way against revision 1's two
// conflicting descriptions of it).
func statLinkTarget(parent int, name string) (isDir, ok bool) {
	m, err := statAt(parent, name)
	if err != nil {
		return false, false
	}
	return m.IsDir, true
}

// linkTargetIsDir answers a DIFFERENT question than statLinkTarget: does the
// link ENTRY ITSELF (already known, via classifyEntry, to carry an
// allow-listed tag) present as directory-shaped, i.e. does
// FILE_ATTRIBUTE_DIRECTORY read from the reparse point's own handle? A
// directory symlink or a junction both set it without following anything —
// unlike Linux, where a symlink carries no such bit and the equivalent
// (storefs_linux.go's linkTargetIsDir) must do one bounded follow instead.
// Used only by Watches' listing decision; every mutation path uses
// statAt/isLinkAt alone.
func linkTargetIsDir(parent int, name string) bool {
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	at, err := attrTagOf(h)
	return err == nil && at.isDir()
}

// readDirFD enumerates a directory through a DUPLICATE of an already-open
// handle, never re-resolving a path — the replacement for fdPath (M16,
// REQUIRED): "each duplicate restarts enumeration independently, which is
// why repeated ReadDir on one artifact works." The duplicate is what lets
// the resulting *os.File's Close not consume the caller's own anchor.
func readDirFD(fd int) ([]os.DirEntry, error) {
	dup, err := dupHandle(windows.Handle(fd))
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dup), "scratchpad-dir")
	if f == nil {
		windows.CloseHandle(dup)
		return nil, fmt.Errorf("scratchpad: os.NewFile rejected the duplicated directory handle")
	}
	defer f.Close()
	return f.ReadDir(-1)
}

// ---------------------------------------------------------------------------
// Create-only file/dir ops, document open, pruning (P3.3/P3.5/P3.6).
// ---------------------------------------------------------------------------

func mkdirsAt(root int, segs []string) (int, error) {
	fd, err := dupFD(root)
	if err != nil {
		return -1, err
	}
	for _, seg := range segs {
		next, e := openRealDirAt(fd, seg)
		if e != nil && errors.Is(e, fs.ErrNotExist) {
			if mkErr := mkdirClaim(fd, seg); mkErr == nil || errors.Is(mkErr, errExists) {
				next, e = openRealDirAt(fd, seg)
			} else {
				e = mkErr
			}
		}
		closeFD(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}

// writeFileAt creates name (create-only) relative to a directory it will
// mkdirsAt into existence first, and writes data through the handle it
// created — R15's "documents are never served through os.Open" applies
// symmetrically to writes: FILE_CREATE|FILE_NON_DIRECTORY_FILE|
// FILE_OPEN_REPARSE_POINT|OBJ_DONT_REPARSE means a pre-planted link at this
// name cannot capture the write (it is refused, translated to
// errExistsReparse, never silently followed).
//
// FILE_OPEN_REPARSE_POINT (M3): this site has NO pre-existing compensating
// control — unlike Publish's directory claim, nothing reopens this write
// strictly afterward to catch a traversed claim, so before this flag a
// serviced unknown tag planted at this name (inside a directory this
// package just created empty, e.g. by mkdirsAt above) could have captured
// `data` as an arbitrary file write at the driver's target. This was the one
// real hole M3 found, not defence in depth.
func writeFileAt(root int, segs []string, data []byte) error {
	parent, err := mkdirsAt(root, segs[:len(segs)-1])
	if err != nil {
		return err
	}
	defer closeFD(parent)
	name := segs[len(segs)-1]
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_WRITE, windows.FILE_CREATE,
		windows.FILE_NON_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, noFollowAttrs, windows.FILE_ATTRIBUTE_NORMAL)
	if err != nil {
		return translateClaim("create file", err)
	}
	f := os.NewFile(uintptr(h), name)
	_, err = f.Write(data) // os.File.Write already loops on a short write; no separate loop needed here
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// openFileAt is openRealFileAt wrapped as an *os.File: one strict open (no
// fstat window), full share mode (R15) — so a document is never served
// through os.Open, whose syscall.Open hard-codes FILE_SHARE_READ|WRITE and
// omits FILE_SHARE_DELETE (P13.go_share_mode), which would let a document
// read veto a concurrent atomic notes replace of the same file.
func openFileAt(parent int, name string) (*os.File, error) {
	h, err := openRealFileAt(parent, name)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), name), nil
}

func readAllAt(parent int, name string) ([]byte, error) {
	f, err := openFileAt(parent, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// pruneAt removes empty project directories bottom-up after an Unwatch/
// Delete, reopening each parent from the anchored root per level so a
// concurrent rename can only make pruning stop, never redirect it
// (unchanged policy from Linux).
func pruneAt(r *rootedFS, segs []string) {
	for i := len(segs); i > 0; i-- {
		parent, err := r.openRealDir(segs[:i-1], false, false)
		if err != nil {
			return
		}
		err = rmdirAt(parent, segs[i-1])
		closeFD(parent)
		if err != nil {
			return
		}
	}
}

func openPathFile(segs []string) (*os.File, error) {
	if len(segs) == 0 {
		return nil, fs.ErrNotExist
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, err
	}
	defer rfs.close()
	parent, err := rfs.openBrowsableDir(segs[:len(segs)-1])
	if err != nil {
		return nil, err
	}
	defer closeFD(parent)
	// Deterministic-race hook ("doc-open"): fires after the parent directory
	// is pinned and before the final document open, so a test can substitute
	// the document itself in that window (A10.rename_race-style attacks).
	// Mirrors storefs_linux.go's openPathFile call site exactly.
	runStoreOpHook("doc-open")
	return openFileAt(parent, segs[len(segs)-1])
}

// OpenDocument pins a validated store-relative regular file for HTTP
// serving. Mirrors storefs_linux.go's twin exactly, over the platform
// primitives above.
func OpenDocument(segs []string) (*os.File, bool) {
	root, err := Root()
	if err != nil || !visibleSegments(root, segs) {
		return nil, false
	}
	for _, seg := range segs {
		if validateSegment(seg) != nil {
			return nil, false
		}
	}
	f, err := openPathFile(segs)
	return f, err == nil
}
