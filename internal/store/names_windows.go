//go:build windows

package store

import (
	"fmt"
	"path"
	"strings"
)

// checkLookupSegmentPlatform rejects, in a single LOOKUP segment (URL paths,
// delete, unwatch, notes doc paths — everything validateSegment guards),
// forms that checkPortableName already refuses at create time but that this
// store must ALSO refuse to address on Windows, because the underlying
// Win32/NT open primitives interpret them dangerously rather than simply
// failing to find them (ADR §7.5, threat-model R11):
//
//   - ':' — NTFS reads everything after it as an alternate-data-stream
//     selector. Measured reachable even through a RootDirectory-relative
//     open (spike-findings.md M12.C_stream/M12.relative_open): a lookup for
//     "doc.html:hidden" does not 404, it opens (or on a write path, creates)
//     a second, hidden stream ON doc.html — invisible to ReadDir/Stat size
//     (M12.invisible), so a size-based guard like maxPreviewBytes could be
//     understated without bound. This is the one genuinely LIVE case here:
//     the other two below are handled by the handle-relative primitive for
//     free (ADR §6.10) and are only defence in depth in this function.
//   - a trailing dot or trailing space — Windows strips both when resolving
//     a name, so two spellings that differ only by a trailing dot or space
//     would silently collide.
//   - a reserved DOS device basename (reservedWindowsNames, names.go) — the
//     handle-relative opens this store issues already fail closed for these
//     (STATUS_OBJECT_NAME_NOT_FOUND, never treated as a device — ADR §6.10's
//     M18.relative_open.* measurement), so this is belt-and-braces, not the
//     primary control.
//
// Rejecting these only on Windows is deliberate and documented in the ADR:
// ':' is a legal Linux filename character and watched Linux repositories use
// it, so this function's Linux twin (names_linux.go) is a permanent no-op —
// never call this from a Linux-reachable code path expecting it to reject
// anything.
func checkLookupSegmentPlatform(s string) error {
	if strings.ContainsRune(s, ':') {
		return fmt.Errorf("invalid path segment %q: contains %q, which NTFS reads as an alternate-data-stream selector; this store refuses to address it", s, ":")
	}
	if strings.HasSuffix(s, ".") || strings.HasSuffix(s, " ") {
		return fmt.Errorf("invalid path segment %q: names ending in a dot or space are ambiguous on Windows (the trailing character is stripped on resolution)", s)
	}
	base := s
	if i := strings.IndexByte(s, '.'); i >= 0 {
		base = s[:i]
	}
	if reservedWindowsNames[strings.ToLower(base)] {
		return fmt.Errorf("invalid path segment %q: %q is a reserved Windows device name", s, base)
	}
	return nil
}

// matchName is the platform pair defaultIgnores/.scratchpadignore glob
// matching (ignore.go's matchSegments) uses to compare one pattern segment
// against one candidate name (ADR §7.4) — the second half of the pair
// alongside nameEquals (storefs_windows.go), which does the same job for
// the reserved-name check. §7.4 measured (M11) that NTFS's real $UpCase
// folding merges ".ssh"/".SSH" and "key.pem"/"key.PEM", so a bare
// case-sensitive path.Match leaves RR5's credential-ignore half live on
// Windows: this folds both operands to lower case before matching, which is
// the ADR's specified mechanism ("path.Match over lower-cased operands").
//
// strings.ToLower (like strings.EqualFold, which nameEquals uses) is a
// Unicode simple-case-folding approximation of $UpCase, not the table
// itself: M11 also measured that Go's folding disagrees with NTFS on the
// Kelvin sign (U+212A) and ß/ẞ, in the OVER-BROAD direction — Go treats
// pairs as equal that NTFS keeps distinct. Over-breadth is safe for a DENY
// rule (defaultIgnores can only hide a name that would otherwise have been
// shown, never fail to hide one NTFS itself folds) and is exactly why the
// ADR chooses it here. It would be unsafe for an identity comparison, where
// a false-equal is a containment bug — never reuse this fold, or
// nameEquals's, for identity; identity is FILE_ID_INFO everywhere, without
// exception.
func matchName(pat, name string) (bool, error) {
	return path.Match(strings.ToLower(pat), strings.ToLower(name))
}
