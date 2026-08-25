package store

import (
	"fmt"
	"strings"
)

// reservedWindowsNames are the DOS device basenames Windows treats as
// special files rather than ordinary filenames. The check is on the
// basename only — an extension does not hide it, so "nul.html" is exactly
// as reserved as "NUL". This is deliberately untagged: a store created on
// Linux must stay movable to a Windows machine, so the rule applies on every
// OS rather than only where it would currently be enforced by the
// filesystem.
var reservedWindowsNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// checkPortableName rejects create-time names that pass nameRe but still
// cannot exist, or cannot be reliably addressed, on Windows:
//
//   - A DOS device basename (case-insensitive), keyed on the portion of the
//     name before the first dot — Windows reserves these regardless of
//     extension, so "COM1.tar.gz" is as unusable as "COM1".
//   - A name ending in a trailing dot or trailing space — Windows strips
//     both when creating the file, so two names that differ only in a
//     trailing dot or space would silently collide, and the trailing
//     character itself would be lost, on a Windows-hosted store. (nameRe
//     already excludes a trailing space; this check names the rule
//     explicitly so it does not depend on that being true forever.)
//
// This is a create-time rule only: it must never be applied to lookup of an
// existing name (validateSegment/existingDir), which has to keep reaching
// whatever a watched repository already named itself.
func checkPortableName(s string) error {
	base := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		base = s[:i]
	}
	if reservedWindowsNames[strings.ToLower(base)] {
		return fmt.Errorf("invalid name %q: %q is a reserved Windows device name and cannot be used, even with an extension", s, base)
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return fmt.Errorf("invalid name %q: names cannot end in a dot or space (Windows strips both)", s)
	}
	return nil
}
