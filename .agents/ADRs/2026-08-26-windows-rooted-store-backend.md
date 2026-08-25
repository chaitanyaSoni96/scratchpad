---
title: Windows Rooted Store Backend
type: adr
status: proposed
created: 2026-08-26
links:
  - ../plans/in-progress/native-windows-support/native-windows-support.md
  - ../plans/in-progress/native-windows-support/spike-findings.md
  - ../plans/in-progress/native-windows-support/threat-model-windows.md
  - ../plans/in-progress/native-windows-support/win32-primitive-survey.md
  - ../plans/in-progress/native-windows-support/platform-api-inventory.md
  - ../plans/in-progress/native-windows-support/reviews/P2.7-boundary-review.md
  - ../spec/native-windows-support.md
---

# Windows Rooted Store Backend

## Status

**Proposed.** P1.6 deliverable. P1.7 red-teams it; P1.8 accepts it or stops the
project. Phase 3 is implemented from this document; where it disagrees with
`threat-model-windows.md` or `.agents/spec/native-windows-support.md`, this
document wins and says so explicitly (§10).

## Evidence rule

Every design claim below carries either a measurement ID from
[spike-findings.md](../plans/in-progress/native-windows-support/spike-findings.md)
(192 measurements, five CI runs, `windows-2025` amd64 and `windows-11-arm`
arm64, 17 REQUIRED security properties holding, zero `SECURITY-FAIL`) or the
marker **[UNMEASURED]**. An unmeasured assumption presented as a fact is the
worst thing this document could contain, so every one of them is flagged inline
and collected in §9.

---

## 1. Context

The Linux store's containment is three ideas: a descriptor is a reference to an
inode rather than to a name; `O_NOFOLLOW` plus a segment-at-a-time walk refuses
links at every component; and every mutation verb takes a parent descriptor and
a bare name. Windows has no `openat`, no `O_NOFOLLOW`, no `/proc/self/fd`, and
no way to lock a directory handle. The question P1.2/P1.4 existed to answer was
whether documented Win32 primitives can re-earn all three properties, or whether
a Windows port necessarily degrades to validate-then-reopen — which the threat
model shows is an arbitrary-file-read and arbitrary-tree-delete primitive in a
product that serves an unauthenticated HTTP endpoint (`RR1`, `RR2`).

They can. This ADR records the design, and it records honestly the four places
where the argument is weaker than the Linux original.

---

## 2. Decision

**Hand-roll the Windows rooted filesystem on `NtCreateFile` with
`OBJECT_ATTRIBUTES.RootDirectory` set to a retained parent handle and
`OBJ_CASE_INSENSITIVE | OBJ_DONT_REPARSE` in `Attributes`.** Do not build on
`os.Root` (§8.1). Keep the existing concrete `rootedFS` / `annotationFS`
build-tag split; introduce no interface and no VFS (spec, *Shared policy,
platform mechanisms*; verified absent by P2.7 §2).

The seven load-bearing mechanisms, each a measured twin of a Linux syscall:

| Linux | Windows | Evidence |
|---|---|---|
| `openat(O_RDONLY\|O_DIRECTORY\|O_NOFOLLOW)` | `NtCreateFile(FILE_OPEN, FILE_DIRECTORY_FILE, OBJ_DONT_REPARSE)` | `P12.openrealdir` |
| `openat(O_RDONLY\|O_NOFOLLOW)` + `fstat S_IFREG` | `NtCreateFile(FILE_OPEN, FILE_NON_DIRECTORY_FILE, OBJ_DONT_REPARSE)` — **one** operation | `P12.openfile_isdir` |
| `mkdirat` → `EEXIST` | `NtCreateFile(FILE_CREATE, FILE_DIRECTORY_FILE)` → `STATUS_OBJECT_NAME_COLLISION` | `P12.mkdir_excl` (REQUIRED) |
| `unlinkat` | `NtCreateFile(FILE_OPEN_REPARSE_POINT)` + `NtSetInformationFile(FileDispositionInformationEx, DELETE\|POSIX)` | `P12.deleteat`, `M10.posix_nt` (REQUIRED) |
| `fstatat(AT_SYMLINK_NOFOLLOW)` | `FILE_ATTRIBUTE_TAG_INFO` read from that handle | `P14.classify.*` |
| `renameat(parent, …, parent, …)` | `NtSetInformationFile(FileRenameInformationEx, RootDirectory=parent)` | `M9` (REQUIRED) |
| `fdPath` = `/proc/self/fd/N` | `DuplicateHandle` + `os.NewFile(dup).ReadDir` | `M16` (REQUIRED) |

`OBJ_DONT_REPARSE` is the primitive the whole design rests on. It fails the open
with `STATUS_REPARSE_POINT_ENCOUNTERED` (`0xC000050B`) if **any** component of
`ObjectName` is a reparse point, not only the final one (`M1.intermediate`,
REQUIRED). The control matters as much as the result: the same open with
`FILE_OPEN_REPARSE_POINT` and *without* `OBJ_DONT_REPARSE` **succeeds** through a
junction (`M1.weak_flag_traverses`), so R3's falsifier — "a design that relies
solely on `FILE_FLAG_OPEN_REPARSE_POINT`" — is a real reachable defect on real
Windows, not a documentation reading.

Four consequences follow immediately and are non-negotiable:

1. **Every mutation is issued relative to a handle that was pinned before the
   check that authorised it.** A handle follows its object through a rename
   (`M7.namefollows`), and a mutation through a retained handle lands in the
   original object even after the pinned ancestor is renamed away and a
   same-named decoy is created in its place (`M7.redirect`, REQUIRED). That is
   the Windows twin of `TestPinnedMutationsIgnoreProjectSwap`.
2. **Classification is always `FILE_ATTRIBUTE_TAG_INFO.ReparseTag` from an open
   handle, never `fs.FileMode`, never `FILE_ATTRIBUTE_REPARSE_POINT` alone, and
   never the name-surrogate bit** (§5).
3. **Identity is `FILE_ID_INFO`** — `VolumeSerialNumber` plus a 128-bit
   `FileId`. A rename leaves it unchanged (`R13.rename`); a new directory created
   under the same name has a different one (`R13.replace`, REQUIRED). Every
   string-prefix path comparison is deleted or demoted to advisory (§7).
4. **No path is ever re-resolved inside a mutation.** The three surviving
   re-resolutions are read-only or advisory, are named individually in §6.9, and
   each has an exact Linux counterpart.

---

## 3. The backend API

Filling the Phase 2 stubs in `storefs_windows.go`, `link_windows.go`,
`annotationfs_windows.go` and `internal/watch/identity_windows.go`.

### 3.1 Handle representation

Shared code keeps passing directory handles as `int`, unchanged from Phase 2.
A Win32 `HANDLE` is pointer-sized, so it round-trips through `int` on the two
supported architectures, and `windows.InvalidHandle` (`^Handle(0)`) is `int(-1)`
— the same sentinel Linux already uses. 32-bit Windows is not a target
(spike §7), so `win32_windows.go` carries a compile-time assertion that fails
the build on a 32-bit `uintptr`; the `FILE_RENAME_INFO` layout depends on the
same assumption.

This is a deliberate refusal to change the P2 boundary: the spec says public
APIs should not change unless a platform requirement makes it unavoidable, and
this one does not.

### 3.2 `storefs_windows.go`

```go
// objectID is FILE_ID_INFO on Windows, dev+ino on Linux. Comparable,
// opaque to shared code, never rendered into a path.
type objectID struct { vol uint64; id [16]byte }

type rootedFS struct {
	root   *os.File   // pinned SCRATCHPAD_ROOT directory handle
	path   string     // the resolved absolute root string THIS handle was opened from
	id     objectID   // recorded at pin time (R13)
	volume volumeInfo // filesystem name + serial, for the R18 gate
}

func openRootedFS(create bool) (*rootedFS, error)
func (r *rootedFS) close() error
func (r *rootedFS) verifyRoot() error   // R13: re-read FILE_ID_INFO, compare with r.id
func (r *rootedFS) openRealDir(segs []string, create, rejectArtifacts bool) (int, error)
func (r *rootedFS) openBrowsableDir(segs []string) (int, error)

func dupFD(fd int) (int, error)                            // DuplicateHandle(DUPLICATE_SAME_ACCESS)
func closeFD(fd int)                                       // CloseHandle
func openDirAt(parent int, name string) (int, error)       // FILE_OPEN|FILE_DIRECTORY_FILE|OBJ_DONT_REPARSE
func mkdirClaim(parent int, name string) error             // FILE_CREATE|FILE_DIRECTORY_FILE|OBJ_DONT_REPARSE
func rmdirAt(parent int, name string) error                // OPEN_REPARSE_POINT + FILE_DIRECTORY_FILE + POSIX dispose
func openFileAt(parent int, name string) (*os.File, error) // FILE_NON_DIRECTORY_FILE|OBJ_DONT_REPARSE
func readAllAt(parent int, name string) ([]byte, error)
func mkdirsAt(root int, segs []string) (int, error)
func writeFileAt(root int, segs []string, data []byte) error
func openPathFile(segs []string) (*os.File, error)
func pruneAt(r *rootedFS, segs []string)
func readDirFD(fd int) ([]os.DirEntry, error)              // M16: dup + os.NewFile + ReadDir
func statAt(parent int, name string) (entryMeta, error)    // NEW — the fstatat twin
func statLinkTarget(path string) (isDir, ok bool)          // NEW — bounded one-hop follow for listings
```

`entryMeta` is shared (`store.go`), because it is a domain shape:

```go
type entryMeta struct {
	IsDir     bool      // FILE_ATTRIBUTE_DIRECTORY and no reparse tag
	IsRegular bool      // neither directory nor reparse point
	IsLink    bool      // carries a tag on the watch allowlist (§5)
	Tag       uint32    // 0 on Linux; the raw reparse tag on Windows, for errors
	Size      int64
	ModTime   time.Time
}
```

`statAt` is the single classification primitive: it opens `name` — **one
component, enforced** — with `FILE_OPEN_REPARSE_POINT | OBJ_CASE_INSENSITIVE`
(not `OBJ_DONT_REPARSE`, because removing or classifying a link must open the
*link*), then reads `FILE_ATTRIBUTE_TAG_INFO` and `BY_HANDLE_FILE_INFORMATION`
from that handle. Final component and whole path coincide, so
`FILE_OPEN_REPARSE_POINT`'s final-component-only guarantee is sufficient here and
only here. The single-component restriction is a runtime check, not a comment.

**Ownership and lifetime, uniformly:**

- Every function that returns an `int` transfers ownership; the caller closes it
  with `closeFD` on success and on every error path (R15).
- Every function that returns an `*os.File` transfers ownership to Go; `Close`
  closes the underlying handle exactly once. `os.NewFile` is only ever handed a
  handle we own outright — `readDirFD` duplicates first so the `*os.File`'s
  `Close` cannot consume a caller's anchor (`M16`, and each duplicate restarts
  enumeration independently, which is why repeated `ReadDir` on one artifact
  works).
- Every walk closes the previous level's handle *before* replacing it, exactly as
  `storefs_linux.go` does; the prototype (`Root.OpenRealDir`,
  `Root.OpenBrowsableDir`) is the reference for the ordering.
- **All** handles are opened `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE`
  (R15) — without `FILE_SHARE_DELETE` the user cannot rename or delete their own
  store root in Explorer while the server runs, and our own renames of pinned
  directories stop working (`M7`: the rename only succeeds because every opener
  granted it).
- `rootedFS.root` lives for one store operation. `rootedFS.path` is the string
  that handle was actually opened from, and shared code uses **that** string for
  every advisory path computation in the same operation instead of calling
  `Root()` again (§7.3).

### 3.3 `link_windows.go`

```go
// watchTags is the entire allowlist. Nothing else is ever accepted as a link,
// anywhere, for any purpose.
var watchTags = [...]uint32{ioReparseTagSymlink, ioReparseTagMountPoint}

func symlinkAt(parent int, target, name string) error   // §6.6 two-step + interlock
func readlinkAt(parent int, name string) (string, error) // FSCTL_GET_REPARSE_POINT, allowlist, \??\ stripped
func unlinkAt(parent int, name string) error             // OPEN_REPARSE_POINT + POSIX dispose
func isLinkAt(parent int, name string) (bool, error)     // statAt().IsLink
func sameWatchTarget(existing, abs string) bool          // FILE_ID_INFO, not string equality
func watchLinkFlavour() linkFlavour                      // capability probe: symlink → junction → none
func IsLinkInfo(fi os.FileInfo) bool
func IsLinkEntry(e os.DirEntry) bool
```

`IsLinkInfo` / `IsLinkEntry` become `Mode()&(os.ModeSymlink|os.ModeIrregular) != 0`
on Windows. This is a **measured-correct over-approximation**, not a guess: a
junction is `ModeIrregular` and neither `ModeSymlink` nor `ModeDir`
(`P14.junction_modesymlink`, and `P14.junction_not_dir` is REQUIRED); a
non-Microsoft tag on a directory is `ModeDir | ModeIrregular`
(`RR1.unknown_tag_isdir`); `DEDUP` and `WOF` are reported as ordinary regular
files, so a deduplicated volume is not made unusable (threat model §4.2's
objection to a blanket reject). Over-approximating "is a link" is fail-**closed**
for every consumer: it stops descent, it reports the entry in `Watches`, and it
routes `Delete` to unlink-only.

`readlinkAt` returns the substitute name with the `\??\` (or `\??\UNC\`) prefix
stripped, and returns an **error** — not a value — for any tag outside
`watchTags`, so shared `Watch` code needs no tag awareness and its signature is
unchanged from Linux.

### 3.4 `annotationfs_windows.go`

```go
type annotationFS struct {
	storeRoot *os.File // pinned root
	root      *os.File // pinned .annotations
	lock      *os.File // pinned .annotations\.lock — the rendezvous object (§6.7)
}

func openAnnotationFS() (*annotationFS, error)
func (a *annotationFS) close() error
func (a *annotationFS) openDir(segs []string, create bool) (int, error)
func (a *annotationFS) readFile(segs []string) ([]byte, error)
func (a *annotationFS) writeFile(segs []string, data []byte) error
func (a *annotationFS) removeSubtree(segs []string) error
func (a *annotationFS) walk(segs []string, visit func([]string, []byte)) error
func removeTreeAt(parent int, name string) error

func lockRendezvous(a *annotationFS, exclusive bool) error // NEW — replaces flockFile(ann.storeRoot, …)
func unlockRendezvous(a *annotationFS) error
func flockFile(f *os.File, exclusive bool) error           // per-document lock only
func funlockFile(f *os.File) error
func openLockFileAt(parent int, name string) (*os.File, error)
```

`lockAnnotations` (shared, `annotations.go`) changes from
`flockFile(ann.storeRoot, exclusive)` to `lockRendezvous(ann, exclusive)`. The
policy — shared for normal work, exclusive held by `Delete`/`Unwatch` across
removing both content and notes — stays in shared code; only the object being
locked moves to the platform side. Linux's `lockRendezvous` is
`flockFile(a.storeRoot, …)`, byte-identical behaviour.

### 3.5 `internal/watch/identity_windows.go`

```go
type dirIdentity struct { vol uint64; id [16]byte }
func identity(dir *os.File) (dirIdentity, error) // GetFileInformationByHandleEx(FileIdInfo)
```

`dirIdentity`'s field layout moves into the platform files (P2.2 deliberately
deferred this). Shared code only compares values and uses them as map keys, so
an opaque comparable struct is enough. `ByHandleFileInformation` is **not**
acceptable: it carries 64 bits of file index, which is insufficient on ReFS
(survey Finding 6).

### 3.6 What `x/sys/windows` v0.41.0 does not ship

Measured (`M17.gaps`). All of the following must be defined by us, in **one**
file, `internal/store/win32_windows.go`, with byte offsets commented and
`unsafe` confined to it — the winspike prototype's layout for
`FILE_RENAME_INFO` is the reference and is deliberately hand-built rather than
declared as a Go struct so the offsets are auditable:

`Openat`, `Symlinkat`, `FILE_ATTRIBUTE_TAG_INFO`, `FILE_ID_INFO`,
`FILE_NAME_INFO`, `FILE_RENAME_INFO`, `FILE_DISPOSITION_INFO_EX`,
`SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE`, NT information classes
`FileDispositionInformationEx` (64) and `FileRenameInformationEx` (65), and every
reparse tag beyond `IO_REPARSE_TAG_SYMLINK` / `IO_REPARSE_TAG_MOUNT_POINT`.

Present and usable as shipped: `NtCreateFile`, `NtSetInformationFile`,
`GetFileInformationByHandleEx`, `SetFileInformationByHandle`, `DeviceIoControl`,
`GetVolumeInformationByHandle`, `LockFileEx`/`UnlockFileEx`,
`CreateSymbolicLink`, and the `OBJ_*` / `FILE_*` / `STATUS_*` constants.

### 3.7 Error translation

`NtCreateFile` returns an `NTSTATUS`, not a Win32 error. The backend keeps both:
the raw status for diagnostics, and a mapped Go error for policy. One type:

```go
type winError struct {
	Op     string
	Status windows.NTStatus // 0 when the source was a Win32 call
	Win32  syscall.Errno    // 0 when the source was an NT call
	err    error            // the fs.Err* or package sentinel
}
func (e *winError) Unwrap() error // preserves errors.Is for callers
```

| NTSTATUS | Win32 | Go mapping | Notes |
|---|---|---|---|
| `STATUS_OBJECT_NAME_COLLISION` | `ERROR_ALREADY_EXISTS` | `errExists` (wraps `fs.ErrExist`) | `M8.claim_over.*` |
| `STATUS_REPARSE_POINT_ENCOUNTERED` **from a `FILE_CREATE` claim** | — | `errExistsReparse` (wraps `errExists`, wraps `fs.ErrExist`) | `M8.claim_error_map` — §6.6 |
| `STATUS_REPARSE_POINT_ENCOUNTERED` from an **open** | `ELOOP` in Go's own map | `errReparse` | `M1.final`, `M2` |
| `STATUS_IO_REPARSE_TAG_NOT_HANDLED` (`0xC0000279`) | 1920 | `errReparse` | unknown tag, no filter driver — `M2.unknown_tag` |
| `STATUS_DELETE_PENDING` (`0xC0000056`) | `ERROR_ACCESS_DENIED` | `errDeletePending` — **never** `errExists` | `M10.legacy_pending`; R6's falsifier |
| `STATUS_OBJECT_NAME_NOT_FOUND` / `STATUS_OBJECT_PATH_NOT_FOUND` | 2 / 3 | `fs.ErrNotExist` | `M18.relative_open.*` |
| `STATUS_NOT_A_DIRECTORY` | `ERROR_PATH_NOT_FOUND` (3) | `errNotDir` — our own sentinel | `M2`: Go's map loses ENOTDIR, so do not rely on the errno |
| `STATUS_FILE_IS_A_DIRECTORY` | — | `errIsDir` | |
| `STATUS_ACCESS_DENIED` | 5 | `fs.ErrPermission` | |
| `STATUS_SHARING_VIOLATION` | `ERROR_SHARING_VIOLATION` (32) | `errSharing` — retryable | `M13.blocked` |
| — | `ERROR_LOCK_VIOLATION` (33) | `errLockViolation` — retryable | `M14.mandatory` |
| `STATUS_DIRECTORY_NOT_EMPTY` | `ERROR_DIR_NOT_EMPTY` (145) | `errNotEmpty` | `M4.nonempty` |
| — | `ERROR_PRIVILEGE_NOT_HELD` (1314) | `errNoLinkPrivilege` | `P14.privilege_not_held`, §6.6 |

The spec's requirement — "error translation should preserve `errors.Is`
behavior for existence, permission, and not-found cases" — is met by `Unwrap`
chaining to `fs.ErrExist` / `fs.ErrPermission` / `fs.ErrNotExist`. `errExists`
stays the shared sentinel `Publish` and `Watch` already branch on, so their
shared code is unchanged.

---

## 4. Containment proof

Per operation, against the threat model's numbered requirements. "Pinned" means
a handle obtained before the authorising check and never re-derived from a name.

### 4.0 The shape of the argument

Three facts, all measured, compose into containment:

- **F-a.** `OBJ_DONT_REPARSE` fails an open if any component is a reparse point
  (`M1.intermediate`, REQUIRED), and the negative control shows
  `FILE_OPEN_REPARSE_POINT` alone does not (`M1.weak_flag_traverses`).
- **F-b.** A handle names an object, not a name: renaming the object, or any
  ancestor, does not redirect operations issued through it (`M7.redirect`,
  REQUIRED; `FILE_NAME_INFO` on the retained handle reports the *new* path
  afterwards, which is the direct demonstration).
- **F-c.** A create-only claim is atomic against the pinned parent
  (`P12.mkdir_excl`, REQUIRED), and a destination-replacing rename is atomic
  against the pinned parent (`M9`, REQUIRED).

Every operation below is F-a for the walk, F-b for the anchor, F-c for the
mutation. Where an operation cannot be reduced to those three, that is called
out as a weakness rather than argued away.

### 4.1 Root open — R12, R13, R18

`Root()` is resolved **once per operation**. The string is validated before it
reaches Win32: it must be absolute and rooted with a drive letter; `C:foo`
(drive-relative), `\foo` (current-drive-relative), `\\server\share\…` (UNC),
`\\?\…` and `\\.\…` (device namespace) are refused with a distinct error
(R18, RR7 — threat model §4.8, §4.9). The handle is opened with
`FILE_FLAG_BACKUP_SEMANTICS | FILE_FLAG_OPEN_REPARSE_POINT`, then
`FILE_ATTRIBUTE_TAG_INFO` must report `FILE_ATTRIBUTE_DIRECTORY` set and
`FILE_ATTRIBUTE_REPARSE_POINT` clear — so a root that is a regular file, a
symlink, a junction **or a volume mount point** is refused on the *tag*, not on
`fs.ModeSymlink` (which misses junctions entirely, §3.4 of the threat model).
`FILE_ID_INFO` is recorded (`P12.root`, `P12.root_file`).

`verifyRoot()` re-reads `FILE_ID_INFO` and compares before every mutation
(R13). It distinguishes a rename — identity unchanged, not an error
(`R13.rename`) — from a replacement — different identity, hard error
(`R13.replace`, REQUIRED).

**Weakness, stated:** R13 is a *diagnostic*, not a control. Because the name is
never re-resolved, a replaced root cannot redirect us anyway; the check exists to
turn "silently operating on the wrong store" into a loud error. It also cannot
detect replacement of an intermediate project directory — that case is covered
structurally by the handle chain (F-b), not by identity.

**Filesystem gate (R18):** `GetVolumeInformationByHandle` on the root handle
gives the filesystem name. Per threat model §9.8 the gate is the **first
mutation**, not the first open — the filesystem cannot be known before a handle
exists. NTFS proceeds silently. Non-NTFS local volumes emit one warning and
proceed (§8.3). UNC and device roots were already refused above.

### 4.2 `openRealDir` — Publish/Watch/Unwatch/Delete ancestors — R1, R3, R6

Each segment is `openDirAt` against the previous handle: one component, one
handle, `OBJ_DONT_REPARSE` (F-a). A reparse point at any position fails the walk
with `errReparse`, which shared code renders as "project ancestor %q is a link
or reparse point". `create` mode is `mkdirClaim` then re-open — never
`MkdirAll`, never a joined path. `rejectArtifacts` consults `dirHasHTMLFD`,
which after F2's hoist reads entries through `readDirFD` and classifies each
through `statAt` from the same handle.

R1's falsifier — "any mutation whose final Win32 call receives a path with more
than one component" — is structurally impossible: `ntOpenAt`'s single-component
restriction is enforced at runtime in `statAt`, `unlinkAt` and the delete path,
and the walk never joins.

**Satisfied:** R1 (mechanism), R3 (`M1.intermediate`), R6 (`P12.mkdir_excl`).

### 4.3 `openBrowsableDir` — invariant 5 — R1, R3, R4

Every segment is `OBJ_DONT_REPARSE`. Exactly one boundary is forgiven, and only
when `crossed` is false and the current directory is not itself an artifact
(the Linux `!dirHasHTMLFD(fd)` guard, preserved). On `errReparse` or `errNotDir`
at an unforgiven boundary the walk fails.

At the one forgiven boundary: read the reparse data from the pinned parent
(`FSCTL_GET_REPARSE_POINT`), refuse any tag outside `watchTags` with a distinct
message, refuse a substitute name beginning `\??\Volume{` (§5.3), then open the
target absolutely with `FILE_FLAG_OPEN_REPARSE_POINT` and require a plain
directory — so a link-to-a-link cannot chain. Every segment after the crossing
is `OBJ_DONT_REPARSE` again, so a nested link **inside the watched source** —
A1's territory, symlink or junction or unknown tag alike — is refused by the same
mechanism with no allowlist consulted.

The tag allowlist is already a parameter in the prototype and was measured both
ways: with `{SYMLINK}` a junction boundary is refused; adding `MOUNT_POINT`
crosses the same boundary with no other change
(`P12.browsable_tag_allowlist`, `P12.browsable_tag_allowlist_junction`).

**Weakness, stated:** the crossing re-opens an absolute path taken from the
reparse data. That is a genuine re-resolution. Between reading the data and
opening the target, A2 can substitute the target — with a link (refused by the
tag check on the reopen) or with a *different real directory* (accepted). The
Linux code has the identical window at `storefs_linux.go:184`
(`Readlinkat` then `unix.Open`). This is parity, not a Windows regression, and
it is recorded as RW3 rather than argued away.

**Satisfied:** R1, R3, R4. Invariant 5 holds by the same argument as Linux.

### 4.4 `Publish` — R6, R13

Ancestors via `openRealDir(create=true, rejectArtifacts=true)`. `verifyRoot()`.
`runStoreOpHook("publish-claim")` — already after the parent is pinned. Then
`mkdirClaim(parent, name)`: a single `FILE_CREATE`, atomic, races and existing
names both surfacing as `errExists` (`P12.mkdir_excl`). Files are written with
`FILE_CREATE | FILE_NON_DIRECTORY_FILE | OBJ_DONT_REPARSE`, so a pre-planted link
at `img/logo.png`'s parent cannot capture the write.

Delete-pending is mapped to its own sentinel, never to `errExists` (R6's
falsifier). Because our own deletes use POSIX semantics the name leaves the
namespace immediately (`M10.posix_nt`, REQUIRED) — which is also the fix for
`TestPublishCreateOnly`'s flakiness that `M10.legacy_pending` demonstrates. A
delete-pending window opened by *another* process is retried under R10's bound
and then reported distinguishably.

**Satisfied:** R6, R13. Invariant 2 holds.

### 4.5 `Delete` — the highest-severity operation — R7, R8

`lockRendezvous(exclusive)`, `openRealDir(create=false, rejectArtifacts=false)`,
`verifyRoot()`, `runStoreOpHook("delete")`, then `isLinkAt(parent, name)`:

- **Tag on the allowlist** → `unlinkAt` only. Measured: removing a junction
  relative to a pinned parent leaves the target byte-intact
  (`P14.unlink_junction`, REQUIRED). Invariant 7 holds for junctions as it does
  for symlinks.
- **Any other reparse tag** → `unlinkAt` as well, never a descent. Removing a
  directory entry in the store's own namespace cannot affect a target whose
  semantics we do not honour.
- **Real directory** → `openDirAt` + `dirHasHTMLFD`, then `removeTreeAt`.

`removeTreeAt` walks by handle: `readDirFD` for entries, `statAt` per entry for
classification, recurse only into `IsDir` (attribute set **and** no tag),
`unlinkAt` everything else. **`FILE_ATTRIBUTE_DIRECTORY` is set on a junction**
(`0x410`, `P14.delete_attr_trap`) — a walk that decided by the directory
attribute alone *would* descend, which is exactly RR1. Deciding on the tag is
what closes it. `P14.delete_descend` is a REQUIRED property covering the
descent case.

Two supporting measurements: Go's own `os.RemoveAll` on a junction removes the
link and leaves the target intact (`RR1.removeall`, REQUIRED), and
`filepath.WalkDir` starting at a junction does not descend (`RR1.walkdir`) — so
the danger is specifically a hand-rolled walk, and ours is the hand-rolled one.

**Satisfied:** R7, R8. RR1 closed by mechanism; the "Delete / target replaced"
matrix cell is the release gate.

### 4.6 `Unwatch` — invariant 7 — R7

`openRealDir(false, false)` refuses to reach the parent through any reparse
point at all. `isLinkAt` must report an **allowlisted** tag; a real directory,
a regular file and an unrecognised tag are all refused. Removal is `unlinkAt`
against the pinned parent. There is no code path from `Unwatch` to a tree walk —
R7's falsifier — and none is added.

Removal flavour is chosen from the tag, not guessed: the entry is opened
`FILE_OPEN_REPARSE_POINT` with `FILE_DIRECTORY_FILE` for `MOUNT_POINT` and for a
directory `SYMLINK`, `FILE_NON_DIRECTORY_FILE` for a file `SYMLINK`, then
POSIX-disposed. The threat model's §3.7 hazard — picking `RemoveDirectoryW` vs
`DeleteFileW` wrongly and "helpfully" falling back to a recursive delete — cannot
arise, because there is no fallback to fall back to.

### 4.7 `Watch` — R2, R6

`openRealDir(create=true, rejectArtifacts=true)`, `verifyRoot()`,
`runStoreOpHook("watch-link")`, then `symlinkAt` (§6.6). The "already inside the
scratchpad" guard is re-expressed (§7.1). The idempotence comparison becomes
object identity, not string equality (§7.2).

### 4.8 Annotations — R9, R10

`.annotations` is created and opened relative to the pinned root with
`OBJ_DONT_REPARSE`, so a replacement by symlink or junction is refused on the
tag, not on a mode bit. Every subsequent segment is `openDirAt`.

The atomic write is: `FILE_CREATE | FILE_NON_DIRECTORY_FILE | DELETE` for a
random `.notes-<hex>.tmp` in the pinned parent; write; then
`NtSetInformationFile(FileRenameInformationEx, RootDirectory=parent,
FILE_RENAME_REPLACE_IF_EXISTS | FILE_RENAME_POSIX_SEMANTICS)` through the temp
file's own handle, with `parent` on the destination side — the same handle on
both sides, matching `annotationfs_linux.go:161`'s `Renameat` exactly. On any
failure before the rename the temp is POSIX-disposed through the handle we
already hold, with no second name lookup — strictly better than the Linux
`Unlinkat(parent, tmp, 0)`.

The Win32 wrapper cannot express this: `SetFileInformationByHandle` refuses a
non-NULL `RootDirectory` with `ERROR_INVALID_PARAMETER` for classes 3 and 22
alike, while the identical buffer with `RootDirectory = NULL` and a
fully-qualified name **succeeds** (`M9.win32_control_nullroot`) — the control
that turns this from a bug report into a design constraint. Fall back from class
65 to class 10 on `STATUS_INVALID_PARAMETER` / `STATUS_NOT_SUPPORTED`, copying
the Go standard library's pattern; both work on builds 26100/26200 (`M9`).

R9's "destination still holds complete prior content on any failure" is
measured: with the destination held without `FILE_SHARE_DELETE`, the replace
fails and the destination still holds the old bytes — never truncated, never
absent (`M13.blocked`).

**Recursive annotation removal** uses the same `removeTreeAt` as §4.5, so R8
covers it. Ancestor pruning reopens each parent from the anchored root, so a
concurrent rename can only make pruning stop, never redirect it — unchanged
policy.

**Durability, stated as a non-guarantee:** no `FlushFileBuffers` before the
rename, matching Linux (which does not `fsync`). The guarantee is atomicity of
*replacement*, not crash durability, on both platforms. **[UNMEASURED]** — P1.3
did not produce a durability measurement, and none is claimed.

### 4.9 The requirement table

| | Satisfied? | By what |
|---|---|---|
| **R1** segment-at-a-time, handle-anchored | **yes** | `ntOpenAt` + single-component enforcement; `P12.openrealdir` |
| **R2** no security decision by string comparison | **yes, with two demotions** | §7.1 identity; §7.3 `joinInRoot` and `visibleSegments` demoted to advisory and documented as such |
| **R3** reparse refused on intermediate components | **yes** | `M1.intermediate` (REQUIRED) + `M1.weak_flag_traverses` control |
| **R4** tag allowlist, refuse-by-default | **yes** | §5; `RR1.unknown_tag_isdir` is why "refuse surrogates" is rejected |
| **R5** never `fs.ModeSymlink` for classification | **yes for mutation; over-approximated for listing** | `statAt` for every mutation; `ModeSymlink\|ModeIrregular` for listings (§3.3) — fail-closed, measured |
| **R6** atomic create-only, collision distinguishable | **yes** | `P12.mkdir_excl`; §6.6's three-way status map |
| **R7** Unwatch removes only an allowlisted link | **yes** | §4.6; `P14.unlink_junction` (REQUIRED) |
| **R8** recursive removal classifies from the handle | **yes** | §4.5; `P14.delete_descend` (REQUIRED), `P14.delete_attr_trap` |
| **R9** annotation write is one atomic replacement | **yes** | `M9` (REQUIRED), `M13.blocked` |
| **R10** bounded retry on transient failures | **yes, on an unmeasured basis** | §8.4; `M13.av` is NOT MEASURED and the bound is a documented choice |
| **R11** lookup validation + case-insensitive guards | **yes** | §7.4, §7.5 |
| **R12** root resolved once and pinned | **overridden** → once *per operation*, carried as `rootedFS.path`+handle | §7.3, §10.2 |
| **R13** pre-mutation root identity re-verification | **yes** | `R13.replace` (REQUIRED) |
| **R14** watcher identity from the registered handle | **yes** | §3.5; signature already changed in P2.2 |
| **R15** `FILE_SHARE_DELETE` everywhere, all handles closed | **yes** | §3.2 ownership rules |
| **R16** depth bound + identity-keyed visited sets | **yes** | §6.8 |
| **R17** deterministic hooks after the pin | **yes** | six new hooks, shared untagged code (§11) |
| **R18** unsupported storage refused or warned | **yes, with a partial override** | §8.3, §10.4 — UNC/device *refused*, not warned |
| **R19** watch failure never disables publish | **yes** | §6.6, §8.5 |
| **R20** shared holds policy, platform holds mechanism | **yes, conditional on F2's hoist landing first** | §11 |

---

## 5. Supported filesystems and reparse tags

### 5.1 The allowlist is refuse-by-default, in three scopes

Conflating the three scopes is the bug this section exists to prevent.

- **Scope A — the one forgiven watch boundary in `openBrowsableDir`:**
  `{IO_REPARSE_TAG_SYMLINK, IO_REPARSE_TAG_MOUNT_POINT}`, plus the volume-mount
  refusal in §5.3.
- **Scope B — everywhere else in every mutation, and every component after the
  boundary is crossed:** the allowlist is **empty**. `OBJ_DONT_REPARSE` refuses
  everything, including `SYMLINK` and `MOUNT_POINT`.
- **Scope C — classification for `Delete`/`Unwatch`/listing:** Scope A's set is
  "a link the store may remove"; every other tag is "a reparse point we do not
  understand" — never descended, never followed, unlinked by `Delete`, refused by
  `Unwatch`.

### 5.2 Why the policy cannot be "refuse name surrogates"

This is the spike's most valuable correction and it overrides an obvious
shortcut. A **non-Microsoft, non-surrogate** tag (`0x00001234`) applied to a
directory is reported by Go as `ModeDir` **and** `ModeIrregular`, `os.Stat` the
same, and the parent listing's `DirEntry.IsDir()` is **true**
(`RR1.unknown_tag_isdir`, `M2.unknown_tag`). `fileStat.Mode` sets `ModeDir`
whenever `isReparseTagNameSurrogate()` is false, and bit 29 is clear for a
third-party tag. The threat model's RR1 analysis covered only the *junction*
case, where the surrogate bit **is** set and `ModeDir` is therefore suppressed.

What limits the damage on the runner is not the classification: it is
`STATUS_IO_REPARSE_TAG_NOT_HANDLED` (`0xC0000279`), returned because no filter
driver services the tag. On a machine that *has* the driver — Windows Containers
(`WCI*`), VFS-for-Git (`PROJFS*`), a vendor filter — the open succeeds and the
`IsDir() == true` classification is the entire defence.

And it is cheap to produce: setting a non-Microsoft tag succeeds with
`SeCreateSymbolicLinkPrivilege` **removed** from the token (`M4.noprivilege`),
on an empty directory (`M4.nonempty`).

**Therefore R4 is implemented as "refuse every tag not explicitly allowed", and
the phrasing "refuse name surrogates" is prohibited.** `R4.allowlist_evidence`
is the measurement that says why.

### 5.3 Volume mount points

A volume mount point carries the **identical** tag to a junction
(`0xA0000003`); only the substitute name distinguishes it, and it begins
`\??\Volume{` (`M3.volume_mount_point`, `M3`). Crossing one moves to a different
volume — different `VolumeSerialNumber`, different `FileId` space, possibly a
filesystem whose security model is not specified.

Scope A therefore inspects the reparse **data**, not just the tag: a substitute
name beginning `\??\Volume{` is refused. This is a name-shaped check, but its
only possible outcome is refusal, so it cannot be turned into a check-then-use.

A volume-serial comparison is **not** used: a legitimate watch target may
perfectly well live on `D:`, and `M3` measured that `FILE_ID_INFO.VolumeSerialNumber`
differs when opened *through* a cross-volume junction. Refusing on serial
mismatch would refuse legitimate watches.

### 5.4 Tags that are not links

`DEDUP` and `WOF`/`WIM` files are reported by Go as ordinary regular files and
must stay usable — a blanket "reject all reparse points" would make a
deduplicated or compressed Server volume unusable (threat model §4.2). They fail
`OBJ_DONT_REPARSE` opens only if the filesystem actually surfaces them as
reparse points; on the measured runner they did not appear. **[UNMEASURED]** on
a deduplicated volume. `APPEXECLINK` (observed live in `WindowsApps`,
`tag=0x8000001B`, non-surrogate — `M2.appexeclink`) is refused, which is correct:
it is a zero-length stub that cannot be served.

### 5.5 Filesystems

| Filesystem | Store root | Watch target | Basis |
|---|---|---|---|
| **NTFS local** | supported | supported | every measurement in the spike |
| **ReFS / Dev Drive** | **warn once before the first mutation, then proceed** | supported, no warning | §8.3 — **[UNMEASURED]**, `M1.refs_smb` |
| **FAT32 / exFAT** | warn; POSIX delete and `FileRenameInformationEx` are documented unsupported and will fail loudly | supported for reads | FAT32 volume present but unmounted on the runner (spike §7) |
| **SMB / UNC** | **refused** (R18, and §10.4's override of the spec's "warning") | refused for the *root*; a watch target on a share is out of scope | no share in CI; A4 out of scope |
| **Device namespace** (`\\.\`, `\\?\GLOBALROOT\`) | **refused** | refused | threat model §4.8 |
| **Cloud-backed (OneDrive)** | warn | supported, with RW13's rehydration cost accepted | **[UNMEASURED]**, `M2.cloud` |

The distinction between root and target is deliberate and load-bearing. The
store root needs `FILE_CREATE`, handle-relative rename, POSIX delete and
`FILE_ID_INFO`; a watch **target** needs only no-follow opens and `FILE_ID_INFO`,
because the store never mutates a watch target. Refusing Dev Drive targets would
refuse a large fraction of real watched source trees for no security gain.

---

## 6. The forced decisions, resolved

### 6.1 `os.Root` — rejected

See §8.1. `M17.follows_inroot_symlink` = YES with no opt-out.

### 6.2 `OBJ_DONT_REPARSE` — adopted as the containment primitive

`M1.intermediate` (REQUIRED) plus the `M1.weak_flag_traverses` control.

### 6.3 Handle-relative rename — `NtSetInformationFile` only

`M9` (REQUIRED) and the `M9.win32_control_nullroot` control. See §4.8.

### 6.4 Directory lock — replaced by a handle-pinned lock file

See §6.7.

### 6.5 Two RR1 vectors — junction and non-surrogate unknown tag

See §5.2.

### 6.6 Link creation, the error map, and `Watch`'s idempotence

**The privilege table, measured** (`P14.*` rows; "privilege removed" means a
child process that removed `SeCreateSymbolicLinkPrivilege` with
`SE_PRIVILEGE_REMOVED`, chosen because `CreateSymbolicLinkW` enables the
privilege on demand and merely disabling it would prove nothing):

| Configuration | `CreateSymbolicLinkW(DIRECTORY)` | `+ ALLOW_UNPRIVILEGED_CREATE` | handle-relative FSCTL `SYMLINK` | junction FSCTL |
|---|---|---|---|---|
| privilege held | ok | ok | ok | ok |
| privilege removed, Developer Mode **on** | 1314 | ok | ok | ok |
| privilege removed, Developer Mode **off** | 1314 | 1314 | 1314 | **ok** |

**Junctions are the only link an unprivileged, Developer-Mode-off user can
create.** That is measured, not assumed. (Run 2 briefly suggested the FSCTL route
never needed Developer Mode; run 3 cleared `AllowDevelopmentWithoutDevLicense`
in a child and showed it does — `P14.devmode_off.symlinkat`. The intermediate
result is recorded so it does not get quoted out of a log.)

**Decision: accept junctions, at the same trust tier as symlinks, in the store's
own namespace only.** `watchLinkFlavour()` probes at creation time: handle-
relative `SYMLINK` FSCTL first, `MOUNT_POINT` second, `errNoLinkPrivilege` third.

**Why, and what is being given up.** The spec asks for junctions to be "created
and identified by the application under an explicitly accepted design". The
identification half is **not achievable**: a junction the store creates is
byte-for-byte indistinguishable from one an attacker creates — same tag, same
`\??\<path>` substitute name — and any sidecar registry recording "this one is
ours" would itself be writable by the same user and would be consulted as a
check-then-use. So the clause cannot be honoured as written (§10.1).

What makes accepting them defensible is a reframing the threat model supports
but does not state: **the boundary that carries the security property is A1
(the watched source tree), not A2 (a process running as the user).** A2 planting
a link in the store root is not a *redirection* of any store operation — it is
content creation, indistinguishable from the user running `scratchpad watch`, and
the Linux code already forgives an arbitrary symlink at that first boundary
without checking who made it. Admitting `MOUNT_POINT` alongside `SYMLINK` in
Scope A is therefore parity with existing Linux behaviour, not a new weakening.
Everything an attacker actually gains from junctions — RR1's recursive delete,
RR2's unbounded traversal, §3.11's wrong-button-on-the-card — is closed by
tag-aware classification, which the design needs **regardless** of whether we
ever create one. Rejecting junctions would not remove one line of that work; it
would only remove `watch` from every machine with Developer Mode off.

Invariant 7 survives: `P14.unlink_junction` is a REQUIRED property showing that
removing a junction relative to a pinned parent leaves the target intact.

**The two-step creation window.** Handle-relative link creation is `FILE_CREATE`
then `FSCTL_SET_REPARSE_POINT`; the name claim is atomic but the *link* is not
(`M8.symlinkat_excl`). A crash between them leaves an **empty real directory**
under the watch name, which today is invisible to `Watches` (not a link),
un-`Unwatch`-able (not a link), un-`Delete`-able (not an artifact), and makes a
repeat `watch` fail hard. That is a wedged name, and it is not acceptable.

Three rules, in order:

1. **Error path self-heals.** On `FSCTL_SET_REPARSE_POINT` failure, POSIX-delete
   the just-created directory through the handle already held. The prototype
   already does this (`links.go`'s `SymlinkAt`).
2. **Crash interlock.** Create the directory with `FILE_DELETE_ON_CLOSE` and
   clear the disposition with `FileDispositionInformation{DeleteFile: FALSE}`
   only after the FSCTL succeeds, so an abrupt process death lets the kernel
   remove the half-built entry. **[UNMEASURED]** — whether `FILE_DELETE_ON_CLOSE`
   permits `FSCTL_SET_REPARSE_POINT` on the same handle, and whether a
   `DeleteFile: FALSE` disposition clears a create-time delete-on-close, must be
   measured in P3.1 before this rule is relied on. If it does not hold, rule 3
   alone carries the case.
3. **Recovery is explicit and user-driven, never silent.** `Watch`'s collision
   branch classifies the existing entry and reports one of four outcomes;
   critically, a bare real directory gets its **own** message — *"`<name>` is a
   directory, not a watch link; an interrupted watch may have left it behind —
   delete it in the web UI and retry"* — and `Delete` is widened to remove an
   **empty** non-artifact directory so that instruction is true. `rmdirAt` fails
   with `errNotEmpty` if anything is inside, so the widening can never destroy
   content and can never follow a link. It is a cross-platform behaviour change
   and needs a Linux regression test (RW19).

   Silently reclaiming the directory was considered and rejected: `Publish`
   leaves its artifact directory empty for the window between `mkdirClaim` and
   the first `writeFileAt`, so an auto-reclaiming `Watch` could delete a
   concurrent `Publish` out from under itself.

**The error map for a taken name.** With `OBJ_DONT_REPARSE`, the claim fails
during name *resolution*, before collision is detected (`M8.claim_over.*`):

| existing entry | claim **with** `OBJ_DONT_REPARSE` | claim **without** |
|---|---|---|
| plain directory | `STATUS_OBJECT_NAME_COLLISION` | same |
| plain file | `STATUS_OBJECT_NAME_COLLISION` | same |
| **junction** | **`STATUS_REPARSE_POINT_ENCOUNTERED`** | `STATUS_OBJECT_NAME_COLLISION` |
| **directory symlink** | **`STATUS_REPARSE_POINT_ENCOUNTERED`** | `STATUS_OBJECT_NAME_COLLISION` |

So:

- `STATUS_OBJECT_NAME_COLLISION` → `errExists`.
- `STATUS_REPARSE_POINT_ENCOUNTERED` **from a `FILE_CREATE` claim** →
  `errExistsReparse`, which wraps `errExists`. This is the branch `Watch`'s
  idempotence relaxation (`store.go:592-599`) hangs off — **not** the collision
  status. A mechanical port of `errors.Is(err, unix.EEXIST)` would turn every
  repeat `watch` of the same folder into a hard error, a functional regression
  invisible to a Linux test suite (`M8.claim_error_map`).
- `STATUS_IO_REPARSE_TAG_NOT_HANDLED` from a claim → also `errExistsReparse`;
  `readlinkAt`'s allowlist then refuses it. **[UNMEASURED]** whether this status
  actually arises on `FILE_CREATE` for an unknown tag — `M8` measured junction
  and directory symlink only. Handling both defensively costs nothing.
- `STATUS_DELETE_PENDING` → `errDeletePending`, distinct, retried, never
  "already exists" (R6's falsifier).
- `Publish` maps `errExists` and `errExistsReparse` to the same user message,
  so publishing over a name held by a watch link does not report "too many
  levels of symbolic links".

**Idempotence comparison.** Byte-exact string comparison against `abs` is wrong
on Windows: `abs` is spelled as the user typed it and the substitute name is
normalised. `Watch` therefore compares by identity — see §7.2.

### 6.7 The annotation rendezvous

`LockFileEx` fails with `ERROR_INVALID_PARAMETER` on a directory handle opened
`GENERIC_READ` **and** on one opened `GENERIC_READ|GENERIC_WRITE`; the control
on a regular file succeeds (`M14.dir_readhandle`, `M14.dir_writehandle`,
`M14.file_control`). The store-root `flock` has no direct Windows equivalent.

**Design: a lock file, opened once by handle when `annotationFS` is created, and
never re-resolved.** The property `annotations.go:124-126` bought with the root
inode — "the rendezvous survives a hostile process renaming and replacing
`.annotations`" — is preserved by the *handle*, not by the location: a handle
keeps naming the same object across a rename of that object or any ancestor
(`M7.namefollows`, `R13.rename`). That is precisely `M14.consequence`'s
recommendation.

Concretely:

- Path: `.annotations\.lock`, created with `openLockFileAt` relative to the
  pinned `.annotations` handle, `FILE_SHARE_READ|WRITE|DELETE`.
- **Ordering:** `openAnnotationFS` pins the root, then `.annotations`, then
  `.lock`, in that order, before any lock is taken. `lockRendezvous` locks byte
  range `[0,1)` of that pinned handle — shared for normal work, exclusive for
  `Delete`/`Unwatch`. No re-open, ever, for the life of the `annotationFS`.
- **Lifetime:** `annotationLock.Close()` does `UnlockFileEx` then `CloseHandle`,
  in that order, on every path including error paths. Handle close releases the
  lock unconditionally, so a crashed holder cannot wedge the store.
- **Blocking policy:** `LOCKFILE_FAIL_IMMEDIATELY` plus a bounded retry rather
  than a blocking `LockFileEx`, then a distinct actionable error. Windows
  byte-range locks are **mandatory**, not advisory — a second handle's `ReadFile`
  over the locked range fails with `ERROR_LOCK_VIOLATION` (`M14.mandatory`) — so
  a *hung* holder is a worse failure mode than `flock`'s and must not be waited
  on forever. (A *crashed* holder is fine: the kernel releases on close.)
- The `.lock` file is never read, so mandatory locking affects only other
  lockers, which is the intent. `walkAnnotationDir` reads only `.json`, so it is
  invisible to reports.

**Weakness, stated:** if `.annotations` is replaced between two *processes'*
independent `openAnnotationFS` calls, the two pin different objects and mutual
exclusion is lost. Linux has the same shape of gap — `lockAnnotations` calls
`openAnnotationFS()` per operation, re-resolving the root string each time — so
the claim being made is the narrower one the Linux comment actually supports:
the rendezvous survives replacement *during* an operation. Recorded as RW5.

Rejected alternative: a lock file in the store **root**. It would match Linux's
choice of object more closely but adds a second reserved name to the store's
visible namespace, which a Linux-created store would not have and a Linux user
would see. The pinned-handle property makes the location non-load-bearing, so
the namespace cost is not worth paying.

### 6.8 `fdPath` has no analogue — `loadArtifact` is refactored on both platforms

This is the single hardest porting constraint (threat model §2, survey Finding
4) and the honest fix changes Linux too.

`GetFinalPathNameByHandleW` is **not** a substitute: it returns a string the OS
must re-resolve, reinstating the TOCTOU the handle removed. It is a display and
diagnostics primitive only, and the spec's listing of it among containment
primitives is overridden (§10.3).

The replacement is measured: `os.NewFile(DuplicateHandle(h)).ReadDir` enumerates
a directory through a retained handle, and each duplicate restarts enumeration
independently (`M16`, REQUIRED).

**Changes, on both platforms:**

1. `loadArtifact(project, name, dir string)` becomes
   `loadArtifactAt(project, name string, dir int)`. Entries come from
   `readDirFD(dir)`; per-entry classification, size and mtime come from
   `statAt(dir, name)`; the recursive size/mtime walk becomes handle-anchored
   with an explicit depth bound (R16) and unlinks-not-descends discipline for
   anything carrying a tag. `filepath.WalkDir` and `filepath.EvalSymlinks`
   disappear from it.
2. **`annotate()` moves out of `loadArtifact` to its callers**, taking the
   *logical* store-relative path rather than the directory string. This fixes a
   latent Linux defect confirmed while writing this ADR:
   `os.Lstat("/proc/self/fd/N")` reports `ModeSymlink` (procfs fd entries *are*
   symlinks), so today **every** artifact returned by `ResolvePath` and `Publish`
   has `IsLink == true`. It is currently unobservable — those artifacts only
   reach `pageCards`, which ignores the field — but it is a live trap for the
   port and for any future caller. Verified locally on Linux, not merely reasoned.
3. `annotate`'s `Linked` detection changes from
   `filepath.EvalSymlinks(a.Dir) != a.Dir` to `WatchLinkFor(rel) != ""`. On
   Windows `EvalSymlinks` does **not** resolve junctions and returns
   `ERROR_PATH_NOT_FOUND` when one is an intermediate component (`M5.junction`),
   so the old test would report `Linked == false` for every junction-watched
   tree — and the card would then offer **Delete** where it must offer
   **Unwatch**, which is threat model §3.11 and the front half of RR1's chain.
4. **`List` becomes handle-anchored.** It walks from the pinned root using
   `readDirFD` + `statAt`, crossing at most one boundary per branch exactly as
   `openBrowsableDir` does, with a depth bound and a visited set keyed on
   `objectID` rather than on `filepath.EvalSymlinks` output (R16). This removes
   the last large path-based walk, removes `hasHTML(path)` and `entryIsDir(path,…)`
   from the hot path, and is where RR9's DoS lives. It is real scope growth on
   both platforms and is stated as such.
5. **The four `/proc/self/fd` sites in `internal/web/server.go`** (296, 555,
   605, 648) are resolved by *passing the handle*, never by making the string
   portable. `store.ResolveFolder` already returns a pinned `*os.File`. Three
   small exported helpers carry it: `store.ReadDirHandle(*os.File)`,
   `store.EntryIsDirAt(*os.File, os.DirEntry)`, `store.StatEntryAt(*os.File, string)`.
   Then `docCount` (296) and `hasRenderable` (648) drop their `dir` string
   entirely — they already hold `dirFile`; `siblings` (605) replaces
   `os.ReadDir(dirPath)` with `ReadDirHandle`; `buildCards`/`folderExtras` (555)
   take the `*os.File` instead of the string, which also fixes
   `pageCard(..., filepath.Join(dir, name), ...)`'s `os.Stat` on a
   `/proc`-derived path.

**Yes, the Linux side changes too**, in items 1–4. That is the price of removing
the crutch rather than emulating it, and it makes the Linux read path
strictly more anchored than it is today.

### 6.9 The three surviving path re-resolutions

Named individually so P3.13's trace review can find zero *unlisted* ones:

1. `openBrowsableDir`'s reopen of a watch target from the reparse data (§4.3).
   Exact Linux counterpart. Read path only.
2. `visibleSegments`' `os.Stat` per segment (`store.go:140`) — deliberately
   advisory (its own comment says so); the authoritative walk is handle-relative.
   On Windows `os.Stat` follows reparse points, so a junction reports
   `IsDir()==true` there; the only consequence is which ignore rules are
   evaluated. Low.
3. `pageCard`'s `os.Stat(filepath.Join(a.Dir, f))` for preview weight. Reads a
   size and an mtime; feeds `maxPreviewBytes`, a DoS guard, not a containment
   control.

Everything else in the read path becomes handle-anchored by §6.8.

### 6.10 Reserved device names

Opening `NUL`, `CON` or `COM1` **relative to a directory handle** returns
`STATUS_OBJECT_NAME_NOT_FOUND`; `COM1\x.html` returns
`STATUS_OBJECT_PATH_NOT_FOUND`; the path-based control `os.OpenFile(<dir>\NUL)`
**succeeds** (`M18.relative_open.*`, `M18.path_open_control`). The DOS device
links do not exist in a directory object's namespace, so the handle-anchored
design closes threat model §4.12's *lookup* hole for free.

Record it as **defence in depth, not the primary control.** R11's lookup rules
still land, for three reasons: `Root()` and every display path still build
strings; `CON.txt` is creatable on build 26100 (`M18.create_CON.txt`) while
`RtlIsDosDeviceName_U` on Server 2019/2022 treats it as a device, so a store
built on one is unaddressable on the other (a data-portability bug for release
notes); and belt-and-braces costs one function. Go's `isReservedName` is
normative because it consults `RtlIsDosDeviceName_U` and therefore tracks the
running OS.

---

## 7. String comparisons: what replaces them

### 7.1 `Watch`'s "already inside the scratchpad" guard

`store.go:564-570` is `strings.HasPrefix(real, realRoot+sep)` on
`EvalSymlinks` output. It is wrong on Windows in four ways at once (case, 8.3
aliases, the `\\?\` prefix, junctions `EvalSymlinks` does not resolve), and
`strings.HasPrefix(short, long)` is **false for the same object**
(`M6.prefix_defect`) — the guard failing in one line. 8.3 generation is
**enabled** on both runners (`M6.enabled`), so this is live, not hypothetical.

Replacement, in two parts, honest about which half is sound:

- **Target *is* the root** — sound. Open the target no-follow, compare
  `FILE_ID_INFO` against the pinned root's. `R13.replace` (REQUIRED) shows
  `FILE_ID_INFO` discriminates objects.
- **Target is *inside* the root** — advisory. Compare
  `GetFinalPathNameByHandleW(VOLUME_NAME_DOS)` output for both sides — both
  derived from handles, so both are long-name canonical (`M6.resolution`) —
  case-insensitively. A bypass yields a self-watching store, which the threat
  model itself rates **RR12 Low**: "a confusing recursive listing, bounded by the
  cycle guard, not an escape".

The hard backstop is §6.8's identity-keyed cycle guard in `List`, `Watches` and
the watcher. Note that `M5.case` measured `EvalSymlinks` *does* canonicalise both
case and 8.3 aliases (`RUNNER~1` → `runneradmin`), which retires RR9's
case-alternating-cycle DoS as described — but `EvalSymlinks` errors when a
junction is an intermediate component, so the guard must not depend on it.

### 7.2 `Watch`'s idempotence comparison

`readlinkAt` keeps its Linux signature. The comparison moves behind a platform
pair, `sameWatchTarget(existing, abs string) bool`: Linux is `existing == abs`
(byte-exact, unchanged); Windows opens both no-follow and compares
`FILE_ID_INFO`, falling back to a case-insensitive string comparison when either
cannot be opened. Identity is the *looser* answer in the right direction — two
spellings of one directory are one object — and threat model §5's Watch row asks
for exactly this ("the comparison must be object identity, not string equality").

### 7.3 `joinInRoot`, `Root()`, and R12

`joinInRoot`'s `filepath.Rel` containment test (`store.go:245-252`) is a string
test that short names defeat. Once every open is handle-relative and every
segment is validated, it is no longer load-bearing: it is retained as defence in
depth and **documented as advisory**, satisfying R2's demand that it "be
re-expressed or demoted".

R12 asks for the root to be pinned for the **process** lifetime. That is
overridden (§10.2): the root is resolved and pinned **once per operation**, and
`rootedFS` carries both the handle and the exact string it was opened from, so
every advisory path computation in that operation uses one consistent value
instead of re-reading `Root()` (threat model §4.15's "two different stores in
flight in one process"). Within one operation R12's falsifier — "any second call
to `Root()` whose result reaches a Win32 API" — holds.

### 7.4 Case folding — and this changes Linux code paths too

`M11` measured the volume's real `$UpCase` behaviour:

- NTFS folds `.annotations`/`.Annotations`, `.ssh`/`.SSH`, `key.pem`/`key.PEM`.
  **RR5 is confirmed as a live defect**, and `ignore.go:378`'s reserved-name
  check and `defaultIgnores`' `path.Match` are case-**sensitive**.
- NTFS does **not** fold `ı` (U+0131), `İ` (U+0130), Kelvin U+212A, or `ß`/`ẞ`.
  Go's `strings.EqualFold` says *equal* for the Kelvin sign and `ß`/`ẞ` where
  NTFS says different.
- Go's over-breadth creates **false collisions**, never bypasses. That makes it
  safe for a **deny** rule and unsafe for an **identity** test — R2 restated as a
  measurement.

The rule, therefore:

- **`Visible`'s `.annotations` reserved-name check** (`ignore.go:378`, shared —
  which is why this is a Linux-touching change) goes through a platform pair,
  `nameEquals(a, b string) bool`: `a == b` on Linux, `strings.EqualFold(a, b)` on
  Windows. A deny rule, so over-breadth is safe; the cost is that a Windows user
  cannot have a top-level directory whose name folds to `.annotations`, which is
  correct behaviour anyway.
- **`defaultIgnores` matching** (`ignore.go:169`) goes through a platform pair,
  `matchName(pattern, name string) (bool, error)`: `path.Match` on Linux,
  `path.Match` over lower-cased operands on Windows. `!`-override lines become
  case-insensitive too, which is consistent.
- **Linux behaviour is byte-identical** — both pairs are exact on Linux, so no
  Linux regression is possible. This is the answer to "does this affect the Linux
  code too": the *file* changes, the *behaviour* does not.
- **Never for identity.** Identity is `FILE_ID_INFO`, everywhere, without
  exception.
- The threat model's "the only fully correct test is 'does opening this name
  relative to the root yield the same file id as `.annotations`'" is right and
  **not implementable at `Visible`'s call site**: `Visible` is a pure string
  predicate consulted before anything exists on disk. That is stated here rather
  than quietly dropped.
- `strings.ToLower`-based `.html`/`.md` suffix tests are left alone: the
  extensions are ASCII, where Go's folding and NTFS's agree.

### 7.5 Lookup-segment validation (R11)

`validateSegment` gains a platform-pair extension, no-op on Linux, that on
Windows rejects in **every** lookup segment:

- `:` — NTFS reads it as an alternate data stream selector. Measured reachable:
  a `RootDirectory`-relative `NtCreateFile` accepts `doc.html:hidden`, and with
  a file literally named `C` present, `C:evil` opens its stream
  (`M12.relative_open`, `M12.C_stream`). Stream bytes are invisible to
  `ReadDir`/`Stat` size (`M12.invisible`), so `maxPreviewBytes` can be understated
  without bound. `filepath.IsLocal` takes the same line.
- a trailing dot or space — `art.` and `art` name one directory but two logical
  doc paths, so `DELETE /a/art.` deletes `art` (threat model §4.11).
- reserved DOS device names as `RtlIsDosDeviceName_U` defines them on the
  running OS (§6.10).

Rejecting `:` only on Windows is deliberate: it is a legal Linux filename
character and watched Linux repositories use it. Per the spec, "malformed or
ambiguous native names must be unreachable rather than normalized to another
entry" — unreachable is what this produces.

---

## 8. Rejected approaches

### 8.1 `os.Root`

Rejected, with evidence, because a future reader will ask.

`os.Root` is genuinely handle-anchored on Windows: it opens each component with
`NtCreateFile` + `OBJECT_ATTRIBUTES.RootDirectory` and `OBJ_DONT_REPARSE`
(`root_windows.go:146-154`), it is `FILE_SHARE_DELETE` everywhere
(`root_windows.go:176`), its `Mkdir` is a genuine create-only claim
(`M17.mkdir_excl`), and it rejects reserved device names (`M17.reserved`). It
satisfies R1, R3, R6 and R15 out of the box. The mechanism is right. The
**policy** is not:

| Requirement | `os.Root` | Evidence |
|---|---|---|
| A no-follow **browse** walk | **NOT met, and not layerable** | `M17.follows_inroot_symlink` = **YES**: `os.Root.OpenRoot` on a symlink that stays *inside* the root **succeeds**, by design, with no opt-out. `openBrowsableDir` (`storefs_linux.go:169`) must refuse exactly this. |
| Tag allowlisting (R4/R5) | **NOT met** | it surfaces `fs.FileMode`, not `FILE_ATTRIBUTE_TAG_INFO`; a junction is `ModeIrregular` (`M17.junction_mode`) and an unknown non-surrogate tag is `ModeDir` (§5.2) |
| Watch-link creation | **NOT met** | `M17.symlink_flavour` = NO: `os.Root.Symlink` on an absolute external directory produces a **file** symlink (`FILE_ATTRIBUTE_DIRECTORY` clear), because a watch target always has a volume name so neither `SYMLINKAT_DIRECTORY` nor `SYMLINKAT_RELATIVE` is set — confirming survey Finding 2 |
| `FILE_ID_INFO` identity (R13/R14) | **NOT met** | no accessor |
| Classify-from-the-handle recursive removal (R8) | **NOT met** | `RemoveAll` is a policy, not a primitive |

The one plausible layering — `Root.Lstat` before `Root.OpenRoot` — is
check-then-use by construction and cannot reproduce `O_NOFOLLOW`'s atomicity
(`M17.lstat_layering` = PARTIAL). Against A2 that is precisely the defect class
the whole design exists to remove.

**The hybrid was considered and rejected too:** `os.Root` is a correct and
auditable answer for the *annotation* tree, which contains no links by
construction and never crosses a watch boundary. But the store's own walks
(`openRealDir`, `openBrowsableDir`, `Delete`, `Watch`) need the hand-rolled
primitives regardless, so the saving is small and the cost is two mechanisms
where one would do — two error maps, two handle-lifetime disciplines, two things
for P6.3 to review.

Cost of the hand-rolled option, stated plainly: `winfs.go` (790 lines) and
`links.go` (261 lines) in the prototype, roughly half of each being executable
mechanism and half comment, plus the missing declarations in §3.6.

### 8.2 The spec's "Fallback" strategy — struck for mutations

The spec offers, as an acceptable alternative, "no-follow handle opens, stable
file IDs, canonical final paths, and pre/post-operation identity checks …
acceptable only if the security review demonstrates that rename/reparse races
cannot redirect a destructive or write operation."

**Position: it cannot be made sound for creation, and it is struck for every
mutation.** The threat model's §9.1 argument is correct and the measurements
sharpen it:

- Pre/post identity checking can *detect* that something moved. It cannot
  *prevent* a create from landing somewhere else, because an atomic name claim
  has no verify-after equivalent — by the time the post-check notices, the
  directory exists. `mkdirClaim`'s whole value is that the claim and the
  containment decision are one kernel operation (`P12.mkdir_excl`).
- "Canonical final paths" is `GetFinalPathNameByHandleW`, which returns a string
  the OS re-resolves; and 8.3 aliasing means the canonical string from one API
  disagrees with the canonical string from another for the *same object*
  (`M6.resolution`, `M6.prefix_defect`). There is no canonical path to compare.
- The race is live, not theoretical: a directory with an open handle **can** be
  renamed when openers granted `FILE_SHARE_DELETE`, which R15 requires them to
  (`M7`).

Where a post-check *is* sound, it is kept: on read paths, "the object moved"
can safely become "not found", which is what `verifyRoot()` does.

### 8.3 Refusing ReFS outright

Rejected. Dev Drive on Windows 11 is ReFS and is exactly where developers keep
source trees; refusing it would refuse a large fraction of watched sources for no
measured gain, and ReFS supports symbolic links and is named alongside NTFS in
Microsoft's documentation for both `FILE_DISPOSITION_INFORMATION_EX` and
`FILE_RENAME_INFORMATION_EX`. But the spike could **not** measure it —
`M1.refs_smb` is explicit that ReFS was not tested, and CI cannot close this gap.

Decision: **store root on ReFS warns once before the first mutation and
proceeds; watch targets on ReFS are unrestricted and silent.** One manual check
on a real Dev Drive before the beta (RW9, owner P6.6). This is a deliberate,
named acceptance of an unmeasured dependency, not an oversight.

### 8.4 An unbounded or documentation-free retry

Rejected in both directions. The retryable set is chosen from documentation —
`ERROR_SHARING_VIOLATION`, `ERROR_LOCK_VIOLATION`, `ERROR_ACCESS_DENIED` when it
carries `STATUS_DELETE_PENDING`, `ERROR_DIR_NOT_EMPTY` — with an explicit bound:
**10 attempts, exponential backoff 1/2/4/…/64 ms, ~250 ms total**, then a
distinct actionable error naming antivirus and the indexer. The bound is a
**policy choice on an unmeasured basis** and must be labelled as such wherever it
appears: `M13.av` is NOT MEASURED and Defender's state on a CI runner is not
representative. The deterministic half was measured — with the destination held
without `FILE_SHARE_DELETE` the replace fails with `ERROR_SHARING_VIOLATION` and
the destination content is preserved (`M13.blocked`), and it succeeded on the
**first** attempt once released (`M13.retry`).

`Delete` has no partial-failure story today — `removeTreeAt` aborts mid-tree and
`Delete` returns after possibly removing half an artifact (`M13.delete_blocked`).
That is pre-existing on Linux and stays; it is recorded as RW20 rather than
silently inherited.

### 8.5 Requiring elevation, or copying watched content

Both rejected outright by the spec, and the measurements make the rejection
cheap: junctions work unprivileged with Developer Mode off, so the degraded mode
is rare. When it does occur — a policy that blocks `FSCTL_SET_REPARSE_POINT`, or
a target volume that forbids reparse points — `watch` fails with
`errNoLinkPrivilege`, whose message names Developer Mode (Settings > System > For
developers) and states that publish, list, serve, delete and notes are
unaffected. It never recommends elevation as the default (R19).

Acceptance criterion 6 needs one test-harness change, already flagged as
EXECUTION.md F5 deferred to this ADR: `testutil.SymlinkCapable` probes
`os.Symlink`, which is the wrong question now. It becomes `WatchLinkCapable`,
probing the store's own link primitive (symlink, then junction). Because
junctions cannot be disabled by the OS, the degraded-mode CI job forces both off
with a test-only switch, `SCRATCHPAD_TEST_WATCH_LINKS=0`.

---

## 9. Remaining risk

Ranked by impact × reachability for the beta's posture (loopback default,
unauthenticated, single user). Every item is **accepted**, **mitigated**, or
**deferred with an owner**.

| # | Risk | Severity | Disposition |
|---|---|---|---|
| **RW1** | Recursive delete through a reparse point (RR1) | Critical | **Mitigated** — §4.5, tag classification + unlink-never-descend. Measured: `P14.delete_descend`, `P14.unlink_junction`, `P14.delete_attr_trap`, `RR1.removeall`. **Release gate:** the "Delete / target replaced" matrix cell must pass before any Windows binary ships. |
| **RW2** | Unbounded reparse traversal on browse (RR2) | Critical | **Mitigated** — `OBJ_DONT_REPARSE` (`M1.intermediate`) + single-boundary allowlist. |
| **RW3** | The three surviving path re-resolutions (RR3) | High | **Accepted, enumerated** — §6.9. All read-only or advisory, each with an exact Linux counterpart. P3.13 must find zero *unlisted* ones. |
| **RW4** | Case-folding bypass of the `.annotations` guard and credential ignores (RR5) | High | **Mitigated** — §7.4. Confirmed live by `M11`. |
| **RW5** | No directory lock; `Delete` racing `SaveNotes` (RR6) | Medium-High | **Mitigated** — §6.7 pinned lock file. Residual: cross-process rendezvous lost if `.annotations` is replaced between two processes' opens. Needs the racing `Delete`-vs-`SaveNotes` test (P3.12). |
| **RW6** | Junctions accepted at the watch boundary and indistinguishable from an attacker's | Medium | **Accepted** — §6.6. Parity with the existing Linux treatment of store-root symlinks. If P1.7/P1.8 rejects this, `watch` becomes Developer-Mode-only; that is a product decision for the human gate, not a security one. |
| **RW7** | The two-step link-creation window | Medium | **Deferred, owner P3.1** — the `FILE_DELETE_ON_CLOSE` interlock is **[UNMEASURED]**. Rule 3 (explicit recovery) must land regardless. |
| **RW8** | Hard links inside a watched source are served (RR4) | Medium | **Accepted** — no structural defence exists; identical on Linux today. Windows makes it cheaper (no privilege) but not newly possible. README and release notes: *watch only trees you trust*. |
| **RW9** | ReFS / Dev Drive unmeasured | Medium | **Deferred, owner P6.6** — `M1.refs_smb`. One manual check on a real Dev Drive before the beta. Store root warns; watch targets unrestricted. |
| **RW10** | Antivirus transient-error distribution unmeasured | Medium | **Deferred, owner P6.1** — `M13.av`. The retry bound in §8.4 is a documented choice, not a measurement, and is labelled as such in code. |
| **RW11** | ADS aliasing via `:` in lookup segments (RR8) | Medium | **Mitigated** — §7.5. Measured reachable (`M12.C_stream`). |
| **RW12** | `ReadDirectoryChangesW` overflow unmeasured | Medium | **Deferred, owner P4.2** — `M15.overflow`. Map the Windows overflow error explicitly; route to reconcile-and-continue, never fatal. |
| **RW13** | Cloud placeholder tags: mass rehydration, `ERROR_CLOUD_FILE_*` (RR10) | Low-Medium | **Deferred, owner P4.6** — `M2.cloud` NOT MEASURED. Follow-up: skip `FILE_ATTRIBUTE_RECALL_ON_*` in the size walk. Documented exclusion + manual pre-beta check. |
| **RW14** | Self-watch via a spelling the advisory prefix check misses (RR12) | Low | **Accepted** — §7.1. Bounded by the identity-keyed cycle guard. |
| **RW15** | An unknown-tag entry in the store root is invisible in listings and un-removable through the UI | Low | **Deferred, owner P4.6** — an inert "unsupported entry" tile with a Delete action. New risk, introduced by the refuse-by-default allowlist. |
| **RW16** | A genuinely non-elevated session was not measured | Low | **Deferred, owner P5.5** — GitHub runners are elevated; the privilege-removal child is a faithful simulation of the *privilege* dimension but not of every ACL difference. One manual confirmation of the §6.6 table on an ordinary account. |
| **RW17** | `List` can be redirected by A2 into listing a tree outside the store | Low | **Accepted, argued** — before §6.8 item 4 lands. Bogus cards' content requests go through handle-anchored `ResolvePath` and 404, so there is no disclosure; only sizes and mtimes leak. Closed entirely once `List` is handle-anchored. |
| **RW18** | The `loadArtifact`/`annotate` refactor changes **Linux** behaviour | Medium | **Mitigated by test** — §6.8 item 2. Must land with a Linux regression test asserting `IsLink` is false for a published artifact. |
| **RW19** | `Delete` widened to remove an empty non-artifact directory | Low | **Accepted** — §6.6 rule 3. Cross-platform behaviour change; `rmdirAt` cannot destroy content or follow a link. Needs a Linux regression test. |
| **RW20** | `Delete` has no partial-failure story; `removeTreeAt` aborts mid-tree | Low | **Accepted** — pre-existing on Linux (`annotationfs_linux.go:183-186`); `M13.delete_blocked` is the error that will trigger it on Windows more often. |
| **RW21** | A4 — shared or redirected store path | N/A | **Out of scope by declaration.** Documented: the store root must be a directory only the user can write. |

### What the spike could not measure — the complete list

`M1.refs_smb` (ReFS, SMB/UNC); FAT32 (volume present but unmounted);
`M2.cloud` (cloud placeholder tags — no OneDrive on a runner);
`M13.av` (antivirus / Windows Search transient distribution);
`M15.overflow` (`ReadDirectoryChangesW` buffer overflow — not deterministically
reproducible); a genuinely non-elevated session (GitHub runners are elevated,
Developer Mode enabled — every privilege-sensitive answer here comes from a
child that removed `SeCreateSymbolicLinkPrivilege`, and where it matters also
cleared `AllowDevelopmentWithoutDevLicense`); 32-bit Windows (not a target).

Introduced by this ADR and not yet measured: the `FILE_DELETE_ON_CLOSE`
interlock (§6.6 rule 2); whether `STATUS_IO_REPARSE_TAG_NOT_HANDLED` arises on a
`FILE_CREATE` claim over an unknown tag (§6.6); annotation write durability
(§4.8); `DEDUP`/`WOF` behaviour under `OBJ_DONT_REPARSE` on a deduplicated
volume (§5.4).

---

## 10. What this ADR overrides

Stated plainly, with the document being overridden named. The spike has
measurement authority; the spec and the threat model were written before it.

### 10.1 Spec, *Reparse points and watch semantics*

> "Treat junctions and other reparse points as untrusted unless created and
> identified by the application under an explicitly accepted design."

**Overridden in its "identified" half.** A store-created junction is
byte-for-byte indistinguishable from an attacker's; identification is not
achievable by inspection, and a sidecar registry would be a check-then-use.
Replaced by: *junctions are accepted at the single watch boundary in the store's
own namespace, on parity with symlinks, and refused everywhere else* (§6.6).
The "other reparse points" half is kept and strengthened into §5's
refuse-by-default allowlist.

### 10.2 Threat model R12

> "The store root is resolved to a fully-qualified local path **once**, at
> process start, and pinned as a handle for the process lifetime."

**Overridden** to *once per operation*, with the handle and the resolved string
carried together in `rootedFS` (§7.3). Reasons: a process-lifetime pin makes
`t.Setenv(store.RootEnv, …)` impossible and would break the entire test suite;
and it adds no property, because a per-operation pin is already unredirectable
within the operation and R13's identity check covers replacement across
operations.

### 10.3 Spec, *Windows rooted filesystem*

`GetFinalPathNameByHandleW` is listed among the candidate containment
primitives. **Overridden:** it returns a string the OS must re-resolve, so any
use of it as an input to a subsequent operation reinstates the TOCTOU the handle
removed, and `M6.resolution` shows it disagrees with `FILE_NAME_INFO` for the
same object when 8.3 aliases exist. It is a **display and diagnostics primitive
only**. The one exception is §7.1's *advisory* containment hint, where its output
is used solely to produce a refusal.

### 10.4 Spec, *Compatibility Policy*

> "Other filesystems and SMB shares return an explicit unsupported warning for
> security-sensitive mutations until tested."

**Partially overridden:** UNC/SMB and device-namespace store roots are **refused
outright**, not warned. A4 (another local user, or a shared/redirected path) is
explicitly out of scope, and a warning-and-proceed on a network root would ship a
configuration whose ACL premise the design does not claim to defend. Non-NTFS
*local* volumes keep the spec's warn-and-proceed treatment (§5.5).

### 10.5 Threat model §4.12 and R11's reserved-device clause

**Corrected, not overridden.** §4.12(b) says "a URL segment `NUL` reaches
`openFileAt`; opening `NUL` succeeds and yields a device". That is true of a
string-path port and **false** of a handle-anchored one (`M18.relative_open.*`).
R11's reserved-device clause is retained as defence in depth rather than as the
primary control (§6.10).

### 10.6 Threat model §3.3(a) and RR9

**Corrected.** `filepath.EvalSymlinks` on Windows *does* canonicalise case and
8.3 aliases (`M5.case`), so `List`'s `visited` guard is sound against a
case-alternating cycle and RR9's specific DoS mechanism does not exist as
described. RR9's other half — an unexpected error killing the fatal-by-design web
process — stands, and R16's triage is still required.

### 10.7 Threat model §4.1's substitution window

**Narrowed.** `FSCTL_SET_REPARSE_POINT` on a **populated** directory fails with
`ERROR_DIR_NOT_EMPTY` (`M4.nonempty`), so an attacker cannot convert an existing
populated tree into a reparse point in place: the tag must be applied first and
content must arrive through the link.

---

## 11. Consequences for Phase 3

Ordered. The first three are **preconditions** — each is a trap that springs the
moment a stub becomes real, and each must land *before* the task it precedes.

**Pre-1 (P2.7 F4, binding on P3.1).** Replace `IsLinkInfo`/`IsLinkEntry` with
`Mode()&(ModeSymlink|ModeIrregular)` **and** convert `entryIsDir`
(`store.go:270-278`) and `entryIsDirFS` (`server.go:325-333`) to a no-follow
classification via `statLinkTarget`, **in the same change**. A partial fix is
worse than none: making `IsLinkEntry` report junctions while leaving
`entryIsDir`'s follow-through `os.Stat` in place converts today's fail-closed
skip into a path-based descent through the junction. Land the Windows-only
failing test (`TestJunctionIsClassifiedAsLink`) first so CI enforces the ordering
rather than a comment.

**Pre-2 (P2.7 F2, binding on P3.2/P3.6).** Hoist four domain decisions out of
`storefs_linux.go` before writing any Windows mechanism: (a) `dirHasHTMLFD`'s
`.html`-suffix predicate, leaving the platform file with `readDirFD` + `statAt`
only; (b) `openRealDir`'s two user-facing error strings, replaced by a typed
`pathError{Kind, Seg}` the shared caller renders; (c) `openBrowsableDir`'s
invariant-5 policy, expressed over a platform allowlist parameter; (d)
`OpenDocument`'s `visibleSegments` + `validateSegment` prologue, into a shared
wrapper over a platform `openPathFile`. Otherwise the Windows author re-authors
policy — R20's reverse falsifier.

**Pre-3 (P2.7 F1, binding on P3.1).** The four `unreachableOnWindows` panics stay
until their real implementations land. There is no safe constant for
`dirHasHTMLFD`: `false` is fail-open in `openRealDir`/`openBrowsableDir` and
fail-closed in `Delete`; `true` inverts both.

Then, keyed to the plan:

| Task | Work this ADR adds or fixes |
|---|---|
| **P3.1** handle wrapper + error mapping | `win32_windows.go` (§3.6), `winError` + the §3.7 table, `objectID`, `rootedFS` with `path`/`id`/`volume`, `verifyRoot`, `dupFD`/`closeFD`/`readDirFD`/`statAt`. Measure the §6.6 rule-2 interlock here. Pre-1 and Pre-3 first. |
| **P3.2** real-directory traversal | `openRealDir` over `openDirAt`+`mkdirClaim`; `pathError`. Pre-2 first. |
| **P3.3** create-only file/dir ops | `mkdirClaim`, `writeFileAt`, `mkdirsAt`; the three-way taken-name map (§6.6). |
| **P3.4** browsable traversal | `openBrowsableDir` with the Scope-A allowlist and the `\??\Volume{` refusal (§5.3). |
| **P3.5** document open and deletion | `openFileAt` (one operation, no fstat window), `openPathFile`, `unlinkAt`, `rmdirAt`; R11's lookup validation (§7.5). |
| **P3.6** pruning and directory reads | `pruneAt`; **and §6.8 items 1–4** — `loadArtifactAt`, `annotate` moved out and re-based on `WatchLinkFor`, handle-anchored `List` with a depth bound and identity-keyed visited set. Both platforms. This is the largest single item and the one most likely to be under-scoped. |
| **P3.7** annotation root | `openAnnotationFS` pinning root → `.annotations` → `.lock`, in that order (§6.7). |
| **P3.8** read and atomic write | `NtSetInformationFile(FileRenameInformationEx→FileRenameInformation)`, §8.4's bounded retry, temp cleanup through the held handle. |
| **P3.9** safe recursive removal | `removeTreeAt` per §4.5; ancestor pruning re-anchored per level. |
| **P3.10** annotation walk/report | `walk` over `readDirFD`+`statAt`, matching Linux ordering and malformed-file handling. |
| **P3.11** port behaviour tests | Close P2.7 F3 first: fail the job if any `--- PASS` coexists with a `not implemented yet` sentinel, or the native job goes green testing nothing. Re-express `SymlinkCapable` as `WatchLinkCapable` (§8.5). |
| **P3.12** deterministic attack tests | The six new hooks in shared untagged code (R17): `root-open`, `browse-segment`, `doc-open`, `notes-replace`, `notes-remove`, `watch-reconcile`. Plus the racing `Delete`-vs-`SaveNotes` test RW5 requires, and the junction/volume-mount/unknown-tag `Watch` variants (`Watches ⊆ Unwatch`-able). |
| **P3.13** security invariants review | Trace every mutation; the only acceptable path re-resolutions are §6.9's three. |
| **P3.14** implementation red team | Native call flags, handle lifetimes, `unsafe` confined to `win32_windows.go`, `FILE_SHARE_DELETE` on every open, the recursive delete. |
| **P4.1/P4.2** watcher | `dirIdentity` field layout moves to the platform files (§3.5); split `desiredDirs`' `dirs` map into fsnotify bookkeeping (path-keyed, forced by the backend API) plus an identity-keyed cycle guard; depth bound; map the `M15.overflow` error to reconcile-and-continue. |
| **P4.3/P4.4** watch links, degraded mode | §6.6's flavour probe and recovery rule; `errNoLinkPrivilege`'s message. |
| **P4.6** web layer | §6.8 item 5 — the four `/proc/self/fd` sites, by passing the handle. RW13's `RECALL_ON_*` skip. RW15's unsupported-entry tile. |
| **P6.6** release matrix | RW9's manual Dev Drive check; RW16's manual non-elevated check. |

---

## 12. The P1.8 question: does a race-resistant strategy exist?

**Yes, and the evidence is stronger than a design argument.**

Every mechanism `storefs_linux.go` depends on has a *measured* Windows twin, not
a documented one: seven syscall equivalents (§2), seventeen REQUIRED security
properties holding on both amd64 and arm64, and zero `SECURITY-FAIL` verdicts
across 192 measurements. The two questions that could have killed the project
both resolved in favour of a workable design — `OBJ_DONT_REPARSE` covers
intermediate components (`M1.intermediate`), and handle-relative atomic rename
works through `NtSetInformationFile` (`M9`). The two that resolved *against* the
obvious approach — no directory lock (`M14`), `os.Root` follows in-root symlinks
(`M17.follows_inroot_symlink`) — each have a concrete replacement that preserves
the property rather than trading it away.

Three honest qualifications the gate should weigh:

1. **The design is not "port the Linux backend".** It requires refactoring
   `loadArtifact`, `annotate` and `List` on **both** platforms (§6.8) to remove
   `fdPath`. Approving this ADR approves that scope. Emulating `fdPath` with
   `GetFinalPathNameByHandleW` is the tempting alternative and it is exactly the
   TOCTOU the design exists to remove.
2. **Junction acceptance is a real widening**, defended on parity grounds rather
   than on the spec's unachievable "identified by the application" test (§6.6,
   §10.1). It is the item most deserving of P1.7's attention. Rejecting it costs
   no security work — the tag-aware classification is required either way — but
   it removes `watch` from every machine with Developer Mode off, which is a
   product decision for the human gate.
3. **Three unmeasured dependencies are carried into the beta, not into Phase 3**:
   ReFS/Dev Drive (RW9), the AV transient distribution (RW10), and a genuinely
   non-elevated session (RW16). None blocks implementation; all three block
   claiming the beta is validated.

Recommendation: **proceed**, with RW1's "Delete / target replaced" test as a hard
release gate, and with the three preconditions in §11 landing before P3.1.
