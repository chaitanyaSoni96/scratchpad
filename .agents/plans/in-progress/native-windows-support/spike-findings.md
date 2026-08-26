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
| 6 | [32905568933](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32905568933) | `ccd8905` | **cancelled** — the P1.3/P1.5 prototype: `AtomicWriteFile`, the sharing/retry matrix, `A1`–`A12` adversarial tests |
| 7 | [32906333884](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32906333884) | `6f8b5c3` | **failure** — an unrelated store fix (`fix(store): stop annotate() from misreading fdPath as a symlink`) run against the P1.3/P1.5 harness surfaced two real defects in it: `A5.unknown_tag_refused` was `SECURITY-FAIL` (R3 was necessary and not sufficient, §4.1/§9.7 below), and `TestP13NeverRemoveThenRename/change_records` hung for the full 20-minute test timeout and panicked |
| 8 | [32908423510](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32908423510) | `47248c3` | **failure** — corrected R3 to a tag-based refusal and unhung the change-records observer; with the hang gone the observer could finally run to completion, which surfaced a second `SECURITY-FAIL`: `P13.change_records` |
| 9 | [32908643117](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32908643117) | `145583a` | **success** — withdrew the `P13.change_records` assertion the runner falsified (§9.6 below); reference run for P1.3/P1.5 |

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

Run 9 is the reference state for the P1.3/P1.5 sections below (§9–§11):
**369 measurement lines** on `windows-2025` (251 YES, 70 INFO, 24 NO, 13
NOT-MEASURED, 11 PARTIAL) and **zero `SECURITY-FAIL` verdicts** — the one
literal occurrence of the string "SECURITY-FAIL" in that run's raw log is the
job-summary script's own match pattern, not a finding. 91 distinct
`RequireProperty` ids reported "holds" in that run (up from the 17 above);
they are not all reproduced by name here, but every one cited in §9–§11 below
is a direct quote of its log line. Runs 6–8 are kept in the table above
because their *failures* are findings in their own right (§9.6, §10.3): this
phase is measurement, and a runner falsifying an assertion and the assertion
being corrected is exactly what it looks like working.

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
11. **`A11.ancestor_swapped` (WARNING, §10.1)**: the containment proof for
    "browse walk cannot reach outside the watch boundary" is not yet complete
    on either platform. The ADR must state the property as bounded (needs
    write access above the user's watched folder) rather than absolute, and
    own a follow-up task for the handle-by-handle target walk on both
    `internal/store/storefs_linux.go` and the Windows backend.
12. **The two-step link-creation window** (`M8.symlinkat_excl`, §10.4,
    restating §5's `M8.symlinkat_excl` with the P1.5 recovery experiment): give
    `unwatch` the power to remove an **empty real directory** under a watch
    name, not only a link, or the name is permanently stuck.
13. **Retry bound and retryable-status set** (§9.3): adopt
    `DefaultReplacePolicy` (10 attempts, 2ms→256ms doubling, 2s budget) and
    `RetryableStatuses` as designed, on the record that the antivirus
    distribution they are sized against is unmeasurable (`M13.av`) and the
    bound is therefore a documented judgment call, not a fitted parameter.
14. **Do not serve documents through `os.Open`** (`P13.go_share_mode`, §9.2):
    the Windows twin of `OpenDocument` must wrap a handle opened with the full
    share mode (`FILE_SHARE_READ|WRITE|DELETE`), or a concurrent notes save or
    `Delete` of an open document burns its entire retry bound against our own
    web server.

---

## 9. P1.3 — Atomic replacement

The instrument is `internal/winspike/atomicwrite.go` (`AtomicWriteFile`,
`ReplacePolicy`, `RetryableStatuses`) plus the tests in
`atomicwrite_test.go`. All results below are Run 9 unless noted.

### 9.1 The write path (`AtomicWriteFile`)

`AtomicWriteFile(parent, name, data, pol)` is entirely handle-relative — no
step resolves a path — and does four things, each with its own measured
property:

1. **Unique temp creation.** `newTempName()` generates an 8-random-byte name
   shaped `.notes-<16 hex chars>.tmp`, "same as `annotationfs_linux.go:110`"
   (source comment). Claimed with `CreateFileAt` (`FILE_CREATE`, i.e.
   create-only) in a loop that retries only on a name collision. Measured:
   `P13.temp_unique` — 64 generated names, 64 distinct, no duplicate.
2. **Handle-relative rename.** `RenameAtNT(src, parent, name,
   fileRenameInformationEx, REPLACE|POSIX)` — `NtSetInformationFile` with
   `RootDirectory=parent`, never a Win32 path-based rename. `P13.write_new`
   (first write through a pinned parent, `attempts=1`) and
   `P13.replace_existing` (replace over an existing destination, `attempts=1`)
   both measured `err=nil`. `REQUIRED property holds` for
   `P13.replace_existing`: "`NtSetInformationFile(FileRenameInformationEx,
   RootDirectory=parent, REPLACE|POSIX)` must replace the destination
   atomically through the pinned parent handle."
3. **The destination is never unlinked (R-3).** `P13.no_dest_removal` —
   REQUIRED property holds: "a successful replace must issue ZERO namespace
   removals naming the destination; recorded []." `P13.audit` cross-checks
   this with an audit of every disposition-setting call `AtomicWriteFile`
   makes: "recorded [], err=<nil>." The negative control,
   `P13.audit_control`, runs the same audit against a *deliberately wrong*
   remove-then-rename implementation and confirms the audit actually catches
   it: "the audit DID see it remove the destination = true. If this were false
   the audit would be worthless and the guard below would be vacuous."
4. **Cleanup on failure goes through the temp's own HANDLE, never its name**
   (R-4). `P13.cleanup_handle_based` renamed the temp to `stolen.tmp` between
   the write and the replace, then failed the replace: "stolen.tmp still
   present=false; `.notes-*` residue []. Cleanup goes through the temp's own
   HANDLE (`DeleteByHandle`), which follows the object through the rename; a
   name-based cleanup would have unlinked nothing and left an orphan file
   inside the store." `P13.cleanup_bound` and `P13.cleanup_permanent` confirm
   the same handle-based cleanup fires after a retry-bound exhaustion and
   after a permanent failure respectively (both: residue `[]`).
   `P13.permanent_failure` classifies one such permanent case — replacing a
   directory-shaped destination — as `STATUS_OBJECT_IS_A_DIRECTORY`
   (`NTSTATUS=0xC00000BA`), non-retryable, destination still present, temp
   residue `[]`.

`A9.rename_failure_statuses` measured which statuses the class-65
(`FileRenameInformationEx`) call returns per failure mode, each paired with
what class-10 (`FileRenameInformation`, the pre-1709 fallback) returns for the
same case, to determine when the class-65→class-10 fallback should fire:
`dest_is_directory` → class65 `STATUS_OBJECT_IS_A_DIRECTORY`, not retryable;
`dest_is_junction` → both classes `nil` (a junction IS a valid replace
target); `dest_held_no_share_delete` → class65
`STATUS_SHARING_VIOLATION`/retryable, class10 `STATUS_ACCESS_DENIED`;
`dest_absent` → both `nil`. Consequence recorded verbatim: "the class-65→
class-10 fallback must fire ONLY on `STATUS_INVALID_PARAMETER` /
`NOT_SUPPORTED` / `INVALID_INFO_CLASS` / `INVALID_DEVICE_REQUEST` (i.e. 'this
build or filesystem does not implement the class'). The stdlib's blanket
'retry on any error' fallback would, on the rows above, silently retry an
ATTACK with a class that has no POSIX semantics." `isUnsupportedRenameClass`
in `atomicwrite.go` implements exactly that allowlisted fallback trigger, not
a blanket one.

### 9.2 Sharing-mode matrix (`P13.sharing.*`)

Measured across all three destination-mutating primitives — `renameEx` (class
65, POSIX semantics), `renameLegacy` (class 10), `posixDelete` (NT class 64) —
crossed with an opener holding the destination via `GENERIC_READ` or
`GENERIC_WRITE` under each of the 8 `FILE_SHARE_*` combinations
(`P13.sharing.renameEx.GENERIC_READ`, `.renameEx.GENERIC_WRITE`,
`.renameLegacy.GENERIC_READ`, `.renameLegacy.GENERIC_WRITE`,
`.posixDelete.GENERIC_READ`, `.posixDelete.GENERIC_WRITE`).

The rule, stated once by `P13.sharing_summary`: **"a destination opened
WITHOUT `FILE_SHARE_DELETE` vetoes the replace (err=true); the destination is
left holding 'COMPLETE-OLD'. An opener that grants `FILE_SHARE_DELETE` does
not veto it. This is the whole rule: the interferer's share mask, not its
access mask, decides."** Read vs. write access on the interfering handle makes
no difference; only whether `FILE_SHARE_DELETE` is in its share mask does.
`renameLegacy` differs in *how* it fails without `FILE_SHARE_DELETE` —
`STATUS_ACCESS_DENIED` rather than `renameEx`'s `STATUS_SHARING_VIOLATION` —
which is why `renameEx`'s status (retryable) and not `renameLegacy`'s
(not retryable, per `RetryableStatuses` below) is the one that matters for the
primary path.

`P13.sharing_never_truncates` — REQUIRED property holds: a vetoed replace must
leave the destination byte-identical, never truncated or absent; found
"COMPLETE-OLD" every time. `P13.share_all_reader_does_not_veto` — REQUIRED
property holds: a reader that grants the full share mode must not veto the
replace (`got nil`).

A distinct defect surfaced here, not in the primitive but in how the store
would naturally end up calling it: **`P13.go_share_mode`**. Go's
`syscall.Open` (what `os.Open` uses) hard-codes
`FILE_SHARE_READ|FILE_SHARE_WRITE` and omits `FILE_SHARE_DELETE`
(`$GOROOT/src/syscall/syscall_windows.go:395`). Measured: with a file held
open by `os.Open`, both the atomic replace and a POSIX delete of that file
fail with `STATUS_SHARING_VIOLATION`; with the *same* file held by a
`CreateFile` that grants `FILE_SHARE_READ|WRITE|DELETE`, both succeed.
Consequence, quoted: "`OpenDocument`'s Windows twin must not return an
`*os.File` opened by `os.Open` — while the web server is streaming a
document, a concurrent notes replace or `Delete` of that document would fail
with a sharing violation and burn the whole retry bound. `os.NewFile` over a
handle WE opened with the full share mode is the fix." This is item 14 in §8.

### 9.3 Retry policy actually implemented

`DefaultReplacePolicy()` in `atomicwrite.go`: `MaxAttempts=10`,
`InitialBackoff=2ms`, `MaxBackoff=256ms` (doubling in between: 2, 4, 8, 16,
32, 64, 128, 256, 256 ms — 766ms of sleep in the worst case), `TotalBudget=2s`
as a hard wall-clock ceiling, `Flush=false`.

`RetryableStatuses` (the exported set, `atomicwrite.go:221`):
`STATUS_SHARING_VIOLATION`, `STATUS_DELETE_PENDING`,
`STATUS_LOCK_NOT_GRANTED`, `STATUS_FILE_LOCK_CONFLICT`,
`STATUS_USER_MAPPED_FILE`, `STATUS_DIRECTORY_NOT_EMPTY` — plus a parallel
Win32-errno set (`ERROR_SHARING_VIOLATION`, `ERROR_LOCK_VIOLATION`,
`ERROR_DIR_NOT_EMPTY`, `ERROR_USER_MAPPED_FILE`) for the paths that go through
a Win32 wrapper rather than the NT call directly. Deliberately **not**
retryable, per the code's own comment: `STATUS_ACCESS_DENIED` (a real ACL
denial once the delete-pending case is carved out into its own status, so
retrying it would only add latency to a permanent failure —
`M13.pending_status`); `STATUS_REPARSE_POINT_ENCOUNTERED` (a link appeared
where a real entry is required — this is an **attack signal**, A2/RR1, and
retrying it would loop against the attacker); `STATUS_OBJECT_NAME_COLLISION`,
`STATUS_OBJECT_PATH_NOT_FOUND`, `STATUS_NOT_SAME_DEVICE`,
`STATUS_DISK_FULL`, `STATUS_MEDIA_WRITE_PROTECTED` (permanent by
construction).

**This set is chosen from documentation, not from a measured distribution: the
antivirus-induced transient-error distribution is explicitly
`MATRIX.EXCLUDED.antivirus_distribution` / `M13.av`, NOT-MEASURED** — "not
measurable on a runner. Defender's realtime state on a CI image is not
representative, so the retryable set is chosen from documentation with a
stated bound, and the DETERMINISTIC half — an interfering handle we open
ourselves — is measured instead (`P13.sharing.*`, `P13.retry.*`)."

What the deterministic half showed: `M13.retry` — a replace that is going to
succeed succeeds on the **first** attempt once the interfering handle closes,
so the retry bound is sized for the tail, not the mean. `P13.retry.hold20ms`
and `P13.retry.hold400ms` held a blocking opener for 20ms and 400ms
respectively; the replace succeeded after 5 attempts/31ms and 9
attempts/514ms. `P13.retry_integrity.hold20ms` / `.hold400ms` — REQUIRED
property holds for both: "whatever the retry outcome, the destination must
hold one COMPLETE version, never a partial one."

`P13.bound` measured the exhausted-bound case: a destination held without
`FILE_SHARE_DELETE` for the whole bound produced 10 attempts over 771ms and a
`*ReplaceError`, whose user-facing message is reproduced in full because it is
the spec's "antivirus/indexer sharing violations exceed retry bounds"
actionable case:

> notes write: could not replace "notes.json" after 10 attempts over 771ms:
> NTSTATUS=0xC0000043(A file cannot be opened because the share access flags
> are incompatible.) errno=32(The process cannot access the file because it is
> being used by another process.) goerr=A file cannot be opened because the
> share access flags are incompatible.. Another program is holding the file
> open without allowing deletion — most often an editor, Explorer's preview
> pane, an antivirus scanner or a backup agent. Close it and retry; the
> previous contents of "notes.json" were left untouched.

`P13.bound_preserves_dest` — REQUIRED property holds: after the bound is
exhausted the destination must still hold its COMPLETE previous content
("COMPLETE-OLD"). `P13.bound_terminates` — the loop terminated in 771ms, "well
inside a request timeout," because each attempt is non-blocking (a share-mode
veto fails immediately rather than waiting).

### 9.4 Concurrent writers (`A12.concurrent_writers`)

`TestA12ConcurrentNoteWriters` (`adversarial_test.go:1235`) runs **8 writers ×
25 replaces** (`const writers, rounds = 8, 25`) of one document, each writer
racing `AtomicWriteFile` against the others and reading back through a
share-all handle. Measured: `A12.concurrent_writers` — per-writer failures all
zero, "torn/partial reads observed 0 []," temp residue `[]`.
`A12.concurrent_temp_residue` — REQUIRED property holds: concurrent writers
must not leave temp files behind. `A12.concurrent_writers` itself is also a
REQUIRED property: "a concurrent reader must never observe a torn or partial
document." Quoted conclusion: "the unique temp name plus the atomic replace
means concurrent writers never share a temp and a reader never sees a partial
file — the rev guard above this layer decides WHICH version wins, not whether
the file is intact." Both `windows-2025` and `windows-11-arm` reported the
identical `8 writers × 25 replaces` line.

**Discrepancy, not silently resolved:** `matrix_test.go:279`'s hardcoded
description of this same result — the string inside
`MATRIX.Notes.concurrent_revisions` — says **"8 writers × 40 replaces"**. That
number does not appear anywhere in the code or in either run's log; the test
constant is `rounds = 25` and every `A12.concurrent_writers` log line on both
architectures reports 25. This is a stale literal in `matrix_test.go`'s
descriptive string, not a re-measurement, and not evidence of 40 replaces
having been run. The finding stands as **8 × 25**, per the primitive's own
measurement line.

### 9.5 Durability and flush

`P13.flush_cost` (INFO): 100× (create temp + write 4 KiB + atomic replace)
without `FlushFileBuffers`: 811µs/op; with it: 4.594ms/op (5.7×).
Recommendation, quoted: "DO NOT flush by default. `annotationfs_linux.go`'s
`writeFile` does not fsync either, so flushing would make Windows strictly
MORE durable than the Linux backend rather than reaching parity, at this cost
per note save. What the atomic replace already guarantees without any flush
is the property that matters: the destination name always resolves to one
COMPLETE version. What it does not guarantee is which version survives a
power loss — and a lost note revision is recoverable by the user, a torn one
is not."

`P13.flush_scope` (INFO): there is no directory-fsync question on Windows —
the rename is a metadata operation on the parent's index and NTFS journals
it, so `FlushFileBuffers` on the temp's own handle before the replace is the
only durability knob, exposed as `ReplacePolicy.Flush` and left `false` by
default per the above.

### 9.6 The withdrawn hypothesis: `ReadDirectoryChangesW` cannot detect a degraded replace

This is the most important self-correction in this batch, and it deserves to
be stated carefully.

The original hypothesis, built into the instrument
(`internal/winspike/dirnotify.go`'s own doc comment, still unchanged in the
tree as of this writing): a raw `ReadDirectoryChangesW` observer could tell
apart "the destination was replaced atomically" from "the destination was
removed and a new file was renamed into its place," because the kernel's own
action codes are supposedly `RENAMED_OLD_NAME`/`RENAMED_NEW_NAME` for the
first and `REMOVED`+`RENAMED_OLD_NAME`+`RENAMED_NEW_NAME` for the second. At
`47248c3` this was wired up as a `RequireProperty` — a `SECURITY-FAIL`-gating
assertion — and the runner contradicted it (Run 8,
`atomicwrite_test.go:648`): "REQUIRED property CONTRADICTED: the atomic
replace must never produce `FILE_ACTION_REMOVED` for the destination" against
records `[ADDED(.tmp) REMOVED(notes.json) RENAMED_OLD_NAME(.tmp)
RENAMED_NEW_NAME(notes.json) MODIFIED(notes.json) ...]` — i.e. the genuine
`AtomicWriteFile` replace, not the deliberately-wrong implementation, produced
a `FILE_ACTION_REMOVED` naming the destination.

Run 9 (`145583a`) withdrew the assertion rather than patch around the
observation, and `P13.change_records` is now an **`NO`**-verdict, informational
finding, quoted in full because the wording matters:

> INSTRUMENT INSUFFICIENT, and the reason is a finding. `AtomicWriteFile`
> produced `[ADDED(.notes-c60166cfd8bc5ff7.tmp) REMOVED(notes.json)
> RENAMED_OLD_NAME(.notes-c60166cfd8bc5ff7.tmp)
> RENAMED_NEW_NAME(notes.json) MODIFIED(notes.json) ADDED(.sentinel)
> MODIFIED(.sentinel)]` ; the deliberately-wrong remove-then-rename produced
> `[ADDED(.notes-b6fade695f6e54b2.tmp) REMOVED(notes.json)
> RENAMED_OLD_NAME(.notes-b6fade695f6e54b2.tmp)
> RENAMED_NEW_NAME(notes.json) MODIFIED(notes.json) ADDED(.sentinel)
> MODIFIED(.sentinel)]` ; identical apart from the temp name = false (both
> contain `FILE_ACTION_REMOVED` naming the destination = true / true). A
> POSIX-semantics rename that REPLACES a destination makes the kernel emit
> `FILE_ACTION_REMOVED` for the replaced file as part of the atomic rename, so
> `ReadDirectoryChangesW` CANNOT distinguish an atomic replace from
> unlink+rename. The hypothesis that it could is withdrawn; the guard against
> the degradation is the namespace-removal audit (`P13.audit`) and the
> continuous-existence observer (`P13.continuous_existence`).

`P13.change_records_control` (Run 8, still true in Run 9's negative control)
independently confirms the two record streams *would* have been
distinguishable if the false-positive hadn't been the atomic case itself:
"the instrument can therefore distinguish the two implementations" — meaning
this was not an instrument bug that produced noise; the instrument worked
correctly and reported that the two implementations are **not**
distinguishable by kernel change notification alone, because POSIX-semantics
rename-with-replace itself emits `FILE_ACTION_REMOVED` for the replaced name.
Identical on `windows-11-arm`.

**What guards the property instead**, now that the change-notification
discriminator is withdrawn:

- **`P13.audit`** — an audit of every disposition-setting call
  `AtomicWriteFile` itself makes, asserting zero of them name the destination.
  REQUIRED property holds: "`AtomicWriteFile` must perform zero namespace
  removals naming the destination (recorded [], err=<nil>)." This is a
  white-box guarantee about what the function does, not a black-box
  observation of what the kernel reports.
- **`P13.continuous_existence`** — a concurrent reader polling the destination
  name across 200 replaces observed it absent 0 times out of 1902 polls,
  against a negative control (remove-then-rename, 200 replaces) that saw it
  absent 1902 times out of 3441 polls. Quoted: "With the control failing on
  roughly half of all polls, this is the black-box discriminator that
  `ReadDirectoryChangesW` turned out not to be: it is empirical rather than
  deterministic, but the margin is not a close call." REQUIRED property
  holds: "the destination name must resolve at every instant during a
  replace."

**Where the log and the code disagree:** `internal/winspike/dirnotify.go`'s
package doc comment still asserts the withdrawn hypothesis as fact — "so 'no
`FILE_ACTION_REMOVED` naming the destination' is a black-box, deterministic
assertion that the destination never left the namespace" — unchanged by the
`145583a` withdrawal. The finding record above is authoritative; that comment
is stale and, since `internal/winspike` is deleted at the end of Phase 1 per
this document's introduction, it is being recorded here rather than fixed in
place (this task is constrained to this file only).

**Also recorded here rather than filed silently:** `P13.watch_sees_removed_on_replace`
(INFO) draws out the consequence for `internal/watch`: every notes save now
emits `REMOVED(<doc>)` immediately followed by `RENAMED_NEW_NAME(<doc>)` and
`MODIFIED(<doc>)`; fsnotify maps that to Remove→Create→Write. "A watcher that
reacts to Remove by dropping state for the document — or a UI that reacts by
hiding it — will flicker on every save. The 250ms debounce in `internal/watch`
absorbs this today on Linux (where a rename emits no unlink at all); on
Windows the debounce is doing real work and must not be removed."

---

## 10. P1.5 — Adversarial tests

The instrument is `internal/winspike/adversarial_test.go` (1327 lines) plus
the sharing/retry cases in `atomicwrite_test.go` and the reserved-name /
matrix wiring in `matrix_test.go`. All results below are Run 9 unless noted;
every containment result was identical on `windows-2025` and
`windows-11-arm`.

### 10.1 WARNING — `A11.ancestor_swapped`: containment BROKE

This is the most important result in this batch. Verdict: **`NO`** —
containment did not hold. Quoted in full:

> an ANCESTOR of the watch target was replaced with a junction between the
> reparse-buffer read and the open-by-name: open -> nil ; entries reached
> [LOOT.txt] ; the attacker's tree was reached = true. THIS IS A REAL WINDOW
> AND IT IS PLATFORM-INDEPENDENT: storefs_linux.go:184 re-opens the readlink
> result as an ABSOLUTE PATH with O_NOFOLLOW, which likewise protects only the
> final component. It is not a Windows regression and it is bounded by the
> attacker already needing write access above the user's watched folder, but
> the ADR should record it rather than let the Windows port inherit it
> silently. The structural fix on both platforms is to walk the target's
> components handle-by-handle instead of opening the string.

Stated plainly: an ancestor directory of the watch target was replaced with a
junction pointing at an attacker-controlled tree in the window between when
`openBrowsableDir` read the reparse buffer (to follow the store's one
permitted watch-boundary link) and when it opened the resolved target by
name — and the attacker's tree **was** reached (`entries reached [LOOT.txt]`,
the attacker's planted file). This is distinct from `A11.target_swapped`
(the watch **target itself** substituted with a link), which **is** refused —
`openAbsoluteDirNoFollow` opens the final component with
`FILE_FLAG_OPEN_REPARSE_POINT` and then refuses a reparse point, closing that
half. The ancestor-substitution half is the one that is open.

**It is platform-independent.** `internal/store/storefs_linux.go`'s
`openBrowsableDir` (currently lines 196–223; the vulnerable call is line
**211**: `unix.Open(string(buf[:n]), unix.O_RDONLY|unix.O_DIRECTORY|
unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)`) re-opens the `readlink` result as an
**absolute path** with `O_NOFOLLOW`, which likewise protects only the final
component of that reopened path — an ancestor of *that* path substituted
between the `readlinkat` and the `open` is not defended against either.
**Note the line-number discrepancy:** the finding's own text cites
`storefs_linux.go:184`; the function is at line 194 and the vulnerable open is
at line 211 in the current tree, and it was already at that line in the
`145583a` commit the finding was measured against (`git show
145583a:internal/store/storefs_linux.go` shows the same 194–223 span). The
`:184` citation appears to simply be wrong, not stale — recorded here as a
log/code disagreement per this document's own rule, not silently corrected
into the finding text.

**What is NOT true of this finding:** it is not a Windows-specific defect, it
is not new to this spike's backend design, and it does not require the
attacker to be unprivileged relative to the store — it requires the attacker
to already have write access to a directory *above* the user's watched
folder, which is a meaningfully high bar (on Linux, write access to an
ancestor directory the user does not own; on Windows, equivalent DACL
control). It is bounded, not absolute.

**Disposition:** the structural fix on both platforms is to walk the target's
path components handle-by-handle (open segment, verify its identity/tag,
descend, repeat) rather than reopening a resolved string, closing the same
class of window that `A1`–`A9` already close for the *store's own* tree. This
is item 11 in §8: the P1.6 ADR's containment proof for "the browse walk
cannot reach outside the watch boundary" must state the property as bounded
and own a follow-up task for the handle-by-handle walk on both
`storefs_linux.go` and the Windows backend it is designing.

### 10.2 The rest of the containment tests: HELD

Every other adversarial containment test in Run 9 **HELD**. Table format:
property tested → verdict → id(s).

| Threat | Verdict | ids |
|---|---|---|
| Mutation through a pinned ancestor handle survives ancestor substitution (realdir / junction / symlink / unknown tag) | **HELD** | `A1.ancestor_replaced.realdir`, `.junction`, `.symlink`, `.unknowntag` |
| Destination substituted with a link/realdir/realfile in the window before an atomic write | **HELD** — write never reaches outside the store; temp always cleaned up | `A2.dest_replaced.*`, `A2.dest_replaced_cleanup.*` |
| A second reparse point nested below an already-crossed boundary, at 1 and 2 levels, junction/symlink boundary × junction/symlink/unknown-tag nested | **HELD** — refused at any depth; the strict (tag-based) variant refuses independent of filter drivers | `A3.nested.*`, `A3.nested_strict.*` |
| Root substituted (realdir/junction/symlink) after `OpenRoot`; a fresh open must not silently accept the substitute | **HELD** — pinned handle stays in the original object; a fresh open differs in identity | `A4.root_replaced.*` |
| `OpenRoot` on a root that is itself a reparse point | **HELD** — refused | `A4.root_reparse_refused.junction`, `.symlink` |
| Root removed while a handle is open | **INFO, not pass/fail** — the object stays alive and usable via the handle even though unreachable by name; "the ADR must decide whether that is acceptable or whether the root identity is re-verified per mutation" | `A4.root_removed` |
| Unknown reparse tag refused **by its tag**, independent of whether a filter driver services it (the R3 self-correction, §10.3) | **HELD** | `A5.unknown_tag_refused`, `A5.strict_open`, `A5.strict_walk` |
| Recursive delete does not descend through a planted junction/symlink/unknown-tag at depth 0 or depth 2 inside the artifact (**threat model RR1, Critical**) | **HELD** — negative control confirms the mechanical port *does* destroy the external target, so the assertion is meaningful | `A6.delete.junction.depth0`/`.depth2`, `A6.delete.symlink.*`, `A6.delete.unknowntag.*`, `A6.negative_control` |
| Entry substituted with a link between enumeration and descent during a recursive delete | **HELD** | `A6.swap_midwalk` |
| Delete through a pinned parent after the parent was renamed away and replaced by a junction | **HELD** | `A6.parent_replaced` |
| Removing a watch link removes only the link, never the external target | **HELD** | `A6.unlink_watch.junction`, `.symlink` |
| Exactly one of N concurrent create-only claims on one name wins | **HELD** — 16 racers, 1 winner, 15 `STATUS_OBJECT_NAME_COLLISION`, 0 other | `A8.concurrent_claim` |
| A create-only claim during a delete-pending window gets a distinct, transient error (not "already exists") | **INFO** — confirms the Windows-only third outcome exists; POSIX-semantics delete removes the window entirely | `A8.delete_pending_claim` |
| A file symlink / junction is refused as an openable document | **HELD** | `A10.file_link_refused`, `A10.dir_link_refused` |
| A document substituted with a link after the parent handle was pinned is never served | **HELD** | `A10.rename_race` |
| URL-shaped segments (`index.html:x`, `::$DATA`, `C:evil`, trailing dot) at the open layer | **INFO** — none of these fail at the open itself; `validateSegment` (R11) is the actual control | `A10.stream_syntax` |
| 8 concurrent writers × 25 replaces of one notes document: no torn read, no temp residue | **HELD** (§9.4; discrepancy with `matrix_test.go`'s "40" noted there) | `A12.concurrent_writers`, `A12.concurrent_temp_residue` |
| The target itself (not an ancestor) substituted with a link between the reparse-buffer read and the open | **HELD** | `A11.target_swapped` |
| The watch target's ancestor substituted with a link in the same window | **BROKE — see §10.1** | `A11.ancestor_swapped` |

### 10.3 The non-surrogate unknown-tag vector — R3 corrected

Threat-model assumption invalidated and then re-closed within this batch
(Runs 7→8, commit `47248c3`, "correct R3 after the runner invalidated it").
At Run 7 (`6f8b5c3`), `A5.unknown_tag_refused` was `SECURITY-FAIL`: the
no-follow walk relied on `OBJ_DONT_REPARSE` to refuse an unknown, non-Microsoft
reparse tag, and the runner showed this refusal actually comes from
`STATUS_IO_REPARSE_TAG_NOT_HANDLED` — no filter driver claims the tag — not
from the no-follow rule itself. `A5.obj_dont_reparse_inert_for_unknown_tags`
states the correction: "for a NON-MICROSOFT tag the WITH-flag and
WITHOUT-flag opens return the SAME status... `OBJ_DONT_REPARSE` therefore does
NOTHING for an unknown tag... On a machine that HAS the driver — Windows
Containers (WCI*), VFS-for-Git (PROJFS*), a vendor filter — the same open
would be SERVICED and the walk would traverse. R3 stated as 'OBJ_DONT_REPARSE
on every component' is NECESSARY AND NOT SUFFICIENT."

The fix, measured as holding by Run 8 and unchanged through Run 9:
`A5.strict_open` opens with `FILE_OPEN_REPARSE_POINT` and reads
`FILE_ATTRIBUTE_TAG_INFO` off the **same handle**, refusing by the tag value
itself rather than by whether the open failed. `A5.unknown_tag_refused` —
REQUIRED property holds: "a non-surrogate unknown reparse tag must be refused
BY ITS TAG, read from a handle opened with `FILE_OPEN_REPARSE_POINT`, so the
refusal does not depend on no filter driver servicing it." `A5.strict_walk` —
REQUIRED property holds for the project-ancestor walk using the same
mechanism. `A5.strict_open_admits_real_dirs` confirms the strict primitive
still opens an ordinary directory (`got nil`) — "or it is useless."
`A5.unknown_tag_removed` — REQUIRED property holds: removing an unknown-tag
entry does not touch anything outside the store.

### 10.4 The two-step link-creation window (`M8.symlinkat_excl`)

`M8.symlinkat_excl` — **`NO`**: "handle-relative `Symlinkat` over a taken
name -> `STATUS_REPARSE_POINT_ENCOUNTERED`. The NAME CLAIM is atomic
(`FILE_CREATE`); the reparse tag is applied in a SECOND step, so a crash
between the two leaves an EMPTY REAL DIRECTORY under the watch name, not a
partial link. That intermediate state is indistinguishable from a
published-but-empty artifact and must be handled by the ADR."

The P1.5 batch adds the crash-window experiment this implies. `A7.two_step_residue`
(INFO) confirms what the residue actually is: "after a crash between the two
steps the name holds:... It is an ORDINARY EMPTY DIRECTORY — not a partial
link, not a broken link, and indistinguishable from a published-but-empty
artifact." `A7.two_step_recovery` (INFO) confirms **no existing CLI verb can
clear it**: "re-running watch over the residue -> ... the create-only claim
fails, as it must... `Delete` would refuse it because `dirHasHTML=false` so it
is not an artifact... `Unwatch` would refuse it because it is a REAL
directory, not a link. So today the name is STUCK... A handle-relative
`rmdir` of the EMPTY directory does work (-> nil, gone=true), which is the
candidate recovery: let `unwatch` remove a link OR an empty real directory,
since an empty directory under a watch name carries no user data by
definition." `A7.two_step_window` — REQUIRED-style conclusion: "the window is
real and its residue is benign for CONTAINMENT (an empty real directory
grants an attacker nothing) but is a usability trap: it consumes a name that
create-only semantics will never release." This is item 12 in §8.

---

## 11. Security Test Matrix coverage

Coverage against the spec's Security Test Matrix
(`.agents/spec/native-windows-support.md`, "Security Test Matrix" table),
measured by `internal/winspike/matrix_test.go`'s own `MATRIX.*` accounting.
`MATRIX.SUMMARY` (Run 9): **"44 matrix cells accounted for: 26 DEMONSTRATED on
the prototype, 9 MECHANISM-ONLY (handed to Phase 3 with the Windows primitive
proven), 9 DOCUMENTED EXCLUSIONS. Zero silent gaps."** Independently counted
from the raw `MATRIX.*` log lines: 26 `YES`, 9 `PARTIAL`, 9 `NOT-MEASURED`,
matching the summary exactly.

`Covered` below means the `MATRIX.*` verdict is `YES` (fully demonstrated on
the prototype). `Partial` means the Windows **primitive** is demonstrated but
part of the cell is `internal/store` policy that this spike cannot exercise
(no `internal/store` import, per this document's introduction) — the P3 owner
is named in each row. `Excluded` means `NOT-MEASURED`, reproduced verbatim in
§11.2.

| Area | Case | Status | Measurement ids | Partial: the internal/store half |
|---|---|---|---|---|
| Root | root missing | Covered | `MX.root_missing` |  |
| Root | root file | Covered | `MX.root_file`, `P12.root_file` |  |
| Root | root link/reparse point | Covered | `MX.root_reparse.junction`/`.symlink`/`.unknowntag`/`.volume_mount_point`, `P14.junction_modesymlink` |  |
| Root | root replaced during operation | Covered | `A4.root_replaced.realdir`/`.junction`/`.symlink`, `A4.root_removed`, `R13.replace` |  |
| Publish | concurrent same-name claim | Covered | `A8.concurrent_claim`, `A8.delete_pending_claim` |  |
| Publish | ancestor replaced | Covered | `A1.ancestor_replaced.realdir`/`.junction`/`.symlink`/`.unknowntag`, `M7.redirect` |  |
| Publish | ancestor link | Covered | `P12.junction_traverse`, `P12.junction_intermediate`, `P12.symlink_traverse`, `A5.strict_walk`, `A5.obj_dont_reparse_inert_for_unknown_tags` |  |
| Publish | artifact ancestor | Partial | `P12.reject_artifact` | `dirHasHTMLFD` uses `strings.ToLower`, not the volume's `$UpCase` folding (M11), so a `.HTML` entry can miss the artifact test — fix + test owned by P3 |
| Browse | one approved watch boundary | Covered | `P12.browsable_boundary`, `P12.browsable_tag_allowlist` |  |
| Browse | nested link | Covered | `A3.nested.*`, `A3.nested_strict.*` (every boundary × nested flavour, 1 and 2 levels) |  |
| Browse | cycle | Partial | `A3.nested.*` (a cycle needs a second link, which is refused), `M5.case` (EvalSymlinks canonicalises case, so RR9's case-alternating-cycle mechanism does not exist) | `List`'s string-keyed visited-set termination is `internal/store` code — owned by P3 |
| Browse | broken target | Covered | `A11.target_swapped` (removes the target entirely; open fails cleanly) |  |
| Browse | target replacement | **Partial — and it is the one that found a real gap** | `A11.target_swapped` (target itself: refused), `A11.ancestor_swapped` (ancestor of target: **not** refused, §10.1) | Not an `internal/store` gap — see §10.1/§8 item 11; the fix is a structural change to the walk on both platforms |
| Documents | file link | Covered | `A10.file_link_refused` (hard-link variant is RR4, accepted not fixed: `MX.directory_hard_links`) |  |
| Documents | directory link | Covered | `A10.dir_link_refused` |  |
| Documents | alternate stream syntax | Partial | `A10.stream_syntax`, `M12` — a RootDirectory-relative open **accepts** `doc.html:hidden` | The control is `validateSegment` rejecting `:` (R11) — `internal/store`, 404 assertions owned by P3 |
| Documents | case variation | Partial | `M11` — the volume's real `$UpCase` folding measured, including `.annotations`/`.Annotations` and `key.pem`/`key.PEM` folding (RR5 confirmed live) | The fix is `internal/store`'s `ignore.go` reserved-name check and `defaultIgnores` |
| Documents | rename race | Covered | `A10.rename_race` |  |
| Delete | target replaced | Covered | `A6.delete.junction/.symlink/.unknowntag` at depth 0/2, `A6.swap_midwalk`, `A6.negative_control` |  |
| Delete | parent replaced | Covered | `A6.parent_replaced` |  |
| Delete | link target untouched | Covered | `A6.unlink_watch.junction`/`.symlink`, `P14.unlink_junction` |  |
| Delete | annotation subtree cleanup | Partial | `RemoveTreeAt` primitive proven containment-safe by `A6.*` | Whether `Delete` actually calls it for the notes subtree, and the re-published-name-inherits-no-notes assertion, is `internal/store` behaviour — owned by P3 |
| Notes | annotation root link | Covered | `MX.notes_root_link.junction`/`.symlink` |  |
| Notes | intermediate link | Covered | `MX.notes_intermediate_link.junction`/`.symlink` |  |
| Notes | concurrent revisions | Partial | `A12.concurrent_writers` — 8 writers × 25 replaces, no torn read, no temp residue (§9.4; note the "40" discrepancy in `matrix_test.go`'s own description string) | The rev guard and `ErrRevMismatch` are `internal/store`; the Windows-extra RR6 (Delete racing SaveNotes without a store-root lock) needs the lock-file substitute chosen in the ADR (M14) and is a P3 test |
| Notes | sharing violation | **Covered — and this cell does not exist on Linux** | `P13.sharing.*` (all 8 share masks × read/write × 3 mutation primitives), `P13.sharing_never_truncates`, `P13.bound`, `P13.bound_preserves_dest`, `P13.retry.hold*`, `M13.pending_status` |  |
| Watch | same-target idempotence | Partial | `M8.claim_over.*` — a create-only claim over an existing LINK fails with `STATUS_REPARSE_POINT_ENCOUNTERED`, not `STATUS_OBJECT_NAME_COLLISION`, so a mechanical port of `Watch`'s `errors.Is(EEXIST)` relaxation turns every repeat `watch` into a hard error | The relaxation itself (`linksTo`) is `internal/store` — owned by P3 |
| Watch | different target collision | Partial | `M8.createsymlink_excl`, `M8.symlinkat_excl` — link creation never replaces | The target-comparison policy (`linksTo`) is `internal/store`, and on Windows must compare OBJECT IDENTITY, not strings, because of 8.3 aliasing (`M6.prefix_defect`) |
| Watch | junction/reparse variants | Covered | `P14.classify.*`, `M3.volume_mount_point`, `A5.*`, `A6.*`, `P14.unlink_junction` |  |
| Watch | (Windows-only cell) no symlink privilege | Covered | `P14.devmode_off.*`, the P1.4 table; `ERROR_PRIVILEGE_NOT_HELD` = 1314 |  |
| Watch | (Windows-only cell, added by this spike) two-step crash window | Covered | `A7.two_step_residue`, `A7.two_step_recovery` (§10.4) |  |
| Names | reserved devices | Covered | `M18.*`, `M18.relative_open.*` |  |
| Names | trailing dot/space | Covered | `MX.trailing_dot_space` |  |
| Names | UNC/drive forms | Partial | none (pure string validation) | The syntactic half is `internal/store` and a P3 test with no Windows primitive to measure; the live half is `MATRIX.EXCLUDED.smb` |
| Names | Unicode and case collisions | Covered | `M11` — the volume's real `$UpCase` against Go's `EqualFold`, including the Kelvin-sign and ß/ẞ disagreements and confirmation RR5 is live |  |

Totals: **26 covered, 9 partial, 9 excluded** — 44 rows, matching
`MATRIX.SUMMARY` exactly.

### 11.1 `MATRIX.EXCLUDED` — the nine documented exclusions, verbatim

Every excluded row carries a stated reason; none is a silent gap.

- **`MATRIX.EXCLUDED.directory_hard_links`** — "CONCEPT DOES NOT EXIST ON
  WINDOWS. `CreateHardLinkW` fails on a directory (`MX.directory_hard_links`
  measures it rather than asserting it from documentation). There is no cell
  to test; §4.4 says so and this confirms it."
- **`MATRIX.EXCLUDED.smb`** — "NO SMB SHARE ON A GITHUB RUNNER. R18 already
  requires refusing UNC for mutations, so this stays policy rather than
  measurement; a live-share check belongs to a manual pre-beta pass."
- **`MATRIX.EXCLUDED.refs_devdrive`** — "NO ReFS VOLUME ON A GITHUB RUNNER
  (the image exposes NTFS C: and NTFS D:). This is the realistic gap — a Dev
  Drive is exactly where developers keep source trees — and it is the one
  thing CI cannot close. `FILE_RENAME_INFORMATION_EX` and
  `FILE_DISPOSITION_INFORMATION_EX` are documented for ReFS but unverified
  here."
- **`MATRIX.EXCLUDED.fat32`** — "THE RUNNER'S ONLY FAT32 VOLUME IS THE
  UNMOUNTED EFI PARTITION. POSIX semantics and class 65 are documented as
  unsupported there; `A9.rename_failure_statuses` establishes which statuses
  justify the class-10 fallback, which is the mechanism that would cover
  FAT32."
- **`MATRIX.EXCLUDED.cloud_placeholders`** — "NO ONEDRIVE ON A GITHUB RUNNER.
  The 'broken target' cell's cloud variant (`ERROR_CLOUD_FILE_*` instead of
  not-found) and RR10's mass-rehydration risk are documented exclusions with
  a manual pre-beta check."
- **`MATRIX.EXCLUDED.antivirus_distribution`** — "NOT MEASURABLE ON A RUNNER
  (`M13.av`). Defender's realtime state on a CI image is not representative,
  so the retryable set is chosen from documentation with a stated bound
  (`RetryableStatuses`) and the DETERMINISTIC half — an interfering handle we
  open ourselves — is measured instead (`P13.sharing.*`, `P13.retry.*`)."
- **`MATRIX.EXCLUDED.readdirchanges_overflow`** — "NOT DETERMINISTICALLY
  REPRODUCIBLE (`M15.overflow`). The `DirObserver` in this package DOES
  detect the overflow condition (a 0-byte return) and reports it rather than
  silently truncating, which is the shape `internal/watch` should copy in
  Phase 3."
- **`MATRIX.EXCLUDED.non_elevated_session`** — "GITHUB RUNNERS EXECUTE
  ELEVATED WITH DEVELOPER MODE ON. The privilege-removal child
  (`P14.noprivilege.*`) is a faithful simulation of the PRIVILEGE dimension
  but not of every ACL difference; one manual confirmation on an ordinary
  user account is still owed."
- **`MATRIX.EXCLUDED.32bit`** — "NOT A TARGET. The `FILE_RENAME_INFORMATION`
  layout in this prototype asserts a 64-bit `HANDLE` and returns an error
  otherwise."
