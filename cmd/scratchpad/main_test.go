package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPublishFilesValidation(t *testing.T) {
	tests := []struct {
		name, dir, html, css, js string
		args                     []string
	}{
		{name: "neither"},
		{name: "both", dir: "dir", html: "page.html"},
		{name: "css with dir", dir: "dir", css: "style.css"},
		{name: "surplus arg", html: "page.html", args: []string{"extra"}},
		{name: "multiple stdin", html: "-", css: "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := publishFiles(tt.dir, tt.html, tt.css, tt.js, tt.args, strings.NewReader("input")); err == nil {
				t.Fatal("expected usage error")
			}
		})
	}
}

func TestFilesFromDirRejectsNamedPipe(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "input"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := filesFromDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular error, got %v", err)
	}
}

func TestPublishFilesReadsStdin(t *testing.T) {
	files, err := publishFiles("", "-", "", "", nil, strings.NewReader("<html>stdin</html>"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(files["index.html"]); got != "<html>stdin</html>" {
		t.Fatalf("index = %q", got)
	}
}

func TestFilesFromDirRejectsNonRegularEntries(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.html")
	if err := os.WriteFile(target, []byte("ok"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "linked.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := filesFromDir(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("expected non-regular error, got %v", err)
	}
}
