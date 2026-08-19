package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// AnnotationsDir is the top-level directory holding every document's note
// sidecar, mirroring the store's own tree: a document at <doc-path> keeps
// its notes at <root>/.annotations/<doc-path>.json. It is system metadata,
// not content — Visible hides it unconditionally (see ignore.go) and
// validateName already rejects it as a publishable/watchable name, so it
// can never collide with a real artifact.
const AnnotationsDir = ".annotations"

// Quote is a W3C-style text quote selector: the exact matched text plus
// enough of its surroundings to disambiguate repeated occurrences.
type Quote struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

// Target is where a note is anchored inside its document.
type Target struct {
	Type        string `json:"type"` // "element" | "text"
	Selector    string `json:"selector,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Quote       *Quote `json:"quote,omitempty"`
}

// Reply is one entry in a note's thread: a comment, or a resolve/reopen
// event (which is just a comment carrying an Action).
type Reply struct {
	By      string    `json:"by"` // "agent" | "user"
	Created time.Time `json:"created"`
	Action  string    `json:"action,omitempty"` // "" | "resolve" | "reopen"
	Body    string    `json:"body,omitempty"`
}

// Annotation is one comment anchored to one target, with its reply thread.
type Annotation struct {
	ID      string     `json:"id"`
	Created time.Time  `json:"created"`
	Updated *time.Time `json:"updated,omitempty"`
	Status  string     `json:"status"` // "open" | "resolved"
	Body    string     `json:"body"`
	Target  Target     `json:"target"`
	Replies []Reply    `json:"replies,omitempty"`
}

// NotesFile is the sidecar document for one subject: all of its
// annotations plus the revision counter used for optimistic concurrency.
type NotesFile struct {
	Rev         int          `json:"rev"`
	Annotations []Annotation `json:"annotations"`
}

// DocNotes pairs a store-relative document path with its notes, used when
// aggregating over more than one document (WalkNotes).
type DocNotes struct {
	Doc   string    `json:"doc"`
	Notes NotesFile `json:"notes"`
}

// ErrRevMismatch is returned by SaveNotes when the caller's expectRev no
// longer matches the file on disk — someone else wrote it first.
var ErrRevMismatch = errors.New("annotations: rev mismatch, refetch and retry")

// ErrNoteNotFound is returned by ResolveNote/ReplyNote when id does not
// name an existing annotation on doc (including when doc has no notes at
// all).
var ErrNoteNotFound = errors.New("annotations: note not found")

// annotationsRoot is <root>/.annotations, the base every sidecar path is
// joined and containment-checked against.
func annotationsRoot() (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, AnnotationsDir), nil
}

// hasDocExt reports whether name's extension marks it as an annotatable
// document (the last path segment of a doc path).
func hasDocExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".html" || ext == ".md"
}

// notesPath maps a store-relative document path to its sidecar JSON file,
// mirroring the doc's own path under .annotations. Every segment goes
// through validateSegment (traversal-safe, same as any lookup path), the
// final segment must look like an annotatable document, and the result is
// containment-checked against .annotations itself.
func notesPath(doc string) (string, error) {
	annRoot, err := annotationsRoot()
	if err != nil {
		return "", err
	}
	doc = strings.Trim(doc, "/")
	if doc == "" {
		return "", fmt.Errorf("invalid doc path %q", doc)
	}
	segs := strings.Split(doc, "/")
	for _, s := range segs {
		if err := validateSegment(s); err != nil {
			return "", fmt.Errorf("invalid doc path %q: %w", doc, err)
		}
	}
	if !hasDocExt(segs[len(segs)-1]) {
		return "", fmt.Errorf("invalid doc path %q: must be .html or .md", doc)
	}
	p, err := joinInRoot(annRoot, append([]string{annRoot}, segs...))
	if err != nil {
		return "", err
	}
	return p + ".json", nil
}

// DocExists reports whether doc names a real, visible document: a top-level
// page of a published or watched artifact, or a loose markdown file in a
// project folder. It is built entirely on ResolvePath/ResolveDoc, which
// already enforce visibility, so a hidden or nonexistent doc reports false
// rather than erroring.
func DocExists(doc string) bool {
	doc = strings.Trim(doc, "/")
	if doc == "" {
		return false
	}
	segs := strings.Split(doc, "/")
	if a, file, ok := ResolvePath(segs); ok {
		if file == "" {
			return false
		}
		for _, p := range a.Pages {
			if p == file {
				return true
			}
		}
		return false
	}
	_, ok := ResolveDoc(segs)
	return ok
}

// LoadNotes reads doc's sidecar file. A document with no notes has no file
// on disk, so a missing file is not an error: it reports the zero
// NotesFile{} (Rev 0, no annotations).
func LoadNotes(doc string) (NotesFile, error) {
	p, err := notesPath(doc)
	if err != nil {
		return NotesFile{}, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return NotesFile{}, nil
		}
		return NotesFile{}, err
	}
	var f NotesFile
	if err := json.Unmarshal(data, &f); err != nil {
		return NotesFile{}, fmt.Errorf("parse notes for %q: %w", doc, err)
	}
	return f, nil
}

// writeNotesFile writes f to p atomically: a temp file in the same
// directory, chmod'd, then renamed over the target, so a concurrent reader
// never observes a half-written sidecar. The temp file is best-effort
// cleaned up on any failure. These files are meant to be human-readable, so
// they are indented and newline-terminated.
func writeNotesFile(p string, f NotesFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p), ".notes-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpName, p); err != nil {
		return err
	}
	ok = true
	return nil
}

// SaveNotes replaces doc's notes file, enforcing that doc is a real
// document and that expectRev still matches what is on disk (optimistic
// concurrency: the viewer PUT and the CLI's read-modify-write both go
// through this). Saving zero annotations deletes the sidecar instead of
// writing an empty one, keeping "has notes" a cheap stat.
func SaveNotes(doc string, f NotesFile, expectRev int) (NotesFile, error) {
	if !DocExists(doc) {
		return NotesFile{}, fmt.Errorf("save notes: no such document %q", doc)
	}
	return saveNotesRaw(doc, f, expectRev)
}

// saveNotesRaw is SaveNotes without the DocExists requirement, used by
// ResolveNote/ReplyNote: an agent must be able to close out notes on a
// document that has since changed or been removed.
func saveNotesRaw(doc string, f NotesFile, expectRev int) (NotesFile, error) {
	p, err := notesPath(doc)
	if err != nil {
		return NotesFile{}, err
	}
	cur, err := LoadNotes(doc)
	if err != nil {
		return NotesFile{}, err
	}
	if cur.Rev != expectRev {
		return NotesFile{}, fmt.Errorf("%w: %q is at rev %d, not %d", ErrRevMismatch, doc, cur.Rev, expectRev)
	}
	if len(f.Annotations) == 0 {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return NotesFile{}, err
		}
		annRoot, err := annotationsRoot()
		if err != nil {
			return NotesFile{}, err
		}
		pruneEmpty(annRoot, p)
		return NotesFile{Rev: expectRev + 1}, nil
	}
	f.Rev = expectRev + 1
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return NotesFile{}, err
	}
	if err := writeNotesFile(p, f); err != nil {
		return NotesFile{}, err
	}
	return f, nil
}

// DeleteNotes removes all of doc's notes (its sidecar file), pruning any
// project directories under .annotations left empty by the removal. Deleting
// an already-noteless doc is not an error.
func DeleteNotes(doc string) error {
	p, err := notesPath(doc)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	annRoot, err := annotationsRoot()
	if err != nil {
		return err
	}
	pruneEmpty(annRoot, p)
	return nil
}

// appendReply is the read-modify-write shared by ResolveNote and ReplyNote:
// load doc's notes, find the annotation by id, append reply (optionally
// changing status), and save at the file's own current rev.
func appendReply(doc, id string, reply Reply, setStatus string) (Annotation, error) {
	f, err := LoadNotes(doc)
	if err != nil {
		return Annotation{}, err
	}
	idx := -1
	for i, a := range f.Annotations {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return Annotation{}, fmt.Errorf("%w: %q in %q", ErrNoteNotFound, id, doc)
	}
	f.Annotations[idx].Replies = append(f.Annotations[idx].Replies, reply)
	if setStatus != "" {
		f.Annotations[idx].Status = setStatus
	}
	saved, err := saveNotesRaw(doc, f, f.Rev)
	if err != nil {
		return Annotation{}, err
	}
	for _, a := range saved.Annotations {
		if a.ID == id {
			return a, nil
		}
	}
	return Annotation{}, fmt.Errorf("%w: %q in %q", ErrNoteNotFound, id, doc)
}

// ResolveNote appends an agent reply marking id resolved — the normal way a
// note gets closed. It does not require the document to currently exist.
func ResolveNote(doc, id, msg string) (Annotation, error) {
	return appendReply(doc, id, Reply{By: "agent", Created: time.Now().UTC(), Action: "resolve", Body: msg}, "resolved")
}

// ReplyNote appends an agent reply without changing status (a comment, e.g.
// a clarifying question).
func ReplyNote(doc, id, msg string) (Annotation, error) {
	return appendReply(doc, id, Reply{By: "agent", Created: time.Now().UTC(), Body: msg}, "")
}

// WalkNotes returns every document's notes under prefix, a store-relative
// slash path ("" for the whole store). When prefix itself names a document
// (its last segment looks like .html/.md), it returns at most that one
// DocNotes, omitted entirely when there is no sidecar file. Otherwise it
// walks the mirrored subtree under .annotations recursively, mapping each
// *.json file back to its doc path. Unreadable or malformed sidecar files
// are skipped silently. Results are sorted by Doc.
func WalkNotes(prefix string) ([]DocNotes, error) {
	prefix = strings.Trim(prefix, "/")
	var segs []string
	if prefix != "" {
		segs = strings.Split(prefix, "/")
		for _, s := range segs {
			if err := validateSegment(s); err != nil {
				return nil, fmt.Errorf("invalid path %q: %w", prefix, err)
			}
		}
	}
	if prefix != "" && hasDocExt(segs[len(segs)-1]) {
		p, err := notesPath(prefix)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(p); err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		f, err := LoadNotes(prefix)
		if err != nil {
			return nil, err
		}
		return []DocNotes{{Doc: prefix, Notes: f}}, nil
	}
	annRoot, err := annotationsRoot()
	if err != nil {
		return nil, err
	}
	walkDir := annRoot
	if prefix != "" {
		walkDir, err = joinInRoot(annRoot, append([]string{annRoot}, segs...))
		if err != nil {
			return nil, err
		}
	}
	var out []DocNotes
	filepath.WalkDir(walkDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			return nil
		}
		rel, err := filepath.Rel(annRoot, path)
		if err != nil {
			return nil
		}
		docPath := filepath.ToSlash(strings.TrimSuffix(rel, ".json"))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil // skip unreadable files silently
		}
		var f NotesFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil // skip malformed files silently
		}
		out = append(out, DocNotes{Doc: docPath, Notes: f})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Doc < out[j].Doc })
	return out, nil
}

// OpenNoteCount sums annotations with Status == "open" across
// WalkNotes(prefix), reporting 0 on any error. It is called per card during
// page render, so it stays a plain walk with no caching layer of its own.
func OpenNoteCount(prefix string) int {
	docs, err := WalkNotes(prefix)
	if err != nil {
		return 0
	}
	n := 0
	for _, d := range docs {
		for _, a := range d.Notes.Annotations {
			if a.Status == "open" {
				n++
			}
		}
	}
	return n
}

// removeNotesFor deletes every note file under an artifact's slash path
// rel: the mirrored subtree .annotations/<rel>/ (every document nested
// inside the artifact, e.g. index.html.json, notes.md.json) and, for
// safety, a bare .annotations/<rel>.json should rel itself ever name a
// document rather than an artifact directory. It then prunes now-empty
// ancestor directories up to (not including) .annotations, and never
// escapes .annotations — an invalid rel simply has nothing to clean up.
//
// Called from Delete and Unwatch: a name freed by a human delete or an
// unwatch must never let a re-published/re-watched artifact of the same
// name inherit the old one's notes.
func removeNotesFor(rel string) error {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return nil
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		if err := validateSegment(s); err != nil {
			return nil
		}
	}
	annRoot, err := annotationsRoot()
	if err != nil {
		return err
	}
	dir, err := joinInRoot(annRoot, append([]string{annRoot}, segs...))
	if err != nil {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	if err := os.Remove(dir + ".json"); err != nil && !os.IsNotExist(err) {
		return err
	}
	pruneEmpty(annRoot, dir)
	return nil
}
