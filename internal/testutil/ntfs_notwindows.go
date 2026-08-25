//go:build !windows

package testutil

import "testing"

// RequireNTFS is a no-op outside Windows: the NTFS restriction only exists
// for the native Windows backend. On Windows the _windows.go implementation
// inspects the volume containing dir and skips when it is not NTFS.
func RequireNTFS(t testing.TB, dir string) {
	t.Helper()
	_ = dir
}
