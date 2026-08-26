//go:build linux

package store

// checkLookupSegmentPlatform is a permanent no-op on Linux (ADR §7.5): the
// forms its Windows twin (names_windows.go) refuses are either meaningless
// here (there is no NTFS alternate-data-stream concept) or are ordinary,
// legal filename content a watched Linux repository is entitled to use
// unmolested — rejecting them on lookup would make an existing watched file
// unreachable for no Linux-relevant reason.
func checkLookupSegmentPlatform(s string) error { return nil }
