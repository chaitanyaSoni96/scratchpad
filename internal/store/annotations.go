package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
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

// lockFileName is the Windows annotation rendezvous's reserved name (ADR
// §6.7): a lock file one hop from the store root, because LockFileEx cannot
// lock a directory handle at all (M14.dir_readhandle/dir_writehandle), so
// the Linux flock-the-root-inode rendezvous (lockRendezvous,
// annotationfs_linux.go) has no direct Windows equivalent. It is an
// untagged, shared constant — like AnnotationsDir, reserved in Visible
// (ignore.go) on both platforms — so a store built on Linux stays movable to
// Windows even though Linux never creates the file (the same reasoning
// checkPortableName already applies to created names, names.go).
const lockFileName = ".scratchpad-lock"

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

// annotationLock is the handle to a held lock, closed with Close regardless
// of which of lockAnnotations/lockDocument produced it. The two constructors
// close very different things (an entire annotationFS plus the rendezvous
// lock, versus one per-document lock file), so Close is a closure rather
// than a fixed set of fields — see lockRendezvous/unlockRendezvous
// (annotationfs_linux.go, annotationfs_windows.go) for why the rendezvous
// object itself is platform-specific even though this policy is not (ADR
// §3.4/§6.7).
type annotationLock struct {
	ann     *annotationFS // non-nil only for lockAnnotations' rendezvous lock; callers chain .ann into openDir/readFile/writeFile
	closeFn func() error
}

func (l *annotationLock) Close() error {
	if l == nil || l.closeFn == nil {
		return nil
	}
	return l.closeFn()
}

// lockAnnotations coordinates every annotation operation with artifact
// cleanup. Normal operations take a shared lock; Delete and Unwatch hold the
// exclusive form across removing both content and its annotation history.
// The object being locked is platform-specific (lockRendezvous's doc
// comment on each platform explains why); the policy here — shared vs.
// exclusive, and that losing the rendezvous must never leak the opened
// annotationFS — is not.
func lockAnnotations(exclusive bool) (*annotationLock, error) {
	ann, err := openAnnotationFS()
	if err != nil {
		return nil, err
	}
	if err := lockRendezvous(ann, exclusive); err != nil {
		ann.close()
		return nil, err
	}
	return &annotationLock{ann: ann, closeFn: func() error {
		err := unlockRendezvous(ann)
		closeErr := ann.close()
		if err != nil {
			return err
		}
		return closeErr
	}}, nil
}

func lockDocument(ann *annotationFS, doc string) (*annotationLock, error) {
	_, err := notesSegments(doc)
	if err != nil {
		return nil, err
	}
	locks, err := ann.openDir([]string{".locks"}, true)
	if err != nil {
		return nil, err
	}
	defer closeFD(locks)
	// A fixed-size key avoids filesystem name limits for deeply nested docs.
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Trim(doc, "/"))))
	f, err := openLockFileAt(locks, name+".lock")
	if err != nil {
		return nil, err
	}
	if err := flockFile(f, true); err != nil {
		f.Close()
		return nil, err
	}
	return &annotationLock{closeFn: func() error {
		err := funlockFile(f)
		closeErr := f.Close()
		if err != nil {
			return err
		}
		return closeErr
	}}, nil
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

func notesSegments(doc string) ([]string, error) {
	if _, err := notesPath(doc); err != nil {
		return nil, err
	}
	doc = strings.Trim(doc, "/")
	segs := strings.Split(doc, "/")
	segs[len(segs)-1] += ".json"
	return segs, nil
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
	guard, err := lockAnnotations(false)
	if err != nil {
		return NotesFile{}, err
	}
	defer guard.Close()
	lock, err := lockDocument(guard.ann, doc)
	if err != nil {
		return NotesFile{}, err
	}
	defer lock.Close()
	return loadNotesRaw(guard.ann, doc)
}

func loadNotesRaw(ann *annotationFS, doc string) (NotesFile, error) {
	segs, err := notesSegments(doc)
	if err != nil {
		return NotesFile{}, err
	}
	data, err := ann.readFile(segs)
	if err != nil {
		// errors.Is, not os.IsNotExist: the latter predates Go's error-
		// wrapping convention and only recognises a fixed set of concrete
		// types (*PathError/*LinkError/*SyscallError, or anything with its
		// own is-shaped Is method) — it does NOT call the target's Unwrap
		// chain the way errors.Is does. Windows's *winError (win32_windows.go)
		// chains to fs.ErrNotExist via Unwrap, exactly as documented (ADR
		// §3.7), but os.IsNotExist never walks that chain, so every miss
		// here read as a hard error instead of "no notes yet" on Windows —
		// found running annotations_test.go natively. Linux's raw
		// unix.ENOENT already satisfies errors.Is(_, fs.ErrNotExist) via
		// syscall.Errno's own Is method, so this is a strict fix, not a
		// behaviour change, there.
		if errors.Is(err, fs.ErrNotExist) {
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
func writeNotesFile(ann *annotationFS, segs []string, f NotesFile) error {
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return ann.writeFile(segs, data)
}

// SaveNotes replaces doc's notes file, enforcing that doc is a real
// document and that expectRev still matches what is on disk (optimistic
// concurrency: the viewer PUT and the CLI's read-modify-write both go
// through this). Saving zero annotations writes an empty revision tombstone;
// WalkNotes omits it, but stale writers still observe the latest revision.
func SaveNotes(doc string, f NotesFile, expectRev int) (NotesFile, error) {
	guard, err := lockAnnotations(false)
	if err != nil {
		return NotesFile{}, err
	}
	defer guard.Close()
	lock, err := lockDocument(guard.ann, doc)
	if err != nil {
		return NotesFile{}, err
	}
	defer lock.Close()
	if !DocExists(doc) {
		return NotesFile{}, fmt.Errorf("save notes: no such document %q", doc)
	}
	return saveNotesRaw(guard.ann, doc, f, expectRev)
}

// saveNotesRaw is SaveNotes without the DocExists requirement, used by
// ResolveNote/ReplyNote: an agent must be able to close out notes on a
// document that has since changed or been removed.
func saveNotesRaw(ann *annotationFS, doc string, f NotesFile, expectRev int) (NotesFile, error) {
	segs, err := notesSegments(doc)
	if err != nil {
		return NotesFile{}, err
	}
	cur, err := loadNotesRaw(ann, doc)
	if err != nil {
		return NotesFile{}, err
	}
	if cur.Rev != expectRev {
		return NotesFile{}, fmt.Errorf("%w: %q is at rev %d, not %d", ErrRevMismatch, doc, cur.Rev, expectRev)
	}
	f.Rev = expectRev + 1
	if err := writeNotesFile(ann, segs, f); err != nil {
		return NotesFile{}, err
	}
	return f, nil
}

// DeleteNotes removes all of doc's notes while preserving its next revision in
// an empty tombstone. Deleting a never-annotated doc is not an error.
func DeleteNotes(doc string) error {
	guard, err := lockAnnotations(false)
	if err != nil {
		return err
	}
	defer guard.Close()
	lock, err := lockDocument(guard.ann, doc)
	if err != nil {
		return err
	}
	defer lock.Close()
	cur, err := loadNotesRaw(guard.ann, doc)
	if err != nil {
		return err
	}
	if len(cur.Annotations) == 0 && cur.Rev == 0 {
		return nil
	}
	_, err = saveNotesRaw(guard.ann, doc, NotesFile{}, cur.Rev)
	return err
}

// appendReply is the read-modify-write shared by ResolveNote and ReplyNote:
// load doc's notes, find the annotation by id, append reply (optionally
// changing status), and save at the file's own current rev.
func appendReply(doc, id string, reply Reply, setStatus string) (Annotation, error) {
	guard, err := lockAnnotations(false)
	if err != nil {
		return Annotation{}, err
	}
	defer guard.Close()
	lock, err := lockDocument(guard.ann, doc)
	if err != nil {
		return Annotation{}, err
	}
	defer lock.Close()
	f, err := loadNotesRaw(guard.ann, doc)
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
	saved, err := saveNotesRaw(guard.ann, doc, f, f.Rev)
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
	guard, err := lockAnnotations(false)
	if err != nil {
		return nil, err
	}
	defer guard.Close()
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
		f, err := loadNotesRaw(guard.ann, prefix)
		if err != nil {
			// loadNotesRaw itself swallows a not-exist read into a zero
			// NotesFile (above), so this branch is defensive rather than
			// reachable today; kept consistent with that fix (errors.Is,
			// not os.IsNotExist) rather than left on the old predicate.
			if errors.Is(err, fs.ErrNotExist) {
				return nil, nil
			}
			return nil, err
		}
		if len(f.Annotations) == 0 {
			return nil, nil
		}
		return []DocNotes{{Doc: prefix, Notes: f}}, nil
	}
	var out []DocNotes
	if err := guard.ann.walk(segs, func(path []string, data []byte) {
		docPath := strings.TrimSuffix(strings.Join(path, "/"), ".json")
		var f NotesFile
		if err := json.Unmarshal(data, &f); err != nil {
			return
		}
		if len(f.Annotations) == 0 {
			return
		}
		out = append(out, DocNotes{Doc: docPath, Notes: f})
	}); err != nil {
		return nil, err
	}
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
// Called from Delete and Unwatch: a name freed by a user delete or an
// unwatch must never let a re-published/re-watched artifact of the same
// name inherit the old one's notes.
func removeNotesFor(guard *annotationLock, rel string) error {
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
	return guard.ann.removeSubtree(segs)
}
