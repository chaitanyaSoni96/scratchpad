//go:build windows

package store

import "os"

// symlinkAt, readlinkAt, unlinkAt and isLinkAt are Phase 2 stubs: real
// Windows link mechanism (Phase 3) must create/read/remove/classify a
// directory symbolic link relative to a pinned parent handle, using
// FILE_ATTRIBUTE_TAG_INFO to identify the reparse tag rather than trusting
// any Go-level mode bit (threat-model-windows.md §4.2, R4, R5). They return
// errWindowsUnimplemented rather than faking success so a caller cannot
// mistake "not implemented" for "no link found" or "link created". In
// today's code every caller (Watch, Unwatch, Delete) already fails earlier,
// at rootedFS.openRealDir's own errWindowsUnimplemented stub in
// storefs_windows.go, so these are never reached — but their signatures
// exist now so Phase 3 fills in behavior instead of redesigning the shape.
func symlinkAt(parent int, target, name string) error {
	return errWindowsUnimplemented
}

func readlinkAt(parent int, name string) (string, error) {
	return "", errWindowsUnimplemented
}

func unlinkAt(parent int, name string) error {
	return errWindowsUnimplemented
}

func isLinkAt(parent int, name string) (isLink bool, err error) {
	return false, errWindowsUnimplemented
}

// IsLinkInfo and IsLinkEntry are Phase 2 PLACEHOLDERS, not stubs: unlike the
// fd-relative mechanism above, they return a real (if imprecise) answer
// instead of an error, because they back read-only listing paths (List,
// Watches, WatchLinkFor, folderUnwatch) that must still produce a folder
// page on Windows even before Phase 3 lands.
//
// They use Go's os.ModeSymlink, which the threat model proves WRONG for
// Windows junctions: Go sets ModeSymlink only for IO_REPARSE_TAG_SYMLINK, so
// a junction (IO_REPARSE_TAG_MOUNT_POINT) reads as ModeIrregular and is
// silently classified "not a link" here (threat-model-windows.md §3.2, §4.2,
// R5). That is acceptable ONLY as long as it stays confined to listing/
// display: every Windows *mutation* path (Watch/Unwatch/Delete/Unwatch) is
// refused earlier by the fd-relative stubs above and by storefs_windows.go,
// so a misclassified junction here can be mislisted, not destroyed.
//
// Phase 3 MUST replace both with a check of an open handle's
// FILE_ATTRIBUTE_TAG_INFO.ReparseTag against an explicit allowlist (R4, R5)
// before implementing any Windows mutation — do not let this placeholder
// survive into a build that also implements Watch/Unwatch/Delete.
func IsLinkInfo(fi os.FileInfo) bool { return fi.Mode()&os.ModeSymlink != 0 }

func IsLinkEntry(e os.DirEntry) bool { return e.Type()&os.ModeSymlink != 0 }
