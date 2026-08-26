//go:build !windows

package testutil

import "testing"

// WatchLinkTestEnv mirrors the Windows-only implementation's constant so a
// shared test can reference it uniformly; it has no effect outside Windows.
const WatchLinkTestEnv = "SCRATCHPAD_TEST_WATCH_LINKS"

// RequireWatchLinks never skips outside Windows: there is only one link
// type there and SymlinkCapable already covers it unconditionally.
func RequireWatchLinks(t testing.TB) { t.Helper() }

// WatchLinkCapable is always true outside Windows.
func WatchLinkCapable() bool { return true }
