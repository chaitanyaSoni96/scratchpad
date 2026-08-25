---
title: Platform API Inventory
type: reference
status: draft
created: 2026-08-26
links: [native-windows-support.md, ../../../spec/native-windows-support.md, threat-model-windows.md]
---

# Platform API Inventory

Phase 2 (P2.1-P2.4) deliverable for [Native Windows Support](native-windows-support.md).
Maps every direct Unix operation that was embedded in the shared (untagged)
files — `internal/store/store.go`, `internal/store/annotations.go`,
`internal/watch/watch.go` — to the narrowest platform-private helper that now
carries it. This is the document the P2.7 reviewer checks the code against:
for every helper, its signature, the Linux syscall(s)/flags implementing it,
the security property it carries (cited to `threat-model-windows.md`), and
every shared call site that uses it.

No general filesystem interface was introduced. Every helper below is a
concrete, build-tag-selected function pair (`internal/store/*_linux.go` /
`*_windows.go`, `internal/watch/identity_unix.go` / `identity_windows.go`),
exactly like the existing `rootedFS`/`annotationFS` split. Shared files hold
policy (what is create-only, what "already exists" means, what a link
governs); platform files hold mechanism (the syscall that enforces it).

## 1. Generic fd/directory mechanism — `storefs_linux.go` / `storefs_windows.go`

These are not link-specific; they back Publish's create-only directory claim
and the fd bookkeeping every mutation shares.

| Helper | Signature | Linux mechanism | Security property (threat model) | Shared call sites |
|---|---|---|---|---|
| `closeFD` | `func closeFD(fd int)` | `unix.Close` | None on its own; correctness of the fds it closes comes from the functions below. | `store.go`: `ResolveFolder`, `ResolvePath` (×2), `Publish` (×2 defers), `Watch` (defer), `Unwatch` (defer), `Delete` (×3) |
| `mkdirClaim` | `func mkdirClaim(parent int, name string) error` | `unix.Mkdirat(parent, name, 0o755)`, EEXIST mapped to `errExists` | §3.5 R6: create-only claim issued relative to a **pinned parent fd**, not a re-resolved path — the property `TestPinnedMutationsIgnoreProjectSwap` proves. | `store.go`: `Publish` |
| `rmdirAt` | `func rmdirAt(parent int, name string) error` | `unix.Unlinkat(parent, name, AT_REMOVEDIR)` | Rollback of a claim that failed after `mkdirClaim` succeeded (Publish), fd-relative so it can't be redirected either. | `store.go`: `Publish` (rollback on `openDirAt` failure) |
| `openDirAt` | `func openDirAt(parent int, name string) (int, error)` | `unix.Openat(parent, name, O_RDONLY\|O_DIRECTORY\|O_CLOEXEC\|O_NOFOLLOW)` (pre-existing, unmoved) | R1/R3: one segment, one fd, `O_NOFOLLOW` refuses a symlinked final component. | `store.go`: `Publish`, `Delete` |
| `dirHasHTMLFD`, `fdPath`, `dupFD`, `mkdirsAt`, `writeFileAt`, `openFileAt`, `readAllAt`, `pruneAt`, `openPathFile`, `rootedFS`/`openRootedFS`/`.openRealDir`/`.openBrowsableDir`, `OpenDocument` | unchanged | pre-existing, unmoved | R1-R3 (unchanged; see original doc comments in `storefs_linux.go`) | unchanged |

`errExists` (`store.go`, shared): `var errExists = errors.New("scratchpad: name already exists")`.
A portable sentinel so `Publish`/`Watch` can do `errors.Is(err, errExists)`
without importing an OS errno package. `mkdirClaim` and `symlinkAt` (below)
are the only functions that produce it, each after translating their own
platform's real "exists" error.

## 2. Link mechanism — `link_linux.go` / `link_windows.go` (new files, P2.3)

| Helper | Signature | Linux mechanism | Security property | Shared call sites |
|---|---|---|---|---|
| `symlinkAt` | `func symlinkAt(parent int, target, name string) error` | `unix.Symlinkat(target, parent, name)`, EEXIST → `errExists` | §3.6 R6: create-only, fd-relative, atomic. | `store.go`: `Watch` |
| `readlinkAt` | `func readlinkAt(parent int, name string) (string, error)` | `unix.Readlinkat(parent, name, buf)` | §3.6: idempotence check reads from the **same pinned parent** that created the link, not a fresh path lookup. | `store.go`: `Watch` (same-target no-op check) |
| `unlinkAt` | `func unlinkAt(parent int, name string) error` | `unix.Unlinkat(parent, name, 0)` | §3.7/§3.8 R7: removes only the named entry, never follows it, never descends. | `store.go`: `Unwatch`, `Delete` (watched-folder branch) |
| `isLinkAt` | `func isLinkAt(parent int, name string) (isLink bool, err error)` | `unix.Fstatat(parent, name, &st, AT_SYMLINK_NOFOLLOW)` + `st.Mode&S_IFMT==S_IFLNK` | §3.7/§3.8 R5: classification without following, fd-relative — this is what stops `Unwatch` from ever reaching a real directory and what routes `Delete` to unlink-only for a watched folder. | `store.go`: `Unwatch`, `Delete` |
| `IsLinkInfo` (exported) | `func IsLinkInfo(fi os.FileInfo) bool` | `fi.Mode()&os.ModeSymlink != 0` | Path-based classification for **read-only display/routing**, not mutation. See §3 below for the Windows caveat. | `store.go`: `annotate`, `WatchLinkFor`. `internal/web/server.go`: `folderUnwatch`. |
| `IsLinkEntry` (exported) | `func IsLinkEntry(e os.DirEntry) bool` | `e.Type()&os.ModeSymlink != 0` | Same as `IsLinkInfo`, for a `ReadDir` result. | `store.go`: `entryIsDir`, `List`'s walk, `Watches`. `internal/web/server.go`: `entryIsDirFS`. `internal/watch/watch.go`: `desiredDirs`. |

### Why `IsLinkInfo`/`IsLinkEntry` are exported

P2.3 asked me to judge whether exporting is right for the two extra call
sites the task named (`internal/web/server.go:237-238,329`) and
`internal/watch/watch.go:252`. I exported both functions directly (rather
than, say, keeping a private pair in `store` and duplicating a thin wrapper
in `web`/`watch`) because:

- `web` and `watch` already import `scratchpad/internal/store` for `Visible`,
  `Root`, etc. — no new dependency.
- The alternative (each package re-implementing `fi.Mode()&os.ModeSymlink`)
  is exactly the duplication the threat model warns about: three copies that
  can drift when Phase 3 changes the Windows mechanism, instead of one.
- These two functions are the **entire** point of doing this work: Phase 3
  only has to change two function bodies, in one file pair, for every "is
  this a link" decision across all three packages to pick up the fix.

### Why the fd-relative and path-based classifiers are separate (`isLinkAt` vs `IsLinkInfo`/`IsLinkEntry`)

They answer the same question from different data: `isLinkAt` is fd-relative
(Fstatat against a pinned parent, used only by mutation paths that already
hold a parent fd) and carries R5's "classify from the handle" property.
`IsLinkInfo`/`IsLinkEntry` are path/`os.FileInfo`/`os.DirEntry`-based, used
only by read-only listing (`List`, `Watches`, `WatchLinkFor`, `annotate`,
`folderUnwatch`, `desiredDirs`) where no fd is held. Collapsing them into one
signature would force either a spurious open on every list operation or a
loss of the fd-relative guarantee on every mutation — both worse than two
small functions.

## 3. Dead code deleted, not ported (judgement call)

`threat-model-windows.md` §2 identifies `ensureProjectDir` (store.go:253-276),
`rejectSymlinkParents` (store.go:278-299), `pruneEmpty` (store.go:798-807) and
`linksTo` (store.go:656-671) as **dead code**: path-based ancestors of the
current handle-anchored design, "kept only as documentation ... must not be
resurrected for the Windows port." I confirmed with
`grep -rn '\bensureProjectDir(\|\brejectSymlinkParents(\|\bpruneEmpty(\|\blinksTo('`
across `internal/` (including `_test.go`) that none of the four is called
from anywhere. All four contained raw `os.ModeSymlink`/`os.Lstat` checks that
P2.3 requires routing through a platform helper ("do not leave a single raw
`ModeSymlink` test in untagged store code").

Given they are confirmed unreachable, I deleted them rather than wrapping
their internals in `IsLinkInfo` — routing dead code through the new
classification helper would satisfy the letter of the instruction while
adding permanent, pointless surface area to the inventory and to Phase 3's
review burden for logic nothing calls. This is a judgement call beyond the
literal task list (which only asked to convert/route `ModeSymlink` sites, not
delete functions); I flagged it here rather than silently doing it, and it is
a pure deletion of unreachable code with a `go build`/`go vet` clean result
and no test referencing them, so there is no behavior to regress.

## 4. Annotation locking — `annotationfs_linux.go` / `annotationfs_windows.go`

| Helper | Signature | Linux mechanism | Security property | Shared call sites |
|---|---|---|---|---|
| `flockFile` | `func flockFile(f *os.File, exclusive bool) error` | `unix.Flock(fd, LOCK_SH or LOCK_EX)` | §3.16: shared/exclusive coordination between `SaveNotes`/`LoadNotes`/etc. and `Delete`/`Unwatch`. | `annotations.go`: `lockAnnotations`, `lockDocument` |
| `funlockFile` | `func funlockFile(f *os.File) error` | `unix.Flock(fd, LOCK_UN)` | Release counterpart. | `annotations.go`: `annotationLock.Close` |
| `openLockFileAt` | `func openLockFileAt(parent int, name string) (*os.File, error)` | `unix.Openat(parent, name, O_CREAT\|O_RDWR\|O_CLOEXEC\|O_NOFOLLOW, 0o600)` | Per-document lock file, fd-relative creation (never follows an existing symlink at that name). | `annotations.go`: `lockDocument` |

### The store-root `flock` has NO Windows equivalent — recorded per task instructions

`lockAnnotations` calls `flockFile(ann.storeRoot, exclusive)` — a lock on the
**store-root directory descriptor**, not on `.annotations` itself, because
(per `annotations.go`'s own comment, preserved verbatim) "the store-root
inode is stable even if a hostile process renames and replaces
`.annotations`". This mechanism is preserved exactly on Linux
(`flockFile`/`funlockFile` are 1:1 wrappers around the same `unix.Flock`
calls on the same descriptor).

On Windows there is no equivalent: `LockFileEx` locks byte ranges of a
**file**, not a directory handle (threat model **M14**, **RR6**). The
`annotationfs_windows.go` stub for `flockFile`/`funlockFile` simply returns
`errWindowsUnimplemented` — it does not attempt a substitute, because every
substitute considered in the threat model (a lock file inside
`.annotations`) reintroduces the exact replacement hazard the store-root
lock exists to avoid. **This needs a real Phase 3 design, not a mechanical
port** — recorded here and in `annotationfs_windows.go`'s doc comment so it
is not mistaken for a simple missing implementation. `openLockFileAt`'s
per-document lock, by contrast, likely ports mechanically (`LockFileEx` on an
ordinary file opened with `FILE_SHARE_READ|WRITE|DELETE`) — also noted in
the stub's doc comment.

## 5. Directory identity — `internal/watch/identity_unix.go` / `identity_windows.go` (P2.2)

Old signature: `identity(path string) (dirIdentity, error)`, calling
`os.Stat(path)` then type-asserting `info.Sys().(*syscall.Stat_t)`.

**New signature:** `identity(dir *os.File) (dirIdentity, error)` — takes an
**already-open directory handle**, not a path. This is a deliberate shape
change made now per the task's explicit instruction ("If that forces a
signature change, make it now ... do not leave Phase 4 with a signature that
cannot work"): the threat model states Windows identity comes from
`GetFileInformationByHandleEx(FileIdInfo)` on an open handle
(§3.17, §4.7, R14), and a path-based signature could never satisfy that on
Windows without introducing exactly the check-then-use pattern the property
exists to avoid ("the identity must come from the same handle the watch is
registered on, or the identity is again a check-then-use" — spec quote in
threat model §3.17).

To supply that handle, `desiredDirs` (`watch.go`) now opens each directory
once with `os.Open(dir)` and uses that single `*os.File` for **both**
`identity(f)` and `f.ReadDir(-1)` (previously two independent path lookups:
`os.Stat(canonical)` for identity, `os.ReadDir(dir)` for entries). This is
mechanically equivalent on Linux — `f.Stat()` is an `Fstat` on the same fd
`f.ReadDir` uses, giving identical `dev`/`ino` values to the old
`os.Stat(canonical)` since both ultimately name the same inode — and is
verified by the unchanged watch test suite (`go test ./internal/watch -race
-count=3` passes; the set of passing test names is unchanged, see
Verification below).

`identity_unix.go` (linux, and every other unix-like GOOS via the `unix`
build-tag): `dir.Stat()` → `fi.Sys().(*syscall.Stat_t)` → `dev`/`ino`.
Mechanically identical to the old code, just fed by a handle instead of a
path.

`identity_windows.go`: stub returning `dirIdentity{}, errWindowsUnimplemented`.
Its doc comment states the required real mechanism
(`GetFileInformationByHandleEx(FileIdInfo)` on the handle) so Phase 3/4 does
not "simplify" the signature back to a path.

**`dirIdentity` struct itself was intentionally left unchanged**
(`dev, ino uint64`), not widened to fit a 128-bit `FileId` + separate
`VolumeSerialNumber`. This is a judgement call: the task's instruction was
specifically about `identity()`'s *signature* (handle vs. path), not the
struct's field layout, and guessing the Windows field shape now (a `[16]byte`?
two `uint64`s? a separate volume field?) without the real
`GetFileInformationByHandleEx` call in hand risks getting it wrong in a way
Phase 3 then has to undo. Since Windows `identity()` is a pure stub today
(dirIdentity{} zero value, never compared against anything real), there is no
cost to deferring the struct shape to Phase 3/4, when it can be sized against
the actual Win32 struct.

`entry.Type()&fs.ModeSymlink != 0` in `desiredDirs` was replaced with
`store.IsLinkEntry(entry)` — see §2 above for why it's the same helper
`store.List`/`Watches` use, not a local copy. `"io/fs"` and `"syscall"` are
no longer imported by `watch.go`.

## 6. Sites left unconverted, with owning task

- **`internal/web/server.go`: four `/proc/self/fd/%d` string-formats**
  (`docCount` line ~296, plus lines ~555, ~605, ~648) construct a Linux-only
  path directly, bypassing `store.fdPath` entirely. These are **not** raw
  `unix.*`/`syscall.Stat_t` references, so they do not fail the P2.4 grep
  gate, and web/server.go beyond the two named `ModeSymlink` sites was
  explicitly out of this task's scope. They will compile on Windows but
  silently misresolve at runtime (no `/proc` filesystem). **Flagging for
  Phase 3/4** — whoever ports the web layer's directory-handle usage should
  route these through `store.fdPath`-equivalent platform mechanism (or
  restructure to avoid needing a re-derived path at all, per threat model
  §2's point about `fdPath` being "the single hardest porting constraint").
- **`internal/winspike/`** — another agent's package, explicitly out of
  scope for this task; not touched, not inventoried here.

## Verification (P2.1-P2.4 gate)

```text
$ make test
go vet ./... && go test ./... && scripts/check-make.sh
ok  	scratchpad/cmd/scratchpad	0.008s
ok  	scratchpad/cmd/scratchpad-web	0.013s
ok  	scratchpad/internal/store	0.021s
ok  	scratchpad/internal/watch	0.450s
ok  	scratchpad/internal/web	0.014s
check-make: ok

$ go test ./... -count=1                     # ok, all packages
$ go test ./internal/store ./internal/watch -race -count=3   # ok
$ GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...  # succeeds
$ GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/...  # succeeds
$ GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...      # succeeds (all packages)
$ GOOS=windows GOARCH=amd64 go vet ./...                      # clean
$ go vet ./...                                                # clean (linux)

$ grep -rn "x/sys/unix\|syscall\." --include='*.go' . | grep -v "_linux.go\|_unix.go\|_test.go"
internal/winspike/winfs.go: ... (another agent's package; out of scope)
# — nothing under internal/store, internal/watch, internal/web, cmd/

$ go test ./... -v -count=1 2>&1 | grep -c -- "--- SKIP"
0

# Passing test names before vs. after this change: byte-identical
# (git stash comparison of `sort`ed "--- PASS" test names, both runs 96 lines).
```
