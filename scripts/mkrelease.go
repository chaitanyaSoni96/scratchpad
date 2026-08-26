//go:build ignore

// mkrelease packages the Windows release archives (P5.4).
//
// It expects `make build-windows` to have produced
// <dist>/windows-<arch>/scratchpad.exe and scratchpad-web.exe for amd64 and
// arm64, zips each pair together with the PowerShell installer and the
// user-facing docs/licence material, verifies the archive contents it just
// wrote (exact name set, non-empty PE executables), and emits a
// coreutils-format SHA256SUMS.txt covering both archives.
//
// Design notes:
//   - Pure Go (archive/zip): Linux CI runners do not guarantee a `zip`
//     binary, and this also keeps the archives deterministic.
//   - Every entry gets a fixed timestamp (SOURCE_DATE_EPOCH if set, else the
//     ZIP epoch 1980-01-01 UTC) so re-running on the same inputs yields
//     byte-identical archives and therefore stable checksums.
//   - scripts/install.ps1 is included when present. Without
//     -require-installer its absence is a loud warning, not an error, so
//     packaging works while the installer is still being written; the
//     tagged-release CI path passes -require-installer so a real release can
//     never ship without it.
//
// Run via `make release-windows`, or directly:
//
//	go run scripts/mkrelease.go -dist dist -version v0.1.0 [-require-installer]
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

var arches = []string{"amd64", "arm64"}

// entry maps a file on disk to its path inside the archive.
type entry struct {
	src      string // path on disk, relative to the repo root
	dst      string // slash-separated path inside the zip
	exe      bool   // must start with the PE "MZ" magic
	optional bool   // may be absent (overridden by -require-installer for the installer)
}

func main() {
	dist := flag.String("dist", "dist", "directory holding windows-<arch>/ build output; archives are written here")
	version := flag.String("version", "dev", "version string used in archive names")
	requireInstaller := flag.Bool("require-installer", false, "fail if scripts/install.ps1 is missing (set on tagged releases)")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("unexpected arguments: %v", flag.Args())
	}

	stamp := timestamp()
	sums := make(map[string][]byte) // zip basename -> sha256

	for _, arch := range arches {
		entries := []entry{
			{src: filepath.Join(*dist, "windows-"+arch, "scratchpad.exe"), dst: "scratchpad.exe", exe: true},
			{src: filepath.Join(*dist, "windows-"+arch, "scratchpad-web.exe"), dst: "scratchpad-web.exe", exe: true},
			{src: filepath.Join("scripts", "install.ps1"), dst: "install.ps1", optional: !*requireInstaller},
			// The installer's `skill`, `all` and `install` verbs copy this
			// file; without it in the archive those verbs — `all` is the
			// default — fail for anyone installing from a release.
			{src: filepath.Join("skill", "SKILL.md"), dst: "skill/SKILL.md"},
			{src: "README.md", dst: "README.md"},
			{src: "LICENSE", dst: "LICENSE"},
			{src: filepath.Join("docs", "cli.md"), dst: "docs/cli.md"},
			{src: filepath.Join("docs", "notes.md"), dst: "docs/notes.md"},
			{src: filepath.Join("docs", "ignore-rules.md"), dst: "docs/ignore-rules.md"},
		}
		name := fmt.Sprintf("scratchpad_%s_windows_%s.zip", *version, arch)
		path := filepath.Join(*dist, name)
		included := writeZip(path, entries, stamp)
		verifyZip(path, included)
		sums[name] = fileSHA256(path)
	}

	// coreutils sha256sum format: "<hex>  <name>" — verifiable with
	// `sha256sum -c SHA256SUMS.txt` in dist/ or `Get-FileHash` on Windows.
	names := make([]string, 0, len(sums))
	for n := range sums {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	for _, n := range names {
		fmt.Fprintf(&buf, "%x  %s\n", sums[n], n)
	}
	sumPath := filepath.Join(*dist, "SHA256SUMS.txt")
	if err := os.WriteFile(sumPath, buf.Bytes(), 0o644); err != nil {
		fatal("write %s: %v", sumPath, err)
	}
	fmt.Printf("wrote %s:\n%s", sumPath, buf.String())
}

// writeZip creates the archive and returns the entries actually included,
// in archive order.
func writeZip(path string, entries []entry, stamp time.Time) []entry {
	f, err := os.Create(path)
	if err != nil {
		fatal("create %s: %v", path, err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)

	var included []entry
	for _, e := range entries {
		data, err := os.ReadFile(e.src)
		if err != nil {
			if os.IsNotExist(err) && e.optional {
				fmt.Fprintf(os.Stderr, "WARNING: %s not found — %s built WITHOUT %s\n", e.src, filepath.Base(path), e.dst)
				continue
			}
			fatal("read %s: %v", e.src, err)
		}
		hdr := &zip.FileHeader{Name: e.dst, Method: zip.Deflate, Modified: stamp}
		mode := os.FileMode(0o644)
		if e.exe {
			mode = 0o755
		}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			fatal("zip %s: add %s: %v", path, e.dst, err)
		}
		if _, err := w.Write(data); err != nil {
			fatal("zip %s: write %s: %v", path, e.dst, err)
		}
		included = append(included, e)
	}
	if err := zw.Close(); err != nil {
		fatal("finalize %s: %v", path, err)
	}
	if err := f.Close(); err != nil {
		fatal("close %s: %v", path, err)
	}
	return included
}

// verifyZip is the P5.4 smoke test: re-open the archive from disk and check
// that it contains exactly the expected names and that each executable is a
// non-empty PE image ("MZ" magic). It never trusts the writer's in-memory
// state — the file on disk is what ships.
func verifyZip(path string, expected []entry) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		fatal("verify %s: open: %v", path, err)
	}
	defer zr.Close()

	want := make(map[string]entry, len(expected))
	for _, e := range expected {
		want[e.dst] = e
	}
	fmt.Printf("%s:\n", path)
	for _, zf := range zr.File {
		e, ok := want[zf.Name]
		if !ok {
			fatal("verify %s: unexpected entry %q", path, zf.Name)
		}
		delete(want, zf.Name)
		rc, err := zf.Open()
		if err != nil {
			fatal("verify %s: open %s: %v", path, zf.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			fatal("verify %s: read %s: %v", path, zf.Name, err)
		}
		if len(data) == 0 {
			fatal("verify %s: %s is empty", path, zf.Name)
		}
		if uint64(len(data)) != zf.UncompressedSize64 {
			fatal("verify %s: %s decompressed to %d bytes, header says %d", path, zf.Name, len(data), zf.UncompressedSize64)
		}
		if e.exe && !bytes.HasPrefix(data, []byte("MZ")) {
			fatal("verify %s: %s does not start with the PE 'MZ' magic", path, zf.Name)
		}
		fmt.Printf("  %10d  %s\n", len(data), zf.Name)
	}
	if len(want) != 0 {
		missing := make([]string, 0, len(want))
		for n := range want {
			missing = append(missing, n)
		}
		sort.Strings(missing)
		fatal("verify %s: missing entries: %v", path, missing)
	}
}

// timestamp returns the fixed per-entry modification time: SOURCE_DATE_EPOCH
// when set (the reproducible-builds convention), else the ZIP format epoch.
// MS-DOS timestamps cannot represent dates before 1980, so the Unix epoch is
// not usable here.
func timestamp() time.Time {
	if s := os.Getenv("SOURCE_DATE_EPOCH"); s != "" {
		sec, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			fatal("invalid SOURCE_DATE_EPOCH %q: %v", s, err)
		}
		return time.Unix(sec, 0).UTC()
	}
	return time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
}

func fileSHA256(path string) []byte {
	f, err := os.Open(path)
	if err != nil {
		fatal("hash %s: %v", path, err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		fatal("hash %s: %v", path, err)
	}
	return h.Sum(nil)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "mkrelease: "+format+"\n", args...)
	os.Exit(1)
}
