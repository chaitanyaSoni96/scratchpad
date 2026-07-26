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
	Dir     string // absolute path (may be or traverse a symlink)
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

// defaultIgnores keeps repo-scale watched trees sane: these directories are
// invisible to scanning and to the filesystem watcher. Extend with a
// comma-separated SCRATCHPAD_IGNORE.
var defaultIgnores = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true,
	"target": true, "__pycache__": true, "venv": true, ".venv": true,
	"coverage": true, "bin": true, "obj": true,
}

// Ignored reports whether a directory name is skipped during scans and
// watching (dot-dirs are always skipped by callers).
func Ignored(name string) bool {
	if defaultIgnores[name] {
		return true
	}
	for _, extra := range strings.Split(os.Getenv("SCRATCHPAD_IGNORE"), ",") {
		if extra != "" && name == strings.TrimSpace(extra) {
			return true
		}
	}
	return false
}

var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`)

func validateName(s string) error {
	if !nameRe.MatchString(s) {
		return fmt.Errorf("invalid name %q: must match %s", s, nameRe.String())
	}
	if s == "." || s == ".." || strings.ContainsAny(s, `/\`) {
		return fmt.Errorf("invalid name %q", s)
	}
	return nil
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
			if !entryIsDir(dir, e) || strings.HasPrefix(e.Name(), ".") || Ignored(e.Name()) {
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
	dir, err := artifactDir(root, project, name)
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
	for i, s := range segs {
		if validateName(s) != nil {
			return Artifact{}, "", false
		}
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
	for _, s := range segs {
		if validateName(s) != nil {
			return "", false
		}
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
			return Artifact{}, fmt.Errorf("%q already exists — names are not reusable until a human deletes the old artifact in the web UI; pick a different name (see list_artifacts)", strings.TrimPrefix(dir[len(root):], string(filepath.Separator)))
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

// Delete removes an artifact and prunes any project directories left empty
// above it. Watched folders (symlinks) are only unlinked — the source is
// never touched — and nothing inside a watched folder can be deleted here.
func Delete(project, name string) error {
	root, err := Root()
	if err != nil {
		return err
	}
	dir, err := artifactDir(root, project, name)
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
			return fmt.Errorf("%s is inside watched folder %q — its files live at the source; delete the watch link instead", (Artifact{Project: project, Name: name}).RelPath(), strings.TrimPrefix(cur[len(root):], string(filepath.Separator)))
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
	for d := filepath.Dir(dir); d != root && strings.HasPrefix(d, root); d = filepath.Dir(d) {
		if rem, err := os.ReadDir(d); err != nil || len(rem) > 0 {
			break
		}
		if os.Remove(d) != nil {
			break
		}
	}
	return nil
}
