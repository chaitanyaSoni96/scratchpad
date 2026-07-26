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
	Dir     string // absolute path
	Entry   string // entry html filename
	Size    int64  // total bytes of all files in the artifact
	ModTime time.Time
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

func loadArtifact(project, name, dir string) (Artifact, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return Artifact{}, false // tolerate concurrent deletes
	}
	var htmls []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(strings.ToLower(e.Name()), ".html") {
			htmls = append(htmls, e.Name())
		}
	}
	if len(htmls) == 0 {
		return Artifact{}, false
	}
	sort.Strings(htmls)
	entry := htmls[0]
	for _, h := range htmls {
		if h == "index.html" {
			entry = h
			break
		}
	}
	a := Artifact{Project: project, Name: name, Dir: dir, Entry: entry}
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if fi, err := d.Info(); err == nil {
			a.Size += fi.Size()
		}
		return nil
	})
	if fi, err := os.Stat(dir); err == nil {
		a.ModTime = fi.ModTime()
	}
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
	var walk func(dir, project string)
	walk = func(dir, project string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
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

// Delete removes an artifact directory and prunes any project directories
// left empty above it.
func Delete(project, name string) error {
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
	root, err := Root()
	if err != nil {
		return nil
	}
	for dir := filepath.Dir(a.Dir); dir != root && strings.HasPrefix(dir, root); dir = filepath.Dir(dir) {
		if rem, err := os.ReadDir(dir); err != nil || len(rem) > 0 {
			break
		}
		if os.Remove(dir) != nil {
			break
		}
	}
	return nil
}
