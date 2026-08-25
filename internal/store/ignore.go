package store

import (
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// IgnoreFile is the per-directory ignore file. One may sit at the store root
// (rules for the whole store) and one in any directory below it, including
// inside a watched source folder — a repo can carry its own, which is what
// makes `include .gitignore` useful: the referenced file sits right next to
// it, so its patterns are anchored where they were written.
const IgnoreFile = ".scratchpadignore"

const (
	// maxIncludeDepth bounds include chains; cycles are caught separately.
	maxIncludeDepth = 8
	// ignoreCacheTTL is how long a parsed file is trusted before its sources
	// are re-stat'ed. Rendering a folder page consults the rules once per
	// directory entry, so re-reading every time would turn a page load into
	// thousands of opens; a second of staleness is invisible next to the
	// 250ms debounce on the change stream that redraws the page anyway.
	ignoreCacheTTL = time.Second
)

// defaultIgnores is the built-in ruleset: everything under the root is
// visible unless it matches one of these. It is written in .scratchpadignore
// syntax and parsed by the same code, so it behaves exactly like a file one
// level shallower than the root's — any `!line` in a real ignore file
// overrides it.
//
// The entries earn their place two ways, and nothing else belongs here: dirs
// whose *cost* would sink a watched repo (thousands of directories, an
// inotify watch each, churning on every build or git command), and files
// whose *contents* should not be one URL away from a LAN-visible server.
// Ordinary dot-folders — .agents, .github, .claude — are none of that, and
// are shown.
const defaultIgnores = `
# Version control and tooling state
.git/
.hg/
.svn/
.jj/
.terraform/
.gradle/
.tox/

# Dependency, cache and build output
node_modules/
vendor/
dist/
build/
target/
obj/
bin/
coverage/
__pycache__/
venv/
.venv/
.direnv/
.cache/
.mypy_cache/
.pytest_cache/
.ruff_cache/
.pnpm-store/
.next/
.nuxt/
.svelte-kit/
.turbo/
.parcel-cache/

# Credentials and machine-local state
.ssh/
.gnupg/
.aws/
.env
.env.*
.netrc
.npmrc
*.pem

# Editor and OS noise
.idea/
.DS_Store
.scratchpadignore
`

// ignoreRule is one parsed pattern line.
type ignoreRule struct {
	segs    []string // pattern split on "/"; "**" matches any number of segments
	negate  bool     // "!pattern": un-ignore, beating the built-in defaults
	dirOnly bool     // "pattern/": matches directories only
}

// ignoreSet is everything one directory's ignore file has to say, in file
// order (last match wins), plus the files it was built from so the cache can
// tell when it went stale.
type ignoreSet struct {
	rules []ignoreRule
	srcs  []ignoreSrc
}

// ignoreSrc is a file the set was parsed from — the ignore file itself and
// anything it included. Absent files are recorded too, so creating one
// invalidates the cached "no rules here".
type ignoreSrc struct {
	path   string
	exists bool
	mod    time.Time
	size   int64
}

func statSrc(path string) ignoreSrc {
	src := ignoreSrc{path: path}
	if fi, err := os.Stat(path); err == nil {
		src.exists, src.mod, src.size = true, fi.ModTime(), fi.Size()
	}
	return src
}

// fresh reports whether every source is still exactly as it was when parsed.
func (s *ignoreSet) fresh() bool {
	for _, src := range s.srcs {
		if cur := statSrc(src.path); cur != src {
			return false
		}
	}
	return true
}

// decide runs rel (the candidate's path relative to the directory holding
// this ignore file, split into segments) past every rule. The last match
// wins, matching gitignore; ok is false when nothing matched at all, which
// leaves the decision to a shallower file or to the defaults.
func (s *ignoreSet) decide(rel []string, isDir bool) (ignore, ok bool) {
	for _, r := range s.rules {
		if r.dirOnly && !isDir {
			continue
		}
		if matchSegments(r.segs, rel) {
			ignore, ok = !r.negate, true
		}
	}
	return ignore, ok
}

// matchSegments matches a segmented pattern against a segmented path. Each
// non-"**" segment is a path.Match glob against one path segment; "**" spans
// any number of segments, including none.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, err := path.Match(pat[0], segs[0]); err != nil || !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
}

// stripComment removes a trailing comment and surrounding space. A "#" only
// starts a comment at the start of the line or after whitespace, so
// "draft#1" is a literal name; "\#" escapes a leading hash.
func stripComment(line string) string {
	if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
		return ""
	}
	for i := 1; i < len(line); i++ {
		if line[i] == '#' && (line[i-1] == ' ' || line[i-1] == '\t') {
			line = line[:i]
			break
		}
	}
	return strings.TrimSpace(line)
}

// parseRule turns one pattern line into a rule. Anchoring follows gitignore:
// a pattern containing a slash anywhere but the end is matched from the
// directory holding the ignore file; a bare name matches at any depth below
// it.
func parseRule(line string) (ignoreRule, bool) {
	var r ignoreRule
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = strings.TrimSpace(line[1:])
	}
	line = strings.TrimPrefix(line, `\`) // \# and \! are literal
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimSuffix(line, "/")
	}
	anchored := strings.Contains(line, "/")
	line = strings.Trim(line, "/")
	if line == "" {
		return r, false
	}
	r.segs = strings.Split(line, "/")
	if !anchored {
		r.segs = append([]string{"**"}, r.segs...)
	}
	return r, true
}

// loadIgnoreSet parses dir's ignore file. `include <path>` merges another
// file's patterns — the path is resolved relative to dir, and the merged
// patterns stay anchored at dir, so `include .gitignore` in a repo root means
// what the repo's own .gitignore means.
func loadIgnoreSet(dir string) *ignoreSet {
	set := &ignoreSet{}
	seen := map[string]bool{}
	var read func(file string, depth int)
	read = func(file string, depth int) {
		key := file
		if real, err := filepath.EvalSymlinks(file); err == nil {
			key = real
		}
		if seen[key] || depth > maxIncludeDepth {
			return
		}
		seen[key] = true
		src := statSrc(file)
		set.srcs = append(set.srcs, src)
		if !src.exists {
			return
		}
		data, err := os.ReadFile(file)
		if err != nil {
			return
		}
		set.parse(string(data), func(rest string) {
			read(filepath.Join(dir, filepath.FromSlash(rest)), depth+1)
		})
	}
	read(filepath.Join(dir, IgnoreFile), 0)
	return set
}

// parse appends every pattern line in body, handing `include` directives to
// onInclude (nil for the built-in defaults, which include nothing).
func (s *ignoreSet) parse(body string, onInclude func(string)) {
	for _, line := range strings.Split(body, "\n") {
		line = stripComment(strings.TrimSuffix(line, "\r"))
		if line == "" {
			continue
		}
		if rest, ok := cutInclude(line); ok {
			if onInclude != nil {
				onInclude(rest)
			}
			continue
		}
		if r, ok := parseRule(line); ok {
			s.rules = append(s.rules, r)
		}
	}
}

var (
	defaultOnce sync.Once
	defaultSet  ignoreSet
)

// defaultRules is the parsed built-in ruleset, consulted before any file.
func defaultRules() *ignoreSet {
	defaultOnce.Do(func() { defaultSet.parse(defaultIgnores, nil) })
	return &defaultSet
}

// cutInclude recognises an `include <path>` directive.
func cutInclude(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, "include")
	if !ok || rest == "" || (rest[0] != ' ' && rest[0] != '\t') {
		return "", false
	}
	rest = strings.Trim(strings.TrimSpace(rest), `"'`)
	return rest, rest != ""
}

type ignoreEntry struct {
	set     *ignoreSet
	checked time.Time
}

var (
	ignoreMu    sync.Mutex
	ignoreCache = map[string]ignoreEntry{}
)

// ignoreSetFor returns dir's parsed rules, re-reading them when the files
// behind them changed.
func ignoreSetFor(dir string) *ignoreSet {
	now := time.Now()
	ignoreMu.Lock()
	e, cached := ignoreCache[dir]
	ignoreMu.Unlock()
	if cached && now.Sub(e.checked) < ignoreCacheTTL {
		return e.set
	}
	if cached && e.set.fresh() {
		ignoreMu.Lock()
		if current, ok := ignoreCache[dir]; ok && current.set == e.set {
			current.checked = now
			ignoreCache[dir] = current
		}
		ignoreMu.Unlock()
		return e.set
	}
	set := loadIgnoreSet(dir)
	ignoreMu.Lock()
	// Publish only if the snapshot is still current; filesystem I/O stays unlocked.
	current, exists := ignoreCache[dir]
	if exists && (!cached || current.set != e.set) {
		set = current.set
	} else {
		ignoreCache[dir] = ignoreEntry{set: set, checked: now}
	}
	ignoreMu.Unlock()
	return set
}

// resetIgnoreCache drops every parsed ignore file. Tests use it to make rule
// edits take effect without waiting out the TTL.
func resetIgnoreCache() {
	ignoreMu.Lock()
	ignoreCache = map[string]ignoreEntry{}
	ignoreMu.Unlock()
}

// Visible reports whether the entry named name inside directory dir is shown
// by scanning, by the filesystem watcher and by the web UI. dir must be a
// path under the store root (watched folders included — they are reached
// through their symlink, so their own ignore files are found in place).
//
// Everything is visible unless a rule hides it. The built-in defaults are
// consulted first, then every .scratchpadignore from the root down: the
// deepest file wins and, within one file, the last matching line wins. So a
// `!pattern` anywhere overrides a default, and a dot-folder like .agents
// needs no rule at all — only the genuinely costly or sensitive ones are
// hidden to begin with.
func Visible(dir, name string, isDir bool) bool {
	visible := true
	decide := func(set *ignoreSet, rel []string) {
		if ignore, ok := set.decide(rel, isDir); ok {
			visible = !ignore
		}
	}
	root, err := Root()
	if err != nil {
		decide(defaultRules(), []string{name})
		return visible
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		decide(defaultRules(), []string{name}) // outside the store: no files apply
		return visible
	}
	// The top-level .annotations directory is system metadata (note sidecar
	// files), not user content: it must never be reachable no matter what a
	// .scratchpadignore says. It isn't in defaultIgnores because that ruleset
	// is meant to be overridable by a "!" line, and this must not be. Checked
	// on name alone (not isDir) so a path is refused even before anything is
	// on disk to stat — e.g. VisiblePath on a not-yet-created sidecar. A
	// .annotations directory nested deeper than the root is ordinary content.
	if rel == "." && name == AnnotationsDir {
		return false
	}
	var segs []string
	if rel != "." {
		segs = strings.Split(rel, string(filepath.Separator))
	}
	// Candidate path relative to the root, then to each deeper ignore file.
	path := append(append(make([]string, 0, len(segs)+1), segs...), name)
	decide(defaultRules(), path)
	cur := root
	for i := 0; ; i++ {
		decide(ignoreSetFor(cur), path[i:])
		if i == len(segs) {
			return visible
		}
		cur = filepath.Join(cur, segs[i])
	}
}
