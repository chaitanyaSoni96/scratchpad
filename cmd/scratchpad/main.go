// scratchpad is a small CLI over the artifact store — the interface for
// humans at the terminal and for agents, which drive it via bash (see
// skill/SKILL.md).
package main

import (
	"encoding/json"
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

const usage = `scratchpad - filesystem artifact store CLI

Usage:
  scratchpad publish -name <name> [-project <p/ath>] -dir <folder>
  scratchpad publish -name <name> [-project <p/ath>] -html <file> [-css <file>] [-js <file>]
  scratchpad watch <folder> [-name <name>] [-project <p/ath>]
  scratchpad unwatch <path> | -name <name> [-project <p/ath>]
  scratchpad watches
  scratchpad list [-json]
  scratchpad delete -name <name> [-project <p/ath>]

publish is CREATE-ONLY: it fails if the name already exists. -dir publishes a
whole folder (any files; needs a top-level .html). Pass "-" as -html to read
HTML from stdin.

watch symlinks a folder into the scratchpad instead of copying: keep editing
it in place and the site updates live. -name defaults to the folder's name.
unwatch removes that link and nothing else — the source folder is never
touched — and watches lists every link currently in the scratchpad. Files
inside a watched folder cannot be deleted through scratchpad at all.

Artifacts land in ~/.scratchpad (override: SCRATCHPAD_ROOT) and are served
at http://localhost:8737/a/<project>/<name>/ by scratchpad-web.`

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
