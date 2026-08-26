package store

import (
	"runtime"
	"testing"
)

// TestValidateNamePortable locks down the Windows-portability rule added to
// validateName: reserved DOS device basenames (checked before the first
// dot, so an extension doesn't hide them) and names ending in a trailing
// dot or space are rejected at create time.
func TestValidateNamePortable(t *testing.T) {
	cases := []struct {
		name    string
		wantErr bool
	}{
		// Reserved device basenames, bare, case-insensitive.
		{"CON", true},
		{"con", true},
		{"Con", true},
		{"PRN", true},
		{"prn", true},
		{"AUX", true},
		{"aux", true},
		{"NUL", true},
		{"nul", true},
		{"Nul", true},
		{"COM1", true},
		{"com1", true},
		{"COM9", true},
		{"COM0", true},
		{"LPT1", true},
		{"lpt1", true},
		{"LPT9", true},
		{"LPT0", true},

		// Reserved basenames with an extension — the DOS device rule keys
		// on the portion before the first dot.
		{"nul.html", true},
		{"Con.txt", true},
		{"COM1.tar.gz", true},
		{"NUL.tar.gz", true},
		{"lpt3.html", true},

		// The classic off-by-one bug: names that merely *start* with a
		// device name must be accepted.
		{"console", false},
		{"nulls", false},
		{"com1x", false},
		{"com10", false}, // COM10 is not a reserved device name
		{"lpt10", false},
		{"prNt", false},
		{"auxiliary", false},

		// Trailing dot / trailing space.
		{"foo.", true},
		{"foo ", true},
		{"a.b.", true},

		// A representative set of names that must still be accepted.
		{"a", false},
		{"my-artifact", false},
		{"v1.2_final", false},
		{"A9", false},
		{"img.png", false},
		{"q3-report-v2", false},
		{"index.html", false},
	}
	for _, c := range cases {
		err := validateName(c.name)
		if c.wantErr && err == nil {
			t.Errorf("validateName(%q) = nil, want error", c.name)
		}
		if !c.wantErr && err != nil {
			t.Errorf("validateName(%q) = %v, want nil", c.name, err)
		}
	}
}

// TestValidateFilePathRejectsDeviceNames confirms the portable-name rule
// reaches asset paths inside a published folder too: a CON.html asset is
// just as unportable as an artifact named CON.
func TestValidateFilePathRejectsDeviceNames(t *testing.T) {
	bad := []string{"CON.html", "nul.html", "assets/COM1.png", "img/lpt3.svg.gz"}
	for _, p := range bad {
		if err := ValidateFilePath(p); err == nil {
			t.Errorf("ValidateFilePath(%q) = nil, want error", p)
		}
	}
	good := []string{"console.html", "nulls.png", "assets/com10.png"}
	for _, p := range good {
		if err := ValidateFilePath(p); err != nil {
			t.Errorf("ValidateFilePath(%q) = %v, want nil", p, err)
		}
	}
}

// TestValidateSegmentAcceptsDeviceNames locks down the split-by-intent
// property: validateSegment (lookup/removal of an existing entry — URLs,
// delete, unwatch) must NOT apply checkPortableName's CREATE-time rule, so a
// watched repo that already named a file/folder CON, nul.txt, or "trailing."
// stays reachable and deletable — on Linux, unconditionally.
//
// On Windows the ADR (§7.5, R11) deliberately narrows this: validateSegment
// gains a platform-pair extension (checkLookupSegmentPlatform,
// names_windows.go) that DOES refuse a reserved device basename and a
// trailing dot/space in a lookup segment there — belt-and-braces alongside
// the handle-relative primitive, which already fails these closed for free
// (ADR §6.10) — plus ':' (an NTFS alternate-data-stream selector), which is
// the one live case (M12.C_stream). This is intentionally NOT a Linux
// regression: ':' is an ordinary Linux filename character a watched Linux
// repository may legitimately use, so checkLookupSegmentPlatform's Linux
// twin is a permanent no-op and the assertions below hold there exactly as
// they did before the ADR. Split by runtime.GOOS rather than forked into two
// files, since the split-by-intent DOCTRINE (lookup stays loose) is shared;
// only this one platform's answer changed.
func TestValidateSegmentAcceptsDeviceNames(t *testing.T) {
	names := []string{
		"CON", "con", "NUL", "nul.txt", "COM1", "COM1.tar.gz",
		"LPT9", "lpt0", "trailing.", "trailing ",
	}
	for _, s := range names {
		err := validateSegment(s)
		if runtime.GOOS == "windows" {
			if err == nil {
				t.Errorf("validateSegment(%q) = nil, want a rejection on Windows (ADR §7.5: reserved device names and a trailing dot/space are refused in lookup segments there, belt-and-braces alongside the handle-relative primitive)", s)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateSegment(%q) = %v, want nil (lookup must stay looser than create)", s, err)
		}
	}
}

// TestValidateSegmentADSSyntax is the live half of the same ADR §7.5 rule:
// unlike the reserved-device-name/trailing-dot-space cases above (which the
// handle-relative primitive already refuses for free on Windows — defence in
// depth here), a ':' in a lookup segment is a genuinely reachable NTFS
// alternate-data-stream selector (spike-findings.md M12.C_stream,
// M12.relative_open: "doc.html:hidden" opens a hidden second stream rather
// than 404ing) that validateSegment must refuse on Windows and must NOT
// refuse on Linux, where ':' is an ordinary, legal filename byte.
func TestValidateSegmentADSSyntax(t *testing.T) {
	cases := []string{"index.html:hidden", "C:evil", "a:b:c", "doc.html::$DATA"}
	for _, s := range cases {
		err := validateSegment(s)
		if runtime.GOOS == "windows" {
			if err == nil {
				t.Errorf("validateSegment(%q) = nil on Windows, want a rejection (ADR §7.5, M12.C_stream: this is a live alternate-data-stream selector, not merely an odd filename)", s)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateSegment(%q) = %v on Linux, want nil (':' is an ordinary legal filename character there)", s, err)
		}
	}
}
