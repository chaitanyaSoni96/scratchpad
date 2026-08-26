//go:build linux

package store

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// rootedFS keeps operations anchored to the inode opened as SCRATCHPAD_ROOT.
type rootedFS struct {
	root *os.File
}

func openRootedFS(create bool) (*rootedFS, error) {
	root, err := Root()
	if err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	// Deterministic-race hook (ADR §11/R17, "root-open"): fires after the root
	// handle is pinned but before the caller does anything else with it, so a
	// test can substitute the root out from under an in-flight operation
	// (A4.root_replaced.*-style attacks) without a timing loop.
	runStoreOpHook("root-open")
	return &rootedFS{root: os.NewFile(uintptr(fd), root)}, nil
}

func (r *rootedFS) close() error { return r.root.Close() }

// nameEquals is the platform pair Visible's reserved-name check (ignore.go)
// uses to compare a candidate entry name against AnnotationsDir/lockFileName
// (ADR §7.4). Linux is byte-exact, matching ext4/most Linux filesystems'
// case-sensitive namespace.
func nameEquals(a, b string) bool { return a == b }

// canonicalLookupName is the third member of the §7.4 name-comparison
// platform pair (alongside nameEquals here and matchName in names_linux.go).
// It answers "what is this entry ACTUALLY called on disk", for a single
// requester-supplied lookup segment, so visibleSegments (store.go) decides
// visibility on the filesystem's own spelling rather than on the spelling the
// requester typed.
//
// On Linux it is a compile-time no-op and must stay one: a Linux filesystem
// namespace is byte-exact, there is no alias for a name to be resolved from,
// and asking the OS to canonicalise here would only introduce a path
// re-resolution with nothing to gain. See storefs_windows.go's twin for the
// hazard it exists to close (P6.3 F1: an NTFS 8.3 short name is a second,
// requester-typeable spelling of a name that Visible's reserved-name check
// and every defaultIgnores/.scratchpadignore rule compare as a string).
func canonicalLookupName(dir, name string) string { return name }

func dupFD(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 0) }

// closeFD closes fd, discarding the error like every existing call site
// already did (most are deferred; the rest are best-effort cleanup on an
// error path already being reported through a different return value).
func closeFD(fd int) { unix.Close(fd) }

// mkdirClaim atomically claims name as a new directory relative to the open
// directory parent: os.Mkdir's create-only guarantee, fd-relative. A
// collision (a race or an existing artifact) surfaces as errExists so
// callers never need to import unix to recognize it.
func mkdirClaim(parent int, name string) error {
	if err := unix.Mkdirat(parent, name, 0o755); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errExists
		}
		return err
	}
	return nil
}

// rmdirAt removes the single empty directory entry name relative to the
// open directory parent — used to roll back a claim that failed after
// mkdirClaim succeeded (Publish) so a partially created name is not left
// behind.
func rmdirAt(parent int, name string) error {
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}

// dirHasHTMLFD reports whether fd directly contains a regular *.html file.
// The error return exists so callers can choose fail-open or fail-closed on
// "could not tell" (a read error): storefs_windows.go's twin has the same
// shape for the same reason (ADR §4.3, "one place the prototype is stricter
// than Linux — keep it"). openBrowsableDir's boundary guard fails CLOSED on
// error (refuses to cross when it cannot rule out an artifact); openRealDir's
// artifact-nesting guard keeps its historical fail-open-on-error behaviour,
// since it only gates directory *creation*, not a security boundary crossing.
func dirHasHTMLFD(fd int) (bool, error) {
	entries, err := readDirFD(fd)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		var st unix.Stat_t
		if strings.HasSuffix(strings.ToLower(e.Name()), ".html") && unix.Fstatat(fd, e.Name(), &st, unix.AT_SYMLINK_NOFOLLOW) == nil && st.Mode&unix.S_IFMT == unix.S_IFREG {
			return true, nil
		}
	}
	return false, nil
}

// objectID is the Linux dev+ino twin of the Windows FILE_ID_INFO-based type
// (ADR §3.2): comparable, opaque to shared code, never rendered into a path.
type objectID struct{ dev, ino uint64 }

func objectIDOf(fd int) (objectID, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return objectID{}, err
	}
	return objectID{dev: uint64(st.Dev), ino: st.Ino}, nil
}

// entryMeta is the shared (store.go) classification shape statAt fills in.
// See store.go's doc comment on the type for the cross-platform contract.

// statAt is fstatat(parent, name, AT_SYMLINK_NOFOLLOW): classification of a
// single directory entry from a pinned parent, without following it. This is
// the Linux twin of the Windows strict-open-plus-tag-read primitive (ADR
// §3.2) — on Linux there is only one link type, so "IsLink" is simply
// S_IFLNK and "Tag" is always 0.
func statAt(parent int, name string) (entryMeta, error) {
	if strings.ContainsAny(name, `\/`) {
		return entryMeta{}, fmt.Errorf("statAt requires a single component, got %q", name)
	}
	var st unix.Stat_t
	if err := unix.Fstatat(parent, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return entryMeta{}, err
	}
	m := entryMeta{
		Size:    st.Size,
		ModTime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec),
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m.IsDir = true
	case unix.S_IFREG:
		m.IsRegular = true
	case unix.S_IFLNK:
		m.IsLink = true
	}
	return m, nil
}

// openRealDirAt is openDirAt under the ADR §3.2 name shared code uses so the
// same call sites compile against either platform's strict primitive: on
// Linux, O_NOFOLLOW already refuses every link at the final component, so
// this is a thin alias, not a second mechanism.
func openRealDirAt(parent int, name string) (int, error) { return openDirAt(parent, name) }

// statSelf classifies the handle itself (Fstat, not Fstatat) — used for a
// directory's own ModTime baseline in loadArtifactAt, where there is no
// parent/name pair to statAt with.
func statSelf(fd int) (entryMeta, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return entryMeta{}, err
	}
	m := entryMeta{Size: st.Size, ModTime: time.Unix(st.Mtim.Sec, st.Mtim.Nsec)}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		m.IsDir = true
	case unix.S_IFREG:
		m.IsRegular = true
	case unix.S_IFLNK:
		m.IsLink = true
	}
	return m, nil
}

// linkTargetIsDir reports whether the link entry (parent, name) — already
// known via classifyEntry to be a link — points at a directory. Unlike
// Windows, a Linux symlink carries no separate "directory reparse point"
// bit of its own (statAt/lstat always report IsDir=false for any symlink,
// directory-target or not), so this does the one bounded follow Scope A
// already permits at exactly this boundary — the same semantics the
// pre-refactor os.Stat(filepath.Join(parent, name)) had, now
// handle-relative instead of path-based. It is used only for listing
// decisions (Watches); every mutation path classifies via statAt/isLinkAt
// alone and never calls this.
func linkTargetIsDir(parent int, name string) bool {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	unix.Close(fd)
	return true
}

// crossWatchBoundary is the ONE forgiven link-boundary crossing (invariant
// 5, ADR §4.3), factored out of openBrowsableDir so store.go's handle-
// anchored List/Watches walk can reuse the identical mechanism instead of a
// second, weaker copy. name must already be known to be a link entry
// (typically via statAt's IsLink) relative to the pinned parent.
func crossWatchBoundary(parent int, name string) (int, error) {
	buf := make([]byte, 4096)
	n, err := unix.Readlinkat(parent, name, buf)
	if err != nil {
		return -1, err
	}
	return openAbsoluteDirNoFollow(string(buf[:n]))
}

// openRealDir walks only real directory entries. The returned descriptor owns
// its reference, so renaming any checked ancestor cannot redirect later work.
func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	for i, seg := range segs {
		next, openErr := openDirAt(fd, seg)
		if openErr != nil && create && errors.Is(openErr, unix.ENOENT) {
			if mkErr := unix.Mkdirat(fd, seg, 0o755); mkErr != nil && !errors.Is(mkErr, unix.EEXIST) {
				unix.Close(fd)
				return -1, mkErr
			}
			next, openErr = openDirAt(fd, seg)
		}
		if openErr != nil {
			// A directory-target symlink at the final requested component can
			// surface as either ELOOP or ENOTDIR depending on kernel/
			// filesystem — verified on a real ext4/6.12 kernel:
			// O_DIRECTORY|O_NOFOLLOW against a symlink returns ENOTDIR, not
			// ELOOP (openBrowsableDir below already accounts for both, for
			// the same reason). This branch used to check ELOOP only, so on
			// that kernel the friendly "is a symlink" message was dead code
			// and callers saw a bare "not a directory" with no mention of the
			// symlink. But ENOTDIR is also exactly what a plain FILE at this
			// name produces, so it must not be assumed to mean "symlink"
			// without checking: classify the entry (still relative to the
			// still-open fd, no re-resolution by path) before wording the
			// message, and fall back to the raw error for a non-link cause.
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				if meta, statErr := statAt(fd, seg); statErr == nil && meta.IsLink {
					unix.Close(fd)
					return -1, fmt.Errorf("project ancestor %q is a symlink", strings.Join(segs[:i+1], "/"))
				}
			}
			unix.Close(fd)
			return -1, openErr
		}
		unix.Close(fd)
		fd = next
		if rejectArtifacts {
			// Fail-open on a read error here (does not gate a security
			// boundary — only whether directory creation is permitted at
			// this name).
			if hasHTML, _ := dirHasHTMLFD(fd); hasHTML {
				unix.Close(fd)
				return -1, fmt.Errorf("%q is an artifact, not a project", strings.Join(segs[:i+1], "/"))
			}
		}
	}
	return fd, nil
}

func mkdirsAt(root int, segs []string) (int, error) {
	fd, err := dupFD(root)
	if err != nil {
		return -1, err
	}
	for _, seg := range segs {
		next, e := openDirAt(fd, seg)
		if errors.Is(e, unix.ENOENT) {
			if e = unix.Mkdirat(fd, seg, 0o755); e == nil {
				next, e = openDirAt(fd, seg)
			}
		}
		unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
	}
	return fd, nil
}

func writeFileAt(root int, segs []string, data []byte) error {
	parent, err := mkdirsAt(root, segs[:len(segs)-1])
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	fd, err := unix.Openat(parent, segs[len(segs)-1], unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o644)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), segs[len(segs)-1])
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

func openFileAt(parent int, name string) (*os.File, error) {
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		if err == nil {
			err = fmt.Errorf("%s is not a regular file", name)
		}
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func readAllAt(parent int, name string) ([]byte, error) {
	f, err := openFileAt(parent, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func pruneAt(r *rootedFS, segs []string) {
	for i := len(segs); i > 0; i-- {
		parent, err := r.openRealDir(segs[:i-1], false, false)
		if err != nil {
			return
		}
		err = unix.Unlinkat(parent, segs[i-1], unix.AT_REMOVEDIR)
		unix.Close(parent)
		if err != nil {
			return
		}
	}
}

// openBrowsableDir permits exactly one symlink entry: the store-owned watch
// boundary. All source-tree descendants are opened without following links.
func (r *rootedFS) openBrowsableDir(segs []string) (int, error) {
	fd, err := dupFD(int(r.root.Fd()))
	if err != nil {
		return -1, err
	}
	crossed := false
	for _, seg := range segs {
		next, openErr := openDirAt(fd, seg)
		if (errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR)) && !crossed {
			// Fail CLOSED here: this guards a security boundary (invariant
			// 5's single crossing), unlike openRealDir's fail-open use above.
			// A read error must not be treated as "not an artifact" — ADR
			// §4.3, "one place the prototype is stricter than Linux — keep
			// it".
			hasHTML, htmlErr := dirHasHTMLFD(fd)
			if htmlErr == nil && !hasHTML {
				next, openErr = crossWatchBoundary(fd, seg)
				if openErr == nil {
					crossed = true
				}
			}
		}
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
		// Deterministic-race hook ("browse-segment"): fires once per resolved
		// path component, after this segment is pinned and before the next is
		// opened, so a test can substitute an already-passed ancestor mid-walk.
		runStoreOpHook("browse-segment")
	}
	return fd, nil
}

// openFilesystemRootNoFollow opens the OS filesystem root "/" as the
// trusted, fixed anchor for walking an absolute watch-target path
// component by component (openAbsoluteDirNoFollow below). "/" is the root
// of the process's mount namespace: open(2) resolves it specially and it
// cannot itself be a symlink, so there is no earlier, more-trusted point to
// pin the walk to than this one. Every step after this is handle-relative,
// so nothing between here and the final component is ever resolved from a
// path string again.
func openFilesystemRootNoFollow() (int, error) {
	return unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
}

// openAbsoluteDirNoFollow opens target — the string read back from the
// store's one permitted watch-link boundary — by walking its components
// one at a time from the filesystem root (openFilesystemRootNoFollow),
// refusing to traverse a symlink, or anything that is not a plain
// directory, at ANY component, not just the last one.
//
// This replaces unix.Open(target, ...|O_NOFOLLOW), which protected only the
// FINAL path component: every intermediate component was still resolved by
// the kernel following symlinks normally. Watch stores a
// filepath.EvalSymlinks-resolved target (store.go, Watch), so at the
// instant a watch is created there are no symlinks anywhere in this path —
// but the store never re-checked that on any later browse. If something
// later replaced a directory on this path with a symlink — content inside
// the watched source doing it (a `git checkout` swapping a tracked
// directory for a symlink is the realistic case, not only a hostile local
// process with write access above the watched folder) or an ancestor
// outside it being repointed — the old code silently followed it on every
// subsequent browse, and the redirected tree was reachable over the
// unauthenticated HTTP endpoint (`internal/web`). This is
// A11.ancestor_swapped, spike-findings.md §10.1: "the structural fix on
// both platforms is to walk the target's components handle-by-handle
// instead of opening the string."
//
// Handle-by-handle closes it: each step opens the next component relative
// to the PREVIOUS step's descriptor with openDirAt (O_NOFOLLOW), so there
// is no path string left, at any point, for a later substitution to
// redirect — only a chain of already-pinned descriptors. A symlink at any
// component surfaces as ELOOP (or ENOTDIR for a non-directory) and the walk
// fails closed; the caller (openBrowsableDir) treats that exactly like the
// old open failing. This is a read-only path: every mutation walk
// (openRealDir) was already handle-relative end to end and is unaffected.
//
// Ancestor symlinks that are legitimate (a moved /home, a convenience
// symlink the caller watched through) are handled at Watch-creation time,
// not here: Watch resolves and stores the fully-real target, so a
// symlink-free path is exactly what a normal, unmolested watch presents to
// this function on every browse. Anything this function refuses is
// therefore a path that changed since the watch was created — which is the
// condition the fix exists to catch.
func openAbsoluteDirNoFollow(target string) (int, error) {
	// filepath.IsAbs on Linux is exactly this prefix check; avoiding the
	// import keeps this file's dependency list unchanged. Watch only ever
	// stores an EvalSymlinks-resolved absolute path as a link target, so a
	// relative one here means the store's own symlink was not written by
	// Watch. There is no safe anchor to walk a relative path from — opening
	// it as a bare string (what the old code implicitly did for even an
	// absolute target) would resolve it against the process's current
	// working directory, exactly the ambient-authority mistake this
	// function exists to remove — so refuse rather than guess an anchor.
	if !strings.HasPrefix(target, "/") {
		return -1, fmt.Errorf("watch link target %q is not an absolute path", target)
	}
	fd, err := openFilesystemRootNoFollow()
	if err != nil {
		return -1, err
	}
	for _, seg := range strings.Split(target, "/") {
		if seg == "" {
			continue // leading "/", a trailing "/", or "//" between components
		}
		next, openErr := openDirAt(fd, seg)
		unix.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

// canonicalizeWatchTarget is filepath.EvalSymlinks, unchanged: the Linux half
// of the ADR §4.3/§4.7 platform pair. See store.go's Watch for why the
// result — not abs — is what gets stored as the link target.
func canonicalizeWatchTarget(target string) (string, error) {
	return filepath.EvalSymlinks(target)
}

// alreadyInsideRoot is the pre-existing "already inside the scratchpad"
// guard (ADR §7.1), byte-identical to the code it replaces: EvalSymlinks on
// the root, then a string-prefix test against the already-canonicalized
// target.
func alreadyInsideRoot(target, root string) bool {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return false
	}
	return target == realRoot || strings.HasPrefix(target, realRoot+string(filepath.Separator))
}

func openPathFile(segs []string) (*os.File, error) {
	if len(segs) == 0 {
		return nil, unix.ENOENT
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
	defer unix.Close(parent)
	// Deterministic-race hook ("doc-open"): fires after the parent directory
	// is pinned and before the final document open, so a test can substitute
	// the document itself in that window (A10.rename_race-style attacks).
	runStoreOpHook("doc-open")
	return openFileAt(parent, segs[len(segs)-1])
}

// OpenDocument pins a validated store-relative regular file for HTTP serving.
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
