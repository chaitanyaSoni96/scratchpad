//go:build !unix

package testutil

// isUnix mirrors the standard "unix" build constraint so RequireUnix skips
// exactly where a //go:build unix file would be excluded.
const isUnix = false
