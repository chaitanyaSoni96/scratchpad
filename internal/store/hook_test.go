package store

import "testing"

// setStoreOpHook installs fn as the store's deterministic race hook for the
// duration of the test and guarantees the hook is cleared when the test
// finishes, so a failing test cannot leak it into later tests. One-shot hooks
// disarm themselves early by calling clearStoreOpHook from inside fn.
func setStoreOpHook(t *testing.T, fn func(op string)) {
	t.Helper()
	testStoreOpHook = fn
	t.Cleanup(clearStoreOpHook)
}

// clearStoreOpHook removes the installed race hook.
func clearStoreOpHook() { testStoreOpHook = nil }
