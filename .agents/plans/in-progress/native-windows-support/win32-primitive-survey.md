---
title: Win32 Primitive Survey (orchestrator findings for Phase 1)
status: draft
created: 2026-08-26
links: [./native-windows-support.md, ../../../spec/native-windows-support.md]
---

# Win32 Primitive Survey

Measured facts gathered before the P1.2 spike, from the toolchain and module
cache actually pinned by this repo. These are inputs to the P1.6 ADR, not the
decision itself. Anything marked **must measure** is a claim this survey could
not settle from source alone and that the spike has to prove on real Windows.

Toolchain: `go1.27.0`; module `go 1.26.5`; `golang.org/x/sys v0.41.0`.

## Finding 1 — the stdlib already ships a handle-anchored Windows rooted FS

`os.Root` (`$GOROOT/src/os/root_windows.go`) is not a path-prefix wrapper. On
Windows it opens each path component with `NtCreateFile` using
`OBJECT_ATTRIBUTES.RootDirectory = <parent handle>` and
`FILE_OPEN_REPARSE_POINT`, i.e. exactly the "preferred" strategy the spec names
in *Windows rooted filesystem*. Relevant properties read from that source:

- `openat` refuses to traverse a reparse point: on `ELOOP`/`ENOTDIR` it reads
  the reparse link and returns `errSymlink`, so traversal never silently
  follows a junction or symlink (`root_windows.go:140-154`).
- `rootStat` distinguishes lstat from stat by checking
  `isReparseTagNameSurrogate()` on the opened handle rather than by string
  inspection (`root_windows.go:211-244`) — this is tag inspection, which is
  what the spec demands over "the reparse attribute alone".
- `os.Root` documents that it rejects Windows reserved device names such as
  `NUL` and `COM1`, on every method.
- `Root.OpenRoot(name)` returns a *new* handle-anchored root. That is a precise
  fit for the spec's "exactly one approved watch boundary": read the watch
  link's target, then open a second, independent root on it, rather than
  teaching one root to cross a link.
- Available methods cover every mechanism the Linux backend needs: `Mkdir`
  (atomic `EEXIST` claim), `OpenFile`, `ReadDir` via `FS()`/`Open`, `Lstat`,
  `Readlink`, `Remove`, `RemoveAll`, `Rename`, `Symlink`, `WriteFile`.

Consequence for the ADR: the question is no longer "can Windows do
handle-relative traversal" but "is `os.Root` a sufficient and *auditable*
mechanism, or does the store need its own `NtCreateFile` wrapper".

## Finding 2 — `os.Root.Symlink` cannot create the watch link we need

`rootSymlink` (`root_windows.go:246-293`) decides the symlink flavour like this:

- `SYMLINKAT_DIRECTORY` is set **only** when the target is root-relative and
  `r.Stat(destPath)` reports a directory.
- `SYMLINKAT_RELATIVE` is set only when the target has no volume name.

A watch target is an absolute path outside the store (`C:\Users\x\proj`), so it
has a volume name, so neither flag is set — `os.Root.Symlink` would create a
**file** symlink pointing at a directory. On Windows the two are not
interchangeable. So watch-link *creation* must be hand-rolled regardless of what
the ADR decides for traversal: either `CreateSymbolicLinkW` with
`SYMBOLIC_LINK_FLAG_DIRECTORY | SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE`,
or a handle-relative directory create followed by `FSCTL_SET_REPARSE_POINT`.

**Must measure:** whether a handle-relative create-then-set-reparse sequence
preserves the create-only atomicity `Watch` relies on today (`store.go:631`
`unix.Symlinkat` → `EEXIST`), and what the intermediate state looks like to a
concurrent reader if the process dies between the two steps.

## Finding 3 — `x/sys/windows` v0.41.0 has the primitives but not the wrappers

Present: `NtCreateFile`, `GetFileInformationByHandleEx`,
`GetFinalPathNameByHandle`, `LockFileEx`, `IO_REPARSE_TAG_SYMLINK`,
`IO_REPARSE_TAG_MOUNT_POINT`, `FILE_OPEN_REPARSE_POINT`, and the
`STATUS_IO_REPARSE_TAG_*` NTSTATUS values.

Absent: `Openat` and `Symlinkat`. Those live in the stdlib-internal
`internal/syscall/windows` package and are not importable. A hand-rolled backend
must reimplement both on top of `NtCreateFile`.

## Finding 4 — the Linux backend's use of `/proc/self/fd` has no Windows analogue

`fdPath` (`storefs_linux.go:39`) turns a pinned descriptor back into a path so
`loadArtifact` can `os.ReadDir` it while keeping the anchor (`store.go:469`,
`store.go:574`). Windows has no `/proc/self/fd`.
`GetFinalPathNameByHandleW` returns a path but re-resolves it on next use,
reintroducing the TOCTOU the anchor exists to prevent.

This is the single largest structural difference and the ADR must address it
head-on: either `loadArtifact` is refactored to read through an open directory
handle on both platforms, or the Windows path accepts a documented, argued
weakening for read-only artifact metadata.

## Finding 5 — advisory locking

`annotations.go:103,127-131,155` uses `unix.Flock` for the annotation guard.
The Windows equivalent is `LockFileEx` with `LOCKFILE_EXCLUSIVE_LOCK` and
`LOCKFILE_FAIL_IMMEDIATELY`, which is mandatory rather than advisory and is
released on handle close. **Must measure:** behaviour of a shared lock upgrade
and of lock release ordering versus the `defer`s in `lockAnnotations`.

## Finding 6 — directory identity for the watcher

`internal/watch/watch.go:281` reads `*syscall.Stat_t` from `os.Stat`. Windows
`os.Stat` yields `*syscall.Win32FileAttributeData`, which carries no volume
serial or file ID. Windows identity therefore needs a directory handle
(`FILE_FLAG_BACKUP_SEMANTICS`) plus `GetFileInformationByHandle` or
`GetFileInformationByHandleEx(FileIdInfo)`. `FileIdInfo` gives a 128-bit file ID
and is the correct choice on ReFS; `ByHandleFileInformation` gives only 64 bits.
