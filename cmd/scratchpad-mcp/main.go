// scratchpad-mcp is a stdio MCP server over the shared filesystem store.
// Deliberately minimal surface: one mutating tool (publish_artifact,
// create-only) and one read-only tool (list_artifacts). Guardrails live in
// the server instructions. Logs go to stderr; stdout is the MCP transport.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"scratchpad/internal/store"
)

const version = "0.2.0"

func baseURL() string {
	if u := os.Getenv("SCRATCHPAD_URL"); u != "" {
		return u
	}
	return "http://localhost:8737"
}

func artifactURL(a store.Artifact) string {
	return baseURL() + "/a/" + a.RelPath() + "/"
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

const instructions = `scratchpad hosts local html/css/js artifacts from ~/.scratchpad, served on %s with live-updating pages.

Rules:
- publish_artifact is CREATE-ONLY. Names are never overwritten: if a name is taken, publishing fails. Deletion is a human action in the web UI — never assume you can free a name; pick a new one (check list_artifacts first, e.g. suffix -v2).
- An artifact is one folder: exactly your provided files. It must contain at least one top-level .html file (index.html preferred, it becomes the entry page). Any number of additional files at relative paths is allowed — css, js, images, fonts, json, subfolders (e.g. img/logo.png). Reference them with relative URLs from your html.
- Binary files (images, fonts): set "base64": true and base64-encode the content. Text files need no encoding.
- Names and every path segment must match ^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$ (no spaces).
- Artifacts can be grouped with an optional project path of any depth, e.g. "demos/charts". An artifact cannot be published inside another artifact.
- Keep artifacts self-contained and reasonably small; the index page live-previews every artifact in an iframe.`

type FileSpec struct {
	Path    string `json:"path" jsonschema:"Relative path inside the artifact, e.g. index.html or img/logo.png"`
	Content string `json:"content" jsonschema:"File content; base64-encoded when base64 is true"`
	Base64  bool   `json:"base64,omitempty" jsonschema:"Set true for binary files whose content is base64-encoded"`
}

type PublishInput struct {
	Project string     `json:"project,omitempty" jsonschema:"Optional project path to group the artifact under, e.g. demos or demos/charts"`
	Name    string     `json:"name" jsonschema:"New artifact name; must not already exist"`
	Files   []FileSpec `json:"files" jsonschema:"The artifact's files; at least one top-level .html required (index.html preferred)"`
}

type PublishOutput struct {
	URL  string `json:"url" jsonschema:"Where the artifact is served"`
	Path string `json:"path" jsonschema:"Directory the artifact was written to"`
	Note string `json:"note,omitempty" jsonschema:"Warning when the hosting server is unreachable"`
}

type ListInput struct{}

type ArtifactInfo struct {
	Project  string `json:"project,omitempty"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Entry    string `json:"entry"`
	Size     int64  `json:"size_bytes"`
	Modified string `json:"modified"`
}

type ListOutput struct {
	Artifacts []ArtifactInfo `json:"artifacts"`
}

func publishHandler(ctx context.Context, req *mcp.CallToolRequest, in PublishInput) (*mcp.CallToolResult, PublishOutput, error) {
	files := make(map[string][]byte, len(in.Files))
	for _, f := range in.Files {
		if f.Base64 {
			b, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				return nil, PublishOutput{}, fmt.Errorf("file %q: invalid base64: %w", f.Path, err)
			}
			files[f.Path] = b
		} else {
			files[f.Path] = []byte(f.Content)
		}
	}
	a, err := store.Publish(in.Project, in.Name, files)
	if err != nil {
		return nil, PublishOutput{}, err
	}
	out := PublishOutput{URL: artifactURL(a), Path: a.Dir}
	if !webAlive() {
		out.Note = fmt.Sprintf("artifact saved, but the scratchpad web server is not reachable at %s — the link will not load until it is started (make web)", baseURL())
	}
	return nil, out, nil
}

func listHandler(ctx context.Context, req *mcp.CallToolRequest, in ListInput) (*mcp.CallToolResult, ListOutput, error) {
	artifacts, err := store.List()
	if err != nil {
		return nil, ListOutput{}, err
	}
	out := ListOutput{Artifacts: []ArtifactInfo{}}
	for _, a := range artifacts {
		out.Artifacts = append(out.Artifacts, ArtifactInfo{
			Project:  a.Project,
			Name:     a.Name,
			URL:      artifactURL(a),
			Entry:    a.Entry,
			Size:     a.Size,
			Modified: a.ModTime.Format("2006-01-02 15:04:05"),
		})
	}
	return nil, out, nil
}

func main() {
	log.SetOutput(os.Stderr)
	if _, err := store.EnsureRoot(); err != nil {
		log.Fatalf("ensure root: %v", err)
	}

	server := mcp.NewServer(
		&mcp.Implementation{Name: "scratchpad", Version: version},
		&mcp.ServerOptions{Instructions: fmt.Sprintf(instructions, baseURL())},
	)

	falseVal := false
	mcp.AddTool(server, &mcp.Tool{
		Name: "publish_artifact",
		Description: fmt.Sprintf("Create a new artifact from a set of files and host it at %s/a/<project>/<name>/. "+
			"Create-only: fails if the name exists (a human must delete via the web UI to free a name). "+
			"Requires at least one top-level .html; any other relative files (css, js, images via base64) may be included.", baseURL()),
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: &falseVal, // create-only: never touches existing data
			IdempotentHint:  false,
			OpenWorldHint:   &falseVal,
		},
	}, publishHandler)

	mcp.AddTool(server, &mcp.Tool{
		Name:        "list_artifacts",
		Description: "List all artifacts currently hosted on the scratchpad, newest first, with URLs and sizes.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: &falseVal,
		},
	}, listHandler)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatalf("mcp server: %v", err)
	}
}
