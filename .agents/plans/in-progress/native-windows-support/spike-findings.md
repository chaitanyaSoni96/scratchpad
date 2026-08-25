---
title: Win32 Spike Findings (P1.2 / P1.4)
type: measurement-record
status: draft
created: 2026-08-26
links: [./threat-model-windows.md, ./win32-primitive-survey.md, ./native-windows-support.md, ../../../spec/native-windows-support.md]
---

# Win32 Spike Findings

Measured facts from the P1.2 rooted-traversal prototype and the P1.4 link
probes, run on real Windows. Every claim below carries the run it came from; a
claim without a run URL is not a finding and does not appear here.

The instrument is `internal/winspike`, a temporary Windows-only package
(`//go:build windows` on every file, empty on Linux, no import of
`internal/store`), plus `.github/workflows/winspike.yml`. **Both are deleted at
the end of Phase 1.** The workflow prints one line per measurement in the form
`WINSPIKE|<id>|<verdict>|<detail>` and lifts them into the job summary, so the
CI log is the evidence.

## Runs

| # | Run | Commit | What it added |
|---|---|---|---|
| 1 | [32901703383](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32901703383) | `ca87348` | the harness: prototype, link probes, M1–M18 |
| 2 | [32902343901](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32902343901) | `11805a1` | privilege-dropping child; run-1 gaps |
| 3 | [32902629190](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32902629190) | `8f9a508` | Developer Mode dependency; unknown-tag vector |
| 4 | [32902862617](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32902862617) | `295fc5b` | unknown tag applied to an empty directory |
| 5 | [32903450305](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32903450305) | `6474bb8` | confirmation run for this document |

Each run executes on **`windows-2025`** (amd64) and, as a secondary data point,
**`windows-11-arm`** (arm64). Every design-deciding answer below was identical
on both.

Run 5 is the reference state of this document: **192 measurements** on
`windows-2025` (107 YES, 28 NO, 49 INFO, 4 NOT-MEASURED, 2 PARTIAL) and **17
REQUIRED security properties holding with zero `SECURITY-FAIL` verdicts**. A
`SECURITY-FAIL` line means the runner contradicted a property the Windows
backend must have; it fails the job, and none appeared.

The seventeen required properties that hold: `P12.mkdir_excl`,
`P12.junction_traverse`, `P12.junction_intermediate`, `P12.symlink_traverse`,
`P12.browsable_nested`, `P14.junction_not_dir`, `P14.delete_descend`,
`P14.unlink_junction`, `M1.intermediate`, `M4.traverse`, `M7.redirect`,
`M8.createsymlink_excl`, `M9`, `M10.posix_nt`, `M16`, `R13.replace`,
`RR1.removeall`.

## The instrument's own limits — read this before quoting anything

1. **The GitHub runner executes ELEVATED, as Administrator, with Developer Mode
   enabled** (`AllowDevelopmentWithoutDevLicense = 1`, run 1 "Runner facts";
   `P14.token`, run 2). Run 1's "unprivileged" answers were therefore worthless.
   Every privilege-sensitive answer here comes from a **child process that
   removed `SeCreateSymbolicLinkPrivilege` from its own token**
   (`SE_PRIVILEGE_REMOVED`, chosen because `CreateSymbolicLinkW` enables the
   privilege on demand and merely *disabling* it would prove nothing), and where
   Developer Mode matters, from a child that also cleared the registry value and
   restored it afterwards (`P14.devmode.restored`, run 3).
2. **One filesystem was measured: NTFS.** ReFS (Dev Drive), SMB/UNC, FAT32 and
   cloud-backed folders were not. `M1.refs_smb`, `M2.cloud`.
3. **No antivirus pressure.** `M13.av` — the transient-error distribution the
   retry bound should be sized against is not measurable on a CI runner.
4. Runner builds: amd64 `10.0.26100` (Server 2025), arm64 `10.0.26200`
   (Windows 11). Both NTFS, both with **8.3 alias generation ENABLED**, both
   with a second NTFS volume at `D:`.

---

## 1. Recommended backend strategy

**Hand-roll the rooted filesystem on `NtCreateFile` +
`OBJECT_ATTRIBUTES.RootDirectory`, and do not build on `os.Root`.** The
prototype is `internal/winspike/winfs.go`; it is ~470 lines of mechanism and it
reproduces `storefs_linux.go` operation for operation.

This is not a preference. `os.Root` fails a requirement that has no workaround:

| Requirement | `os.Root` | Evidence |
|---|---|---|
| Segment-at-a-time, handle-anchored (R1) | **met** | uses `OBJECT_ATTRIBUTES.RootDirectory` internally |
| `OBJ_DONT_REPARSE` on every component (R3) | **met** | `os/root_windows.go:146-154` |
| Atomic create-only claim (R6) | **met** | `M17.mkdir_excl` = YES, run 4 |
| `FILE_SHARE_DELETE` everywhere (R15) | **met** | `os/root_windows.go:176` |
| Rejects reserved device names | **met** | `M17.reserved` = YES (`openat NUL: path escapes from parent`), run 4 |
| **A no-follow BROWSE walk** | **NOT met, and not layerable** | `M17.follows_inroot_symlink` = **YES**, run 2 — `os.Root.OpenRoot` on a symlink that stays inside the root **succeeds**. `openBrowsableDir` (`storefs_linux.go:169`) must refuse exactly this, and `os.Root` exposes no opt-out. |
| Tag allowlisting (R4/R5) | **NOT met** | it surfaces `fs.FileMode`; a junction is `ModeIrregular` (`M17.junction_mode`, run 4) and an unknown non-surrogate tag is `ModeDir` (§4 below) |
| Watch-link creation | **NOT met** | `M17.symlink_flavour` = NO, run 2: `os.Root.Symlink` on an absolute external directory produces a **file** symlink (`FILE_ATTRIBUTE_DIRECTORY` clear), confirming survey Finding 2 |
| `FILE_ID_INFO` identity (R13/R14) | **NOT met** | no accessor |
| Classify-from-the-handle recursive removal (R8) | **NOT met** | `RemoveAll` is a policy, not a primitive |

The one plausible layering — `Root.Lstat` before `Root.OpenRoot` — is a
check-then-use pattern by construction and cannot reproduce `O_NOFOLLOW`'s
atomicity (`M17.lstat_layering` = PARTIAL, run 2). Against A2 that is exactly
the class of defect the whole design exists to remove.

What the hand-rolled option costs, precisely (`M17.gaps`, run 4):
`x/sys/windows` v0.41.0 ships `NtCreateFile`, `NtSetInformationFile`,
`GetFileInformationByHandleEx`, `SetFileInformationByHandle`,
`DeviceIoControl` and the `OBJ_*`/`FILE_*`/`STATUS_*` constants, but **not**
`Openat`, `Symlinkat`, `FILE_ATTRIBUTE_TAG_INFO`, `FILE_ID_INFO`,
`FILE_NAME_INFO`, `FILE_RENAME_INFO`, `FILE_DISPOSITION_INFO_EX`,
`SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE`, or NT information classes
64/65. All of those are in the prototype.

A hybrid is available and worth the ADR's attention: `os.Root` is a correct and
auditable answer for the *annotation* tree, which contains no links by
construction and never crosses a watch boundary. The store's own walks
(`openRealDir`, `openBrowsableDir`, `Delete`, `Watch`) need the hand-rolled
primitives regardless, so the saving is small and the cost is two mechanisms
where one would do.

### Prototype status

Every mechanism `storefs_linux.go` uses has a working Windows twin, measured:

| Linux | Windows | Verdict |
|---|---|---|
| `open(O_RDONLY\|O_DIRECTORY\|O_NOFOLLOW)` | `CreateFile(BACKUP_SEMANTICS\|OPEN_REPARSE_POINT)` + tag check | `P12.root`, `P12.root_file` YES (run 4) |
| `openat(O_DIRECTORY\|O_NOFOLLOW)` | `NtCreateFile(FILE_DIRECTORY_FILE, OBJ_DONT_REPARSE)` | `P12.openrealdir` YES |
| `openat(O_NOFOLLOW)` + `fstat` `S_IFREG` | `NtCreateFile(FILE_NON_DIRECTORY_FILE, OBJ_DONT_REPARSE)` — **one** operation, no open-then-check window | `P12.openfile_isdir` YES |
| `mkdirat` → `EEXIST` | `NtCreateFile(FILE_CREATE)` → `STATUS_OBJECT_NAME_COLLISION` | `P12.mkdir_excl` YES |
| `unlinkat` | `NtCreateFile(FILE_OPEN_REPARSE_POINT)` + `FileDispositionInformationEx` | `P12.deleteat` YES |
| `fstatat(AT_SYMLINK_NOFOLLOW)` | `FILE_ATTRIBUTE_TAG_INFO` from the handle | `P14.classify.*` |
| `renameat(parent, …, parent, …)` | `NtSetInformationFile(FileRenameInformationEx, RootDirectory=parent)` | `M9.nt_ex_rootdir` YES |
| **`fdPath` = `/proc/self/fd/N`** | `DuplicateHandle` + `os.NewFile(...).ReadDir` | `M16` YES |
| `dupFD` through a mutation | `DuplicateHandle`, handle survives ancestor rename | `M7.redirect` YES |
| retained-parent containment | pinned `FILE_ID_INFO` re-verified | `R13.replace` YES |

---

## 2. The design-deciding answers

### M9 — `FILE_RENAME_INFO.RootDirectory`: **the Win32 wrapper refuses it; the NT call honours it**

Run 2, [32902343901](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32902343901), reconfirmed run 4.

| Call | Result |
|---|---|
| `SetFileInformationByHandle(FileRenameInfoEx=22)`, `RootDirectory`=parent, relative name, `REPLACE\|POSIX` | `ERROR_INVALID_PARAMETER` (87) |
| `SetFileInformationByHandle(FileRenameInfoEx=22)`, `RootDirectory`=parent, `REPLACE` only | `ERROR_INVALID_PARAMETER` (87) |
| `SetFileInformationByHandle(FileRenameInfo=3)`, `RootDirectory`=parent | `ERROR_INVALID_PARAMETER` (87) |
| **`NtSetInformationFile(FileRenameInformationEx=65)`, `RootDirectory`=parent, `REPLACE\|POSIX`** | **success**, destination replaced atomically |
| `NtSetInformationFile(FileRenameInformation=10)`, `RootDirectory`=parent | success |
| control: `SetFileInformationByHandle(FileRenameInfoEx)`, `RootDirectory`=**NULL**, full path | **success** |

The control is what makes this an answer rather than a bug report: the identical
buffer succeeds through the Win32 wrapper when `RootDirectory` is NULL
(`M9.win32_control_nullroot` YES), so the layout is right and the wrapper is
specifically refusing a non-NULL `RootDirectory`.

**Consequence.** `annotationfs_linux.go:135`'s "same descriptor on both sides"
rename *does* have a Windows equivalent, but only through
`NtSetInformationFile`. The atomic annotation replace can be handle-relative,
and the ADR must record that the documented Win32 API is not usable for it. Both
NT classes work on build 26100/26200; `FileRenameInformationEx` is Windows 10
1709+, and the Go standard library already falls back from 65 to 10, which is
the right pattern to copy.

### M14 — `LockFileEx` on a directory handle: **no**

Run 4. `LockFileEx(LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY)`:

- on the pinned root **directory** handle (`GENERIC_READ`) → `ERROR_INVALID_PARAMETER` (87)
- on a directory handle opened `GENERIC_READ|GENERIC_WRITE` → `ERROR_INVALID_PARAMETER` (87)
- control, on a regular **file** handle → success

So the store-root rendezvous of `annotations.go:119-136` has no direct
equivalent, as the threat model predicted. Two further measured facts shape the
replacement:

- Windows byte-range locks are **mandatory**: a second handle's `ReadFile` over
  the locked range failed with `ERROR_LOCK_VIOLATION` (33) (`M14.mandatory`).
  A crashed or hung holder is therefore a worse failure mode than `flock`'s.
- A **handle** keeps naming the same object across a rename
  (`M7.namefollows`, `R13.rename` — `FILE_ID_INFO` unchanged after the root was
  renamed). So a lock **file** opened once at process start and never
  re-resolved preserves the exact property `annotations.go:124-126` bought with
  the root inode: the rendezvous survives `.annotations` being renamed away.
  That is the design RR6 should be closed with, and it needs the racing
  `Delete`-vs-`SaveNotes` test.

### M1 — `OBJ_DONT_REPARSE` covers intermediate components: **yes**

Run 4, build 26100 / 26200.

- reparse point as the **final** component → `STATUS_REPARSE_POINT_ENCOUNTERED` (`0xC000050B`)
- reparse point as an **intermediate** component of `j\deep` → `STATUS_REPARSE_POINT_ENCOUNTERED`
- **control**: the same open with `FILE_OPEN_REPARSE_POINT` and *without*
  `OBJ_DONT_REPARSE` → **succeeds** (`M1.weak_flag_traverses` YES)

The control is the important half: it demonstrates on real Windows that
`FILE_FLAG_OPEN_REPARSE_POINT` protects only the final component, so R3's
falsifier ("a design that relies solely on `FILE_FLAG_OPEN_REPARSE_POINT`") is a
real, reachable defect and not a theoretical one.

### M7 — handle survival across ancestor renames: **yes, and the race is live**

Run 4. A directory with an open handle **can** be renamed on Windows when the
opener granted `FILE_SHARE_DELETE` (which R15 requires it to). The Windows twin
of `TestPinnedMutationsIgnoreProjectSwap` then passes: after renaming the pinned
ancestor away and creating a same-named decoy, `MkdirAt` through the retained
handle landed **in the original object** and **not** in the decoy
(`M7.redirect`, a `RequireProperty`). `FILE_NAME_INFO` on the retained handle
reports the *new* path afterwards, which is the direct demonstration that a
handle references the object, not the name.

### M11 — the volume's real `$UpCase` folding

Run 4. Method: create name A, then attempt `FILE_CREATE` for name B in the same
directory; a collision means the volume folds them together.

| pair | NTFS folds | Go `EqualFold` | agree |
|---|---|---|---|
| `i` / `I` | yes | yes | ✓ |
| `ı` (U+0131) / `I`, `ı` / `i` | **no** | no | ✓ |
| `İ` (U+0130) / `i`, `İ` / `I` | **no** | no | ✓ |
| `K` (U+212A Kelvin) / `K`, / `k` | **no** | **yes** | ✗ |
| `ß` / `ẞ` | **no** | **yes** | ✗ |
| `ß` / `ss` | no | no | ✓ |
| `Ａ` (U+FF21) / `A` | **no** | no | ✓ |
| `Ａ` / `ａ` (full-width pair) | **yes** | yes | ✓ |
| `.annotations` / `.Annotations` | **yes** | yes | ✓ |
| `key.pem` / `key.PEM` | **yes** | yes | ✓ |
| `.ssh` / `.SSH` | **yes** | yes | ✓ |

Two independent conclusions:

1. **RR5 is confirmed as a live defect.** NTFS folds `.annotations`/`.Annotations`,
   `.ssh`/`.SSH` and `key.pem`/`key.PEM`, while `ignore.go:378`'s reserved-name
   check and `defaultIgnores`' `path.Match` are case-**sensitive**. One letter's
   case reaches the note sidecars and the credential-shaped files.
2. **Go's `EqualFold` is not a safe substitute for the volume's folding, in the
   over-broad direction.** For the Kelvin sign and `ß`/`ẞ` Go says "equal" where
   NTFS says "different". That direction creates false collisions rather than
   bypasses, so it is safe *for a deny rule* and unsafe *for an identity test* —
   which is R2's point restated as a measurement. The Turkish dotless-i class
   that §4.10 item 5 worried about does **not** fold on this volume at all.

### M5 — `filepath.EvalSymlinks` canonicalises case: **yes**

Run 4. `mixedcase\inner`, `MIXEDCASE\INNER` and the exact spelling all resolved
to the identical string `…\MiXeDcAsE\Inner`, and the 8.3 component `RUNNER~1`
was expanded to `runneradmin` in the process. So `toNorm`/`normBase`
(`FindFirstFile` per component) canonicalises both case and short names, and
`List`'s `visited` cycle guard (`store.go:407`) is **sound against a
case-alternating cycle**. RR9's case-varying-cycle DoS does not exist as
described.

It is not sound against junctions, though (`M5.junction`): with a junction as
the **final** component `EvalSymlinks` returns the junction's own path
unresolved; with a junction as an **intermediate** component it returns
`ERROR_PATH_NOT_FOUND`. A1 therefore still controls whether a subtree appears in
`List` at all — §3.3(a)'s second half stands.

### M10 — `FILE_DISPOSITION_POSIX_SEMANTICS`: **available and necessary**

Run 4, builds 26100/26200, NTFS.

- `NtSetInformationFile(FileDispositionInformationEx=64, DELETE|POSIX_SEMANTICS|IGNORE_READONLY)`
  → success; the name left the namespace **immediately** while the handle was
  still open (`RequireProperty` passed).
- The Win32 wrapper form (`FileDispositionInfoEx=21`) also works — note the
  contrast with M9: the wrapper is fine here, it is specifically `RootDirectory`
  it refuses.
- **Legacy** `FileDispositionInfo`: the name stays resolvable-as-delete-pending
  and re-claiming it fails with `STATUS_DELETE_PENDING` (`0xC0000056`) →
  `ERROR_ACCESS_DENIED`. This is §4.14's flakiness in `TestPublishCreateOnly`,
  demonstrated.
- Memory-mapped file: **POSIX delete succeeded**; the *legacy* disposition
  failed with `ERROR_ACCESS_DENIED` (`M10.mapped`, `M10.mapped_legacy`). So
  POSIX semantics is the right answer for liveness as well as for the
  delete-pending window, and the `STATUS_CANNOT_DELETE` the threat model
  expected belongs to the legacy path.

### 8.3 short names — M6

Run 4. 8.3 generation is **enabled** on the runner's C: volume
(`fsutil 8dot3name query C:` and `M6.enabled`).

- `GetFinalPathNameByHandleW(VOLUME_NAME_DOS)` on a handle opened through the
  alias returns the **long** name; `VOLUME_NAME_GUID` returns the
  `\\?\Volume{…}\` form.
- `GetFileInformationByHandleEx(FileNameInfo)` returns the **short** name when
  the handle was opened through the alias. The two disagree, so a port that
  reaches for "the path from the handle" must know which one it is getting.
  (`FILE_NAME_INFO` does track renames — `M7.namefollows` — so it is not simply
  a frozen copy of the opened string; it is not a canonical spelling either.)
- `strings.HasPrefix(short, long)` is `false` for the same object
  (`M6.prefix_defect`), which is `Watch`'s containment guard
  (`store.go:610-616`) failing in one line.

---

## 3. The junction / `ModeSymlink` claim: **CONFIRMED**

Run 4, `P14.junction_modesymlink`, demonstrated with a junction created by the
harness (`FSCTL_SET_REPARSE_POINT`, `IO_REPARSE_TAG_MOUNT_POINT`).

| | junction | dir symlink | file symlink | plain dir | plain file |
|---|---|---|---|---|---|
| `os.Lstat().Mode()` | `?rw-rw-rw-` | `Lrw-rw-rw-` | `Lrw-rw-rw-` | `drwxrwxrwx` | `-rw-rw-rw-` |
| `ModeSymlink` | **false** | true | true | false | false |
| `ModeDir` | **false** | false | false | true | false |
| `ModeIrregular` | **true** | false | false | false | false |
| `DirEntry.IsDir()` (parent listing) | **false** | false | false | true | false |
| `os.Stat()` | `drwxrwxrwx` (**follows**) | `drwxrwxrwx` | `-rw-rw-rw-` | dir | file |
| `filepath.EvalSymlinks` | **unresolved** (returns the link) | resolved to target | resolved to target | itself | itself |
| `os.ReadDir(link)` | **succeeds** (the path-based open follows it) | succeeds | error | succeeds | error |
| `FILE_ATTRIBUTE_TAG_INFO` | `attrs=0x410 tag=0xA0000003` | `attrs=0x410 tag=0xA000000C` | `attrs=0x420 tag=0xA000000C` | `attrs=0x10 tag=0` | `attrs=0x20 tag=0` |

So a junction is neither `ModeSymlink` nor `ModeDir`, exactly as §3.2/§3.4/§4.2
claim. Every consequence the threat model drew follows: `entryIsDir`
(`store.go:316`), `List`'s link test (`store.go:417`), `Watches`
(`store.go:722`) and `WatchLinkFor` (`store.go:691`) all misclassify junctions
today.

Two measurements that go beyond the claim:

- **`FILE_ATTRIBUTE_DIRECTORY` is SET on a junction** (`0x410` = DIRECTORY |
  REPARSE_POINT, `P14.delete_attr_trap`). A recursive delete that decides by the
  directory attribute alone — the exact RR1 scenario — *would* descend.
- **The stdlib is safe here.** `os.RemoveAll` on a junction removed only the
  link and left the target tree byte-intact (`RR1.removeall`, a
  `RequireProperty`), and `filepath.WalkDir` starting at a junction does not
  descend (`RR1.walkdir`, run 2). The danger is a hand-rolled walk, not Go's.
  Conversely, that non-descent is §3.3(b)'s silent correctness failure: a
  junction-backed artifact reports as empty rather than as refused.
- Removing a junction relative to a pinned parent handle left the target intact
  (`P14.unlink_junction`, a `RequireProperty`).

---

## 4. Threat-model assumptions this spike INVALIDATED or corrected

These are the most valuable results here.

### 4.1 The tag policy cannot be "refuse name surrogates" — a second RR1 vector

Run 4, `RR1.unknown_tag_isdir` and `M2.unknown_tag`.

A **non-Microsoft, non-surrogate** reparse tag (`0x00001234`) applied to a
directory is reported by Go as:

- `os.Lstat().Mode()` = `d?rwxrwxrwx` — **`ModeDir` AND `ModeIrregular`**
- `os.Stat().Mode()` = `d?rwxrwxrwx`
- parent `DirEntry.IsDir()` = **true**, `Type()` = `d?---------`

`fileStat.Mode` sets `ModeDir` whenever `isReparseTagNameSurrogate()` is false
(`$GOROOT/src/os/types_windows.go:190-204`), and bit 29 is clear for a
third-party tag. §4.2 and RR1 analysed only the **junction** case, where the
surrogate bit *is* set and `ModeDir` is therefore suppressed. For a
non-surrogate tag the suppression does not happen and **`IsDir()` is true**.

What limits the damage on this runner is not the classification: it is that the
kernel returns `STATUS_IO_REPARSE_TAG_NOT_HANDLED` (`0xC0000279`) when no filter
driver services the tag, so the open fails. On a machine that *has* the driver —
Windows Containers (`WCI*`), VFS-for-Git (`PROJFS*`), a vendor filter — the open
succeeds and the `IsDir() == true` classification is the entire defence.

**Consequence for R4:** the allowlist must be "refuse every tag that is not
explicitly allowed". A rule expressed as "refuse name surrogates" would admit
this class. R4 already says allowlist; this measurement is why the alternative
phrasing must not be adopted as a shortcut.

### 4.2 "Name already taken" does not have one error — M8, and it breaks Watch's idempotence

Run 4, `M8.claim_over.*` and `M8.claim_error_map`.

| existing entry | claim **with** `OBJ_DONT_REPARSE` | claim **without** it |
|---|---|---|
| plain directory | `STATUS_OBJECT_NAME_COLLISION` | `STATUS_OBJECT_NAME_COLLISION` |
| plain file | `STATUS_OBJECT_NAME_COLLISION` | `STATUS_OBJECT_NAME_COLLISION` |
| **junction** | **`STATUS_REPARSE_POINT_ENCOUNTERED`** | `STATUS_OBJECT_NAME_COLLISION` |
| **directory symlink** | **`STATUS_REPARSE_POINT_ENCOUNTERED`** | `STATUS_OBJECT_NAME_COLLISION` |

`OBJ_DONT_REPARSE` fires during name resolution, before the collision is
detected. Two consequences the threat model does not state:

1. **R6.** `Publish` must map *both* `STATUS_OBJECT_NAME_COLLISION` and
   `STATUS_REPARSE_POINT_ENCOUNTERED` to "already exists", while still keeping
   `STATUS_DELETE_PENDING` distinguishable. Otherwise publishing over a name
   currently held by a watch link reports "too many levels of symbolic links".
2. **`Watch`'s documented idempotence is reached from the wrong branch.**
   `store.go:642` relaxes `EEXIST` by reading the existing link from the pinned
   parent and accepting an identical target. On Windows the create-only claim
   for an existing link fails with the **reparse** status, not the collision
   status, so a mechanical port of `errors.Is(err, unix.EEXIST)` turns *every
   repeat `watch` of the same folder* into a hard error. This is a functional
   regression, not a security one, and it is invisible to a Linux test suite.

### 4.3 Reserved device names are NOT reachable through a handle-relative open

Run 4, `M18.relative_open.*`. Opening `NUL`, `CON` or `COM1` relative to a
directory **handle** returns `STATUS_OBJECT_NAME_NOT_FOUND`; the path-based
control `os.OpenFile(<dir>\NUL, O_RDONLY)` **succeeds**
(`M18.path_open_control`). `COM1\x.html` gives `STATUS_OBJECT_PATH_NOT_FOUND`;
`CON.txt` and `NUL.txt` give `STATUS_OBJECT_NAME_NOT_FOUND`.

§4.12(b) says "a URL segment `NUL` reaches `openFileAt`; opening `NUL` succeeds
and yields a device". That is true of a **string-path** port and false of a
handle-anchored one: the relative name is resolved in the directory object's
namespace, where the DOS device links do not exist. The handle-anchored design
closes §4.12's *lookup* hole for free. R11's `:`/trailing-dot/case rules stand
on their own merits; its reserved-device clause is a defence in depth on
Windows, not the primary control — and it remains necessary for anything that
still builds a path (the `\\?\`-free display paths, and the `Root()` string
itself).

### 4.4 A reparse tag can only be applied to an EMPTY directory

Run 4, `M4.nonempty`: `FSCTL_SET_REPARSE_POINT` on a populated directory fails
with `ERROR_DIR_NOT_EMPTY` (145). An attacker cannot convert an existing
populated tree into a reparse point in place; the tag has to be applied first
and the content arrives through the link. This narrows §4.1's substitution
window in a way the threat model does not note.

### 4.5 The "unknown tag" matrix cell is testable after all

Run 3, `M4.noprivilege` = YES. §5's Watch row said creating a synthetic unknown
tag "needs `FSCTL_SET_REPARSE_POINT` with a non-Microsoft tag — **[MEASURE M4]**
whether that is possible unprivileged; if not, this cell is a documented
exclusion." Measured with `SeCreateSymbolicLinkPrivilege` **removed** from the
token: it **succeeds**. The cell is not an exclusion, and given §4.1 above it is
one of the more important cells.

### 4.6 `EvalSymlinks` case canonicalisation makes RR9's case-cycle DoS a non-issue

See M5 above. RR9's other half (an unexpected error killing the web process)
stands; the specific case-alternating-cycle mechanism does not.

### 4.7 The spec's Developer Mode premise is CORRECT — but only after a scare

Run 2 measured a handle-relative `FSCTL_SET_REPARSE_POINT` with
`IO_REPARSE_TAG_SYMLINK` **succeeding** after the privilege was removed, which
would have meant `watch` never needs Developer Mode and would have invalidated
spec acceptance criterion 6. Run 3 isolated the cause by clearing
`AllowDevelopmentWithoutDevLicense` in a child process: with Developer Mode
**off** and the privilege removed, the FSCTL route fails with
`ERROR_PRIVILEGE_NOT_HELD` too. Recorded here because the intermediate result is
the kind of thing that gets quoted out of a log.

---

## 5. P1.4 — link options, measured

All rows below with "privilege removed" come from a child process that stripped
`SeCreateSymbolicLinkPrivilege`; the Developer-Mode-off rows additionally
cleared the registry value and restored it (runs 2 and 3).

| Configuration | `CreateSymbolicLinkW(DIRECTORY)` | `+ ALLOW_UNPRIVILEGED_CREATE` | handle-relative `Symlinkat` (FSCTL, `IO_REPARSE_TAG_SYMLINK`) | junction (FSCTL, `MOUNT_POINT`) |
|---|---|---|---|---|
| privilege held (elevated CI default) | success | success | success | success |
| privilege **removed**, Developer Mode **on** | `ERROR_PRIVILEGE_NOT_HELD` (1314) | **success** | **success** | success |
| privilege **removed**, Developer Mode **off** | `ERROR_PRIVILEGE_NOT_HELD` | `ERROR_PRIVILEGE_NOT_HELD` | `ERROR_PRIVILEGE_NOT_HELD` | **success** |

Readings:

- `ERROR_PRIVILEGE_NOT_HELD` is **1314** and is the error the CLI must explain
  (spec: "Detect and explain `ERROR_PRIVILEGE_NOT_HELD`, including how to enable
  Developer Mode").
- Developer Mode relaxes the privilege check for `CreateSymbolicLinkW`
  **only when the flag is passed**, and for the raw FSCTL route
  **unconditionally**. Both are gated by it.
- **Junctions are the only link an unprivileged, Developer-Mode-off user can
  create.** That is the whole case for the P1.4 fallback, and it is now measured
  rather than assumed.
- The fallback is cheap at the *traversal* layer and expensive elsewhere. The
  prototype's `OpenBrowsableDir` takes the allowed tags as a parameter: with
  `{SYMLINK}` a junction boundary is refused, and adding `MOUNT_POINT` crosses
  the same boundary with no other change
  (`P12.browsable_tag_allowlist`, `P12.browsable_tag_allowlist_junction`, run 4).
  §9.7's list of everything *else* a junction fallback would have to rewrite
  (`entryIsDir`, `List`, `Watches`, `WatchLinkFor`, the watcher) is confirmed by
  the classification table in §3 above and is where the real cost sits.

### `CreateSymbolicLinkW` as an `O_EXCL` analogue — M8

- Over an existing name: `ERROR_ALREADY_EXISTS` (183), and `os.IsExist` is true.
  Over an existing **real directory**: the same. It never replaces
  (`M8.createsymlink_excl`, a `RequireProperty`).
- The handle-relative route is **two steps**: `FILE_CREATE` claims the name
  atomically, then `FSCTL_SET_REPARSE_POINT` applies the tag. A crash between
  them leaves an **empty real directory** under the watch name — not a partial
  link, and indistinguishable from a published-but-empty artifact. The ADR must
  say what happens to that state (`M8.symlinkat_excl`). This answers the survey's
  Finding 2 "must measure".

---

## 6. Full measurement table

`P` = the run that produced it. Detail lives in that run's log and in the
uploaded `winspike-log-*` artifact.

| ID | Answer | Run |
|---|---|---|
| **M1** `OBJ_DONT_REPARSE` | Honoured on build 26100/26200; covers **intermediate** components; the `FILE_OPEN_REPARSE_POINT`-only control traverses. ReFS/SMB not measured. | 4 |
| **M2** error per tag class | junction & dir symlink → `STATUS_REPARSE_POINT_ENCOUNTERED`→`ELOOP`; file symlink → `STATUS_NOT_A_DIRECTORY` for a dir open (Go maps it to `ERROR_PATH_NOT_FOUND`/3, not to a distinct ENOTDIR) and `STATUS_REPARSE_POINT_ENCOUNTERED` for a file open; unknown tag → `STATUS_IO_REPARSE_TAG_NOT_HANDLED` (`0xC0000279`, errno 1920); APPEXECLINK observed live in `WindowsApps` (`tag=0x8000001B`, non-surrogate). Cloud tags not measured. | 4 |
| **M3** volume mount point vs junction | **Identical tag** (`0xA0000003`); distinguishable only by the `\??\Volume{…}\` substitute name, or by `FILE_ID_INFO.VolumeSerialNumber` differing when opened *through* it (measured across C:→D:). | 4 |
| **M4** non-Microsoft tag unprivileged | **Yes** — succeeds with the privilege removed. Requires an **empty** directory. | 3, 4 |
| **M5** `EvalSymlinks` case | **Canonicalises case and 8.3**. Does not resolve junctions; errors when a junction is intermediate. | 4 |
| **M6** 8.3 aliasing | Generation **enabled** on the runner. `GetFinalPathNameByHandleW` returns the long name; `FILE_NAME_INFO` returns the **opened** (short) name. `VOLUME_NAME_GUID` available. | 4 |
| **M7** handle survival | Rename of a directory with an open handle **succeeds**; the handle follows the object; the mutation cannot be redirected to a decoy. | 4 |
| **M8** `CreateSymbolicLinkW` `O_EXCL` | Yes, `ERROR_ALREADY_EXISTS`. Handle-relative route is atomic in the **name claim** only. Reparse-point collisions report the **reparse** status, not the collision status. | 4 |
| **M9** `FILE_RENAME_INFO.RootDirectory` | **Win32 `SetFileInformationByHandle` refuses it** (`ERROR_INVALID_PARAMETER`); **`NtSetInformationFile` honours it** for classes 65 and 10. | 2, 4 |
| **M10** POSIX delete | Available (NT class 64 and Win32 class 21). Removes the name immediately; legacy disposition leaves a delete-pending window that reports `ERROR_ACCESS_DENIED`. Mapped file: POSIX succeeds, legacy fails. | 4 |
| **M11** `$UpCase` | Table in §2. `.annotations`/`.ssh`/`.pem` fold; Turkish-i class and Kelvin/`ß` do **not**; full-width pairs do. | 4 |
| **M12** ADS detection | The stream **is** visible after the fact from `FILE_NAME_INFO` and `GetFinalPathNameByHandleW`. A `RootDirectory`-relative `NtCreateFile` **accepts** `doc.html:hidden`, and with a file named `C` present, `C:evil` opens its stream. Streams are invisible to `ReadDir`/`Stat` size. | 4 |
| **M13** transient errors | Deterministic half only: destination held without `FILE_SHARE_DELETE` → `STATUS_SHARING_VIOLATION`→`ERROR_SHARING_VIOLATION` (32), destination content preserved; succeeded on the **first** attempt once released. AV distribution **not measured**. | 4 |
| **M14** `LockFileEx` on a directory | **No** (`ERROR_INVALID_PARAMETER`), read or write handle. File control succeeds and is **mandatory** (`ERROR_LOCK_VIOLATION` to a second reader). | 4 |
| **M15** fsnotify | Watch **survives** a rename of the watched directory and keeps delivering, but the event paths are built from the **registration path** and are therefore stale. A populated subtree renamed in atomically produces **one** Create for the top directory and no per-descendant events. Overflow **not measured**. | 4 |
| **M16** `ReadDir` on a duplicated handle | **Yes**, and each duplicate restarts the enumeration independently. This is the `fdPath` replacement. | 4 |
| **M17** `os.Root` | See §1. Follows in-root symlinks; no tag access; wrong symlink flavour. | 2, 4 |
| **M18** `RtlIsDosDeviceName_U` | `NUL`/`nul`/`CON`/`AUX`/`PRN` → `0x6`; `COM1`/`COM9`/`LPT1` → `0x8`; `CONIN$` → `0xC`; `NUL ` and `NUL.` → `0x6`; **`CON.txt`, `NUL.txt`, `NUL.tar.gz`, `COM0` → 0 (not devices)** on build 26100 **and** 26200. `CON.txt` is creatable. | 4 |

---

## 7. What was NOT measured, and why

| Gap | Why | What to do about it |
|---|---|---|
| **ReFS / Dev Drive** (M1, M9, M10) | the runner exposes one NTFS volume plus a second NTFS volume at `D:`; no ReFS. Dev Drive is exactly where developers keep source trees, so this is the realistic gap. | §9.8's "decide ReFS explicitly" cannot be answered from CI. Manual check on a Dev Drive before the beta, or an explicit unsupported-with-warning decision. |
| **SMB / UNC** (M1) | no share available in CI. | R18 already requires refusing UNC for mutations; keep that as policy, not measurement. |
| **FAT32** | a FAT32 volume exists on the runner but is unmounted (EFI). | POSIX semantics and `FILE_RENAME_INFORMATION_EX` are documented as unsupported there; the Go stdlib's fallback chain is the pattern to copy. |
| **Cloud placeholders** (M2, RR10) | no OneDrive on a runner. | documented exclusion + manual pre-beta check. |
| **Antivirus / Windows Search transient distribution** (M13) | Defender's state on a runner is not representative. | choose the retryable set from documentation with a bound; do not claim a measured distribution. |
| **`ReadDirectoryChangesW` overflow** (M15) | not deterministically reproducible. | map the Windows overflow error explicitly in Phase 3; the existing reconcile path is the right shape. |
| **A genuinely non-elevated session** | GitHub runners are elevated. | the privilege-removal child is a faithful simulation of the *privilege* dimension, but not of every ACL-related difference. Worth one manual confirmation of the P1.4 table on an ordinary user account. |
| **32-bit Windows** | not a target. | the `FILE_RENAME_INFO` layout in the prototype asserts a 64-bit `HANDLE`. |

---

## 8. What the P1.6 ADR now has to decide

Each of these is a decision the measurements have made ready, not a question
they left open.

1. **Backend**: hand-rolled `NtCreateFile` (§1). Record `os.Root` as rejected,
   with `M17.follows_inroot_symlink` as the reason.
2. **Rename**: `NtSetInformationFile(FileRenameInformationEx)` with a fallback to
   `FileRenameInformation`, never the Win32 wrapper. State that the documented
   Win32 API cannot express the operation (M9).
3. **Delete**: `FileDispositionInformationEx` with POSIX semantics, so `Publish`'s
   create-only contract keeps one meaning (M10).
4. **Error map for a taken name**: collision **and** reparse status both mean
   "already exists"; delete-pending is distinct; `Watch`'s idempotence branch
   hangs off the reparse status (§4.2).
5. **Tag policy**: explicit allowlist, never "refuse surrogates" (§4.1). Decide
   whether `MOUNT_POINT` is in it, knowing it also admits volume crossings unless
   the reparse **data** is inspected (M3).
6. **Link creation**: `SYMLINK` via handle-relative FSCTL when the privilege or
   Developer Mode allows it; the junction fallback is the only unprivileged
   option and costs the classification rewrites in §9.7 (§5).
7. **Annotation lock**: a lock **file** pinned by handle at process start,
   preserving the rename-survival property the root inode gave on Linux (M14).
8. **The two-step link creation window**: what a crash between `FILE_CREATE` and
   `FSCTL_SET_REPARSE_POINT` leaves behind, and how `watch`/`unwatch`/`list`
   treat it (M8).
9. **Reserved device names in lookup**: keep R11's rule, but record that the
   handle-anchored design already closes the reachable half, so it is defence in
   depth rather than the control (§4.3).
10. **ReFS**: decide explicitly; it is the one gap CI cannot close (§7).
