//go:build !windows

// Package winspike is the Phase 1 (P1.2 / P1.4) Windows measurement harness.
//
// TEMPORARY. It exists to answer the open questions in
// .agents/plans/in-progress/native-windows-support/threat-model-windows.md §8
// on a real Windows runner, and is deleted at the end of Phase 1. Nothing
// outside this package may import it, and it must never import internal/store.
//
// On non-Windows platforms the package is deliberately empty so that
// `go build ./...`, `go vet ./...` and `go test ./...` on Linux compile it to
// nothing. All content lives in files tagged `//go:build windows`.
package winspike
