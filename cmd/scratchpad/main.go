// scratchpad is a small CLI over the artifact store — the interface for
// the user at the terminal and for agents, which drive it via bash (see
// skill/SKILL.md).
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"scratchpad/internal/store"
)

func baseURL() string {
	if u := os.Getenv("SCRATCHPAD_URL"); u != "" {
		return u
	}
	return "http://localhost:8737"
}

// webAlive probes the hosting server so publish can warn about dead links.
func webAlive() bool {
	client := http.Client{Timeout: 700 * time.Millisecond}
	resp, err := client.Get(baseURL() + "/")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode < 500
}

const usage = `scratchpad — host html artifacts straight from the filesystem

Any folder under ~/.scratchpad (override: SCRATCHPAD_ROOT) that directly
contains an .html file is hosted at http://localhost:8737/a/<project>/<name>/
(override: SCRATCHPAD_URL). The site refreshes as files change; .md files
render as styled pages.

Usage:
  scratchpad watch <folder> [-name <name>] [-project <p/ath>]
  scratchpad watches
  scratchpad unwatch <path> | -name <name> [-project <p/ath>]
  scratchpad publish -name <name> [-project <p/ath>] -dir <folder>
  scratchpad publish -name <name> [-project <p/ath>] -html <file> [-css <file>] [-js <file>]
  scratchpad list [-json]
  scratchpad delete -name <name> [-project <p/ath>]
  scratchpad notes [<path>] [-all] [-json]
  scratchpad notes resolve <doc-path> <id> -m "what changed"
  scratchpad notes reply   <doc-path> <id> -m "text"

watch symlinks the folder in, so saved edits are live; a folder with no
top-level .html is hosted as a tree of artifacts. Repeating the same watch is
a no-op. unwatch removes only the link.

publish copies a frozen snapshot in and is create-only: a taken name is an
error, never an overwrite. The folder needs a top-level .html; "-" reads a
file from stdin.

notes reads review feedback left in the viewer ("notes <path>", or no path
for the whole store) and closes it: resolve marks a note done with a summary
of the change, reply comments without closing. Creating, editing, deleting
and reopening notes happen in the web UI only.`

const notesUsage = `scratchpad notes [<path>] [-all] [-json]
scratchpad notes resolve <doc-path> <id> -m "what changed"
scratchpad notes reply   <doc-path> <id> -m "text"

<path> is a document, an artifact, or a folder (omit it for the whole
store). -all includes resolved notes; -json prints raw JSON instead of the
markdown report. resolve closes a note with a summary of what changed;
reply comments without closing (e.g. a clarifying question). -m/-message is
required for both.

There is no create, edit, delete, or reopen here: an agent can answer and
close feedback but can never author it, erase it, or overrule the user's
reopen — those are the user's, in the web UI.`

func readInput(path string) ([]byte, error) {
	if path == "-" {
		return os.ReadFile("/dev/stdin")
	}
	return os.ReadFile(path)
}

func filesFromDir(dir string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = b
		return nil
	})
	return files, err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help", "help":
		fmt.Println(usage)
	case "publish":
		fs := flag.NewFlagSet("publish", flag.ExitOnError)
		project := fs.String("project", "", "optional project path (e.g. demos/charts)")
		name := fs.String("name", "", "artifact name (required)")
		dir := fs.String("dir", "", "publish this whole folder")
		htmlPath := fs.String("html", "", "path to html file, or - for stdin")
		cssPath := fs.String("css", "", "path to css file")
		jsPath := fs.String("js", "", "path to js file")
		fs.Parse(os.Args[2:])

		var files map[string][]byte
		switch {
		case *dir != "":
			var err error
			if files, err = filesFromDir(*dir); err != nil {
				fatal(err)
			}
		case *htmlPath != "":
			files = map[string][]byte{}
			html, err := readInput(*htmlPath)
			if err != nil {
				fatal(err)
			}
			files["index.html"] = html
			if *cssPath != "" {
				b, err := readInput(*cssPath)
				if err != nil {
					fatal(err)
				}
				files["style.css"] = b
			}
			if *jsPath != "" {
				b, err := readInput(*jsPath)
				if err != nil {
					fatal(err)
				}
				files["script.js"] = b
			}
		default:
			fatal(fmt.Errorf("one of -dir or -html is required"))
		}
		a, err := store.Publish(*project, *name, files)
		if err != nil {
			fatal(err)
		}
		fmt.Printf("published %s\n%s/a/%s/\n", a.Dir, baseURL(), a.RelPath())
		if !webAlive() {
			fmt.Fprintf(os.Stderr, "warning: scratchpad web server not reachable at %s — the link will not load until it is started (make web)\n", baseURL())
		}
	case "watch":
		fs := flag.NewFlagSet("watch", flag.ExitOnError)
		project := fs.String("project", "", "optional project path")
		name := fs.String("name", "", "link name (default: folder's base name)")
		// accept the folder as a positional arg before or after flags
		args := os.Args[2:]
		var target string
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			target, args = args[0], args[1:]
		}
		fs.Parse(args)
		if target == "" && fs.NArg() > 0 {
			target = fs.Arg(0)
		}
		if target == "" {
			fatal(fmt.Errorf("usage: scratchpad watch <folder> [-name n] [-project p]"))
		}
		if *name == "" {
			*name = filepath.Base(strings.TrimRight(target, "/"))
		}
		link, err := store.Watch(*project, *name, target)
		if err != nil {
			fatal(err)
		}
		rel := *name
		if *project != "" {
			rel = *project + "/" + *name
		}
		fmt.Printf("watching %s -> %s\n%s/a/%s/\n", link, target, baseURL(), rel)
		if !webAlive() {
			fmt.Fprintf(os.Stderr, "warning: scratchpad web server not reachable at %s\n", baseURL())
		}
	case "unwatch":
		fs := flag.NewFlagSet("unwatch", flag.ExitOnError)
		project := fs.String("project", "", "optional project path")
		name := fs.String("name", "", "link name")
		// the link may be given positionally as a whole path, like the
		// scratchpad URL shows it (project/name), or via the flags
		args := os.Args[2:]
		var target string
		if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
			target, args = args[0], args[1:]
		}
		fs.Parse(args)
		if target != "" {
			rel := strings.Trim(target, "/")
			if i := strings.LastIndex(rel, "/"); i >= 0 {
				*project, *name = rel[:i], rel[i+1:]
			} else {
				*project, *name = "", rel
			}
		}
		if *name == "" {
			printWatches("nothing to unwatch — no watched folders")
			fatal(fmt.Errorf("usage: scratchpad unwatch <path> | -name <name> [-project <p/ath>]"))
		}
		if err := store.Unwatch(*project, *name); err != nil {
			fatal(err)
		}
		rel := *name
		if *project != "" {
			rel = *project + "/" + *name
		}
		fmt.Printf("unwatched %s (source folder kept)\n", rel)
	case "watches":
		printWatches("no watched folders")
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "output JSON")
		fs.Parse(os.Args[2:])
		artifacts, err := store.List()
		if err != nil {
			fatal(err)
		}
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(artifacts)
			return
		}
		if len(artifacts) == 0 {
			fmt.Println("no artifacts")
			return
		}
		for _, a := range artifacts {
			fmt.Printf("%-40s  %8d B  %s  %s/a/%s/\n",
				a.RelPath(), a.Size, a.ModTime.Format("2006-01-02 15:04"), baseURL(), a.RelPath())
		}
	case "delete":
		fs := flag.NewFlagSet("delete", flag.ExitOnError)
		project := fs.String("project", "", "optional project path")
		name := fs.String("name", "", "artifact name (required)")
		fs.Parse(os.Args[2:])
		if err := store.Delete(*project, *name); err != nil {
			fatal(err)
		}
		fmt.Println("deleted")
	case "notes":
		if len(os.Args) > 2 && os.Args[2] == "resolve" {
			notesResolve(os.Args[3:])
			break
		}
		if len(os.Args) > 2 && os.Args[2] == "reply" {
			notesReply(os.Args[3:])
			break
		}
		// Anything else is a <path> — except the user-only verbs, which are
		// absent on purpose and so must say so. Read as a path they would
		// quietly report "no notes", which reads like the note is gone rather
		// than like the command does not exist.
		if len(os.Args) > 2 && userOnlyVerbs[os.Args[2]] {
			fmt.Fprintf(os.Stderr, "scratchpad notes has no %q: creating, editing, reopening and deleting notes are the user's, in the web UI.\n\n%s\n",
				os.Args[2], notesUsage)
			os.Exit(2)
		}
		notesRead(os.Args[2:])
	default:
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
}

// printWatches lists the watch links, or empty when there are none.
func printWatches(empty string) {
	links, err := store.Watches()
	if err != nil {
		fatal(err)
	}
	if len(links) == 0 {
		fmt.Println(empty)
		return
	}
	for _, l := range links {
		fmt.Printf("%-40s -> %s\n", l.Path, l.Target)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}

// notesRead implements `scratchpad notes [<path>] [-all] [-json]`. <path> may
// be a document, an artifact, a project folder, or omitted entirely (the
// whole store), and — like watch/unwatch — may appear before or after the
// flags.
// userOnlyVerbs are the note verbs the CLI deliberately does not implement:
// an agent can answer and close feedback but can never author it, erase it, or
// overrule the user's reopen. Named here only so a typo'd attempt gets that
// answer instead of being silently read as a path.
var userOnlyVerbs = map[string]bool{
	"create": true, "new": true, "add": true,
	"edit": true, "update": true,
	"delete": true, "rm": true, "remove": true,
	"reopen": true,
}

func notesRead(args []string) {
	fs := flag.NewFlagSet("notes", flag.ExitOnError)
	all := fs.Bool("all", false, "include resolved notes")
	asJSON := fs.Bool("json", false, "raw JSON instead of the markdown report")
	var path string
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		path, args = args[0], args[1:]
	}
	fs.Parse(args)
	if path == "" && fs.NArg() > 0 {
		path = fs.Arg(0)
	}
	docs, err := store.WalkNotes(path)
	if err != nil {
		fatal(err)
	}
	if *asJSON {
		if !*all {
			docs = openOnly(docs)
		}
		if docs == nil {
			docs = []store.DocNotes{}
		}
		json.NewEncoder(os.Stdout).Encode(docs)
		return
	}
	fmt.Print(store.FormatReport(docs, store.ReportOptions{Path: path, All: *all}))
}

// openOnly filters docs down to their open annotations, dropping any
// document left with none — the same shape FormatReport's default view
// shows, so --json and the report agree on what they display.
func openOnly(docs []store.DocNotes) []store.DocNotes {
	var out []store.DocNotes
	for _, d := range docs {
		var open []store.Annotation
		for _, a := range d.Notes.Annotations {
			if a.Status == "open" {
				open = append(open, a)
			}
		}
		if len(open) > 0 {
			nf := d.Notes
			nf.Annotations = open
			out = append(out, store.DocNotes{Doc: d.Doc, Notes: nf})
		}
	}
	return out
}

// notesResolve implements `scratchpad notes resolve <doc-path> <id> -m msg`.
func notesResolve(args []string) {
	doc, id, msg := parseNoteArgs("resolve", args)
	a, err := store.ResolveNote(doc, id, msg)
	if err != nil {
		noteFatal(err, doc)
	}
	fmt.Printf("resolved %s on %s: %s\n", id, doc, oneLineTrunc(a.Body, 80))
}

// notesReply implements `scratchpad notes reply <doc-path> <id> -m msg`.
func notesReply(args []string) {
	doc, id, msg := parseNoteArgs("reply", args)
	_, err := store.ReplyNote(doc, id, msg)
	if err != nil {
		noteFatal(err, doc)
	}
	fmt.Printf("replied to %s on %s\n", id, doc)
}

// parseNoteArgs pulls the two positional args (doc-path, id) — which, like
// watch/unwatch, may sit before or after the flags — and the required
// -m/-message summary shared by resolve and reply. It exits 2 with
// notesUsage on any shape error, since resolve/reply without a summary is
// exactly the case that would leave the user with nothing to read next.
func parseNoteArgs(verb string, args []string) (doc, id, msg string) {
	fs := flag.NewFlagSet("notes "+verb, flag.ExitOnError)
	m := fs.String("m", "", "summary of what changed (required)")
	alias := fs.String("message", "", "alias for -m")

	var positional []string
	i := 0
	for i < len(args) && len(positional) < 2 && !strings.HasPrefix(args[i], "-") {
		positional = append(positional, args[i])
		i++
	}
	fs.Parse(args[i:])
	positional = append(positional, fs.Args()...)

	if len(positional) < 2 {
		fmt.Fprintln(os.Stderr, notesUsage)
		os.Exit(2)
	}
	doc, id = positional[0], positional[1]
	msg = *m
	if msg == "" {
		msg = *alias
	}
	if msg == "" {
		fmt.Fprintf(os.Stderr, "error: -m is required — a %s with no summary of what changed is what the user reviewing this reads next\n", verb)
		os.Exit(2)
	}
	return doc, id, msg
}

// noteFatal reports store errors from resolve/reply, special-casing
// ErrNoteNotFound with a pointer at how to list the doc's ids.
func noteFatal(err error, doc string) {
	if errors.Is(err, store.ErrNoteNotFound) {
		fmt.Fprintf(os.Stderr, "error: no such note on %s — run `scratchpad notes %s` to list its ids\n", doc, doc)
		os.Exit(1)
	}
	fatal(err)
}

// oneLineTrunc folds s onto one line and truncates it to n runes, so it
// cannot break the line it is printed inside.
func oneLineTrunc(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
