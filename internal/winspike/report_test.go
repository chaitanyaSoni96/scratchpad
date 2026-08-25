//go:build windows

package winspike

import (
	"fmt"
	"testing"
)

// Verdicts used by Report. The CI log is the evidence, so these strings are
// part of the interface: `Select-String 'WINSPIKE|'` must produce a complete,
// stable table.
const (
	Yes         = "YES"          // the property holds
	No          = "NO"           // the property does not hold
	Partial     = "PARTIAL"      // holds under stated conditions only
	NotMeasured = "NOT-MEASURED" // could not be measured here; detail says why
	Info        = "INFO"         // environment fact, not a yes/no question
	Violation   = "SECURITY-FAIL" // a REQUIRED security property is contradicted
)

// Report prints one measurement. Measurements report; they do not assert.
// A surprising answer is data, not a test failure — the only exception is
// RequireProperty below.
//
// Format (one line, pipe-separated, greppable):
//
//	WINSPIKE|<id>|<verdict>|<detail>
func Report(t testing.TB, id, verdict, format string, args ...any) {
	t.Helper()
	t.Logf("WINSPIKE|%s|%s|%s", id, verdict, fmt.Sprintf(format, args...))
}

// RequireProperty is the one place a measurement is allowed to fail the build:
// when the runner contradicts a property the Windows backend is REQUIRED to
// have, a green log would be worse than useless.
func RequireProperty(t testing.TB, id string, ok bool, format string, args ...any) {
	t.Helper()
	detail := fmt.Sprintf(format, args...)
	if ok {
		Report(t, id, Yes, "REQUIRED property holds: %s", detail)
		return
	}
	Report(t, id, Violation, "REQUIRED property CONTRADICTED: %s", detail)
	t.Errorf("winspike %s: required security property does not hold: %s", id, detail)
}
