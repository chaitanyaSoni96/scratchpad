// Package store is the shared filesystem-backed artifact store.
// The root directory (default ~/.scratchpad, overridable via SCRATCHPAD_ROOT)
// is the sole source of truth: an artifact is any directory that directly
// contains at least one .html file. Everything beneath an artifact directory
// is its assets; every directory above it (any depth) is its project path.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const RootEnv = "SCRATCHPAD_ROOT"

// errExists is the portable "name already claimed" sentinel returned by the
// platform-private create-only helpers (mkdirClaim, symlinkAt). It lets
// Publish and Watch recognize a collision with errors.Is without importing
// an OS-specific errno package (see platform-api-inventory.md).
var errExists = errors.New("scratchpad: name already exists")

// testStoreOpHook makes validation/use race tests deterministic. It is nil in
// production and deliberately sits after the parent descriptor is pinned.
var testStoreOpHook func(string)

func runStoreOpHook(op string) {
	if testStoreOpHook != nil {
		testStoreOpHook(op)
	}
}

type Artifact struct {
	Project string // slash-separated project path, "" for root-level artifacts
	Name    string
	Dir     string   // absolute path (may be or traverse a symlink)
	Entry   string   // entry html filename
	Pages   []string // all top-level html files, sorted
	Size    int64    // total bytes of all files in the artifact
	ModTime time.Time
	IsLink  bool // Dir itself is a symlink (a watched folder: delete = unlink)
	Linked  bool // Dir lives inside a watched folder; not deletable here
}

// entryMeta is the shared classification shape statAt fills in from a single
// no-follow open of one directory entry, relative to a pinned parent. It is
// the cross-platform "fstatat(AT_SYMLINK_NOFOLLOW)" twin: Tag is always 0 on
// Linux (there is only one link type there) and is the raw reparse tag on
// Windows, present so callers and error messages can name it. IsLink means
// "this entry is a link the store itself might create or remove" (a Linux
// symlink, or a Windows tag on the watch allowlist) — never "carries any
// reparse tag at all", which is why a Windows entry with a Tag the allowlist
// does not recognise reports IsLink == false (see classifyEntry).
type entryMeta struct {
	IsDir     bool // real directory, no reparse tag (Windows) / S_IFDIR (Linux)
	IsRegular bool
	IsLink    bool // an entry the store may create/remove as a watch link
	Tag       uint32
	Size      int64
	ModTime   time.Time
}

// classifyEntry is the single decision List/Watches/WatchLinkFor's
// handle-anchored walk uses to decide what a directory entry is, from
// statAt's tag-aware, no-follow classification alone — never from
// os.DirEntry.IsDir() and never from a path-based follow-through. This is
// the fix for both traps Pre-1 (ADR §11) named in the old path-based
// entryIsDir: a non-surrogate unknown reparse tag can report
// DirEntry.IsDir() == true (RR1.unknown_tag_isdir) despite being neither a
// real directory nor a link the store understands, and the old
// follow-through os.Stat resolved a link by path, which is exactly the
// A11-style redirection this store now refuses everywhere else. explore
// reports whether the entry should be considered at all (false for "an
// unrecognized reparse tag" — Scope C, never explored, never listed); m is
// meaningful only when explore is true.
func classifyEntry(parentFD int, name string) (m entryMeta, explore bool) {
	meta, err := statAt(parentFD, name)
	if err != nil {
		return entryMeta{}, false
	}
	if meta.Tag != 0 && !meta.IsLink {
		return entryMeta{}, false // Scope C: a reparse tag we do not understand
	}
	return meta, true
}

// MultiPage reports whether the artifact is a page collection: several
// top-level html files and no index.html to crown one of them the entry.
func (a Artifact) MultiPage() bool {
	return len(a.Pages) > 1 && a.Entry != "index.html"
}

// RelPath is the URL path segment(s) identifying the artifact.
func (a Artifact) RelPath() string {
	if a.Project == "" {
		return a.Name
	}
	return a.Project + "/" + a.Name
}

func Root() (string, error) {
	if r := os.Getenv(RootEnv); r != "" {
		return r, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".scratchpad"), nil
}

func EnsureRoot() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, nil
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

// validateName guards names the store creates — published artifacts, watch
// links and the project directories made for them. It also rules out names
// that are syntactically fine on Linux but are unportable to Windows (a
// reserved DOS device basename, or a trailing dot/space) — see
// checkPortableName — so a store built on one OS stays movable to the other.
func validateName(s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("invalid name %q: must match %s", s, nameRe.String())
	}
	if s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("invalid name %q", s)
	}
	if err := checkPortableName(s); err != nil {
		return err
	}
	return nil
}

// validateSegment guards a path segment the store only looks *up*: an
// existing entry named by a URL or by delete/unwatch. It is deliberately
// looser than validateName — a watched folder is the source repo's to name,
// so its spaces, unicode and (un-ignored) dot-directories must stay reachable
// — but keeps every traversal guard.
func validateSegment(s string) error {
	if s == "" || s == "." || s == ".." || len(s) > 255 {
		return fmt.Errorf("invalid path segment %q", s)
	}
	if strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("invalid path segment %q", s)
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("invalid path segment %q", s)
		}
	}
	// checkLookupSegmentPlatform (names_windows.go/names_linux.go) is the
	// ADR §7.5/R11 platform-pair extension: on Windows it additionally
	// refuses ':' (NTFS alternate-data-stream syntax — the one live case,
	// M12.C_stream), a trailing dot/space, and a reserved DOS device name,
	// none of which this function rejected before; it is a permanent no-op
	// on Linux, where ':' is an ordinary, legal filename character a watched
	// repository may legitimately use.
	return checkLookupSegmentPlatform(s)
}

// visibleSegments reports whether a lookup path is traversable: every segment
// syntactically safe and, while the path is still store tree, not hidden by
// ignore rules. It stops checking at an artifact directory — everything below
// one is that artifact's assets, served as published, not filtered.
func visibleSegments(root string, segs []string) bool {
	dir := root
	for _, s := range segs {
		if err := validateSegment(s); err != nil {
			return false
		}
		if hasHTML(dir) {
			return true // inside an artifact: the rest is assets
		}
		full := filepath.Join(dir, s)
		fi, err := os.Stat(full)
		if !Visible(dir, s, err == nil && fi.IsDir()) {
			return false
		}
		dir = full
	}
	return true
}

// VisiblePath reports whether a slash path under the root can be browsed:
// used by the web layer before it renders or lists anything for that path.
func VisiblePath(p string) bool {
	p = strings.Trim(p, "/")
	if p == "" {
		return true
	}
	root, err := Root()
	if err != nil {
		return false
	}
	return visibleSegments(root, strings.Split(p, "/"))
}

// ResolveFolder resolves a visible project folder. It permits crossing one
// symlink rooted in the store (a deliberate watch), but never follows another
// symlink inside that watched source tree.
func ResolveFolder(p string) (*os.File, bool) {
	root, err := Root()
	if err != nil || filepath.IsAbs(p) || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return nil, false
	}
	var segs []string
	if p != "" {
		segs = strings.Split(p, "/")
	}
	for _, s := range segs {
		if err := validateSegment(s); err != nil {
			return nil, false
		}
	}
	if !visibleSegments(root, segs) {
		return nil, false
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, false
	}
	defer rfs.close()
	fd, err := rfs.openBrowsableDir(segs)
	if err != nil {
		return nil, false
	}
	if hasHTML, _ := dirHasHTMLFD(fd); hasHTML {
		closeFD(fd)
		return nil, false
	}
	return os.NewFile(uintptr(fd), p), true
}

// SplitProject validates a slash-separated project path and returns its
// segments ([] for the empty path).
func SplitProject(project string) ([]string, error) {
	if project == "" {
		return nil, nil
	}
	segs := strings.Split(strings.Trim(project, "/"), "/")
	for _, s := range segs {
		if err := validateName(s); err != nil {
			return nil, err
		}
	}
	return segs, nil
}

// artifactDir joins root/project.../name after validation and verifies the
// result stays inside root.
func artifactDir(root, project, name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	segs, err := SplitProject(project)
	if err != nil {
		return "", err
	}
	parts := append([]string{root}, append(segs, name)...)
	return joinInRoot(root, parts)
}

// existingDir is artifactDir for entries the store only looks up or removes:
// same containment guarantee, lookup validation instead of the create-time
// naming rules, so delete and unwatch reach everything the UI can show.
func existingDir(root, project, name string) (string, error) {
	segs := strings.Split(strings.Trim(project, "/"), "/")
	if project == "" {
		segs = nil
	}
	for _, s := range append(append([]string{}, segs...), name) {
		if err := validateSegment(s); err != nil {
			return "", err
		}
	}
	return joinInRoot(root, append([]string{root}, append(segs, name)...))
}

// joinInRoot joins parts and verifies the result stays inside root.
func joinInRoot(root string, parts []string) (string, error) {
	dir := filepath.Join(parts...)
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes root")
	}
	return dir, nil
}

// hasHTML reports whether dir directly contains a regular *.html file.
func hasHTML(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
			return true
		}
	}
	return false
}

// annotate fills the symlink-related fields of an artifact from a
// handle-relative, no-follow classification of (parentFD, name) — never
// from a path. This replaces a latent defect (ADR §6.8 item 2): two former
// callers loaded an artifact through a Linux-only /proc/self/fd handle path,
// which os.Lstat always reports as a symlink regardless of what the fd
// points to, so annotate must run at the call site using the parent handle
// and the entry's own name — never a.Dir — once Project/Name are set.
// isLinkAt is the same fd-relative primitive Unwatch/Delete already use to
// make this decision, so there is exactly one "is this entry a link"
// mutation-path check in the whole store.
func annotate(a *Artifact, parentFD int, name string) {
	if isLink, err := isLinkAt(parentFD, name); err == nil && isLink {
		a.IsLink = true
		return
	}
	if link := WatchLinkFor(a.RelPath()); link != "" {
		a.Linked = true
	}
}

// maxArtifactWalkDepth bounds the handle-anchored walks below (R16): List's
// descent into project trees and loadArtifactAt's own size/mtime walk inside
// one artifact. Neither platform enforced a bound before this; it is a
// carried-forward gap being closed here, not a regression (ADR §4.5's
// "carried-forward gap" note, §11 P3.9 fixes the analogous removeTreeAt bound).
const maxArtifactWalkDepth = 64

// sizeWalkAt sums file sizes and finds the newest mtime under the directory
// named by dirFD, handle-anchored and depth-bounded (R16). A link entry
// (Scope A tag on Windows, any symlink on Linux) is never descended for size
// accounting, matching filepath.WalkDir's own no-follow default, which this
// replaces; an entry with an unrecognized reparse tag (Scope C) is likewise
// skipped, never explored.
func sizeWalkAt(dirFD, depth int) (size int64, modTime time.Time) {
	if depth > maxArtifactWalkDepth {
		return 0, time.Time{}
	}
	entries, err := readDirFD(dirFD)
	if err != nil {
		return 0, time.Time{}
	}
	for _, e := range entries {
		name := e.Name()
		meta, explore := classifyEntry(dirFD, name)
		if !explore || meta.IsLink {
			continue
		}
		if meta.IsDir {
			childFD, err := openRealDirAt(dirFD, name)
			if err != nil {
				continue
			}
			s, m := sizeWalkAt(childFD, depth+1)
			closeFD(childFD)
			size += s
			if m.After(modTime) {
				modTime = m
			}
			continue
		}
		if !meta.IsRegular {
			continue
		}
		size += meta.Size
		if meta.ModTime.After(modTime) {
			modTime = meta.ModTime
		}
	}
	return size, modTime
}

// loadArtifactAt reads the directory named by dirFD — a pinned handle to the
// artifact's own directory, already resolved past any link by the caller —
// and builds the Artifact if it qualifies (contains at least one top-level
// .html). It leaves IsLink/Linked/Dir unset: callers must set Dir to the
// artifact's logical display path and call annotate(&a, parentFD, name)
// themselves — see annotate's doc comment. This replaces loadArtifact +
// fdPath, which had no Windows equivalent (ADR §6.8): fdPath was a
// /proc/self/fd/N string fed back into os.ReadDir/os.Stat/filepath.WalkDir,
// none of which exist without a real filesystem path, and all of which are
// gone from this function.
func loadArtifactAt(project, name string, dirFD int) (Artifact, bool) {
	entries, err := readDirFD(dirFD)
	if err != nil {
		return Artifact{}, false // tolerate concurrent deletes
	}
	var htmls, pages []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".html":
			htmls = append(htmls, e.Name())
			pages = append(pages, e.Name())
		case ".md":
			pages = append(pages, e.Name())
		}
	}
	if len(htmls) == 0 {
		return Artifact{}, false // only html qualifies a dir as an artifact
	}
	sort.Strings(htmls)
	sort.Strings(pages)
	entry := htmls[0]
	for _, h := range htmls {
		if h == "index.html" {
			entry = h
			break
		}
	}
	a := Artifact{Project: project, Name: name, Entry: entry, Pages: pages}
	if m, err := statSelf(dirFD); err == nil {
		a.ModTime = m.ModTime
	}
	// ModTime is the newest mtime in the tree, not the directory's: editing a
	// file in place leaves the directory untouched, and the web UI keys
	// preview iframes on the modtime to reload them when content changes.
	size, newest := sizeWalkAt(dirFD, 0)
	a.Size = size
	if newest.After(a.ModTime) {
		a.ModTime = newest
	}
	return a, true
}

// List walks the whole root and returns all artifacts, newest first.
// Artifact directories are not descended into: their subtrees are assets.
// Handle-anchored end to end (ADR §6.8 item 4): classifyEntry decides
// real-directory-vs-link-vs-unrecognized-tag from a single no-follow stat of
// each entry, and crossWatchBoundary is the same mechanism openBrowsableDir
// uses for the one link crossing invariant 5 permits per branch. The visited
// set is keyed on objectID (R16), not on a resolved path string, so it is
// meaningful on both platforms and cannot be defeated by two path spellings
// of one directory.
func List() ([]Artifact, error) {
	root, err := EnsureRoot()
	if err != nil {
		return nil, err
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, err
	}
	defer rfs.close()
	rootFD, err := dupFD(int(rfs.root.Fd()))
	if err != nil {
		return nil, err
	}
	var out []Artifact
	visited := map[objectID]bool{}
	var walk func(dirFD int, dirPath, project string, crossedLink, ownFD bool, depth int)
	walk = func(dirFD int, dirPath, project string, crossedLink, ownFD bool, depth int) {
		if ownFD {
			defer closeFD(dirFD)
		}
		if depth > maxArtifactWalkDepth {
			return
		}
		if id, err := objectIDOf(dirFD); err != nil || visited[id] {
			return
		} else {
			visited[id] = true
		}
		entries, err := readDirFD(dirFD)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			if !Visible(dirPath, name, true) {
				continue
			}
			meta, explore := classifyEntry(dirFD, name)
			if !explore || meta.IsLink && crossedLink {
				continue
			}
			var childFD int
			if meta.IsLink {
				childFD, err = crossWatchBoundary(dirFD, name)
			} else if meta.IsDir {
				childFD, err = openRealDirAt(dirFD, name)
			} else {
				continue
			}
			if err != nil {
				continue
			}
			sub := filepath.Join(dirPath, name)
			if a, ok := loadArtifactAt(project, name, childFD); ok {
				a.Dir = sub
				annotate(&a, dirFD, name)
				out = append(out, a)
				closeFD(childFD)
				continue
			}
			child := name
			if project != "" {
				child = project + "/" + name
			}
			walk(childFD, sub, child, crossedLink || meta.IsLink, true, depth+1)
		}
	}
	walk(rootFD, root, "", false, true, 0)
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	return out, nil
}

// Resolve returns the artifact at project/name if the directory qualifies.
func Resolve(project, name string) (Artifact, bool, error) {
	root, err := Root()
	if err != nil {
		return Artifact{}, false, err
	}
	dir, err := existingDir(root, project, name)
	if err != nil {
		return Artifact{}, false, err
	}
	// existingDir already validated project/name with the LOOSER lookup
	// rules (validateSegment) a watched folder's own name needs — do not
	// re-split with SplitProject, which enforces validateName's stricter
	// creation-time regex and would wrongly refuse a lookup existingDir just
	// allowed.
	var segs []string
	if project != "" {
		segs = strings.Split(strings.Trim(project, "/"), "/")
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return Artifact{}, false, err
	}
	defer rfs.close()
	parent, err := rfs.openBrowsableDir(segs)
	if err != nil {
		return Artifact{}, false, err
	}
	defer closeFD(parent)
	childFD, err := crossOrOpen(parent, name)
	if err != nil {
		return Artifact{}, false, nil
	}
	defer closeFD(childFD)
	a, ok := loadArtifactAt(project, name, childFD)
	if ok {
		a.Dir = dir
		annotate(&a, parent, name)
	}
	return a, ok, nil
}

// crossOrOpen opens name relative to parent for read-only classification:
// a real directory via the strict primitive, or the one allowed link
// boundary via crossWatchBoundary — the same two-way choice List's walk and
// openBrowsableDir make, exposed for the single-entry callers (Resolve).
func crossOrOpen(parent int, name string) (int, error) {
	meta, explore := classifyEntry(parent, name)
	if !explore {
		return -1, fmt.Errorf("%q is not a usable entry", name)
	}
	if meta.IsLink {
		return crossWatchBoundary(parent, name)
	}
	if meta.IsDir {
		return openRealDirAt(parent, name)
	}
	return -1, fmt.Errorf("%q is not a directory", name)
}

// ReadDirHandle, EntryIsDirAt and StatEntryAt are the three exported
// helpers ADR §6.8 item 5 names for internal/web's four remaining
// /proc/self/fd sites (docCount, buildCards/folderExtras, siblings,
// hasRenderable in server.go) to pass ResolveFolder's already-pinned
// *os.File through instead of re-deriving a path that has no Windows
// analogue. That refactor is P4.6's, not done here — these are the
// primitives it needs, added now so it does not also have to invent
// platform mechanism.
func ReadDirHandle(dir *os.File) ([]os.DirEntry, error) { return readDirFD(int(dir.Fd())) }

// EntryIsDirAt reports whether e (an entry of dir, as read by
// ReadDirHandle) should be treated as browsable: a real directory, or a
// link on the watch allowlist — the same classifyEntry decision List uses,
// so a junction reads as browsable here exactly as it does there.
func EntryIsDirAt(dir *os.File, e os.DirEntry) bool {
	m, explore := classifyEntry(int(dir.Fd()), e.Name())
	return explore && (m.IsDir || m.IsLink)
}

// StatEntryAt is statAt(dir.Fd(), name) reduced to the fields
// pageCard/docCount-style preview-weight accounting needs (§6.9 row 3),
// without exposing the unexported entryMeta type across the package
// boundary.
func StatEntryAt(dir *os.File, name string) (isDir bool, size int64, modTime time.Time, err error) {
	m, err := statAt(int(dir.Fd()), name)
	if err != nil {
		return false, 0, time.Time{}, err
	}
	return m.IsDir, m.Size, m.ModTime, nil
}

// ResolvePath walks URL path segments and splits them into the artifact and
// the file path inside it: the artifact is the shallowest prefix directory
// that directly contains html.
func ResolvePath(segs []string) (a Artifact, file string, ok bool) {
	root, err := Root()
	if err != nil {
		return Artifact{}, "", false
	}
	if !visibleSegments(root, segs) {
		return Artifact{}, "", false
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return Artifact{}, "", false
	}
	defer rfs.close()
	for i, s := range segs {
		parent, openErr := rfs.openBrowsableDir(segs[:i])
		if openErr != nil {
			return Artifact{}, "", false
		}
		fd, openErr := crossOrOpen(parent, s)
		if openErr != nil {
			closeFD(parent)
			return Artifact{}, "", false
		}
		hasHTML, _ := dirHasHTMLFD(fd)
		if hasHTML {
			project := strings.Join(segs[:i], "/")
			a, ok := loadArtifactAt(project, s, fd)
			closeFD(fd)
			if !ok {
				closeFD(parent)
				return Artifact{}, "", false
			}
			a.Dir = filepath.Join(append([]string{root}, segs[:i+1]...)...)
			annotate(&a, parent, s)
			closeFD(parent)
			return a, strings.Join(segs[i+1:], "/"), true
		}
		closeFD(fd)
		closeFD(parent)
	}
	return Artifact{}, "", false
}

// ResolveDoc resolves URL segments to a loose markdown file living in plain
// project directories (not inside any artifact — ResolvePath covers those).
func ResolveDoc(segs []string) (string, bool) {
	if len(segs) == 0 || !strings.HasSuffix(strings.ToLower(segs[len(segs)-1]), ".md") {
		return "", false
	}
	root, err := Root()
	if err != nil {
		return "", false
	}
	if !visibleSegments(root, segs) {
		return "", false
	}
	f, ok := OpenDocument(segs)
	if !ok {
		return "", false
	}
	f.Close()
	return filepath.Join(append([]string{root}, segs...)...), true
}

// ValidateFilePath checks a relative asset path for a published file: every
// segment must be a valid name, with no traversal or absolute paths.
func ValidateFilePath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) {
		return fmt.Errorf("invalid file path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if err := validateName(seg); err != nil {
			return fmt.Errorf("invalid file path %q: %w", p, err)
		}
	}
	return nil
}

// Publish creates a new artifact from a set of relative-path files. It is
// create-only: publishing over an existing directory is an error — deletion
// is the user's action in the web UI. At least one top-level .html is required.
func Publish(project, name string, files map[string][]byte) (Artifact, error) {
	entryFound := false
	for p := range files {
		if err := ValidateFilePath(p); err != nil {
			return Artifact{}, err
		}
		if !strings.Contains(p, "/") && strings.HasSuffix(strings.ToLower(p), ".html") {
			entryFound = true
		}
	}
	if !entryFound {
		return Artifact{}, errors.New("at least one top-level .html file is required (index.html preferred)")
	}
	root, err := EnsureRoot()
	if err != nil {
		return Artifact{}, err
	}
	dir, err := artifactDir(root, project, name)
	if err != nil {
		return Artifact{}, err
	}
	segs, _ := SplitProject(project)
	rfs, err := openRootedFS(true)
	if err != nil {
		return Artifact{}, err
	}
	defer rfs.close()
	parent, err := rfs.openRealDir(segs, true, true)
	if err != nil {
		return Artifact{}, err
	}
	defer closeFD(parent)
	runStoreOpHook("publish-claim")
	// mkdirClaim (os.Mkdir, not MkdirAll) atomically claims the name:
	// concurrent publishes and existing artifacts both surface as errExists.
	if err := mkdirClaim(parent, name); err != nil {
		if errors.Is(err, errExists) {
			return Artifact{}, fmt.Errorf("%q already exists — names are not reusable until the user deletes the old artifact in the web UI; pick a different name (see `scratchpad list`)", strings.TrimPrefix(dir[len(root):], string(filepath.Separator)))
		}
		return Artifact{}, err
	}
	artifactFD, err := openDirAt(parent, name)
	if err != nil {
		_ = rmdirAt(parent, name)
		return Artifact{}, err
	}
	defer closeFD(artifactFD)
	cleanup := func() { _ = removeTreeAt(parent, name) }
	for p, content := range files {
		if err := writeFileAt(artifactFD, strings.Split(p, "/"), content); err != nil {
			cleanup()
			return Artifact{}, err
		}
	}
	a, ok := loadArtifactAt(project, name, artifactFD)
	a.Dir = dir
	if !ok {
		cleanup()
		return Artifact{}, errors.New("publish verification failed")
	}
	annotate(&a, parent, name)
	return a, nil
}

// Watch symlinks an external directory into the store so it is hosted
// live. The target may be a single artifact folder (contains html) or a
// whole tree of artifact folders. Create-only like Publish, with one
// exception: re-watching the same folder under the same name is a no-op
// rather than an error, so the call is safe to repeat.
func Watch(project, name, target string) (string, error) {
	abs, err := filepath.Abs(target)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s is not a directory", abs)
	}
	root, err := EnsureRoot()
	if err != nil {
		return "", err
	}
	// real is abs with every symlink component resolved away — including a
	// symlinked ANCESTOR of abs, not just abs itself if it happens to be a
	// link. This is the creation-time half of the ancestor-symlink
	// trade-off (see openAbsoluteDirNoFollow in storefs_linux.go for the
	// browse-time half): a legitimately symlinked ancestor (e.g. /home
	// itself is a symlink on some systems, or the caller reached the
	// target through a convenience symlink) is resolved ONCE, here, so
	// Watch keeps working for that case exactly as it always did — the
	// watch link stored on disk points at the real, symlink-free path, not
	// the possibly-symlinked one the caller typed. The cost: if an
	// ancestor symlink used to reach the target is later repointed
	// elsewhere, the existing watch keeps serving the directory it
	// resolved to at watch time, not the new one — re-run `scratchpad
	// watch` to pick up the move. What Watch refuses to do is silently
	// paper over an ancestor that CANNOT be resolved right now (a symlink
	// loop, a dangling link, a permission error partway down): that fails
	// the call immediately, with the OS's own reason attached, rather than
	// leaving the caller to discover a broken watch only when a browse
	// later 404s.
	real, err := canonicalizeWatchTarget(abs)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", abs, err)
	}
	if alreadyInsideRoot(real, root) {
		return "", fmt.Errorf("%s is already inside the scratchpad", abs)
	}
	link, err := artifactDir(root, project, name)
	if err != nil {
		return "", err
	}
	segs, _ := SplitProject(project)
	rfs, err := openRootedFS(true)
	if err != nil {
		return "", err
	}
	defer rfs.close()
	parent, err := rfs.openRealDir(segs, true, true)
	if err != nil {
		return "", err
	}
	defer closeFD(parent)
	runStoreOpHook("watch-link")
	// symlinkAt is atomic like mkdirClaim in Publish: errExists if taken. The
	// one exception is a link that already points at this exact folder —
	// that is the state the caller asked for, so re-watching is a no-op and
	// an agent can run watch unconditionally instead of probing `watches`
	// first. The link is created pointing at real (the resolved path), not
	// abs, so every subsequent browse walks a target with no symlinks left
	// in its ancestry at all — see openAbsoluteDirNoFollow.
	if err := symlinkAt(parent, real, name); err != nil {
		if !errors.Is(err, errExists) {
			return "", err
		}
		linkTarget, readErr := readlinkAt(parent, name)
		if readErr != nil || !sameWatchTarget(linkTarget, real) {
			return "", fmt.Errorf("%q already exists — delete it in the web UI or pick a different name", strings.TrimPrefix(link[len(root):], string(filepath.Separator)))
		}
	}
	if !hasHTML(real) {
		fmt.Fprintln(os.Stderr, "note: no top-level .html in the watched folder — it will be treated as a project tree; only subfolders containing .html will show up")
	}
	return link, nil
}

// WatchLink is one watch symlink in the store.
type WatchLink struct {
	Path   string // slash path under the root
	Target string // the source directory the link points at
}

// WatchLinkFor returns the slash path of the watch link governing rel — rel
// itself when it is the link, an ancestor when rel lives inside a watched
// tree — or "" when nothing on the path is a link.
func WatchLinkFor(rel string) string {
	if rel == "" {
		return ""
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return ""
	}
	defer rfs.close()
	fd, err := dupFD(int(rfs.root.Fd()))
	if err != nil {
		return ""
	}
	segs := strings.Split(rel, "/")
	for i, seg := range segs {
		meta, explore := classifyEntry(fd, seg)
		if !explore {
			closeFD(fd)
			return ""
		}
		if meta.IsLink {
			closeFD(fd)
			return strings.Join(segs[:i+1], "/")
		}
		if !meta.IsDir {
			closeFD(fd)
			return ""
		}
		next, err := openRealDirAt(fd, seg)
		closeFD(fd)
		if err != nil {
			return ""
		}
		fd = next
	}
	closeFD(fd)
	return ""
}

// Watches lists every watch link in the store, shallowest first. Watched
// trees and artifact subtrees are not descended into: symlinks below them
// belong to the source folder, not to the store.
func Watches() ([]WatchLink, error) {
	root, err := EnsureRoot()
	if err != nil {
		return nil, err
	}
	rfs, err := openRootedFS(false)
	if err != nil {
		return nil, err
	}
	defer rfs.close()
	rootFD, err := dupFD(int(rfs.root.Fd()))
	if err != nil {
		return nil, err
	}
	var out []WatchLink
	visited := map[objectID]bool{}
	var walk func(dirFD int, dirPath, rel string, depth int)
	walk = func(dirFD int, dirPath, rel string, depth int) {
		defer closeFD(dirFD)
		if depth > maxArtifactWalkDepth {
			return
		}
		if id, err := objectIDOf(dirFD); err != nil || visited[id] {
			return
		} else {
			visited[id] = true
		}
		entries, err := readDirFD(dirFD)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			sub, child := filepath.Join(dirPath, name), name
			if rel != "" {
				child = rel + "/" + name
			}
			// Links are reported whatever the ignore rules say: hiding a
			// watched folder from the UI must not strand its link with no
			// way to list or unwatch it. Rules only prune the descent. An
			// unrecognized reparse tag (Scope C) is never listed here — it
			// gets its own inert "unsupported entry" affordance (RW15,
			// owner P4.6), not a phantom watch.
			meta, explore := classifyEntry(dirFD, name)
			if !explore {
				continue
			}
			if meta.IsLink {
				if !linkTargetIsDir(dirFD, name) {
					continue // a file-type link is not a watched folder
				}
				if target, err := readlinkAt(dirFD, name); err == nil {
					out = append(out, WatchLink{Path: child, Target: target})
				}
				continue
			}
			if !meta.IsDir || !Visible(dirPath, name, true) {
				continue
			}
			childFD, err := openRealDirAt(dirFD, name)
			if err != nil {
				continue
			}
			if hasHTML, _ := dirHasHTMLFD(childFD); hasHTML {
				closeFD(childFD)
				continue
			}
			walk(childFD, sub, child, depth+1)
		}
	}
	walk(rootFD, root, "", 0)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Unwatch removes a watch link, leaving the source folder untouched. Unlike
// Delete it refuses anything that is not itself a symlink, so it can never
// destroy stored files — and it works for watched project trees, which are
// not artifacts and therefore not reachable through Delete.
func Unwatch(project, name string) error {
	annotationGuard, err := lockAnnotations(true)
	if err != nil {
		return err
	}
	defer annotationGuard.Close()
	root, err := Root()
	if err != nil {
		return err
	}
	_, err = existingDir(root, project, name)
	if err != nil {
		return err
	}
	rel := (Artifact{Project: project, Name: name}).RelPath()
	segs := strings.Split(strings.Trim(project, "/"), "/")
	if project == "" {
		segs = nil
	}
	rfs, openErr := openRootedFS(false)
	if openErr != nil {
		return openErr
	}
	defer rfs.close()
	parent, openErr := rfs.openRealDir(segs, false, false)
	if openErr != nil {
		if link := WatchLinkFor(rel); link != "" {
			return fmt.Errorf("%s is not a watch link — it lives inside watched folder %q; unwatch that instead", rel, link)
		}
		return fmt.Errorf("refusing to unwatch through a symlinked project: %w", openErr)
	}
	defer closeFD(parent)
	runStoreOpHook("unwatch")
	isLink, statErr := isLinkAt(parent, name)
	if statErr != nil {
		return fmt.Errorf("%s not found", rel)
	}
	if !isLink {
		if link := WatchLinkFor(rel); link != "" {
			return fmt.Errorf("%s is not a watch link — it lives inside watched folder %q; unwatch that instead", rel, link)
		}
		return fmt.Errorf("%s is not a watched folder", rel)
	}
	if err := unlinkAt(parent, name); err != nil {
		return err
	}
	pruneAt(rfs, segs)
	// A name freed by unwatch is reusable (publish/watch is create-only), so
	// any notes left over from the unwatched folder must go with it or a
	// same-named artifact published later would inherit them.
	return removeNotesFor(annotationGuard, (Artifact{Project: project, Name: name}).RelPath())
}

// Delete removes an artifact and prunes any project directories left empty
// above it. Watched folders (symlinks) are only unlinked — the source is
// never touched — and nothing inside a watched folder can be deleted here.
func Delete(project, name string) error {
	annotationGuard, err := lockAnnotations(true)
	if err != nil {
		return err
	}
	defer annotationGuard.Close()
	root, err := Root()
	if err != nil {
		return err
	}
	_, err = existingDir(root, project, name)
	if err != nil {
		return err
	}
	segs := strings.Split(strings.Trim(project, "/"), "/")
	if project == "" {
		segs = nil
	}
	rfs, openErr := openRootedFS(false)
	if openErr != nil {
		return openErr
	}
	defer rfs.close()
	parent, openErr := rfs.openRealDir(segs, false, false)
	if openErr != nil {
		return fmt.Errorf("%s is inside a watched folder — its files live at the source; unwatch the link instead", (Artifact{Project: project, Name: name}).RelPath())
	}
	defer closeFD(parent)
	runStoreOpHook("delete")
	if isLink, statErr := isLinkAt(parent, name); statErr == nil && isLink {
		// Unwatch: remove only the link, never the source.
		if err := unlinkAt(parent, name); err != nil {
			return err
		}
	} else {
		artifactFD, err := openDirAt(parent, name)
		if err == nil {
			hasHTML, htmlErr := dirHasHTMLFD(artifactFD)
			closeFD(artifactFD)
			if htmlErr != nil || !hasHTML {
				err = fmt.Errorf("could not verify %q is an artifact", name)
			}
		}
		if err != nil {
			return fmt.Errorf("artifact %s not found", (Artifact{Project: project, Name: name}).RelPath())
		}
		if err := removeTreeAt(parent, name); err != nil {
			return err
		}
	}
	pruneAt(rfs, segs)
	// Names are reusable once the user deletes here (Publish/Watch are
	// create-only), so a re-published/re-watched artifact of the same name
	// must start with a clean slate rather than inheriting the old one's notes.
	return removeNotesFor(annotationGuard, (Artifact{Project: project, Name: name}).RelPath())
}
