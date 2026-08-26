//go:build windows

package store

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// watchTags is the ENTIRE allowlist (Scope A, ADR §5.1). Nothing else is
// ever accepted as a link, anywhere, for any purpose. Junctions are accepted
// at the same trust tier as symlinks (§6.6): the security property lives in
// tag-aware classification, which this design needs regardless of whether it
// ever creates a junction, and rejecting them would only remove `watch` from
// every machine with Developer Mode off.
var watchTags = [...]uint32{windows.IO_REPARSE_TAG_SYMLINK, windows.IO_REPARSE_TAG_MOUNT_POINT}

func isWatchTag(tag uint32) bool {
	for _, t := range watchTags {
		if t == tag {
			return true
		}
	}
	return false
}

// linkFlavour records which link mechanism was actually used to satisfy a
// watch-link creation, so a caller (diagnostics, P3.11/P3.12 tests) can tell
// which one a given `watch` used without parsing an error string.
type linkFlavour int

const (
	linkFlavourNone linkFlavour = iota
	linkFlavourSymlink
	linkFlavourJunction
)

// watchLinkFlavour is NOT a separate probe call: per §6.6, the only honest
// way to know which flavour will succeed for a given parent/privilege
// combination is to attempt the claim, because Developer Mode and privilege
// state are process/session properties a dry-run probe would have to fake a
// second claim to test anyway. symlinkAt's own try-symlink-then-junction
// fallback (below) IS the probe, applied to the real name being claimed;
// this function is kept as a named capability query for tests that need the
// answer without publishing anything, by claiming and immediately removing
// a throwaway name.
func watchLinkFlavour(parent int, probeName, target string) (linkFlavour, error) {
	if err := symlinkAt(parent, target, probeName); err != nil {
		return linkFlavourNone, err
	}
	flavour := linkFlavourSymlink
	if tag, err := readLinkTagAt(parent, probeName); err == nil && tag == windows.IO_REPARSE_TAG_MOUNT_POINT {
		flavour = linkFlavourJunction
	}
	_ = unlinkAt(parent, probeName)
	return flavour, nil
}

// readLinkTagAt is readlinkAt's tag-only sibling, used by watchLinkFlavour's
// probe so it does not need to re-validate the target it just wrote itself.
func readLinkTagAt(parent int, name string) (uint32, error) {
	h, err := ntOpenAt(windows.Handle(parent), name, fileReadAccess, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return 0, translateOpen("readlink", err)
	}
	defer windows.CloseHandle(h)
	tag, _, _, err := readLinkHandle(h)
	return tag, err
}

// symlinkAt creates a link named name, relative to the open directory
// parent, pointing at the absolute directory target. Create-only: an
// existing name surfaces as errExists/errExistsReparse (translateClaim),
// matching Publish's directory-claim semantics.
//
// The two-step creation window (ADR §6.6): the name claim (FILE_CREATE) is
// atomic, but applying the reparse tag afterward is a second step, so a
// crash between them leaves an empty real directory under the watch name.
// Rule 1 — "the error path self-heals, in both flavours, through the
// handle" — is implemented here: the post-claim reopen requests DELETE
// access, and on ANY failure setting the reparse tag, the just-created
// directory is POSIX-disposed through that SAME handle (never a
// re-open-by-name, which would be exactly the re-resolution this design
// removes everywhere else).
func symlinkAt(parent int, target, name string) error {
	if err := mkdirClaim(parent, name); err != nil {
		return err
	}
	// Fires after the claim and before the reopen below, so a test can put
	// the just-claimed directory into a state that fails ONLY this specific
	// reopen (e.g. a competing handle sharing less than FILE_GENERIC_WRITE
	// needs) without needing privilege manipulation — the same deterministic
	// fault-injection shape runStoreOpHook already provides for the
	// annotation-tree race tests (annotationfs_windows.go).
	runStoreOpHook("symlink-reopen")
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_GENERIC_WRITE|windows.FILE_GENERIC_READ|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE|windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		// rule 1: the claim must not outlive the failure. There is no handle
		// to dispose through here — the reopen is what just failed — so this
		// is the same by-name rollback Publish uses at its own post-claim
		// reopen (store.go's openDirAt/rmdirAt pair): rmdirAt refuses a
		// non-empty directory and never follows a link, so it cannot destroy
		// content even if the name was substituted in the window between the
		// claim and this failed reopen.
		_ = rmdirAt(parent, name)
		return translateOpen("open new link", err)
	}
	defer windows.CloseHandle(h)

	setErr := setSymlinkReparse(h, target)
	if setErr != nil {
		// Try a junction instead: per §6.6's measured privilege table, a
		// junction is the ONLY flavour an unprivileged, Developer-Mode-off
		// user can create, so refusing to fall back would remove `watch`
		// from that entire population for no containment benefit — the tag
		// allowlist (Scope A) treats both flavours identically once created.
		setErr = setMountPointReparse(h, `\??\`+target, target)
	}
	if setErr != nil {
		_ = deleteByHandlePosix(h) // rule 1: self-heal through the handle already held, not a re-open by name
		return translateSetReparseErr(setErr)
	}
	return nil
}

// translateSetReparseErr maps FSCTL_SET_REPARSE_POINT's failure modes,
// principally ERROR_PRIVILEGE_NOT_HELD (measured: privilege removed +
// Developer Mode off refuses BOTH the symlink and, without
// ALLOW_UNPRIVILEGED_CREATE, the junction FSCTL too — since symlinkAt
// already tried the junction fallback above, reaching this function at all
// means both flavours failed).
func translateSetReparseErr(err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == windows.ERROR_PRIVILEGE_NOT_HELD {
		return errNoLinkPrivilege
	}
	return translateOpen("create link", err)
}

// readlinkAt reads the target of the link named name, relative to the open
// directory parent, WITHOUT resolving it, and returns an ERROR — not a
// value — for any tag outside watchTags, so shared Watch/browse code needs
// no tag awareness at all (ADR §3.3).
//
// Both required validations happen here, exactly once, so every call site
// (Watch's target read, openBrowsableDir/crossWatchBoundary's boundary
// crossing) gets them for free:
//   - SYMLINK_FLAG_RELATIVE is refused: a link created with it carries a
//     RELATIVE substitute name ("..\..\Users"), which CreateFile would
//     resolve against the process's current working directory — the store
//     itself never creates one (setSymlinkReparse always writes Flags=0),
//     but Scope A forgives a link the store did not create, so this is
//     reachable via a pre-existing or attacker-planted link.
//   - After stripping the \??\ (or \??\UNC\) prefix, the target must pass
//     validateAbsoluteWindowsPath — the SAME validator Root() uses (§4.1) —
//     and must not begin \??\Volume{ (§5.3: identical tag to a junction;
//     only the substitute name distinguishes a volume mount point, and
//     crossing one moves to a different volume with a different security
//     surface this design does not evaluate).
func readlinkAt(parent int, name string) (string, error) {
	h, err := ntOpenAt(windows.Handle(parent), name, fileReadAccess, windows.FILE_OPEN,
		windows.FILE_OPEN_REPARSE_POINT, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return "", translateOpen("readlink", err)
	}
	defer windows.CloseHandle(h)
	tag, substitute, relative, err := readLinkHandle(h)
	if err != nil {
		return "", err
	}
	if !isWatchTag(tag) {
		return "", fmt.Errorf("%q is a %s reparse point (tag 0x%08X), which is not an allowed watch boundary", name, tagName(tag), tag)
	}
	if relative {
		return "", fmt.Errorf("%q is a relative symlink, which this store never creates and will not follow", name)
	}
	if strings.HasPrefix(substitute, `\??\Volume{`) {
		return "", fmt.Errorf("%q is a volume mount point, which this store refuses to cross (it would move to a different volume)", name)
	}
	stripped := stripNTPrefix(substitute)
	if err := validateAbsoluteWindowsPath(stripped); err != nil {
		return "", fmt.Errorf("%q resolves to an unsupported target %q: %w", name, stripped, err)
	}
	return stripped, nil
}

// stripNTPrefix turns the \??\ (or \??\UNC\) substitute-name form stored in
// a reparse point into an ordinary Win32 path.
func stripNTPrefix(s string) string {
	if strings.HasPrefix(s, `\??\UNC\`) {
		return `\\` + s[len(`\??\UNC\`):]
	}
	return strings.TrimPrefix(s, `\??\`)
}

// unlinkAt removes the single directory entry name (a link, in every
// current caller) relative to the open directory parent. It opens the entry
// as the link itself (FILE_OPEN_REPARSE_POINT) and POSIX-disposes it —
// never follows, never descends. isLinkAt's caller (Unwatch/Delete) has
// already confirmed name is an allow-listed tag before this is called.
func unlinkAt(parent int, name string) error {
	h, err := ntOpenAt(windows.Handle(parent), name, windows.FILE_READ_ATTRIBUTES|windows.DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_DIRECTORY_FILE, windows.OBJ_CASE_INSENSITIVE, 0)
	if err != nil {
		return translateOpen("unlink", err)
	}
	defer windows.CloseHandle(h)
	return translateOpen("unlink", deleteByHandlePosix(h))
}

// isLinkAt classifies name, relative to the open directory parent, without
// following it: isLink is meaningful only when err == nil. This is
// statAt(parent, name).IsLink, named separately because it is the
// fd-relative decision Unwatch/Delete/annotate make about ONE entry they
// already hold a parent handle for (R5: classify from the handle, never
// fs.FileMode, never the name-surrogate bit).
func isLinkAt(parent int, name string) (isLink bool, err error) {
	m, err := statAt(parent, name)
	if err != nil {
		return false, err
	}
	return m.IsLink, nil
}

// sameWatchTarget is Watch's idempotence comparison (ADR §7.2). Byte-exact
// string comparison is wrong on Windows (abs is spelled as the user typed
// it; the substitute name is normalised), so this opens both no-follow and
// compares FILE_ID_INFO, falling back to a case-insensitive string
// comparison when either cannot be opened (e.g. the existing link is
// dangling). Identity is the LOOSER answer in the right direction: two
// spellings of one directory are one object.
func sameWatchTarget(existing, abs string) bool {
	fdA, errA := openAbsoluteDirNoFollowWin(existing)
	if errA == nil {
		defer closeFD(fdA)
	}
	fdB, errB := openAbsoluteDirNoFollowWin(abs)
	if errB == nil {
		defer closeFD(fdB)
	}
	if errA == nil && errB == nil {
		idA, e1 := objectIDOf(fdA)
		idB, e2 := objectIDOf(fdB)
		if e1 == nil && e2 == nil {
			return idA == idB
		}
	}
	return strings.EqualFold(existing, abs)
}

// isNotALinkAt reports whether err (from readlinkAt) means "name exists but
// is not a reparse point at all" — FSCTL_GET_REPARSE_POINT on a real
// directory fails with ERROR_NOT_A_REPARSE_POINT, raw and untranslated
// (readLinkHandle returns DeviceIoControl's error as-is). Watch's collision
// branch (store.go) uses this to give a bare real directory (interrupted
// two-step creation residue, or an ordinary published artifact) its own
// remediation message, distinct from "a link pointing elsewhere" and from
// "a reparse point on no allowlisted tag" (readlinkAt's own tag/relative/
// volume-mount-point refusals, which fall through to the generic branch).
func isNotALinkAt(err error) bool { return errors.Is(err, windows.ERROR_NOT_A_REPARSE_POINT) }

// IsLinkInfo/IsLinkEntry are the shared, read-only-listing classification
// helpers (List, Watches, WatchLinkFor's exported surface, plus
// internal/web and internal/watch call sites). Mode()&(ModeSymlink|
// ModeIrregular) != 0 is a MEASURED-correct over-approximation, not a
// guess (ADR §3.3): a junction is ModeIrregular and neither ModeSymlink nor
// ModeDir (P14.junction_modesymlink, P14.junction_not_dir REQUIRED); a
// non-Microsoft tag on a directory is ModeDir|ModeIrregular
// (RR1.unknown_tag_isdir); DEDUP/WOF report as ordinary regular files.
// Over-approximating "is a link" is fail-CLOSED for every consumer: it
// stops descent, it reports the entry in Watches, and it routes Delete to
// unlink-only. This is the Pre-1 fix (ADR §11): the FIRST trap in the old
// entryIsDir — "if e.IsDir() return true" running before any link test —
// is closed by store.go's classifyEntry, which never consults
// os.DirEntry.IsDir()/IsLinkEntry() at all for a mutation or list decision;
// these two functions remain for the read-only display/routing call sites
// that predate a parent handle being available (folderUnwatch,
// desiredDirs) and are never used to decide a descent in this package.
func IsLinkInfo(fi os.FileInfo) bool {
	return fi.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0
}

func IsLinkEntry(e os.DirEntry) bool {
	return e.Type()&(os.ModeSymlink|os.ModeIrregular) != 0
}
