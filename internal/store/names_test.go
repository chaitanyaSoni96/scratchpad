package store

import "testing"

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
// delete, unwatch) must NOT apply the portable-name rule, so a watched repo
// that already named a file/folder CON, nul.txt, or "trailing." stays
// reachable and deletable.
func TestValidateSegmentAcceptsDeviceNames(t *testing.T) {
	names := []string{
		"CON", "con", "NUL", "nul.txt", "COM1", "COM1.tar.gz",
		"LPT9", "lpt0", "trailing.", "trailing ",
	}
	for _, s := range names {
		if err := validateSegment(s); err != nil {
			t.Errorf("validateSegment(%q) = %v, want nil (lookup must stay looser than create)", s, err)
		}
	}
}
