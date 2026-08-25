//go:build windows

package watch

import (
	"errors"
	"os"
)

// errWindowsUnimplemented mirrors the sentinel used across
// internal/store's Windows stubs (see storefs_windows.go there): a distinct,
// never-nil error so a caller cannot mistake "not implemented yet" for a
// real identity.
var errWindowsUnimplemented = errors.New("scratchpad: Windows directory identity is not implemented yet (plan phase 3/4)")

// identity is a Phase 2 stub. dir is deliberately an already-open handle,
// not a path — see identity_unix.go's doc comment and R14 in
// threat-model-windows.md — because the real Windows implementation must
// call GetFileInformationByHandleEx(FileIdInfo) on this exact handle to read
// VolumeSerialNumber plus the 128-bit FileId (threat-model-windows.md
// §3.17, §4.7). Deriving identity from a fresh path lookup instead would be
// exactly the check-then-use pattern the handle-based signature exists to
// rule out, so Phase 3/4 must not "simplify" this back to identity(path
// string).
func identity(dir *os.File) (dirIdentity, error) {
	return dirIdentity{}, errWindowsUnimplemented
}
