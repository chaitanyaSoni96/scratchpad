//go:build windows

package watch

import "testing"

// TestStripFinalPathDOSPrefix is P6.3 finding F2's regression test: a pure
// string-transformation test, deliberately independent of any real handle,
// volume or network share, because spike-findings.md's M1 records "SMB /
// UNC: no share available in CI" — there is no way to reproduce the actual
// mapped-drive trigger end to end on any runner this project has. What CAN
// be pinned down deterministically is the shape
// GetFinalPathNameByHandleW(VOLUME_NAME_DOS) is documented (and, for the
// plain drive-letter case, measured by M6.resolution) to return, and that
// stripFinalPathDOSPrefix turns each shape into a well-formed Win32 path
// rather than finalPathDOS's original TrimPrefix(s, `\\?\`)-only version,
// which silently produced "UNC\server\share\..." — missing its leading
// `\\`, neither a valid drive-letter path nor a valid UNC path — for the
// UNC shape. That malformed string used to flow straight into
// canonicalDir's return value and then into w.backend.Add (watch.go) with
// nothing in between to catch it, and reconcile treats any Add failure as
// fatal to the whole watcher.
func TestStripFinalPathDOSPrefix(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drive letter",
			in:   `\\?\C:\Users\example\scratchpad`,
			want: `C:\Users\example\scratchpad`,
		},
		{
			name: "drive letter root",
			in:   `\\?\Z:\`,
			want: `Z:\`,
		},
		{
			// The exact shape a mapped network drive resolves to (a
			// documented Win32 quirk, not something that requires a real
			// share to characterize): GetFinalPathNameByHandleW returns the
			// UNC form even when the caller opened the file through an
			// ordinary drive letter.
			name: "UNC — the mapped-drive-letter case",
			in:   `\\?\UNC\fileserver\scratchpad\projects\inside`,
			want: `\\fileserver\scratchpad\projects\inside`,
		},
		{
			name: "UNC typed directly",
			in:   `\\?\UNC\server\share`,
			want: `\\server\share`,
		},
		{
			name: "no recognized prefix passes through unchanged",
			in:   `C:\already\bare`,
			want: `C:\already\bare`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripFinalPathDOSPrefix(c.in); got != c.want {
				t.Fatalf("stripFinalPathDOSPrefix(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}
