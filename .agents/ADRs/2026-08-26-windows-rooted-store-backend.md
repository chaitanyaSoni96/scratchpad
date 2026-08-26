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
  - ../plans/in-progress/native-windows-support/reviews/P1.7-red-team.md
  - ../spec/native-windows-support.md
---

# Windows Rooted Store Backend

## Revision note — revision 2

**Revision 1** was committed in `47248c3` and was stale on arrival. That commit's
own message reads *"correct R3 after the runner invalidated it"*: it introduced
the **strict** traversal primitives into `internal/winspike/` to fix a
`SECURITY-FAIL`, and the ADR it added in the same commit still specified the
pre-fix mechanism in every place that mattered. It also cited **zero** evidence
from P1.3 (atomic replacement) and P1.5 (adversarial tests) — no `A1`–`A12`, no
`P13.*` — although those runs contain every direct attack simulation, produced
two `SECURITY-FAIL` verdicts, and contain the only **NO** verdict in the corpus
that lands on a containment question.

**Revision 2** closes
[reviews/P1.7-red-team.md](../plans/in-progress/native-windows-support/reviews/P1.7-red-team.md)
(verdict: *ACCEPT WITH CONDITIONS* — accept the decisions, require six document
fixes). What changed:

| Finding | Severity | Closed where |
|---|---|---|
| **F1** `OBJ_DONT_REPARSE` is inert for non-Microsoft tags; the ADR named it as the containment primitive | Critical | §2, §3.2, §4.0 F-a, §4.2, §4.9 R3, §5.1, §5.2, §6.2, §11 — the **strict open** replaces it everywhere; `OBJ_DONT_REPARSE` is demoted to *necessary and not sufficient* |
| **F2** `A11.ancestor_swapped`: §4.3's "a link-to-a-link cannot chain" and RW3's bound are true only of the **final** component | High | §4.3 rewritten with the handle-by-handle target walk, the `readlinkAt` absoluteness rule and an honest reachability statement; §6.9, RW3, new RW22 |
| **F3** §4.5 prescribed classify-then-open on the RW1 critical path; the prototype refuses to, and `A6.negative_control` shows the ADR's shape destroys the external tree | High | §4.5 replaced with operation-as-classification |
| **F4** the evidence rule, §8.4's retry bound and §4.8's durability note were all pre-P1.3 | High | Evidence rule, §4.8, §8.4, §9, §12 re-based on run **32908643117** |
| **F5** RW5's "Linux has the same shape of gap" is false, and its cited evidence measures rename survival where the hazard is delete-and-recreate | High | §6.7 redesigned (rendezvous moved one hop from the root), RW5 corrected, residual owned |
| **F6** R16's reconcile-triage clause is unowned, and an unserviced reparse tag makes it a **boot loop**, not a restart | Med-High | §4.9 R16 row, §6.11 (new), §11 P4.2, RW13, new RW23 |
| **F7** §6.9's list of surviving re-resolutions was incomplete by the ADR's own text | Medium | §6.9 rewritten as an exhaustive list; Pre-1 gains `entryIsDir`'s first line |
| **F8** rule 3's "delete it in the web UI" is not true, and `CreateJunctionAt` does not self-heal | Medium | §6.6 rules 1 and 3, RW7, RW19, §11 P4.3/P4.6 |
| **F9** §10.2's R12 override guts R13, and `A4.root_removed` was left undecided | Medium | §4.1, §4.9 R13 row, §10.2 — both decided |
| **F10** §10.1 overrode a spec clause it did not need to | Low | §10.1 override withdrawn and retargeted at invariant 5's "store-owned"; §12 item 2 |
| **F11** five small prototype items | Low | §11 P3.1/P3.8 checklist |
| **F12** RW1's release gate is unenforceable without branch protection | Low | RW1 row |
| **F13** no migration plan for the spike's assertions before `internal/winspike` is deleted | Low-Med | §11.1 (new) — the property→test inventory |

Two red-team remedies are **rejected with reasons**, in §6.7 (lock-object
identity revalidation does not restore mutual exclusion) and §4.3 (leaving the
ancestor case merely "recorded and bounded"). Both rejections are argued in
place rather than left silent.

## Status

**Proposed (revision 2).** P1.6 deliverable, revised against P1.7. P1.8 accepts
it or stops the project; per P1.7's recommendation, a re-run of P1.7 against
§2 / §3.2 / §4.3 / §4.5 / §6.7 / §9 is expected before acceptance. Phase 3 is
implemented from this document; where it disagrees with
`threat-model-windows.md` or `.agents/spec/native-windows-support.md`, this
document wins and says so explicitly (§10).

## Evidence rule

Every design claim below carries either a measurement ID from
[spike-findings.md](../plans/in-progress/native-windows-support/spike-findings.md)
or the marker **[UNMEASURED]**. An unmeasured assumption presented as a fact is
the worst thing this document could contain, so every one of them is flagged
inline and collected in §9.

**The authoritative run is run 9,
[32908643117](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32908643117)
(commit `145583a`)**, jobs 97998072330 (`windows-2025`, amd64) and 97998072492
(`windows-11-arm`, arm64). On `windows-2025` it emitted **369 measurement
lines** — 251 YES, 70 INFO, 24 NO, 13 NOT-MEASURED, 11 PARTIAL — with **91
distinct `RequireProperty` ids holding** and **zero `SECURITY-FAIL` verdicts**.
Every design-deciding answer was identical on both architectures. Revision 1
quoted run 5's state ("192 measurements, five CI runs … 17 REQUIRED security
properties"); that tally is superseded.

**Nine runs, and two of them failed.** "Zero `SECURITY-FAIL`" is true of run 9
and would be materially incomplete as a summary of the corpus, so the failures
are recorded here rather than in a footnote:

- **Run 7** ([32906333884](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32906333884), `6f8b5c3`) — `SECURITY-FAIL` on `A5.unknown_tag_refused`. R3 as
  originally stated was falsified. **This changed the design** (§5.1, §6.2, F1).
- **Run 8** ([32908423510](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32908423510), `47248c3`) — `SECURITY-FAIL` on `P13.change_records`. The
  instrument's own hypothesis was falsified, not the design; the assertion was
  withdrawn in run 9 and replaced by two stronger guards (§4.8).
- Run 6 ([32905568933](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32905568933), `ccd8905`) was cancelled.

Two rules govern citations in this document, both introduced by P1.7 after it
spot-checked eleven that failed:

1. **A `PARTIAL` is never quoted as a `YES`.** The eleven `PARTIAL` verdicts in
   run 9 are "the Windows mechanism is demonstrated, the remaining half is
   `internal/store` policy owned by P3"; where this document relies on one it
   says so and names the P3 owner.
2. **A measurement's scope is quoted, not its headline.** `M1.intermediate` is a
   YES *about a junction*; generalising it to every reparse tag is exactly the
   error F1 corrects.

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
`OBJECT_ATTRIBUTES.RootDirectory` set to a retained parent handle, and make
every traversal open a **strict open**: `FILE_OPEN_REPARSE_POINT` plus
`OBJ_CASE_INSENSITIVE | OBJ_DONT_REPARSE`, followed by a
`FILE_ATTRIBUTE_TAG_INFO` read **from that same handle**, refusing anything that
carries a reparse tag.** Do not build on `os.Root` (§8.1). Keep the existing
concrete `rootedFS` / `annotationFS` build-tag split; introduce no interface and
no VFS (spec, *Shared policy, platform mechanisms*; verified absent by P2.7 §2).

The seven load-bearing mechanisms, each a measured twin of a Linux syscall:

| Linux | Windows | Evidence |
|---|---|---|
| `openat(O_RDONLY\|O_DIRECTORY\|O_NOFOLLOW)` | **strict open**: `NtCreateFile(FILE_OPEN, FILE_DIRECTORY_FILE\|FILE_OPEN_REPARSE_POINT, OBJ_DONT_REPARSE)` + tag refusal from the returned handle | `A5.strict_open`, `A5.strict_walk`, `A5.strict_open_admits_real_dirs` (REQUIRED); `P12.openrealdir` |
| `openat(O_RDONLY\|O_NOFOLLOW)` + `fstat S_IFREG` | **strict open** with `FILE_NON_DIRECTORY_FILE` — one open, one tag read, no `fstat` window | `A5.strict_open`; `P12.openfile_isdir` |
| `mkdirat` → `EEXIST` | `NtCreateFile(FILE_CREATE, FILE_DIRECTORY_FILE)` → `STATUS_OBJECT_NAME_COLLISION` | `P12.mkdir_excl` (REQUIRED), `A8.concurrent_claim` |
| `unlinkat` | `NtCreateFile(FILE_OPEN_REPARSE_POINT)` + `NtSetInformationFile(FileDispositionInformationEx, DELETE\|POSIX)` | `P12.deleteat`, `M10.posix_nt` (REQUIRED), `A5.unknown_tag_removed` (REQUIRED) |
| `fstatat(AT_SYMLINK_NOFOLLOW)` | `FILE_ATTRIBUTE_TAG_INFO` read from that handle | `P14.classify.*` |
| `renameat(parent, …, parent, …)` | `NtSetInformationFile(FileRenameInformationEx, RootDirectory=parent)` | `M9`, `P13.replace_existing` (both REQUIRED) |
| `fdPath` = `/proc/self/fd/N` | `DuplicateHandle` + `os.NewFile(dup).ReadDir` | `M16` (REQUIRED) |

### 2.1 Why the strict open, and not `OBJ_DONT_REPARSE` alone

This is the single most important correction in revision 2, and it reverses a
sentence revision 1 opened this section with.

`OBJ_DONT_REPARSE` fails an open with `STATUS_REPARSE_POINT_ENCOUNTERED`
(`0xC000050B`) if any component of `ObjectName` is a **junction** — including an
intermediate one, which `FILE_OPEN_REPARSE_POINT` alone does not cover
(`M1.intermediate` REQUIRED; negative control `M1.weak_flag_traverses`). Read at
face value that looks like the whole containment story. It is not, and run 7
proved it with a `SECURITY-FAIL`.

`A5.obj_dont_reparse_inert_for_unknown_tags` (**YES**, run 9), quoted:

> for a NON-MICROSOFT tag the WITH-flag and WITHOUT-flag opens return the SAME
> status (`STATUS_IO_REPARSE_TAG_NOT_HANDLED`, `0xC0000279`), whereas for the
> junction they differ (refused vs traverses). `OBJ_DONT_REPARSE` therefore does
> NOTHING for an unknown tag: the open is refused because no filter driver
> claimed the tag, not because we asked not to reparse. On a machine that HAS
> the driver — Windows Containers (`WCI*`), VFS-for-Git (`PROJFS*`), a vendor
> filter — the same open would be SERVICED and the walk would traverse. **R3
> stated as 'OBJ_DONT_REPARSE on every component' is NECESSARY AND NOT
> SUFFICIENT.**

Read that carefully: on the CI runner the refusal that looked like containment
was *"no filter driver claimed this tag"*. It is an accident of which drivers
are installed. Docker Desktop installs `WCIFS`; VFS for Git installs `ProjFS`.
Both are ordinary developer software. On such a machine the same walk traverses.

And the tag is cheap to plant: setting a non-Microsoft tag succeeds with
`SeCreateSymbolicLinkPrivilege` **removed** from the token (`M4.noprivilege`) on
an empty directory (`M4.nonempty`).

The strict open removes the dependency entirely. `FILE_OPEN_REPARSE_POINT`
bypasses reparse processing for **every** tag, serviced or not, so the open
returns a handle to the **entry itself**; the tag is then read from that same
handle and anything tagged is refused with a typed error carrying the tag. One
kernel operation, one check, one object: no check-then-use window, and no
dependence on the machine's driver set. `OBJ_DONT_REPARSE` is **retained** in
the same call — it is free, and it still short-circuits Microsoft-tagged
intermediate components before the handle is even created.

Six REQUIRED properties rest on this, all holding in run 9: `A5.strict_open`,
`A5.unknown_tag_refused`, `A5.strict_open_admits_real_dirs` (the primitive still
opens an ordinary directory — "or it is useless"), `A5.strict_walk` (the
project-ancestor walk), and `A3.nested_strict.{symlink,junction}_boundary/*`
(every boundary flavour × every nested flavour, at one and two levels below a
crossed boundary).

The reference implementation is `openStrictAt` / `OpenRealDirAt` /
`OpenRealFileAt` in `internal/winspike/winfs.go`. **Phase 3 copies that shape.**
Where this document and the prototype disagree, the prototype and the
measurements win.

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
4. **No path is ever re-resolved inside a mutation.** Measured against direct
   attack: a pinned-ancestor mutation survives the ancestor being replaced by a
   real directory, a junction, a symlink or an unknown-tag directory
   (`A1.ancestor_replaced.realdir` / `.junction` / `.symlink` / `.unknowntag`),
   and a delete through a pinned parent survives the parent being renamed away
   and replaced by a junction (`A6.parent_replaced`). The surviving
   re-resolutions are all on read or advisory paths and are enumerated
   exhaustively in §6.9 — revision 1 claimed there were three; there are more,
   and §6.9 now lists them all.

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

// The strict primitives (§2.1). openStrictAt is the shared body; the two
// exported forms differ only in access mask and FILE_{DIRECTORY,NON_DIRECTORY}_FILE.
// Single component, enforced at runtime — not by comment.
func openStrictAt(parent int, name string, access, options uint32) (int, error)
func openRealDirAt(parent int, name string) (int, error)   // strict; the openat(O_DIRECTORY|O_NOFOLLOW) twin
func openRealFileAt(parent int, name string) (int, error)  // strict; the openat(O_NOFOLLOW)+S_IFREG twin

func mkdirClaim(parent int, name string) error             // FILE_CREATE|FILE_DIRECTORY_FILE|OBJ_DONT_REPARSE
func rmdirAt(parent int, name string) error                // OPEN_REPARSE_POINT + FILE_DIRECTORY_FILE + POSIX dispose
func openFileAt(parent int, name string) (*os.File, error) // openRealFileAt + os.NewFile, full share mode
func readAllAt(parent int, name string) ([]byte, error)
func mkdirsAt(root int, segs []string) (int, error)
func writeFileAt(root int, segs []string, data []byte) error
func openPathFile(segs []string) (*os.File, error)
func pruneAt(r *rootedFS, segs []string)
func readDirFD(fd int) ([]os.DirEntry, error)              // M16: dup + os.NewFile + ReadDir
func statAt(parent int, name string) (entryMeta, error)    // NEW — the fstatat twin
func statLinkTarget(parent int, name string) (isDir, ok bool) // NEW — see below; NOT path-based
```

**`openRealDirAt` / `openRealFileAt` replace revision 1's `openDirAt` /
`openFileAt`.** Revision 1 specified them as
`FILE_OPEN|FILE_DIRECTORY_FILE|OBJ_DONT_REPARSE` with no tag read, which is the
primitive `A5.obj_dont_reparse_inert_for_unknown_tags` falsified (§2.1). A Phase
3 author implementing revision 1's §3.2 literally would have shipped it. The
strict form is:

```go
func openStrictAt(parent int, name string, access, options uint32) (int, error) {
	// name must be exactly one component — enforced, not commented.
	h, err := ntOpenAt(parent, name, access, FILE_OPEN,
		options|FILE_OPEN_REPARSE_POINT, OBJ_CASE_INSENSITIVE|OBJ_DONT_REPARSE, 0)
	if err != nil { return -1, err }
	at, err := attrTagOf(h)                 // same handle, no second lookup
	if err != nil { closeFD(h); return -1, err }
	if at.isReparse() { closeFD(h); return -1, &reparseRefusal{name, at.tag, at.attrs} }
	return h, nil
}
```

`reparseRefusal` carries the tag so callers can render an actionable error and
so an allowlist can be applied **by the caller** (§4.3's Scope A) rather than
buried inside the primitive. `errors.As` on it is how `openBrowsableDir`
recognises a candidate watch boundary.

**`statLinkTarget` is handle-relative and no-follow, not a path-based one-hop
follow.** Revision 1 described it two ways in the same document — §3.2 said
*"bounded one-hop follow for listings"* and Pre-1 said *"convert `entryIsDir`
… to a **no-follow** classification via `statLinkTarget`"* — and the difference
decides whether `entryIsDir` can be redirected through a junction. It is
resolved here in the safe direction: `statLinkTarget(parent, name)` is
`statAt(parent, name)` reduced to *(is this a real directory, was the answer
obtained)*, and it never follows. A reparse-tagged entry answers
`isDir=false, ok=true` — the fail-closed answer, which stops descent. There is
no path argument and no follow, so there is nothing to redirect.

**Documents are never served through `os.Open`.** Measured:
`P13.go_share_mode` — Go's `syscall.Open` hard-codes
`FILE_SHARE_READ|FILE_SHARE_WRITE` and omits `FILE_SHARE_DELETE`
(`$GOROOT/src/syscall/syscall_windows.go:395`); with a file held open by
`os.Open`, both the atomic replace and a POSIX delete of that file fail with
`STATUS_SHARING_VIOLATION`, while the same file held by a `CreateFile` granting
the full share mode permits both. So `openPathFile`/`openFileAt` must return
`os.NewFile` over a handle **we** opened with `FILE_SHARE_READ|WRITE|DELETE`, or
the web server streaming a document would burn a concurrent notes save's entire
retry bound against itself (§8.4). This is R15 applied to the read path, and it
also applies to `internal/watch`'s `desiredDirs`, which uses `os.Open` today
(§6.11).

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

**`readlinkAt` must validate the substitute name before returning it, and the
prototype does not.** `ReadLinkHandle` (`internal/winspike/winfs.go`) parses the
`SYMLINK` reparse body's `SubstituteNameOffset`/`Length` and **never reads the
`Flags` word at offset 8 of the body**. A link created with
`SYMLINK_FLAG_RELATIVE` (`0x1`) carries a relative substitute name — `..\..\Users`
— which `stripNTPrefix` passes through unchanged and `CreateFile` then resolves
**against the process working directory**. The store only ever creates absolute
links, but Scope A forgives links the store did not create, by design (§6.6), so
this is reachable. Required, in `readlinkAt`, for both tags:

- **`SYMLINK`:** read the `Flags` word; a link with `SYMLINK_FLAG_RELATIVE` set
  is refused with a distinct error. (`MOUNT_POINT` has no relative form; its
  substitute name is always `\??\`-prefixed, and one that is not is refused on
  the same rule.)
- **After stripping:** the target must pass the *same* validator §4.1 applies to
  `Root()` — absolute, drive-letter-rooted, not UNC, not `\\?\`, not `\\.\`, not
  drive-relative (`C:foo`), not current-drive-relative (`\foo`). One validator,
  one rule, two call sites.
- **`\??\Volume{`** is refused separately by §5.3.

Owner: **P3.4**. **[UNMEASURED]** — no run-9 measurement exercises a
`SYMLINK_FLAG_RELATIVE` link; `A11.*` used absolute links throughout. P3.4 adds
the case as a permanent test (§11.1).

### 3.4 `annotationfs_windows.go`

```go
type annotationFS struct {
	storeRoot *os.File // pinned root
	root      *os.File // pinned .annotations
	lock      *os.File // pinned <root>\.scratchpad-lock — the rendezvous object (§6.7)
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

- **F-a (revised in revision 2).** A **strict open** of one component against a
  pinned parent either returns a handle to a real, untagged entry or fails —
  and the decision is made from `FILE_ATTRIBUTE_TAG_INFO` read from **that same
  handle**, so it is one indivisible operation with no observable intermediate
  state and no dependence on which filter drivers are installed
  (`A5.strict_open`, `A5.unknown_tag_refused`, `A5.strict_open_admits_real_dirs`,
  `A5.strict_walk`, all REQUIRED). `OBJ_DONT_REPARSE` is carried in the same
  call and cheaply refuses Microsoft-tagged **intermediate** components before a
  handle exists (`M1.intermediate`, REQUIRED; negative control
  `M1.weak_flag_traverses`) — but it is **necessary and not sufficient**, and
  F-a does not rest on it (§2.1, `A5.obj_dont_reparse_inert_for_unknown_tags`).
- **F-b.** A handle names an object, not a name: renaming the object, or any
  ancestor, does not redirect operations issued through it (`M7.redirect`,
  REQUIRED; `FILE_NAME_INFO` on the retained handle reports the *new* path
  afterwards, which is the direct demonstration). Measured against substitution
  as well as renaming: `A1.ancestor_replaced.*`, `A6.parent_replaced`.
- **F-c.** A create-only claim is atomic against the pinned parent
  (`P12.mkdir_excl`, REQUIRED; `A8.concurrent_claim` — 16 racers, 1 winner, 15
  `STATUS_OBJECT_NAME_COLLISION`, 0 other), and a destination-replacing rename
  is atomic against the pinned parent (`M9` and `P13.replace_existing`, both
  REQUIRED).

Every operation below is F-a for the walk, F-b for the anchor, F-c for the
mutation. **The composition holds for every walk that is handle-relative end to
end.** Exactly one walk in the design is not — the crossing of the single
permitted watch boundary, which consumes a path string read out of a reparse
buffer — and §4.3 treats it as its own case rather than as an instance of F-a.
Where an operation cannot be reduced to F-a/F-b/F-c, that is called out as a
weakness rather than argued away.

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
never re-resolved, a replaced root cannot redirect us anyway. It also cannot
detect replacement of an intermediate project directory — that case is covered
structurally by the handle chain (F-b), not by identity.

**And revision 1 overstated even the diagnostic (F9).** §10.2 overrides R12 from
a process-lifetime pin to a per-operation pin. Under a per-operation pin,
`verifyRoot()` compares the root's identity against a value recorded
*microseconds earlier in the same operation* — a case F-b already makes
harmless. It cannot see a replacement **between** operations, which is precisely
the case R13 was written for ("silently operating on the wrong store"). So
`verifyRoot()` alone cannot do the one thing revision 1 credited it with.

**Decision: add a process-level last-seen root identity, keyed on the resolved
root string.** `openRootedFS` consults a package-level
`map[string]objectID` after pinning:

- key absent → record and proceed (this is the `t.Setenv(store.RootEnv, …)`
  case, which must keep working — it is why R12 was overridden at all);
- key present, identity equal → proceed;
- key present, identity **different** → the loud error R13 exists to produce.

That is four lines, it costs nothing, and it restores the cross-operation
property the override removed. `R13.replace` (REQUIRED) is the measurement that
`FILE_ID_INFO` discriminates objects; that the *cache* discriminates across
operations is **[UNMEASURED]** and is a P3.1 test (§11.1). §4.9's R13 row is
downgraded to "yes, conditional on this cache" until P3.1 lands it.

**`A4.root_removed` — decided here, because the finding says the ADR must.** The
measurement (INFO, run 9): removing the pinned root while a handle is open
returns nil; a subsequent `MkdirAt` through the handle returns
`STATUS_DELETE_PENDING`; **`Verify()` returns nil**. Windows keeps the object
alive for the open handle, so the store keeps working against a directory no
longer reachable by name.

**Decision: this is acceptable within an operation and must be detected across
operations, and `verifyRoot()` must not pretend to detect it.**

- *Within* an operation it is the correct behaviour and the exact analogue of
  Linux: an operation that began against a live root, holding a descriptor to
  it, completes against that object. That is F-b, not a defect. The one visible
  consequence — `STATUS_DELETE_PENDING` from a mutation through the handle — is
  already mapped to `errDeletePending`, distinct and reported, never `errExists`
  (§3.7, R6's falsifier).
- *Across* operations it needs no new mechanism: the root is re-opened by name
  each operation (§10.2), and a removed root fails that open with
  `STATUS_OBJECT_NAME_NOT_FOUND` → `fs.ErrNotExist`. A removed-and-recreated
  root is caught by the identity cache above.
- `verifyRoot()` is therefore **not** extended to attempt removal detection: it
  cannot (`A4.root_removed` measured `Verify() → nil`), and a check that cannot
  fire is worse than no check because it invites reliance.

**Filesystem gate (R18):** `GetVolumeInformationByHandle` on the root handle
gives the filesystem name. Per threat model §9.8 the gate is the **first
mutation**, not the first open — the filesystem cannot be known before a handle
exists. NTFS proceeds silently. Non-NTFS local volumes emit one warning and
proceed (§8.3). UNC and device roots were already refused above.

### 4.2 `openRealDir` — Publish/Watch/Unwatch/Delete ancestors — R1, R3, R6

Each segment is `openRealDirAt` against the previous handle: one component, one
strict open, tag read from that handle (F-a). A reparse point at any position —
`SYMLINK`, `MOUNT_POINT`, an unknown non-Microsoft tag, serviced by a filter
driver or not — fails the walk with `errReparse` carrying the tag, which shared
code renders as "project ancestor %q is a link or reparse point (tag 0x…)".
`create` mode is `mkdirClaim` then re-open — never `MkdirAll`, never a joined
path. `rejectArtifacts` consults `dirHasHTMLFD`, which after Pre-2's hoist reads
entries through `readDirFD` and classifies each through `statAt` from the same
handle.

R1's falsifier — "any mutation whose final Win32 call receives a path with more
than one component" — is structurally impossible: the single-component
restriction is a **runtime check** in `openStrictAt`, `statAt`, `unlinkAt` and
the delete path, and the walk never joins.

**Satisfied:** R1 (mechanism), R3 (`A5.strict_walk`, REQUIRED — the
project-ancestor walk specifically; `A5.unknown_tag_refused`, REQUIRED, for the
tag that `OBJ_DONT_REPARSE` cannot cover; `M1.intermediate` for the cheap
Microsoft-tag short circuit), R6 (`P12.mkdir_excl`, `A8.concurrent_claim`).

### 4.3 `openBrowsableDir` — invariant 5 — R1, R3, R4

Every segment is a strict open. Exactly one boundary is forgiven, and only when
`crossed` is false and the current directory is not itself an artifact (the
Linux `!dirHasHTMLFD(fd)` guard — see the fail-open note at the end of this
section). On a reparse refusal or `errNotDir` at an unforgiven boundary the walk
fails.

At the one forgiven boundary: read the reparse data from the pinned parent
(`FSCTL_GET_REPARSE_POINT`), refuse any tag outside `watchTags` with a distinct
message, apply `readlinkAt`'s absoluteness rules (§3.3) and §5.3's `\??\Volume{`
refusal, then reach the target by the **handle-by-handle walk** below. Every
segment after the crossing is a strict open again, so a nested link **inside the
watched source** — A1's territory, symlink or junction or unknown tag alike — is
refused with no allowlist consulted, at any depth. Measured exhaustively:
`A3.nested.*` and `A3.nested_strict.*` cover every boundary flavour (junction,
symlink) × every nested flavour (junction, symlink, unknown tag) at one and two
levels below the crossing, and every one is refused; the `nested_strict` variants
are the ones whose refusal does not depend on installed filter drivers.

The tag allowlist is a parameter, measured both ways: with `{SYMLINK}` a junction
boundary is refused; adding `MOUNT_POINT` crosses the same boundary with no other
change (`P12.browsable_tag_allowlist`, `P12.browsable_tag_allowlist_junction`).

#### `A11.ancestor_swapped` — what revision 1 got wrong

Revision 1 said the reopen *"require[s] a plain directory — so a link-to-a-link
cannot chain"*, and RW3 said a substituted target is *"refused by the tag check
on the reopen"*. **Both sentences are true only of the final component, and
revision 1 stated them as if they bounded the whole path.** That is the same
error the ADR itself warns about two sections earlier when it quotes
`M1.weak_flag_traverses`: `FILE_FLAG_OPEN_REPARSE_POINT` protects the last
component and nothing else.

The spike measured the consequence directly. `A11.ancestor_swapped` is the only
**NO** verdict in run 9 that lands on a containment question, and
`spike-findings.md` §10.1 titles it *"WARNING — containment BROKE"*:

> an ANCESTOR of the watch target was replaced with a junction between the
> reparse-buffer read and the open-by-name: open -> nil ; entries reached
> [LOOT.txt] ; the attacker's tree was reached = true. THIS IS A REAL WINDOW AND
> IT IS PLATFORM-INDEPENDENT … The structural fix on both platforms is to walk
> the target's components handle-by-handle instead of opening the string.

Its sibling `A11.target_swapped` **HELD** — substituting the *target itself* is
refused, which is exactly the half revision 1's sentence described. Four things
must be said plainly about the half that did not hold:

1. **It is not a race, and calling it one understates it.** The spike staged it
   as a TOCTOU because a deterministic hook needs a window, but the store never
   validates the watch target's ancestors *at all* — not at watch time, not at
   browse time. Any ancestor that becomes a reparse point at any point after
   `watch` is followed on every subsequent browse, indefinitely. There is no
   window to lose.
2. **A1 reaches it, not only A2.** The threat model bounds this by "the attacker
   already needs write access above the user's watched folder", which is right
   for a plain filesystem attacker. But a watched **git repository** is the
   canonical case and `git checkout` creates links: a branch that replaces
   `docs/` with a link, in a repo whose `docs/.scratchpad` is watched, turns a
   `git switch` into a browse redirect. That is content inside the watched
   source — A1 territory.
3. **The payoff is over the network.** The redirected tree is served by an
   unauthenticated HTTP endpoint; under `LAN=1` it is readable by anything that
   can reach the host.
4. **It is bounded to reads.** Every mutation walk is `openRealDir`, which is
   handle-relative end to end and refuses a reparse point at every component
   (`A1.ancestor_replaced.*`, `A6.parent_replaced`). No mutation reaches through
   this path. So: High as a proof defect, Medium as a live exposure.

**The red-team's minimum ask — record it, correct the two sentences, and keep
the weaker behaviour as a bounded acceptance — is rejected as insufficient.**
Reasoning: a "bounded" acceptance is only meaningful if the bound is a
*condition the attacker must arrange*, and here the condition is permanent once
arranged and arrangeable by A1 through an ordinary `git checkout`. The
structural fix costs one loop.

#### The decision: walk the target handle-by-handle

**`openAbsoluteDirNoFollow` is replaced by a component-at-a-time walk of the
target path, anchored at the volume root, using the strict primitive at every
component.** Concretely:

1. `readlinkAt` has already guaranteed the target is absolute, drive-rooted and
   non-relative (§3.3).
2. Open the **volume root** (`C:\`) by name once, `FILE_FLAG_BACKUP_SEMANTICS |
   FILE_FLAG_OPEN_REPARSE_POINT`, and require a real untagged directory. This is
   the anchor; it is the Windows counterpart of Linux's
   `openFilesystemRootNoFollow`, which opens `/`.
3. Walk every remaining component with `openRealDirAt` against the previous
   handle. A reparse point at **any** component fails the walk closed, and
   `openBrowsableDir` treats that exactly as it treated the old open failing.
4. No path string survives step 2. From there it is a chain of pinned handles,
   so there is nothing left for a later substitution to redirect.

**This is now the shipped Linux shape, not a proposal.** While this revision was
being written, `internal/store/storefs_linux.go` gained
`openFilesystemRootNoFollow` and a rewritten `openAbsoluteDirNoFollow` doing
precisely this, with `A11.ancestor_swapped` named in its doc comment. The
Windows backend mirrors it, substituting the volume root for `/` and the strict
open for `O_NOFOLLOW`. *(No Go file is edited by this revision; the Linux change
is cross-referenced, not authored here.)*

**The compatibility cost, stated rather than left as an unstated default.** The
walk refuses a target whose path crosses a *legitimate* junction or volume mount
point — a redirected `%USERPROFILE%`, a Dev Drive mounted into a folder rather
than onto a letter, a OneDrive-redirected `Documents`. This cost does not arise
on Linux, and it does not arise on Windows either **if the stored target is
already fully resolved**, which is where it is paid instead:

- On Linux, `Watch` stores a `filepath.EvalSymlinks`-resolved target, so a
  normal watch presents a link-free path to this walk on every browse and
  anything refused is a path that *changed since the watch was created* — which
  is the condition the fix exists to catch.
- **On Windows, `EvalSymlinks` cannot play that role**: it does not resolve
  junctions and returns `ERROR_PATH_NOT_FOUND` when one is an intermediate
  component (`M5.junction`). So `Watch` must canonicalise differently: open the
  target by path **following** reparse points (an ordinary
  `FILE_FLAG_BACKUP_SEMANTICS` open, no `FILE_FLAG_OPEN_REPARSE_POINT`), take
  `GetFinalPathNameByHandleW(VOLUME_NAME_DOS)` from that handle, strip the
  `\\?\` prefix, re-validate it through §4.1's validator, and store **that** as
  the link target.
- **This is a deliberate, narrow extension of §10.3's carve-out.** §10.3 bans
  `GetFinalPathNameByHandleW` as an input to a subsequent operation. Here its
  output is not consumed by a following operation; it is written into the link
  as a durable record, and every later use of it is re-validated and then walked
  handle-by-handle. `M6.resolution` measured that its output is handle-derived
  and long-name canonical. §10.3 is amended accordingly.

**Residual, named:** the volume root is reached through a drive letter, which is
a DOS device link resolved from the process's device map. Whether an
unprivileged same-user process can rebind an *existing* letter in its own logon
session (`DefineDosDevice`) is **[UNMEASURED]** — owner **P3.4** to measure,
**P6.1** to stress. It is A2 territory in any case, and A2 can already run
`scratchpad watch` against any tree it likes, so this is not the cheapest attack
available to it.

**Fallback, pre-specified so it is not invented under pressure.** If P4.3/P6.6
measure the compatibility cost on real machines and find it unacceptable, the
documented retreat is: forgive reparse points at *ancestor* components only when
the tag is on Scope A's allowlist, keep the strict requirement on the final
component, and re-rate RW22 from *Mitigated* to *Accepted, argued*. That retreat
is a decision for a recorded ADR amendment, not for an implementer.

#### One place the prototype is stricter than Linux — keep it

`OpenBrowsableDir` in the prototype requires `DirHasHTML` to **succeed** before
forgiving the boundary (`htmlErr == nil && !hasHTML`), whereas
`storefs_linux.go`'s `dirHasHTMLFD` returns `false` on a read error and the
Linux walk therefore forgives the boundary when it could not tell. Revision 1
said the Linux guard was "preserved"; preserving the fail-open half would be a
small regression against the prototype. **Keep the prototype's fail-closed
form**, and tighten the Linux side to match (owner **P3.4**, cross-platform,
with the Linux regression test).

**Satisfied:** R1, R3, R4. **Invariant 5 now holds absolutely rather than
"by the same argument as Linux"** — the argument revision 1 made was the one
`A11.ancestor_swapped` falsified on both platforms.

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
- **Real directory** → `openRealDirAt` + `dirHasHTMLFD`, then `removeTreeAt`.

#### `removeTreeAt`: the operation IS the classification

**Revision 1 specified the weaker algorithm here, on the ADR's own Critical risk
and its own declared release gate.** It said: *"`readDirFD` for entries, `statAt`
per entry for classification, recurse only into `IsDir`"* — classify, then open.
The prototype deliberately refuses to do that, and it is right. Replace it with:

```
removeTreeAt(parent, name):
    h, err := openRealDirAt(parent, name)      // STRICT: one op, tag from the handle
    if err is not-exist:            return nil
    if err is reparse-refusal or not-a-directory:
                                    return unlinkAt(parent, name)   // remove the ENTRY, never a target
    if err is anything else:        return err
    for each entry in readDirFD(h):
        removeTreeAt(h, entry.Name())          // recurse against the PINNED handle
    closeFD(h)
    return unlinkAt(parent, name)              // FILE_DIRECTORY_FILE + POSIX dispose
```

**Nothing is decided from a separately-observed attribute. The attempted
no-follow directory open comes first, and its FAILURE is the classification.**
There is therefore no check-then-use window at all: if the entry is, or
*becomes*, a reparse point, the open fails and the entry is unlinked as a link.
`statAt` survives in this path for **reporting only** — never to decide a
descent.

Three reasons this is not a stylistic preference:

- **The attribute is a trap.** `FILE_ATTRIBUTE_DIRECTORY` is **set** on a
  junction (`0x410`, `P14.delete_attr_trap`), so a walk that decided by the
  directory attribute alone would descend — which is RR1 exactly.
- **The window is real and is a REQUIRED property.** `A6.swap_midwalk` exists
  precisely to exercise what classify-then-open reopens: an ordinary
  subdirectory swapped for a junction *between the enumeration and the descent*.
  It **HELD** for the operation-as-classification shape.
- **The negative control proves the assertion has teeth.**
  `A6.negative_control` runs the mechanical classify-then-recurse port —
  `removeTreeAtByAttributeUNSAFE` in the prototype — against the same fixture
  and confirms it **destroys the external target tree**. A test that never fails
  against a broken implementation is not a test; this one does fail.

Coverage, all HELD in run 9: `A6.delete.junction.depth0` / `.depth2`,
`A6.delete.symlink.*`, `A6.delete.unknowntag.*` (a planted link at the top of
the artifact and two levels inside it, in all three flavours),
`A6.parent_replaced`, `A6.unlink_watch.junction` / `.symlink`, plus
`P14.delete_descend` (REQUIRED) and `A5.unknown_tag_removed` (REQUIRED —
removing an unknown-tag entry touches nothing outside the store).

Two supporting measurements: Go's own `os.RemoveAll` on a junction removes the
link and leaves the target intact (`RR1.removeall`, REQUIRED), and
`filepath.WalkDir` starting at a junction does not descend (`RR1.walkdir`) — so
the danger is specifically a hand-rolled walk, and ours is the hand-rolled one.

**One carried-forward gap:** `RemoveTreeAt` has **no depth bound**, although R16
names `removeTreeAt` specifically. This is pre-existing on Linux, not introduced
by the port. Fix both, in P3.9 (§11).

**Satisfied:** R7, R8. RR1 closed by mechanism; the "Delete / target replaced"
matrix cell (`MATRIX.Delete.target_replaced`, YES in run 9) is the release gate,
subject to F12's caveat in RW1.

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

**One step Linux gets for free and Windows must do explicitly (new in revision
2).** Linux's `Watch` stores a `filepath.EvalSymlinks`-resolved target, which is
what makes §4.3's handle-by-handle browse walk cheap: a normal watch presents a
link-free path on every browse, so anything the walk refuses is a path that
*changed since the watch was created*. `EvalSymlinks` cannot do that job on
Windows — it does not resolve junctions and errors when one is an intermediate
component (`M5.junction`). So `Watch` canonicalises through a handle instead:
open the target by path **following** reparse points, take
`GetFinalPathNameByHandleW(VOLUME_NAME_DOS)` from that handle, strip `\\?\`,
re-validate through §4.1's validator, and store that string as the link target.
See §4.3 for why this is a legitimate narrow exception to §10.3's ban.

### 4.8 Annotations — R9, R10

`.annotations` is created and opened relative to the pinned root with a **strict
open**, so a replacement by symlink, junction *or an unknown tag* is refused on
the tag read from the handle, not on a mode bit and not on whether a filter
driver happens to service it. Every subsequent segment is `openRealDirAt`.
Measured: `MX.notes_root_link.junction` / `.symlink` and
`MX.notes_intermediate_link.junction` / `.symlink`.

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
that turns this from a bug report into a design constraint.

**The class-65 → class-10 fallback trigger is an allowlist, not "retry on any
error".** `A9.rename_failure_statuses` measured what class 65 returns per failure
mode against what class 10 returns for the same case, and the consequence is
recorded verbatim in the spike: the fallback must fire **only** on
`STATUS_INVALID_PARAMETER` / `STATUS_NOT_SUPPORTED` /
`STATUS_INVALID_INFO_CLASS` / `STATUS_INVALID_DEVICE_REQUEST` — i.e. "this build
or filesystem does not implement the class". The Go standard library's blanket
fallback would, on the measured rows, *silently retry an attack* with a class
that has no POSIX semantics: `dest_held_no_share_delete` returns
`STATUS_SHARING_VIOLATION` (retryable) from class 65 but `STATUS_ACCESS_DENIED`
(**not** retryable) from class 10, and `dest_is_directory` returns
`STATUS_OBJECT_IS_A_DIRECTORY`, which is permanent. `isUnsupportedRenameClass`
in the prototype implements exactly this allowlist and is the reference. Both
classes work on builds 26100/26200 (`M9`).

**R9's three guards, all measured — and none of them is the one revision 1
implied.**

- *The destination is never unlinked.* `P13.no_dest_removal` (REQUIRED): a
  successful replace issues **zero** namespace removals naming the destination,
  audited across every disposition-setting call the write path makes
  (`P13.audit`). The audit's own negative control, `P13.audit_control`, runs it
  against a deliberately-wrong remove-then-rename and confirms the audit
  **catches** it — without that control the guard would be vacuous.
- *The name resolves at every instant.* `P13.continuous_existence` (REQUIRED): a
  concurrent reader polling the destination across 200 replaces observed it
  absent **0 times in 1902 polls**, against a negative control
  (remove-then-rename) that saw it absent **1902 times out of 3441**. Empirical
  rather than deterministic, and the margin is not a close call.
- *A vetoed replace never truncates.* `P13.sharing_never_truncates` and
  `P13.bound_preserves_dest` (both REQUIRED) found "COMPLETE-OLD" every time;
  `M13.blocked` is the same result for the single deterministic case, and
  `P13.sharing_summary` states the whole rule once: *"the interferer's share
  mask, not its access mask, decides"* — a destination opened without
  `FILE_SHARE_DELETE` vetoes the replace; one that grants it does not
  (`P13.share_all_reader_does_not_veto`, REQUIRED).

**Cleanup goes through the temp's own handle, never its name** (R-4 in the
prototype's contract). `P13.cleanup_handle_based`: the temp was renamed to
`stolen.tmp` between the write and the replace and the replace was then failed —
no residue, because `DeleteByHandle` follows the object through the rename where
a name-based cleanup would have unlinked nothing and orphaned a file inside the
store. `P13.cleanup_bound` and `P13.cleanup_permanent` confirm the same after a
retry-bound exhaustion and after a permanent failure.

**A withdrawn hypothesis, recorded because it changed what guards R9.** Revision
1 predated this. `P13.change_records` was wired as a REQUIRED property on the
belief that `ReadDirectoryChangesW` could distinguish an atomic replace from
remove-then-rename by its `FILE_ACTION_*` codes. The runner contradicted it —
run 8's `SECURITY-FAIL` — because a POSIX-semantics rename that *replaces* a
destination makes the kernel emit `FILE_ACTION_REMOVED` for the replaced file as
part of the atomic rename. Run 9 withdrew the assertion (now **NO**,
informational) rather than patching around the observation, and its replacement
is stronger, not weaker: the white-box audit plus the black-box
continuous-existence observer above, the latter with a proven-failing negative
control. *(The prototype's `dirnotify.go` package comment still asserts the
withdrawn hypothesis as fact; the finding record is authoritative and the
comment dies with the package.)*

**One consequence for `internal/watch`**, from
`P13.watch_sees_removed_on_replace` (INFO): every notes save on Windows emits
`REMOVED(<doc>)` immediately followed by `RENAMED_NEW_NAME(<doc>)` and
`MODIFIED(<doc>)`, which fsnotify maps to Remove→Create→Write. A watcher that
reacts to Remove by dropping state for the document, or a UI that reacts by
hiding it, will flicker on every save. The 250 ms debounce in `internal/watch`
absorbs this — on Linux a rename emits no unlink at all, so on Windows **the
debounce is doing real work and must not be removed**. Owner: P4.2.

**Concurrency:** `A12.concurrent_writers` (REQUIRED) ran 8 writers × 25 replaces
of one document — zero per-writer failures, zero torn or partial reads — and
`A12.concurrent_temp_residue` (REQUIRED) found no temp files left behind. The
`rev` guard above this layer decides *which* version wins, not whether the file
is intact. *(`matrix_test.go`'s descriptive string says "8 writers × 40
replaces"; the constant is 25 and every log line reports 25. The finding is
8 × 25; the "40" is a stale literal, not a second measurement.)*

**Recursive annotation removal** uses the same `removeTreeAt` as §4.5, so R8
covers it. Ancestor pruning reopens each parent from the anchored root, so a
concurrent rename can only make pruning stop, never redirect it — unchanged
policy.

**Durability, stated as a non-guarantee — and it *is* measured.** No
`FlushFileBuffers` before the rename, matching Linux (which does not `fsync`).
The guarantee is atomicity of *replacement*, not crash durability, on both
platforms. Revision 1 marked this **[UNMEASURED]**; that was wrong.
`P13.flush_cost` (INFO) measured it at 100× (create temp + write 4 KiB + atomic
replace): **811 µs/op without `FlushFileBuffers`, 4.594 ms/op with it — 5.7×**,
with an explicit recommendation not to flush by default, because flushing would
make Windows strictly *more* durable than the Linux backend rather than reaching
parity, at that cost per note save. What the atomic replace already guarantees
without any flush is the property that matters: the destination name always
resolves to one **complete** version. What it does not guarantee is *which*
version survives a power loss — and a lost note revision is recoverable by the
user, a torn one is not. `P13.flush_scope` (INFO) closes the obvious follow-up
question: there is no directory-`fsync` analogue on Windows, because the rename
is a metadata operation on the parent's index and NTFS journals it, so a flush
on the temp's own handle is the only durability knob there is. It is exposed as
`ReplacePolicy.Flush` and defaults to `false`.

### 4.9 The requirement table

| | Satisfied? | By what |
|---|---|---|
| **R1** segment-at-a-time, handle-anchored | **yes** | `ntOpenAt` + single-component enforcement; `P12.openrealdir` |
| **R2** no security decision by string comparison | **yes, with two demotions** | §7.1 identity; §7.3 `joinInRoot` and `visibleSegments` demoted to advisory and documented as such |
| **R3** reparse refused on intermediate components | **yes, by the strict open — not by `OBJ_DONT_REPARSE`** | §2.1; `A5.strict_open`, `A5.unknown_tag_refused`, `A5.strict_walk`, `A5.strict_open_admits_real_dirs`, `A3.nested_strict.*` (all REQUIRED). `M1.intermediate` covers only Microsoft tags and `A5.obj_dont_reparse_inert_for_unknown_tags` bounds it — revision 1 cited `M1.intermediate` + `M1.weak_flag_traverses` for the whole claim, and that evidence does not reach it |
| **R4** tag allowlist, refuse-by-default | **yes** | §5; `RR1.unknown_tag_isdir` is why "refuse surrogates" is rejected |
| **R5** never `fs.ModeSymlink` for classification | **yes for mutation; over-approximated for listing** | `statAt` for every mutation; `ModeSymlink\|ModeIrregular` for listings (§3.3) — fail-closed, measured |
| **R6** atomic create-only, collision distinguishable | **yes** | `P12.mkdir_excl`, `A8.concurrent_claim`; §6.6's three-way status map |
| **R7** Unwatch removes only an allowlisted link | **yes** | §4.6; `P14.unlink_junction` (REQUIRED), `A6.unlink_watch.*` |
| **R8** recursive removal classifies from the handle | **yes** | §4.5's operation-as-classification; `P14.delete_descend` (REQUIRED), `P14.delete_attr_trap`, `A6.swap_midwalk`, `A6.negative_control`. Revision 1 specified classify-then-open here and `A6.negative_control` shows that shape destroys the external tree |
| **R9** annotation write is one atomic replacement | **yes** | `M9`, `P13.replace_existing`, `P13.no_dest_removal`, `P13.continuous_existence`, `P13.sharing_never_truncates`, `P13.bound_preserves_dest` (all REQUIRED), plus the `P13.audit_control` / continuous-existence negative controls |
| **R10** bounded retry on transient failures | **yes; bound measured, distribution not** | §8.4; `P13.bound` (10 attempts / 771 ms), `P13.bound_terminates`, `P13.retry.hold*`, `P13.retry_integrity.*` (REQUIRED). `M13.av` is NOT MEASURED and the *sizing* remains a documented choice |
| **R11** lookup validation + case-insensitive guards | **yes** | §7.4, §7.5 |
| **R12** root resolved once and pinned | **overridden** → once *per operation*, carried as `rootedFS.path`+handle | §7.3, §10.2 |
| **R13** pre-mutation root identity re-verification | **yes, conditional on the process-level identity cache landing** | `R13.replace` (REQUIRED) gives the discrimination; §4.1 gives the cross-operation check the per-operation pin removed. Until P3.1 lands the cache this row is **partial** — `verifyRoot()` alone compares against a value recorded microseconds earlier |
| **R14** watcher identity from the registered handle | **partial** | §3.5 supplies `FILE_ID_INFO` identity, but `identity(*os.File)` reads a handle the **watcher** opened while `backend.Add(string)` registers a different, path-keyed handle. §11's P4.1 concedes the map stays path-keyed, forced by the fsnotify backend API. The falsifier (a path-string identity) is avoided; the property as written is not fully delivered |
| **R15** `FILE_SHARE_DELETE` everywhere, all handles closed | **yes for store primitives; one known violation outside them** | §3.2 ownership rules. `internal/watch`'s `desiredDirs` uses `os.Open`, which `P13.go_share_mode` measured omits `FILE_SHARE_DELETE` — owner P4.1 (§6.11) |
| **R16** depth bound + identity-keyed visited sets + **reconcile error triage** | **partial — the triage clause is the gap** | §6.8 delivers the depth bound and the identity-keyed visited set. The triage clause is specified in §6.11 and owned by **P4.2**; until it lands, an unreadable directory is a persistent failure to start (RW23) |
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
  boundary is crossed:** the allowlist is **empty**, and the enforcement is the
  **strict open** (§2.1) — `FILE_OPEN_REPARSE_POINT` plus a tag read from the
  same handle — refusing every tag including `SYMLINK` and `MOUNT_POINT`.
  Revision 1 said *"`OBJ_DONT_REPARSE` refuses everything"*; that is false for
  non-Microsoft tags on a machine with a filter driver that services them
  (`A5.obj_dont_reparse_inert_for_unknown_tags`), which is why Scope B is now
  stated as a property of the primitive and not of a flag.
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
(`WCI*`), VFS-for-Git (`PROJFS*`), a vendor filter — the open succeeds.

**Revision 1 then drew the wrong conclusion from that fact**, and the correction
matters because the two halves of the design have opposite answers:

- **For traversal** (`openRealDir`, `openBrowsableDir`) classification is never
  consulted at all. The walk's entire defence is the **open failing** — which is
  exactly why it must fail for a reason we control (the tag we read from our own
  handle) rather than for a reason the machine's driver set controls. That is
  §2.1, and it is the whole of F1.
- **For listing, `Delete` and `Unwatch`**, classification *is* the defence, and
  the `IsDir() == true` trap below is the reason it must be tag-based
  (`RR1.unknown_tag_isdir`) rather than mode-based.

Revision 1 stated the second answer as if it covered the first.

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
a **strict** open only if the filesystem actually surfaces them as reparse
points to `FILE_ATTRIBUTE_TAG_INFO`; on the measured runner they did not appear.
**[UNMEASURED]** on a deduplicated volume — and note this is a case where the
strict open is *stricter* than revision 1's primitive, since it refuses on the
tag rather than on the open failing, so the measurement matters more than it did
before. Owner: **P6.6**, as part of the release matrix. `APPEXECLINK` (observed live in `WindowsApps`,
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

### 6.2 The strict open — adopted as the containment primitive

`FILE_OPEN_REPARSE_POINT` + `FILE_ATTRIBUTE_TAG_INFO` from the same handle,
refusing by tag value (`A5.strict_open`, `A5.unknown_tag_refused`,
`A5.strict_walk`, `A5.strict_open_admits_real_dirs`, `A3.nested_strict.*` — all
REQUIRED). `OBJ_DONT_REPARSE` is carried in the same call as a cheap
intermediate-component filter for Microsoft tags (`M1.intermediate`, REQUIRED,
plus the `M1.weak_flag_traverses` control) and is **necessary and not
sufficient** (`A5.obj_dont_reparse_inert_for_unknown_tags`). Full argument in
§2.1. Revision 1 named `OBJ_DONT_REPARSE` here; that is the primitive run 7
falsified with a `SECURITY-FAIL`.

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

1. **Error path self-heals — in both flavours, and through the handle.** On
   `FSCTL_SET_REPARSE_POINT` failure, POSIX-delete the just-created directory
   through the handle already held. Two corrections to revision 1, both verified
   in the prototype:
   - **`CreateJunctionAt` has no cleanup at all.** Only `SymlinkAt` cleans up,
     and per §6.6's own privilege table junctions are the **only** flavour
     available to an unprivileged, Developer-Mode-off user — i.e. exactly the
     population most likely to hit this window. Both flavours must self-heal.
   - **Neither cleans up when the post-claim *reopen* fails**, only when the
     FSCTL does. Both failure points leave the same wedged empty directory.
   - **"Through the handle already held" is not implementable as revision 1
     specified it.** `SymlinkAt`'s cleanup is `DeleteAt(parent, name, …)` — a
     re-open **by name**, which is precisely the re-resolution the design
     removes everywhere else. To do what this rule says, the post-claim reopen
     must request `DELETE` access; it currently requests
     `FILE_GENERIC_WRITE|FILE_GENERIC_READ`. Add `DELETE` and dispose through
     that handle. Owner: **P4.3**.
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

   **The recovery instruction is not true yet, and the UI half is part of the
   change.** `internal/web/templates/list.tmpl` renders a folder card's `delete`
   button **only** inside `{{with .Unwatch}}` (line 43): a plain directory with
   no watch link has no delete affordance at all, so "delete it in the web UI"
   currently names an action the user cannot take. Widening `store.Delete` does
   not create the button. RW19's scope therefore includes the card affordance —
   owner **P4.6** — and the message must not ship before it.

   Silently reclaiming the directory was considered and rejected: `Publish`
   leaves its artifact directory empty for the window between `mkdirClaim` and
   the first `writeFileAt`, so an auto-reclaiming `Watch` could delete a
   concurrent `Publish` out from under itself.

   **Divergence from the spike's own recommendation, stated.**
   `A7.two_step_recovery` (INFO) recommended letting **`unwatch`** clear an
   empty real directory ("an empty directory under a watch name carries no user
   data by definition"), and spike §8 item 12 repeats it. This ADR chooses
   **`Delete`** instead, deliberately: `unwatch` is agent-reachable and `Delete`
   is user-only, so routing the recovery through `Delete` preserves the
   create-only-for-agents asymmetry that the store's whole verb split exists to
   maintain. The measurement is not being contradicted — `A7.two_step_recovery`
   measured that a handle-relative `rmdir` of the empty directory works, which
   is true through either verb — only its suggested placement.

   Residue characterisation, measured: `A7.two_step_residue` (INFO) confirms the
   leftover is "an ORDINARY EMPTY DIRECTORY — not a partial link, not a broken
   link, and indistinguishable from a published-but-empty artifact", and
   `A7.two_step_window` records that it is "benign for CONTAINMENT (an empty
   real directory grants an attacker nothing) but is a usability trap: it
   consumes a name that create-only semantics will never release."

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

**Design: a lock file in the store root, opened once by handle when
`annotationFS` is created, and never re-resolved.**

Revision 1 put the lock file at `.annotations\.lock` and justified it with a
parity claim that is **false**, on evidence that measures a different property.
Both are corrected here.

#### What Linux actually buys, and what revision 1 claimed

`lockAnnotations` (`annotations.go:117-129`) flocks `ann.storeRoot` — **the
store root directory inode itself**, the very object the whole operation is
anchored on. Losing the rendezvous on Linux requires replacing the *store root*,
which needs write access to the root's **parent**.

Revision 1's design reaches its rendezvous root → `.annotations` → `.lock`, so
losing it requires replacing **either** `.annotations` **or** `.lock` — two
objects, both *inside* the store, one and two hops from where a same-user
process is already writing. That is a strictly larger and much cheaper surface,
and `annotations.go:124-126`'s own comment names `.annotations` being renamed
away as precisely the case the root inode was chosen to survive. RW5's *"Linux
has the same shape of gap"* is therefore withdrawn.

**And the cited evidence does not reach the claim.** Revision 1 argued the
property is "preserved by the handle, not by the location" and cited
`M7.namefollows` and `R13.rename`. Both measure **rename** survival. A handle
does survive a rename. The hazard here is **delete-and-recreate**, which is
unmeasured, and which this design's own R15 makes trivial: **`FILE_SHARE_DELETE`
is mandatory on every handle**, so the rendezvous object is deletable out from
under its holder, silently, with no error on either side. After such a swap two
processes hold locks on two different objects and both believe they are
excluded.

#### The red-team's remedy (a) is rejected, with reasoning

P1.7 offered two fixes: (a) re-validate the lock object's `FILE_ID_INFO` after
acquiring the lock, by re-opening `.lock` relative to the pinned `.annotations`
handle and comparing; or (b) move the rendezvous to a root-level lock file.

**(a) does not work, and it is worth writing down why so it is not reinvented.**
Trace two processes:

1. P1 opens `.lock` → object X, locks X, re-opens by name → X, comparison passes.
2. The attacker deletes `.lock` and creates a new one → object Y.
3. P2 opens `.lock` → Y, locks Y (succeeds; different object), re-opens by name
   → Y, comparison passes.

Both processes pass their own check and mutual exclusion is still lost. The
revalidation closes only the window between *one* process's own open and its own
lock — a window F-b already makes uninteresting. It buys a detector for a case
that was not the hazard, and it would read as a fix. **Rejected.**

#### The decision

**(b), plus a detector, plus an honest residual.**

- **The lock file moves to the store root**, as a second reserved name, pinned
  one hop from the anchor. This halves the swappable surface from two objects to
  one and puts the remaining object as close to the anchor as Windows permits.
  Revision 1 rejected this on cosmetic grounds — *"adds a second reserved name
  to the store's visible namespace"* — and that objection is withdrawn:
  `Visible`'s reserved-name check (`ignore.go:378`) is a single
  `if rel == "." && name == AnnotationsDir` line, a second name is a one-line
  change, and the entry is invisible to the user by construction. A real
  security property was traded for a cosmetic one.
- **The lock file is claimed create-only** (`FILE_CREATE`, falling back to
  `FILE_OPEN` on collision) and its `FILE_ID_INFO` is recorded at pin time. A
  **process-level last-seen identity**, the same mechanism §4.1 adds for the
  root, turns a swap *between operations in this process* into a loud error.
  This is a **detector, not a control** — it is labelled as such here so no
  later reader promotes it — and it does not close the cross-process case above.
- **Residual, stated exactly:** if the root-level lock file is deleted and
  recreated between two *processes'* `openAnnotationFS` calls, the two pin
  different objects and mutual exclusion is lost with no error on either side.
  **[UNMEASURED]** — no run-9 measurement covers delete-and-recreate of a locked
  object; `M7.namefollows` and `R13.rename` cover rename only.
- **This does not reach Linux parity and the ADR does not claim it does.**
  Windows cannot lock a directory handle at all (`M14.dir_readhandle`,
  `M14.dir_writehandle` — `ERROR_INVALID_PARAMETER` on both `GENERIC_READ` and
  `GENERIC_READ|GENERIC_WRITE`; `M14.file_control` succeeds on a regular file),
  so the rendezvous must live on a **child** of the anchor rather than on the
  anchor itself. That is a structural difference between the platforms, not a
  choice this design made.
- **Measured before beta, with an owner.** **P3.12** adds the racing
  `Delete`-vs-`SaveNotes` test *and* a delete-and-recreate swap test that fails
  loudly if the rendezvous splits; **P6.1** runs it under the stress campaign.
  If the swap proves reachable in practice, the pre-specified retreat is a
  **named kernel object** rendezvous — a mutex or semaphore in the `Local\`
  namespace named from the pinned root's `FILE_ID_INFO`, which lives outside the
  filesystem namespace entirely and so cannot be swapped by a filesystem
  attacker at all, is released on process death (`WAIT_ABANDONED`) the way
  `flock` is, and would need a reader/writer protocol built over it. That is a
  larger change with its own unmeasured surface; it is named here so the option
  is on the record rather than discovered late. **[UNMEASURED]** in every
  respect.

Concretely:

- Path: `<root>\.scratchpad-lock`, created with `openLockFileAt` relative to the
  pinned **root** handle, `FILE_SHARE_READ|WRITE|DELETE`, and added to
  `Visible`'s reserved-name check alongside `.annotations` (which, per §7.4,
  goes through the case-insensitive `nameEquals` pair on Windows).
- **Ordering:** `openAnnotationFS` pins the root, then the lock file, then
  `.annotations`, before any lock is taken. `lockRendezvous` locks byte range
  `[0,1)` of that pinned handle — shared for normal work, exclusive for
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

**What the design *does* deliver, stated narrowly:** the rendezvous survives
renaming of the lock file, of `.annotations`, or of any ancestor
(`M7.namefollows`, `R13.rename`), and it holds unconditionally *within* an
operation and across operations that overlap in one process. The ordering rule
(root → lock → `.annotations`, all pinned before any lock is taken) closes RR6
between `Delete` and `SaveNotes` in the normal case, which is the case that
matters. The gap is the swap, not the ordering. Recorded as RW5 with the
residual above.

Everything else in this section is unchanged and was not challenged:
`LOCKFILE_FAIL_IMMEDIATELY` plus a bounded retry is the right answer to
`M14.mandatory` (a *hung* holder must not be waited on; a *crashed* one is
released by the kernel on close), and mandatory-ness is harmless here because
the locked range belongs to a file nobody ever reads.

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
   *logical* store-relative path rather than the directory string. This fixed a
   latent Linux defect: `os.Lstat("/proc/self/fd/N")` reports `ModeSymlink`
   (procfs fd entries *are* symlinks), so **every** artifact returned by
   `ResolvePath` and `Publish` had `IsLink == true`.

   **Status: the move has already landed** — commit `6f8b5c3`, *"fix(store):
   stop annotate() from misreading fdPath as a symlink"*, with
   `TestIsLinkFalseForPlainArtifact` and
   `TestIsLinkTruePositiveThroughResolvePath` as regression coverage. Revision 1
   described it in the future tense; only the Windows half remains.

   **The Windows half is not done by that move.** `annotate` still calls
   `os.Lstat(a.Dir)` and `filepath.EvalSymlinks(a.Dir)` on a real path. Revision
   1 said annotate takes "the *logical* store-relative path", but a
   store-relative path cannot be `Lstat`ed, and revision 1 never said where
   `IsLink` then comes from. It comes from **`statAt(parentFD, name)`**: the
   caller already holds the parent handle in every one of the four call sites,
   so `annotate` takes `(parentFD, name, rel)` and classifies from the handle.
   Owner: **P3.6**.
3. `annotate`'s `Linked` detection changes from
   `filepath.EvalSymlinks(a.Dir) != a.Dir` to `WatchLinkFor(rel) != ""`. On
   Windows `EvalSymlinks` does **not** resolve junctions and returns
   `ERROR_PATH_NOT_FOUND` when one is an intermediate component (`M5.junction`),
   so the old test would report `Linked == false` for every junction-watched
   tree — and the card would then offer **Delete** where it must offer
   **Unwatch**, which is threat model §3.11 and the front half of RR1's chain.

   **Honest about what this sub-item is and is not.** §6.8 presents the whole
   item as making the read path *"strictly more anchored"*. This sub-item is
   not: `WatchLinkFor` (`store.go:639-653`) is an *n*-`os.Lstat` path walk, so
   the change swaps one path-based test for another that is **correct** where
   the old one was **wrong**. It is a correctness fix, not an anchoring fix, and
   the residual path-walk is listed in §6.9. Making it handle-anchored is
   possible — walk from the pinned root with `statAt` per segment — and is
   folded into P3.6 rather than left implicit.
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

### 6.9 The surviving path re-resolutions — the complete list

This section exists so P3.13's trace review can find zero *unlisted* ones.
**Revision 1 listed three and its own text introduced more**, which made that
criterion unmeetable. The list is now exhaustive; anything P3.13 finds that is
not here is a defect in this document, not in the trace.

| # | Site | What it is now | Disposition |
|---|---|---|---|
| 1 | `openBrowsableDir`'s crossing of the watch boundary (§4.3) | **Was** an absolute reopen of the reparse target — the `A11.ancestor_swapped` hole. **Is** a handle-by-handle walk from the volume root; the string is consumed one component at a time under the strict primitive | **Closed**, not merely enumerated. Read path only. RW22 |
| 2 | `visibleSegments`' `os.Stat` per segment (`store.go:140`) | Advisory by its own comment; the authoritative walk is handle-relative | Accepted. On Windows `os.Stat` follows reparse points, so a junction reports `IsDir()==true`; the only consequence is which ignore rules are evaluated. Low |
| 3 | `pageCard`'s `os.Stat(filepath.Join(a.Dir, f))` for preview weight | Reads a size and an mtime; feeds `maxPreviewBytes`, a DoS guard | Accepted. Not a containment control |
| 4 | `statLinkTarget` | Revision 1 described it two ways; §3.2 resolves it as handle-relative and no-follow | **Not a re-resolution.** Listed because revision 1's §3.2 wording made it one |
| 5 | `annotate`'s `os.Lstat(a.Dir)` and `filepath.EvalSymlinks(a.Dir)` (`store.go:289-298`) | Path-based classification of an artifact directory | **Must be replaced** by `statAt(parentFD, name)` — §6.8 item 2. Owner P3.6. Until then, `IsLink`/`Linked` are decided from a path on Windows, where a junction defeats both |
| 6 | `WatchLinkFor(rel)`'s *n*-`os.Lstat` walk (`store.go:639-653`) | Decides whether a card offers **Unwatch** or **Delete** | Accepted for correctness, listed for honesty (§6.8 item 3). Handle-anchoring it is folded into P3.6. The failure mode is the wrong button, i.e. threat model §3.11 |
| 7 | `Watches()`'s `os.ReadDir(dir)`, `os.Readlink(sub)`, `os.Stat(sub)` and `hasHTML(sub)` (`store.go:663-694`) | The entire watch enumeration is a path walk | **Not mentioned anywhere in revision 1.** Must become `readDirFD` + `statAt` + `readlinkAt` from the pinned root, with the same depth bound and identity-keyed visited set as `List` (R16). Owner **P3.6** |
| 8 | `entryIsDir`'s first line (`store.go:270-278`) | `if e.IsDir() { return true }` before any link test | **A live trap, and Pre-1 names only the second one.** `RR1.unknown_tag_isdir` (**NO** verdict, used correctly as a negative result) shows a non-surrogate tag makes `DirEntry.IsDir()` **true**, so this line returns before `IsLinkEntry`'s over-approximation can save it. Pre-1 must fix the first line as well as the follow-through `os.Stat` |

Rows 5–8 are the ones revision 1 omitted. Rows 1 and 4 are resolved rather than
accepted. Rows 2, 3, 6 are accepted with reasons. Row 7 is the largest remaining
piece of work and is the one most likely to be under-scoped, for the same reason
§6.8 item 4 is.

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

### 6.11 Reconcile error triage — R16's third clause (new in revision 2)

R16 has three clauses: a depth bound, an identity-keyed visited set, and *"a
`reconcile` failure is triaged into 'retry' versus 'fatal' rather than being
uniformly fatal"*. §6.8 delivers the first two. Revision 1 marked R16 **"yes"**
and delivered no triage, gave it no owner, and did not list it in §11 — §11's
P4.1/P4.2 row covered only the `M15.overflow` mapping (RW12).

**On Windows that clause is load-bearing in a way it is not on Linux, and it
needs no attacker.** Traced end to end in the current tree:

- `cmd/scratchpad-web/main.go:49` — `log.Fatalf("watcher stopped: %v", err)`.
- `internal/watch/watch.go:79` — `newWatcher` calls `reconcile()` at **startup**
  and returns its error, which `main.go:29` turns into
  `log.Fatalf("start watcher: %v", err)`.
- `desiredDirs`' `walk` (`watch.go:240-246`) returns a hard error from
  `os.Open(dir)` for anything that is not `os.IsNotExist`.
- A non-Microsoft or unserviced reparse-tagged directory returns
  `STATUS_IO_REPARSE_TAG_NOT_HANDLED` → Win32 **1920**, which is not
  `os.IsNotExist`.

So a single such directory inside the store or a watched tree kills the web
process **and prevents it from starting again**. That is a boot loop, not the
"the supervisor restarts it" framing RR9 was rated Medium on. And the objects
that produce it are ordinary: an `APPEXECLINK` (observed live in `WindowsApps`,
`M2.appexeclink`), a OneDrive placeholder (`M2.cloud`, **NOT MEASURED** —
Windows 11 puts Documents and Desktop there by default), or a ProjFS entry in a
watched repository.

**Required behaviour, owned by P4.2:**

1. **An entry that cannot be read or classified is skipped and logged once,
   never fatal.** The skip set is per-entry and explicit: `errReparse`
   (including 1920), `errSharing`, `fs.ErrPermission`, and the
   `ERROR_CLOUD_FILE_*` family. A skipped directory is simply not watched; the
   250 ms debounce and the periodic reconcile already tolerate a missing watch,
   and the user sees a degraded refresh for that subtree rather than a dead
   server.
2. **Fatal is reserved for "the watch subsystem itself is broken"** — the
   fsnotify event or error channel closing, or a backend `Add`/`Remove` failing
   for a reason unrelated to one entry. `M15.overflow`'s Windows equivalent
   routes to reconcile-and-continue (RW12), never fatal.
3. **The startup call gets the same triage as the steady-state one.** A
   condition that merely degrades a running server must not prevent it starting.
4. **`desiredDirs` must stop using `os.Open`.** `P13.go_share_mode` measured
   that Go's `syscall.Open` omits `FILE_SHARE_DELETE`, so today the watcher's
   own handles can veto the user's deletes and our own replaces. It must open
   through the store's full-share-mode primitive (R15). Owner **P4.1**.

Recorded as RW23. Until it lands, §4.9's R16 row is **partial**.

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

Rejected in both directions. **Revision 1 quoted the pre-P1.3 guess here — "10
attempts, exponential backoff 1/2/4/…/64 ms, ~250 ms total" — and anyone sizing
an HTTP timeout off it would have been off by roughly 3×.** The bound was
measured and widened in `47248c3`; the numbers below are `DefaultReplacePolicy`
and `P13.bound` as they stand in run 9.

**The bound: 10 attempts, backoff 2 ms doubling to a 256 ms ceiling, and a 2 s
wall-clock hard budget.** The sleeps before attempts 2–10 are
2, 4, 8, 16, 32, 64, 128, 256, 256 ms — **766 ms of retrying in the worst
case**; the 2 s budget is the ceiling for the case where individual attempts
themselves *block* rather than failing immediately (a share-mode veto fails
immediately; a filter driver need not). Measured end to end: `P13.bound` held a
destination without `FILE_SHARE_DELETE` for the whole bound and recorded **10
attempts over 771 ms** terminating in a `*ReplaceError`, and
`P13.bound_terminates` confirms that is "well inside a request timeout".
`P13.bound_preserves_dest` (REQUIRED) confirms the destination still held its
complete previous content afterwards.

**The retryable set** (`RetryableStatuses`): `STATUS_SHARING_VIOLATION`,
`STATUS_DELETE_PENDING`, `STATUS_LOCK_NOT_GRANTED`,
`STATUS_FILE_LOCK_CONFLICT`, `STATUS_USER_MAPPED_FILE`,
`STATUS_DIRECTORY_NOT_EMPTY`, plus a parallel Win32-errno set
(`ERROR_SHARING_VIOLATION` 32, `ERROR_LOCK_VIOLATION` 33, `ERROR_DIR_NOT_EMPTY`
145, `ERROR_USER_MAPPED_FILE` 1224) for paths that go through a Win32 wrapper
and so never produce an NTSTATUS.

**Deliberately *not* retryable, each for a stated reason:**
`STATUS_ACCESS_DENIED` — at the NT layer this is an ACL denial, because the
delete-pending case that Win32 collapses into `ERROR_ACCESS_DENIED` keeps its
own status here (`M13.pending_status`), so retrying it would only add latency to
a permanent failure; **`STATUS_REPARSE_POINT_ENCOUNTERED` — a link appeared
where a real entry is required, which is an attack signal (A2/RR1), and retrying
it would loop against the attacker**; `STATUS_OBJECT_NAME_COLLISION`,
`STATUS_OBJECT_PATH_NOT_FOUND`, `STATUS_NOT_SAME_DEVICE`, `STATUS_DISK_FULL`,
`STATUS_MEDIA_WRITE_PROTECTED` — permanent by construction.

**What is measured and what is not, kept apart.** The *bound's behaviour* is
measured: `P13.retry.hold20ms` and `P13.retry.hold400ms` held a blocking opener
for 20 ms and 400 ms and the replace succeeded after 5 attempts/31 ms and 9
attempts/514 ms respectively, with `P13.retry_integrity.hold20ms` / `.hold400ms`
(both REQUIRED) confirming the destination held one *complete* version whatever
the outcome. The *distribution the bound is sized against* is not: `M13.av` /
`MATRIX.EXCLUDED.antivirus_distribution` is NOT MEASURED, because Defender's
realtime state on a CI image is not representative. So the **sizing** remains a
documented judgment call and must be labelled as such in code — sized for the
tail rather than the mean, because a replace that is going to succeed succeeds
on the **first** attempt once the interfering handle closes (`M13.retry`), and
capped at the point where an actionable error ("close the program holding the
file") is kinder than more waiting.

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
| **RW1** | Recursive delete through a reparse point (RR1) | Critical | **Mitigated** — §4.5's operation-as-classification (revision 1 specified the weaker classify-then-open here). Measured: `A6.delete.*` at depth 0 and 2 in three flavours, `A6.swap_midwalk`, `P14.delete_descend`, `P14.unlink_junction`, `P14.delete_attr_trap`, `RR1.removeall` — with `A6.negative_control` proving the assertion has teeth. **Release gate:** `MATRIX.Delete.target_replaced` (YES in run 9) must pass before any Windows binary ships. **Caveat (F12):** `main` has no branch protection rule, so "required check" is a no-op at the repository level — the gate is a note until an operator adds one. Owner: operator, tracked in EXECUTION.md. |
| **RW2** | Unbounded reparse traversal on browse (RR2) | Critical | **Mitigated** — the strict open at every component (`A5.strict_walk`, `A3.nested_strict.*`) + the single-boundary allowlist. Revision 1 credited `OBJ_DONT_REPARSE`; see §2.1. |
| **RW3** | The surviving path re-resolutions (RR3) | High | **Enumerated and partly closed** — §6.9's table, now complete. Rows 1 and 4 are closed; rows 2, 3, 6 accepted with reasons; rows 5, 7, 8 are work owned by P3.6 and Pre-1. P3.13 must find zero *unlisted* ones. |
| **RW4** | Case-folding bypass of the `.annotations` guard and credential ignores (RR5) | High | **Mitigated** — §7.4, both halves: `nameEquals` (the `.annotations` guard) and `matchName` (`defaultIgnores`, including the credential-ignore lines). Confirmed live by `M11`. **Interim correction:** this row read "Mitigated" from the point `nameEquals` shipped, but that was false until `matchName` landed in `e4b84a2` — only the `.annotations` guard half was case-folded before that commit, so `defaultIgnores`' credential entries (`.env`, `.netrc`, `*.pem`, `.ssh/`) stayed case-sensitive and bypassable on Windows for the intervening period. Both halves are in place as of `e4b84a2`, so "Mitigated" is accurate again now. |
| **RW5** | No directory lock; `Delete` racing `SaveNotes` (RR6) | Medium-High | **Mitigated in the normal case; residual measured before beta.** §6.7's root-level pinned lock file. **Revision 1's parity claim — "Linux has the same shape of gap" — is withdrawn as false:** Linux flocks the store-root *inode*, so losing the rendezvous needs write access to the root's parent; this design's rendezvous is a *child* of the anchor, because Windows cannot lock a directory handle at all (`M14.dir_readhandle`, `M14.dir_writehandle`). **Residual:** the lock file deleted-and-recreated between two *processes'* opens splits mutual exclusion silently — enabled by R15's own mandatory `FILE_SHARE_DELETE`, and **[UNMEASURED]** (the cited `M7.namefollows` / `R13.rename` measure *rename* survival, not delete-and-recreate). Owners: **P3.12** (racing test + swap test), **P6.1** (stress). Pre-specified retreat if reachable: a named kernel object rendezvous (§6.7). |
| **RW6** | Junctions accepted at the watch boundary and indistinguishable from an attacker's | Medium | **Accepted** — §6.6. Parity with the existing Linux treatment of store-root symlinks. If P1.7/P1.8 rejects this, `watch` becomes Developer-Mode-only; that is a product decision for the human gate, not a security one. |
| **RW7** | The two-step link-creation window | Medium | **Deferred, owner P3.1** — the `FILE_DELETE_ON_CLOSE` interlock is **[UNMEASURED]** and shipping it unmeasured is acceptable because rule 3 is unconditional and carries the case alone. **But rule 3 is not true yet, on three counts** (§6.6): the web UI has no delete button on a plain folder card (`list.tmpl:43`), `CreateJunctionAt` has no cleanup at all while junctions are the only unprivileged flavour, and neither flavour cleans up when the post-claim *reopen* fails. Owners: **P4.3** (cleanup + `DELETE` access on the reopen), **P4.6** (the card affordance), **P3.1** (measure the interlock). Residue characterised: `A7.two_step_residue`, `A7.two_step_recovery`, `A7.two_step_window`. |
| **RW8** | Hard links inside a watched source are served (RR4) | Medium | **Accepted** — no structural defence exists; identical on Linux today. Windows makes it cheaper (no privilege) but not newly possible. README and release notes: *watch only trees you trust*. |
| **RW9** | ReFS / Dev Drive unmeasured | Medium | **Deferred, owner P6.6** — `M1.refs_smb`. One manual check on a real Dev Drive before the beta. Store root warns; watch targets unrestricted. |
| **RW10** | Antivirus transient-error distribution unmeasured | Medium | **Deferred, owner P6.1** — `M13.av`. The retry bound in §8.4 is a documented choice, not a measurement, and is labelled as such in code. |
| **RW11** | ADS aliasing via `:` in lookup segments (RR8) | Medium | **Mitigated** — §7.5. Measured reachable (`M12.C_stream`). |
| **RW12** | `ReadDirectoryChangesW` overflow unmeasured | Medium | **Deferred, owner P4.2** — `M15.overflow`. Map the Windows overflow error explicitly; route to reconcile-and-continue, never fatal. |
| **RW13** | Cloud placeholder tags: mass rehydration, `ERROR_CLOUD_FILE_*` (RR10) | **Medium** (raised from Low-Medium) | **Deferred, owners P4.6 and P4.2** — `M2.cloud` NOT MEASURED. Follow-up: skip `FILE_ATTRIBUTE_RECALL_ON_*` in the size walk. Revision 1 worried only about rehydration cost; the same objects also reach the fatal reconcile path (RW23), and Windows 11 puts Documents and Desktop in OneDrive by default. Documented exclusion + manual pre-beta check. |
| **RW14** | Self-watch via a spelling the advisory prefix check misses (RR12) | Low | **Accepted** — §7.1. Bounded by the identity-keyed cycle guard. |
| **RW15** | An unknown-tag entry in the store root is invisible in listings and un-removable through the UI | Low | **Deferred, owner P4.6** — an inert "unsupported entry" tile with a Delete action. New risk, introduced by the refuse-by-default allowlist. |
| **RW16** | A genuinely non-elevated session was not measured | Low | **Deferred, owner P5.5** — GitHub runners are elevated; the privilege-removal child is a faithful simulation of the *privilege* dimension but not of every ACL difference. One manual confirmation of the §6.6 table on an ordinary account. |
| **RW17** | `List` can be redirected by A2 into listing a tree outside the store | Low | **Accepted, argued** — before §6.8 item 4 lands. Bogus cards' content requests go through handle-anchored `ResolvePath` and 404, so there is no disclosure; only sizes and mtimes leak. Closed entirely once `List` is handle-anchored. |
| **RW18** | The `loadArtifact`/`annotate` refactor changes **Linux** behaviour | Medium | **Mitigated by test** — §6.8 item 2. Must land with a Linux regression test asserting `IsLink` is false for a published artifact. |
| **RW19** | `Delete` widened to remove an empty non-artifact directory | Low | **Accepted, with the UI half in scope** — §6.6 rule 3. Cross-platform behaviour change; `rmdirAt` cannot destroy content or follow a link. Needs a Linux regression test **and** the folder-card delete affordance (`list.tmpl` renders one only inside `{{with .Unwatch}}`), without which rule 3's message names an action the user cannot perform. Owner **P4.6** for the UI half. |
| **RW20** | `Delete` has no partial-failure story; `removeTreeAt` aborts mid-tree | Low | **Accepted** — pre-existing on Linux (`annotationfs_linux.go:183-186`); `M13.delete_blocked` is the error that will trigger it on Windows more often. |
| **RW21** | A4 — shared or redirected store path | N/A | **Out of scope by declaration.** Documented: the store root must be a directory only the user can write. |
| **RW22** | `A11.ancestor_swapped` — an ancestor of the watch target is a reparse point, so the browse walk lands in an attacker tree | High as a proof defect, Medium as live exposure | **Mitigated in revision 2** — §4.3's handle-by-handle walk from the volume root. Was a measured **NO** in run 9 and unmentioned in revision 1; recorded in `spike-findings.md` §10.1 as *"containment BROKE"*. It is **not a race** (ancestors were never validated, so the condition was persistent), it is reachable by **A1** through a `git checkout` that swaps a directory for a link in a watched repo, its payoff is read disclosure over the unauthenticated endpoint (network-readable under `LAN=1`), and it is bounded to reads because every mutation walk is handle-relative. Residuals: the drive letter's device-map binding (**[UNMEASURED]**, P3.4/P6.1) and the compatibility cost of refusing targets whose path crosses a legitimate mount point (P4.3/P6.6, with a pre-specified retreat). |
| **RW23** | An unreadable entry makes `reconcile` fatal — **at startup as well** | Medium-High | **Deferred, owner P4.2** — §6.11. Not an attack: an `APPEXECLINK` (`M2.appexeclink`, observed live), a OneDrive placeholder (`M2.cloud`, NOT MEASURED) or a ProjFS entry returns Win32 1920, which is not `IsNotExist`, so `desiredDirs` errors, `newWatcher` fails, and `main.go:29` calls `log.Fatalf`. That is a **persistent failure to start**, not the transient restart RR9 was rated on. R16's third clause is the fix and had no owner in revision 1. |
| **RW24** | The watcher's own handles lack `FILE_SHARE_DELETE` | Low-Medium | **Deferred, owner P4.1** — `desiredDirs` uses `os.Open`, and `P13.go_share_mode` measured that Go's `syscall.Open` hard-codes `FILE_SHARE_READ\|FILE_SHARE_WRITE` only. Consequence: the watcher can veto the user's own delete and our own atomic replaces, burning a whole retry bound against ourselves. R15 violation outside the store primitives. |

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
`FILE_CREATE` claim over an unknown tag (§6.6); `DEDUP`/`WOF` behaviour under a
strict open on a deduplicated volume (§5.4).

**Added in revision 2, all with owners:** a `SYMLINK_FLAG_RELATIVE` watch link
(§3.3, P3.4); whether an unprivileged same-user process can rebind an existing
drive letter in its own logon session (§4.3, P3.4/P6.1); the compatibility cost
of refusing a watch target whose path crosses a legitimate junction or volume
mount point (§4.3, P4.3/P6.6); delete-and-recreate of the rendezvous lock object
between two processes (§6.7, P3.12/P6.1); the process-level root-identity cache
discriminating across operations (§4.1, P3.1).

**Removed in revision 2:** annotation write durability was marked
**[UNMEASURED]** in revision 1 and is not — `P13.flush_cost` measured it (§4.8).
Also note `M13.av`'s scope: the retry *bound's behaviour* is measured
(`P13.bound`, `P13.retry.*`); only the antivirus *distribution* it is sized
against is not.

---

## 10. What this ADR overrides

Stated plainly, with the document being overridden named. The spike has
measurement authority; the spec and the threat model were written before it.

### 10.1 Spec, *Reparse points and watch semantics* — **override withdrawn**

Revision 1 overrode this clause:

> "Treat junctions and other reparse points as untrusted unless created and
> identified by the application under an explicitly accepted design."

**The override was unnecessary and is withdrawn.** The unachievability argument
that motivated it is still correct — a store-created junction is byte-for-byte
indistinguishable from an attacker's (same tag, same `\??\<path>` substitute
name, an equally forgeable `PrintName`), and a sidecar registry would be
attacker-writable and consulted as a check-then-use — but the spec answers the
question itself, six lines later:

> The phase-zero spike **may recommend application-created junctions as a
> fallback** if they can preserve all containment and unwatch guarantees without
> elevation. That fallback must be explicit and tested, not silently selected
> for arbitrary reparse points. — spec, *Reparse points and watch semantics*

This design satisfies that clause literally: containment preserved
(`P14.unlink_junction`, `P14.delete_descend`, `A6.*`, Scope A/B/C), unwatch
guarantees preserved (§4.6), no elevation required, and an explicit allowlist
rather than arbitrary tags. **No override is needed. §12 item 2's framing is
corrected accordingly, and P1.8's ask shrinks.**

### 10.1a Spec invariant 5 — the "store-owned" half

> "Browsing may cross exactly one **store-owned** watch link and must reject
> nested links or reparse points beyond it." — spec, invariant 5

**Overridden in its "store-owned" half only**, which is where the unverifiability
argument actually lands. "Store-owned" is not decidable by inspection for exactly
the reason above. Replaced by: *browsing may cross exactly one link in the
store's own namespace whose tag is on Scope A's allowlist, whoever created it;
everything beyond it is refused* (§4.3, §5.1).

The parity argument is what makes this defensible, and it is unchanged from
revision 1: A1 (content in the watched source) cannot plant a link in the
store's namespace at all, and A2 planting one is content creation
indistinguishable from the user running `scratchpad watch` — which the Linux
code already forgives without asking who made it. Everything an attacker gains
from junctions specifically (RR1's recursive delete, RR2's traversal, §3.11's
wrong-button-on-the-card) is closed by tag-aware classification, which the
design needs whether or not the store ever creates one.

**One residual to record, configuration-dependent:** under `LAN=1` a reparse
point in the store namespace is an **exfiltration** primitive, not merely a
local read — it converts a filesystem-write capability into network-readable
content. README and release notes must say so alongside RW8's *"watch only trees
you trust"*. Owner: **P6.4**, **P6.7**.

The "other reparse points" half of the original clause is kept and strengthened
into §5's refuse-by-default allowlist.

### 10.2 Threat model R12

> "The store root is resolved to a fully-qualified local path **once**, at
> process start, and pinned as a handle for the process lifetime."

**Overridden** to *once per operation*, with the handle and the resolved string
carried together in `rootedFS` (§7.3). Reasons: a process-lifetime pin makes
`t.Setenv(store.RootEnv, …)` impossible and would break the entire test suite;
and it adds no property *within* an operation, because a per-operation pin is
already unredirectable there.

**The consequence for R13 that revision 1 did not state.** Revision 1 justified
the override partly on "R13's identity check covers replacement across
operations". Under a per-operation pin it does not: `verifyRoot()` compares
against a value recorded microseconds earlier in the *same* operation, which
F-b already makes harmless, and it cannot see a replacement *between*
operations — the case R13 was written for. §4.1 restores that property with a
process-level last-seen identity keyed on the resolved root **string** (different
string → new entry, so `t.Setenv` keeps working; same string, different identity
→ the loud error). §4.1 also decides `A4.root_removed`, which revision 1 left
open although the finding text says explicitly that "the ADR must decide".

### 10.3 Spec, *Windows rooted filesystem*

`GetFinalPathNameByHandleW` is listed among the candidate containment
primitives. **Overridden:** it returns a string the OS must re-resolve, so any
use of it as an input to a subsequent operation reinstates the TOCTOU the handle
removed, and `M6.resolution` shows it disagrees with `FILE_NAME_INFO` for the
same object when 8.3 aliases exist. It is a **display and diagnostics primitive
only**. Two narrow exceptions, both stated so they are not mistaken for
loopholes:

1. §7.1's *advisory* containment hint, where its output is used solely to
   produce a refusal.
2. §4.3's canonicalisation of a watch target at **`Watch` time**, where its
   output is written into the link as a durable record rather than consumed by a
   following operation, and where every later use of that record is re-validated
   and then walked handle-by-handle. This exists because `filepath.EvalSymlinks`
   — the primitive Linux uses for the same job — does not resolve junctions on
   Windows (`M5.junction`), so there is no other way to store a fully-resolved
   target. Added in revision 2.

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

**Both traps in `entryIsDir`, not one.** Revision 1 named the follow-through
`os.Stat` and missed the line above it: `if e.IsDir() { return true }` returns
before any link test runs, and `RR1.unknown_tag_isdir` (**NO** verdict, used
correctly as a negative result) shows a non-surrogate tag makes
`DirEntry.IsDir()` **true**. `IsLinkEntry`'s over-approximation never gets a
chance to save it. Pre-1 must fix the first line as well.

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
| **P3.1** handle wrapper + error mapping | `win32_windows.go` (§3.6), `winError` + the §3.7 table, `objectID`, `rootedFS` with `path`/`id`/`volume`, `verifyRoot`, **the process-level root-identity cache (§4.1, F9)**, `dupFD`/`closeFD`/`readDirFD`/`statAt`, and **`openStrictAt`/`openRealDirAt`/`openRealFileAt` (§2.1)**. Measure the §6.6 rule-2 interlock here. Pre-1 and Pre-3 first. **Plus F11's prototype checklist:** the three unchecked `uint16` truncations (`ntOpenAt`'s `us.Length`, and `SubstituteNameLength`/`PrintNameLength`/`ReparseDataLength` in the reparse writers) must become clean errors, with `maxReparseSize` enforced on the **write** side where it is currently defined and never checked; `windows.Write` must gain a short-write loop, since the Linux original's `os.File.Write` guarantees full-write-or-error. |
| **P3.2** real-directory traversal | `openRealDir` over **`openRealDirAt`**+`mkdirClaim`; `pathError`. Revision 1 said "`openDirAt`+`mkdirClaim`" — implementing that literally ships the primitive `A5.obj_dont_reparse_inert_for_unknown_tags` falsified. Pre-2 first. |
| **P3.3** create-only file/dir ops | `mkdirClaim`, `writeFileAt`, `mkdirsAt`; the three-way taken-name map (§6.6). |
| **P3.4** browsable traversal | `openBrowsableDir` with the Scope-A allowlist and the `\??\Volume{` refusal (§5.3), over the strict primitive. **Plus, new in revision 2:** the handle-by-handle target walk anchored at the volume root (§4.3, closing `A11.ancestor_swapped`), mirroring `storefs_linux.go`'s `openFilesystemRootNoFollow`/`openAbsoluteDirNoFollow`; `readlinkAt`'s `SYMLINK_FLAG_RELATIVE` refusal and absolute/drive-rooted validation (§3.3); `Watch`-time canonicalisation via `GetFinalPathNameByHandleW` (§4.3, §10.3 exception 2); the fail-closed `DirHasHTML` form, tightened on Linux to match. Measure the drive-letter device-map question. |
| **P3.5** document open and deletion | `openRealFileAt` (one operation, no fstat window), `openPathFile`, `unlinkAt`, `rmdirAt`; R11's lookup validation (§7.5). **`os.Open` must not appear on this path** (`P13.go_share_mode`, §3.2). |
| **P3.6** pruning and directory reads | `pruneAt`; **and §6.8 items 1–4** — `loadArtifactAt`, `annotate` re-based on `statAt(parentFD, name)` and `WatchLinkFor` (the *move* out of `loadArtifact` already landed in `6f8b5c3`), handle-anchored `List` with a depth bound and identity-keyed visited set. **Plus §6.9 rows 5, 6 and 7:** `Watches()`'s whole path walk (`os.ReadDir`/`os.Readlink`/`os.Stat`/`hasHTML`) becomes handle-anchored, and `WatchLinkFor`'s *n*-`Lstat` walk with it. Both platforms. This is the largest single item and the one most likely to be under-scoped. |
| **P3.7** annotation root | `openAnnotationFS` pinning root → **`<root>\.scratchpad-lock`** → `.annotations`, in that order (§6.7), with the lock file create-only, its identity recorded, and the second reserved name added to `Visible` (through `nameEquals`, §7.4). |
| **P3.8** read and atomic write | `NtSetInformationFile(FileRenameInformationEx→FileRenameInformation)` with the **allowlisted** fallback trigger only (`A9.rename_failure_statuses`, §4.8 — not the stdlib's blanket retry), §8.4's bounded retry with the measured numbers, temp cleanup through the held handle. |
| **P3.9** safe recursive removal | `removeTreeAt` per §4.5's **operation-as-classification** shape; `statAt` for reporting only. Add the **depth bound** R16 names and that neither platform has today (F11). Ancestor pruning re-anchored per level. |
| **P3.10** annotation walk/report | `walk` over `readDirFD`+`statAt`, matching Linux ordering and malformed-file handling. |
| **P3.11** port behaviour tests | Close P2.7 F3 first: fail the job if any `--- PASS` coexists with a `not implemented yet` sentinel, or the native job goes green testing nothing. Re-express `SymlinkCapable` as `WatchLinkCapable` (§8.5). **Own §11.1's migration inventory with P3.12.** |
| **P3.12** deterministic attack tests | The six new hooks in shared untagged code (R17): `root-open`, `browse-segment`, `doc-open`, `notes-replace`, `notes-remove`, `watch-reconcile`. Plus the racing `Delete`-vs-`SaveNotes` test **and the lock-object delete-and-recreate swap test** RW5 requires, and the junction/volume-mount/unknown-tag `Watch` variants (`Watches ⊆ Unwatch`-able). **Own §11.1's migration inventory with P3.11.** |
| **P3.13** security invariants review | Trace every mutation; the only acceptable path re-resolutions are the rows in §6.9's table. Anything found that is not in that table is a defect in this ADR and must be reported as one. |
| **P3.14** implementation red team | Native call flags, handle lifetimes, `unsafe` confined to `win32_windows.go`, `FILE_SHARE_DELETE` on every open, the recursive delete. **Verify the strict open is the primitive everywhere a walk descends** — the single highest-value check, because F1 is what a mechanical port gets wrong. |
| **P4.1** watcher identity | `dirIdentity` field layout moves to the platform files (§3.5); split `desiredDirs`' `dirs` map into fsnotify bookkeeping (path-keyed, forced by the backend API) plus an identity-keyed cycle guard. **Replace `desiredDirs`' `os.Open` with a full-share-mode open** (RW24, `P13.go_share_mode`). |
| **P4.2** watcher reconciliation | Depth bound; map the `M15.overflow` error to reconcile-and-continue. **Plus R16's third clause in full — §6.11's error triage, including the startup path** (RW23). This is the item revision 1 left unowned. Also: keep the 250 ms debounce, which `P13.watch_sees_removed_on_replace` shows is doing real work on Windows. |
| **P4.3/P4.4** watch links, degraded mode | §6.6's flavour probe and recovery rules; `errNoLinkPrivilege`'s message. **Cleanup on *both* the FSCTL and the reopen failure path, in *both* flavours, with `DELETE` access on the post-claim reopen** (F8). Measure the §4.3 mount-point compatibility cost on real machines. |
| **P4.6** web layer | §6.8 item 5 — the four `/proc/self/fd` sites, by passing the handle. RW13's `RECALL_ON_*` skip. RW15's unsupported-entry tile. **The folder-card delete affordance RW19 and §6.6 rule 3 depend on** (`list.tmpl` renders one only inside `{{with .Unwatch}}`). |
| **P6.1** stress and race | The RW5 rendezvous-swap campaign; the drive-letter device-map question (§4.3). |
| **P6.4/P6.7** docs and release notes | The `LAN=1` exfiltration residual (§10.1a) alongside RW8's "watch only trees you trust". |
| **P6.6** release matrix | RW9's manual Dev Drive check; RW16's manual non-elevated check; the §4.3 mount-point compatibility verdict. |

### 11.1 Migrating the spike's assertions before `internal/winspike` dies

`internal/winspike` is deleted at the end of Phase 1. The `A*` and `P13*`
properties exist **only** there, and several of them are the only evidence that
the corrected design differs from the naive one. Revision 1 named six hooks and
one matrix cell and covered none of the rest.

**Rule: a property is migrated only when its negative control is migrated with
it.** The controls are the expensive part and the easy part to lose — a test
that cannot fail against a broken implementation is not a test, which is exactly
what `A6.negative_control`, `P13.audit_control` and the continuous-existence
control exist to prove.

**This table is the inventory, and it must be green before `internal/winspike`
is removed.** Owners: **P3.11** and **P3.12**; **P3.13** verifies.

| Spike property | Why it must survive | Inherits into |
|---|---|---|
| `A5.strict_open`, `A5.unknown_tag_refused`, `A5.strict_walk`, `A5.strict_open_admits_real_dirs` | F1's proof: the containment primitive refuses by tag, not by driver availability | `internal/store` traversal tests — **P3.2**, **P3.4** |
| `A5.obj_dont_reparse_inert_for_unknown_tags` | The *reason* for the strict open. A regression here silently reinstates the falsified design | **P3.2** (as a documented negative measurement, with the comparison both ways) |
| `A6.swap_midwalk` **+ `A6.negative_control`** | F3's proof, on the RW1 critical path. The control demonstrates the mechanical port destroys the external tree | **P3.9** — the control ships as an unexported deliberately-wrong function next to the right one, as in the prototype |
| `A6.delete.{junction,symlink,unknowntag}.depth{0,2}`, `A6.parent_replaced`, `A6.unlink_watch.*` | RW1's coverage matrix | **P3.9**, **P3.12** |
| `A3.nested_strict.*` | Invariant 5 beyond the crossing, independent of filter drivers | **P3.4** |
| `A11.target_swapped` **and `A11.ancestor_swapped`** | The ancestor case must go from **NO** to a passing assertion, or §4.3's fix is unverified. Cross-platform: the Linux twin lands with `openAbsoluteDirNoFollow` | **P3.4**, plus a Linux regression test |
| `A10.rename_race`, `A10.file_link_refused`, `A10.dir_link_refused` | Document serving under substitution | **P3.5** |
| `A12.concurrent_writers`, `A12.concurrent_temp_residue` | No torn reads, no temp residue under concurrency | **P3.8** |
| `P13.continuous_existence` **+ its remove-then-rename control** | The black-box discriminator that replaced the withdrawn `ReadDirectoryChangesW` hypothesis | **P3.8** |
| `P13.no_dest_removal` / `P13.audit` **+ `P13.audit_control`** | The white-box guarantee that the replace never degrades into unlink+rename | **P3.8** |
| `P13.sharing_never_truncates`, `P13.bound_preserves_dest`, `P13.retry_integrity.*` | R9's "complete old or complete new, never partial" | **P3.8** |
| `P13.go_share_mode` | Why documents and watcher handles must not come from `os.Open` | **P3.5**, **P4.1** |
| `A1.ancestor_replaced.*`, `A2.dest_replaced*`, `A4.root_replaced.*`, `A4.root_reparse_refused.*`, `A7.two_step_*`, `A8.concurrent_claim` | The hook-driven race suite | **P3.12** |
| `MATRIX.Delete.target_replaced` | RW1's release gate | **P3.12**, gated per RW1 |

---

## 12. The P1.8 question: does a race-resistant strategy exist?

**Yes, and the evidence is stronger than a design argument.**

Every mechanism `storefs_linux.go` depends on has a *measured* Windows twin, not
a documented one: seven syscall equivalents (§2), **91 REQUIRED security
properties holding on both amd64 and arm64 across 369 measurement lines in run
32908643117**, with zero `SECURITY-FAIL` verdicts in that run. The two questions
that could have killed the project both resolved in favour of a workable design
— reparse points can be refused at **every** component with no check-then-use
window (`A5.strict_walk`, `A3.nested_strict.*`; and `M1.intermediate` for the
Microsoft-tag short circuit), and handle-relative atomic rename works through
`NtSetInformationFile` (`M9`, `P13.replace_existing`). The two that resolved
*against* the obvious approach — no directory lock (`M14`), `os.Root` follows
in-root symlinks (`M17.follows_inroot_symlink`) — each have a replacement, and
§6.7 is now honest that the lock replacement does not reach full Linux parity.

**And two runs failed on the way, which is the point of running them.** Run 7's
`SECURITY-FAIL` falsified R3 as originally stated and produced the strict
primitives; run 8's falsified the spike's own change-record hypothesis and
produced two better guards. A measurement phase in which nothing is contradicted
is a measurement phase that was not measuring.

Four honest qualifications the gate should weigh:

1. **The design is not "port the Linux backend".** It requires refactoring
   `loadArtifact`, `annotate` and `List` on **both** platforms (§6.8) to remove
   `fdPath`. Approving this ADR approves that scope. Emulating `fdPath` with
   `GetFinalPathNameByHandleW` is the tempting alternative and it is exactly the
   TOCTOU the design exists to remove.
2. **Junction acceptance is a real widening**, defended on parity grounds. It
   needs **no spec override** — the spec authorises an application-created
   junction fallback that is "explicit and tested, not silently selected for
   arbitrary reparse points", and this design is exactly that (§10.1). What does
   need overriding is invariant 5's *"store-owned"*, which is unverifiable by
   inspection (§10.1a). Revision 1 asked P1.8 to approve the larger override;
   the ask is smaller and more accurate now. Rejecting junctions would cost no
   security work — the tag-aware classification is required either way — but
   would remove `watch` from every machine with Developer Mode off, which is a
   product decision for the human gate. One residual to weigh alongside it: under
   `LAN=1`, a reparse point in the store namespace is an exfiltration primitive,
   not merely a local read.
3. **Three unmeasured dependencies are carried into the beta, not into Phase 3**:
   ReFS/Dev Drive (RW9), the AV transient distribution (RW10), and a genuinely
   non-elevated session (RW16). None blocks implementation; all three block
   claiming the beta is validated. Revision 2 adds five smaller ones, each with
   an owner and none of them blocking Phase 3 (§9).
4. **Two containment questions are closed by *changes* rather than by
   measurements that already held**, and both change Linux too. `A11.ancestor_swapped`
   was a measured **NO** — the browse walk could land in an attacker tree via an
   ancestor reparse point — and §4.3's handle-by-handle target walk is the fix,
   already shipped on the Linux side. `A5.obj_dont_reparse_inert_for_unknown_tags`
   falsified the traversal primitive, and §2.1's strict open is the fix, already
   written in the prototype. Neither is speculative, but neither is "the
   measurement held" either, and the gate should read them as design commitments
   it is approving rather than as facts it is accepting.

Recommendation: **proceed**, with RW1's "Delete / target replaced" test as a hard
release gate (and an operator adding the branch protection that makes a gate a
gate), and with the three preconditions in §11 landing before P3.1.
