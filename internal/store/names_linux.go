//go:build linux

package store

import "path"

// checkLookupSegmentPlatform is a permanent no-op on Linux (ADR §7.5): the
// forms its Windows twin (names_windows.go) refuses are either meaningless
// here (there is no NTFS alternate-data-stream concept) or are ordinary,
// legal filename content a watched Linux repository is entitled to use
// unmolested — rejecting them on lookup would make an existing watched file
// unreachable for no Linux-relevant reason.
func checkLookupSegmentPlatform(s string) error { return nil }

// matchName is the platform pair defaultIgnores/.scratchpadignore glob
// matching (ignore.go's matchSegments) uses to compare one pattern segment
// against one candidate name (ADR §7.4). It sits alongside nameEquals
// (storefs_linux.go), the other half of the same pair, for the same reason:
// ext4 and most Linux filesystems are byte-exact/case-sensitive, so this is
// plain path.Match with no folding — a case variant of a hidden name (e.g.
// "KEY.PEM" against "*.pem") is a genuinely different, non-ignored file
// here, unlike on NTFS (names_windows.go).
func matchName(pat, name string) (bool, error) { return path.Match(pat, name) }
