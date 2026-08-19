// Package store is the shared filesystem-backed artifact store.
// The root directory (default ~/.scratchpad, overridable via SCRATCHPAD_ROOT)
// is the sole source of truth: an artifact is any directory that directly
// contains at least one .html file. Everything beneath an artifact directory
// is its assets; every directory above it (any depth) is its project path.
package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const RootEnv = "SCRATCHPAD_ROOT"

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
// links and the project directories made for them.
func validateName(s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("invalid name %q: must match %s", s, nameRe.String())
	}
	if s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("invalid name %q", s)
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
	return nil
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

// entryIsDir reports whether a directory entry is a directory, following
// symlinks (watched folders appear as symlinks in the store).
func entryIsDir(parent string, e os.DirEntry) bool {
	if e.IsDir() {
		return true
	}
	if e.Type()&os.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(parent, e.Name()))
	return err == nil && fi.IsDir()
}

// annotate fills the symlink-related fields of an artifact.
func annotate(a *Artifact) {
	if fi, err := os.Lstat(a.Dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		a.IsLink = true
		return
	}
	real, err := filepath.EvalSymlinks(a.Dir)
	if err == nil && real != a.Dir {
		a.Linked = true
	}
}

func loadArtifact(project, name, dir string) (Artifact, bool) {
	entries, err := os.ReadDir(dir)
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
	a := Artifact{Project: project, Name: name, Dir: dir, Entry: entry, Pages: pages}
	sizeRoot := dir
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		sizeRoot = real // WalkDir does not follow a symlinked root
	}
	if fi, err := os.Stat(dir); err == nil {
		a.ModTime = fi.ModTime()
	}
	// ModTime is the newest mtime in the tree, not the directory's: editing a
	// file in place leaves the directory untouched, and the web UI keys
	// preview iframes on the modtime to reload them when content changes.
	filepath.WalkDir(sizeRoot, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			a.Size += fi.Size()
			if fi.ModTime().After(a.ModTime) {
				a.ModTime = fi.ModTime()
			}
		}
		return nil
	})
	annotate(&a)
	return a, true
}

// List walks the whole root and returns all artifacts, newest first.
// Artifact directories are not descended into: their subtrees are assets.
func List() ([]Artifact, error) {
	root, err := EnsureRoot()
	if err != nil {
		return nil, err
	}
	var out []Artifact
	visited := map[string]bool{} // symlink-cycle guard
	var walk func(dir, project string)
	walk = func(dir, project string) {
		if real, err := filepath.EvalSymlinks(dir); err != nil || visited[real] {
			return
		} else {
			visited[real] = true
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !entryIsDir(dir, e) || !Visible(dir, e.Name(), true) {
				continue
			}
			sub := filepath.Join(dir, e.Name())
			if a, ok := loadArtifact(project, e.Name(), sub); ok {
				out = append(out, a)
				continue
			}
			child := e.Name()
			if project != "" {
				child = project + "/" + e.Name()
			}
			walk(sub, child)
		}
	}
	walk(root, "")
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
	a, ok := loadArtifact(project, name, dir)
	return a, ok, nil
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
	for i, s := range segs {
		dir := filepath.Join(append([]string{root}, segs[:i+1]...)...)
		if hasHTML(dir) {
			project := strings.Join(segs[:i], "/")
			a, ok := loadArtifact(project, s, dir)
			if !ok {
				return Artifact{}, "", false
			}
			return a, strings.Join(segs[i+1:], "/"), true
		}
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
	p := filepath.Join(append([]string{root}, segs...)...)
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return "", false
	}
	return p, true
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
// is a human action in the web UI. At least one top-level .html is required.
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
	// No ancestor may itself be an artifact: artifacts cannot nest.
	segs, _ := SplitProject(project)
	for i := range segs {
		anc := filepath.Join(append([]string{root}, segs[:i+1]...)...)
		if hasHTML(anc) {
			return Artifact{}, fmt.Errorf("%q is an artifact, not a project — artifacts cannot contain artifacts", strings.Join(segs[:i+1], "/"))
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return Artifact{}, err
	}
	// os.Mkdir (not MkdirAll) atomically claims the name: concurrent
	// publishes and existing artifacts both surface as EEXIST.
	if err := os.Mkdir(dir, 0o755); err != nil {
		if os.IsExist(err) {
			return Artifact{}, fmt.Errorf("%q already exists — names are not reusable until a human deletes the old artifact in the web UI; pick a different name (see `scratchpad list`)", strings.TrimPrefix(dir[len(root):], string(filepath.Separator)))
		}
		return Artifact{}, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	for p, content := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			cleanup()
			return Artifact{}, err
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			cleanup()
			return Artifact{}, err
		}
	}
	a, ok := loadArtifact(project, name, dir)
	if !ok {
		cleanup()
		return Artifact{}, errors.New("publish verification failed")
	}
	return a, nil
}

// Watch symlinks an external directory into the store so it is hosted
// live. The target may be a single artifact folder (contains html) or a
// whole tree of artifact folders. Create-only, like Publish.
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
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		if realRoot, err := filepath.EvalSymlinks(root); err == nil {
			if real == realRoot || strings.HasPrefix(real, realRoot+string(filepath.Separator)) {
				return "", fmt.Errorf("%s is already inside the scratchpad", abs)
			}
		}
	}
	link, err := artifactDir(root, project, name)
	if err != nil {
		return "", err
	}
	segs, _ := SplitProject(project)
	for i := range segs {
		if hasHTML(filepath.Join(append([]string{root}, segs[:i+1]...)...)) {
			return "", fmt.Errorf("%q is an artifact, not a project", strings.Join(segs[:i+1], "/"))
		}
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		return "", err
	}
	// os.Symlink is atomic like os.Mkdir in Publish: EEXIST if taken.
	if err := os.Symlink(abs, link); err != nil {
		if os.IsExist(err) {
			return "", fmt.Errorf("%q already exists — delete it in the web UI or pick a different name", strings.TrimPrefix(link[len(root):], string(filepath.Separator)))
		}
		return "", err
	}
	if !hasHTML(abs) {
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
	root, err := Root()
	if err != nil || rel == "" {
		return ""
	}
	segs := strings.Split(rel, "/")
	cur := root
	for i, seg := range segs {
		cur = filepath.Join(cur, seg)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return strings.Join(segs[:i+1], "/")
		}
	}
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
	var out []WatchLink
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			name := e.Name()
			sub, child := filepath.Join(dir, name), name
			if rel != "" {
				child = rel + "/" + name
			}
			// Links are reported whatever the ignore rules say: hiding a
			// watched folder from the UI must not strand its link with no
			// way to list or unwatch it. Rules only prune the descent.
			if e.Type()&os.ModeSymlink != 0 {
				target, lerr := os.Readlink(sub)
				fi, serr := os.Stat(sub)
				if lerr == nil && serr == nil && fi.IsDir() {
					out = append(out, WatchLink{Path: child, Target: target})
				}
				continue
			}
			if e.IsDir() && Visible(dir, name, true) && !hasHTML(sub) {
				walk(sub, child)
			}
		}
	}
	walk(root, "")
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Unwatch removes a watch link, leaving the source folder untouched. Unlike
// Delete it refuses anything that is not itself a symlink, so it can never
// destroy stored files — and it works for watched project trees, which are
// not artifacts and therefore not reachable through Delete.
func Unwatch(project, name string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	dir, err := existingDir(root, project, name)
	if err != nil {
		return err
	}
	rel := (Artifact{Project: project, Name: name}).RelPath()
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("%s not found", rel)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		if link := WatchLinkFor(rel); link != "" {
			return fmt.Errorf("%s is not a watch link — it lives inside watched folder %q; unwatch that instead", rel, link)
		}
		return fmt.Errorf("%s is not a watched folder", rel)
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	pruneEmpty(root, dir)
	// A name freed by unwatch is reusable (publish/watch is create-only), so
	// any notes left over from the unwatched folder must go with it or a
	// same-named artifact published later would inherit them.
	removeNotesFor((Artifact{Project: project, Name: name}).RelPath())
	return nil
}

// pruneEmpty removes project directories left empty above a removed entry.
func pruneEmpty(root, dir string) {
	for d := filepath.Dir(dir); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if rem, err := os.ReadDir(d); err != nil || len(rem) > 0 {
			break
		}
		if os.Remove(d) != nil {
			break
		}
	}
}

// Delete removes an artifact and prunes any project directories left empty
// above it. Watched folders (symlinks) are only unlinked — the source is
// never touched — and nothing inside a watched folder can be deleted here.
func Delete(project, name string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	dir, err := existingDir(root, project, name)
	if err != nil {
		return err
	}
	// Refuse when any parent component is a symlink: the files belong to
	// the watched source folder.
	cur := root
	for _, seg := range strings.Split(strings.TrimPrefix(dir[len(root):], string(filepath.Separator)), string(filepath.Separator)) {
		cur = filepath.Join(cur, seg)
		if cur == dir {
			break
		}
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s is inside watched folder %q — its files live at the source; unwatch the link instead", (Artifact{Project: project, Name: name}).RelPath(), strings.TrimPrefix(cur[len(root):], string(filepath.Separator)))
		}
	}
	if fi, err := os.Lstat(dir); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		// Unwatch: remove only the link, never the source.
		if err := os.Remove(dir); err != nil {
			return err
		}
	} else {
		a, ok, err := Resolve(project, name)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("artifact %s not found", (Artifact{Project: project, Name: name}).RelPath())
		}
		if err := os.RemoveAll(a.Dir); err != nil {
			return err
		}
	}
	pruneEmpty(root, dir)
	// Names are reusable once a human deletes here (Publish/Watch are
	// create-only), so a re-published/re-watched artifact of the same name
	// must start with a clean slate rather than inheriting the old one's notes.
	removeNotesFor((Artifact{Project: project, Name: name}).RelPath())
	return nil
}
