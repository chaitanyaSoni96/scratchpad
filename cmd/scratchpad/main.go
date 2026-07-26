// scratchpad is a small CLI over the artifact store, for humans and for
// agents without MCP support (e.g. pi, which drives CLIs via bash).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"scratchpad/internal/store"
)

const usage = `scratchpad - filesystem artifact store CLI

Usage:
  scratchpad publish -name <name> [-project <p/ath>] -dir <folder>
  scratchpad publish -name <name> [-project <p/ath>] -html <file> [-css <file>] [-js <file>]
  scratchpad list [-json]
  scratchpad delete -name <name> [-project <p/ath>]

publish is CREATE-ONLY: it fails if the name already exists. -dir publishes a
whole folder (any files; needs a top-level .html). Pass "-" as -html to read
HTML from stdin. Artifacts land in ~/.scratchpad (override: SCRATCHPAD_ROOT)
and are served at http://localhost:8737/a/<project>/<name>/ by scratchpad-web.`

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
		fmt.Printf("published %s\nhttp://localhost:8737/a/%s/\n", a.Dir, a.RelPath())
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
			fmt.Printf("%-40s  %8d B  %s  http://localhost:8737/a/%s/\n",
				a.RelPath(), a.Size, a.ModTime.Format("2006-01-02 15:04"), a.RelPath())
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

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
