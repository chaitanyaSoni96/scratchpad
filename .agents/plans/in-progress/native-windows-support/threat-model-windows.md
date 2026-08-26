---
title: Windows Threat Model
type: threat-model
status: draft
created: 2026-08-26
links: [native-windows-support.md, ../../../spec/native-windows-support.md]
---

# Windows Threat Model

Phase 1 (P1.1) deliverable for [Native Windows Support](native-windows-support.md).
It enumerates what the Linux store relies on for containment, what an attacker
could do if those guarantees were emulated with string paths on Windows, and the
falsifiable properties any Phase 1 backend design must satisfy. It deliberately
proposes no implementation — that is P1.6's ADR.

Every claim about this codebase cites `file.go:line`. Every claim about Win32 is
either sourced to Microsoft Learn / the Go standard library source, or is marked
**[MEASURE]** for the P1.2 spike. An honest "must be measured" outranks a
confident guess.

## 1. Trust boundaries and attacker model

The store root is `%USERPROFILE%\.scratchpad` (`store.go:60-69`), inside the
user's own profile, and both binaries run as that user without elevation. There
is no authentication boundary anywhere in the product: `cmd/scratchpad-web`
serves content, a delete endpoint and notes-write endpoints to whoever can reach
the socket (`internal/web/server.go:46-60`). So "the user's own account" is not a
security boundary we can add one to; the boundaries that do exist are these.

### Boundary B1 — store root / outside the store root

Everything the store writes, deletes or serves must stay inside the root, with
exactly one deliberate exception: content reached through a store-owned watch
link (`store.go:594-651`). Crossing B1 in the wrong direction is the central
security property of the product. Invariants 4, 5, 6 and 7 of the spec are all
restatements of it.

### Boundary B2 — store-owned names / watched source content

Names the store *creates* are strict ASCII (`nameRe`, `store.go:82`,
`validateName`, `store.go:89-100`). Names the store merely *looks up* are much
looser (`validateSegment`, `store.go:107-120`) because a watched repository names
its own folders. Everything an untrusted watched tree contributes arrives through
the loose path.

### Boundary B3 — artifact origin / Scratchpad origin

Card previews run `sandbox="allow-scripts"` with no `allow-same-origin`
(`internal/web/templates/list.tmpl:29,57,79`), so auto-running artifact JS gets an
opaque origin. The viewer deliberately grants `allow-same-origin`
(`internal/web/templates/viewer.tmpl:43`); see
[ADR 2026-08-25](../../../ADRs/2026-08-25-keep-viewer-same-origin.md).

### Attackers

**A1 — content the user watched in from an untrusted source tree.** *In scope,
primary.* `scratchpad watch` links a live external directory into the store
(`store.go:594-651`); the target is typically a checkout, and its contents change
under us without any store action. A1 controls file and directory *names*
(including reserved, aliasing and reparse-point forms), can plant links of every
kind, can create cycles, and can rewrite content between two of our reads. A1
does **not** control the store root itself, and cannot make the store create a
name. This is the attacker the current Linux design spends most of its
complexity on (`openBrowsableDir`, `storefs_linux.go:169-196`;
`TestListDoesNotFollowSymlinksInsideWatch`, `store_test.go:377-401`).

**A2 — another local process running as the same user, racing the store.** *In
scope, primary.* This models a malicious or merely careless program — an agent,
an installer, a sync client, a `git checkout` — that renames or replaces a path
component between our validation and our use. It is the attacker the existing
`testStoreOpHook` race tests target (`store.go:24-32`,
`TestPinnedMutationsIgnoreProjectSwap`, `store_test.go:403-432`). We cannot stop
A2 from destroying its own data; the requirement is narrower and achievable: no
store operation may be *redirected* by A2 into acting on something the user did
not name. The Linux answer is that a retained directory descriptor is a reference
to an inode, not to a name, so a rename after the descriptor is pinned cannot
redirect anything (`storefs_linux.go:57-58`).

**A3 — JavaScript inside a published or watched artifact.** *In scope,
partially.* Preview JS is opaque-origin and cannot call the mutation endpoints.
Viewer JS is same-origin by an accepted decision and *can* call
`DELETE /a/{path...}`, `PUT /notes/{path...}` and `DELETE /notes/{path...}`. A3
is therefore a *client* of the store API, not of the filesystem: its escalation
path is to get the store to accept a path that names something outside the
store. Every Windows name-aliasing hazard in §4 is directly reachable by A3
because the HTTP path segments land in `validateSegment` (`store.go:107-120`) and
then in native opens.

**A4 — another local user, or a service account, where the store is on a shared
or redirected path.** *Out of scope for the beta, explicitly.* If
`SCRATCHPAD_ROOT` points at `C:\ProgramData\...`, a UNC share, or a
world-writable directory, an attacker with write access there wins trivially and
no handle discipline saves us. The spec already restricts supported storage to
local NTFS (Compatibility Policy) and the plan defers services in favour of a
per-user Scheduled Task (P5.2). What we *must* do is fail loudly rather than
silently degrade — see R18. We do not claim to defend a store root whose ACL
lets another principal write into it.

**A5 — a remote network attacker.** *Out of scope.* Loopback is the default on
every platform (Review Checklist); `LAN=1` is an explicit, documented decision to
expose an unauthenticated site. Windows adds nothing new here except that
Windows Firewall prompts on first LAN bind, which is a usability note, not a
control.

### Explicitly not claimed

- We do not defend against a user who deliberately publishes or opens a hostile
  artifact in the viewer (ADR 2026-08-25).
- We do not defend against an attacker who already has write access to the store
  root's parent directory or to `%USERPROFILE%` itself.
- We do not attempt confidentiality of note sidecars against A2; they are
  ordinary files in the user's profile.
- We do not claim NTFS ACL enforcement. The store's containment is structural
  (where operations can reach), not permission-based.

## 2. What "handle-anchored" buys on Linux

The whole Linux backend is three ideas, and each has to be re-earned on Windows.

1. **A descriptor is a reference to an inode, not to a name.** `openRootedFS`
   pins the root with `O_RDONLY|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW`
   (`storefs_linux.go:30`); every later step is `openat`-relative to that
   descriptor (`openDirAt`, `annotationfs_linux.go:55-57`). Renaming any checked
   ancestor afterwards cannot redirect the work — this is stated in the code
   (`storefs_linux.go:57-58`) and tested (`store_test.go:403-432`).
2. **`O_NOFOLLOW` refuses the *final* component if it is a link, and the
   segment-by-segment walk extends that to the whole path.** Intermediate
   components are still followed by the kernel, which is precisely why
   `openRealDir` and `openBrowsableDir` walk one segment at a time rather than
   opening a joined path.
3. **Mutation verbs take a parent descriptor and a name**: `Mkdirat`
   (`store.go:561`), `Symlinkat` (`store.go:637`), `Unlinkat`/`AT_REMOVEDIR`
   (`store.go:787,844`, `annotationfs_linux.go:161,181,191`), `Renameat` with the
   *same* parent descriptor on both sides (`annotationfs_linux.go:135`), and
   `Fstatat(AT_SYMLINK_NOFOLLOW)` for classification (`store.go:778,842`,
   `annotationfs_linux.go:169,244`). None of them re-resolves a path.

Two Linux-only crutches deserve naming now because they have no Windows analogue
and the spec does not mention either:

- **`fdPath` (`storefs_linux.go:41`) returns `/proc/self/fd/N`.** It is how
  fd-anchored code re-enters the path-based helpers: `ResolvePath` calls
  `loadArtifact(project, s, fdPath(fd))` (`store.go:475`) and `Publish` calls
  `loadArtifact(project, name, fdPath(artifactFD))` (`store.go:580`). This is the
  single hardest porting constraint in the store, and
  `GetFinalPathNameByHandleW` is *not* a replacement — it returns a string that
  has to be re-resolved by the OS, which reintroduces exactly the TOCTOU the
  descriptor removed.
- **`flock` on the store-root descriptor** coordinates annotation work with
  artifact deletion (`annotations.go:119-136`). The comment at
  `annotations.go:124-126` is explicit that the *store-root inode* is the
  rendezvous "even if a hostile process renames and replaces `.annotations`". On
  Windows you cannot byte-range-lock a directory handle **[MEASURE M14]**, and
  the obvious substitute — a lock *file* inside `.annotations` — reintroduces the
  exact replacement hazard that comment says it avoided.

Note also that `ensureProjectDir` (`store.go:253-276`), `rejectSymlinkParents`
(`store.go:278-299`), `pruneEmpty` (`store.go:798-807`) and `linksTo`
(`store.go:656-671`) are **dead code**: path-based ancestors of the current
handle-anchored design, kept only as documentation. They must not be resurrected
for the Windows port; every one of them is a check-then-use pattern.

## 3. Per-operation attack enumeration

For each operation: the Linux guarantee, named down to the syscall and flag, then
what an attacker gets on Windows if that guarantee is merely *emulated* with
string paths — build a path, validate the string, hand it to `CreateFileW`.
Throughout, "string-path emulation" means any design where the value passed to
the kernel is a full path re-resolved from the process's namespace rather than a
name resolved relative to a retained handle.

### 3.1 Root open — `openRootedFS` (`storefs_linux.go:20-35`)

**Linux:** `unix.Open(root, O_RDONLY|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW)`
(`storefs_linux.go:30`). `O_DIRECTORY` refuses a regular file; `O_NOFOLLOW`
refuses a symlinked root; the resulting `*os.File` is a reference to the inode
and every later operation is relative to it. `EnsureRoot`'s `os.MkdirAll`
(`store.go:76`) runs *before* the pin, so a root created under a race is still
re-opened and re-validated.

**Windows if emulated:** `Root()` (`store.go:60-69`) returns
`os.Getenv("SCRATCHPAD_ROOT")` **verbatim, never made absolute**. On Windows that
string may be `C:foo` (drive-relative, resolved against the per-drive current
directory), `\foo` (current-drive-relative), `\\server\share\x`, `\\?\C:\x`, or
`\\.\PhysicalDrive0`. Each re-resolution of the same string can name a different
object as the process CWD changes. A2 can also swap the root directory for a
junction between `MkdirAll` and the open. Because the root path string is
recomputed on *every* call (`Root()` is called by `openRootedFS`,
`openAnnotationFS` (`annotationfs_linux.go:23-28`), `Visible`
(`ignore.go:361`), `VisiblePath`, `ResolvePath`, …), string emulation means the
"root" is not one object but N independent lookups. **A2 wins by replacing the
root between any two of them.**

### 3.2 `ResolvePath` (`store.go:455-486`) and `ResolveFolder` (`store.go:162-193`)

**Linux:** two-stage. `visibleSegments` (`store.go:126-143`) does a path-based
*visibility* pre-check (deliberately advisory), then `openBrowsableDir`
(`storefs_linux.go:169-196`) does the authoritative walk: each segment via
`openDirAt` = `openat(fd, seg, O_RDONLY|O_DIRECTORY|O_CLOEXEC|O_NOFOLLOW)`. A
symlink yields `ELOOP`/`ENOTDIR`, and *exactly one* such failure is forgiven
(`crossed` at `storefs_linux.go:174,177,186`) provided we are not already inside
an artifact; the link is then read with `Readlinkat` and reopened absolute with
`O_NOFOLLOW` (`storefs_linux.go:184`). Every subsequent segment is `O_NOFOLLOW`
again, so a second link inside the watched source is refused — this is
invariant 5, and it is tested (`store_test.go:313-347`).

**Windows if emulated:** the "one link boundary" counter is the whole security
property, and on Windows there is no single attribute that means "link".
`CreateFileW` follows every reparse point in the path unless
`FILE_FLAG_OPEN_REPARSE_POINT` is set, and that flag is documented to affect the
*final* component only ("If an existing file is opened and it is a symbolic link,
the handle returned is a handle to the symbolic link" — Microsoft Learn,
`CreateFileW`, Symbolic Link Behavior). So string emulation crosses **unbounded**
reparse points silently: A1 plants a junction inside a watched checkout pointing
at `C:\Users\<u>\.ssh`, and the unauthenticated web server serves it at
`/a/<watch>/<junction>/id_rsa`. Invariant 5 is violated with no error anywhere.
Worse, Go's own view has changed: since Go 1.23 a junction
(`IO_REPARSE_TAG_MOUNT_POINT`) is **not** `ModeSymlink` and **not** `ModeDir`
(`$GOROOT/src/os/types_windows.go:206-230`), so a port that ports the *Go* type
checks rather than the *reparse tag* will classify junctions as "neither link nor
directory" and reach inconsistent conclusions in different code paths.

### 3.3 `List` (`store.go:398-436`)

**Linux:** purely path-based and deliberately best-effort — it is a *listing*,
not a mutation. Its safety rests on three things: `filepath.EvalSymlinks`
canonicalisation into a `visited` map as a cycle guard (`store.go:407`), refusing
to descend a symlink once one has been crossed (`isLink && crossedLink`,
`store.go:417,424`), and `Visible` (`store.go:418`).

**Windows if emulated:** all three degrade.
(a) The cycle guard keys on `filepath.EvalSymlinks` output. On Windows Go 1.23+,
`walkSymlinks` only follows `ModeSymlink` (`$GOROOT/src/path/filepath/symlink.go:89`),
so a *junction* is not resolved; and if the junction is not the final component
the call returns `ENOTDIR`, aborting the walk. A1 therefore controls whether
subtrees appear at all. **[MEASURE M5]** whether `toNorm`/`normBase`
(`$GOROOT/src/path/filepath/symlink_windows.go:25-40`, `FindFirstFile` per
component) canonicalises *case*: if it does not, an attacker-built cycle whose
components alternate case produces a fresh map key each iteration and `walk`
recurses until the process dies. `List` is called on every folder page, so that
is a remote-triggerable crash of the web binary.
(b) `entryIsDir` (`store.go:316-325`) tests `e.IsDir()` then `ModeSymlink`; a
junction is neither, so watched trees linked by junction become invisible rather
than refused — a silent correctness failure that will be read as "watch is
broken" rather than as a security decision.
(c) `Visible` is case-sensitive; see §4.10.

### 3.4 `Watches` (`store.go:701-738`)

**Linux:** `e.Type()&os.ModeSymlink != 0` (`store.go:722`) identifies the link,
`os.Readlink` gives the target, and the walk refuses to descend into artifacts or
past a link, so links inside a watched source are never reported as store watches.

**Windows if emulated:** with Go ≥1.23 semantics a junction is not `ModeSymlink`
and not a directory, so it is *neither reported nor descended*: a junction-based
watch would be un-listable and therefore un-`unwatch`-able through the UI
(`handleUnwatch`, `internal/web/server.go:912-926`), stranding a live link into
an external tree with no way to remove it. This directly contradicts the plan's
P1.4 idea of junctions as a documented fallback: link *creation* is the small
part; classification, listing, and removal all need tag-aware rewrites.

### 3.5 `Publish` (`store.go:526-587`)

**Linux:** the ancestors are opened as real directories with
`openRealDir(segs, create=true, rejectArtifacts=true)` (`store.go:553`,
`storefs_linux.go:59-87`) — each segment `openat(…, O_NOFOLLOW|O_DIRECTORY)`, so
`ELOOP` means "project ancestor is a symlink" and is reported as such
(`storefs_linux.go:75-77`). The name is then claimed with a single atomic
`unix.Mkdirat(parent, name, 0o755)` (`store.go:561`): create-only, races and
existing names both surface as `EEXIST`. Files are written with
`O_WRONLY|O_CREAT|O_EXCL|O_CLOEXEC|O_NOFOLLOW` (`storefs_linux.go:116`), so no
pre-planted symlink can capture a write. The race hook sits *after* the parent
descriptor is pinned (`store.go:558`), which is what makes
`TestPinnedMutationsIgnoreProjectSwap` (`store_test.go:403-432`) a real proof
rather than a timing test.

**Windows if emulated:** three separate losses.
1. *Ancestor substitution.* A2 renames `root\project` away and drops a junction
   in its place between the ancestor check and `CreateDirectoryW`. With a path
   string the new directory is created inside the junction target — the exact
   scenario `store_test.go:403-432` proves impossible on Linux.
2. *Create-only becomes advisory.* `CreateDirectoryW` does return
   `ERROR_ALREADY_EXISTS` atomically, so the primitive exists; but it must be
   issued **relative to a pinned parent handle**, otherwise the atomicity is
   about a name in whatever directory the path resolved to this time.
3. *Write capture.* `CREATE_NEW` is the `O_EXCL` analogue, but without
   `FILE_FLAG_OPEN_REPARSE_POINT` (or `OBJ_DONT_REPARSE`) a pre-planted symlink
   or junction at `img\logo.png`'s parent redirects the write. Note Go's own
   `openat` sets `FILE_OPEN_REPARSE_POINT` for the `O_CREAT|O_EXCL` case
   specifically to avoid this (`$GOROOT/src/internal/syscall/windows/at_windows.go:131`).

### 3.6 `Watch` (`store.go:594-651`)

**Linux:** target validated with `os.Stat`+`IsDir` (`store.go:599-604`),
"already inside the store" rejected via `EvalSymlinks` prefix comparison
(`store.go:610-616`), ancestors opened as real directories (`store.go:627`), then
`unix.Symlinkat(abs, parent, name)` (`store.go:637`) — atomic, create-only,
`EEXIST` on collision. The one relaxation is idempotence: on `EEXIST` the
existing link is read with `Readlinkat` from the *same pinned parent*
(`store.go:642`) and accepted only if it equals `abs` byte-for-byte.

**Windows if emulated:**
- The "already inside the scratchpad" guard is a `strings.HasPrefix` on
  `EvalSymlinks` output (`store.go:612-614`). On Windows that comparison is
  wrong four ways at once: case (`C:\Users\u` vs `c:\users\U`), 8.3 aliases
  (`C:\PROGRA~1`), the `\\?\` prefix, and junctions that `EvalSymlinks` does not
  resolve at all. A1/A2 can therefore watch a folder that *is* the store root by
  spelling it differently, producing a self-referential tree.
- `CreateSymbolicLinkW` is not atomic-create in the `EEXIST` sense the code
  relies on **[MEASURE M8]**; and unprivileged creation requires
  `SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE` *plus* Developer Mode
  (Microsoft Learn, `CreateSymbolicLinkW`), otherwise `ERROR_PRIVILEGE_NOT_HELD`.
- The idempotence check compares the readlink result to `abs` as a string. On
  Windows the stored substitute name is normalised (`\??\C:\…`, and Go strips or
  keeps the prefix depending on the `winreadlinkvolume` GODEBUG,
  `$GOROOT/src/os/file_windows.go:407-435`). A string comparison that fails turns
  a documented no-op into a hard error; a comparison made too loose turns a
  *different* target into an accepted no-op, which is a containment failure.

### 3.7 `Unwatch` (`store.go:744-795`)

**Linux:** `openRealDir(segs, false, false)` (`store.go:768`) refuses to reach the
parent through a symlink at all; `Fstatat(parent, name, AT_SYMLINK_NOFOLLOW)`
(`store.go:778`) classifies without following; `S_IFLNK` is *required*
(`store.go:781`) so a real directory can never be destroyed here; removal is
`unix.Unlinkat(parent, name, 0)` (`store.go:787`) — removes the link, never the
target. This is invariant 7.

**Windows if emulated:** classification is the whole game. `GetFileAttributesW`
returning `FILE_ATTRIBUTE_REPARSE_POINT` says *a* reparse point, not *which*; the
spec is right that the attribute alone is not safety. If the port instead uses
Go's `ModeSymlink`, junctions fall through the check. Removal also differs: a
symbolic link to a directory and a junction are both removed with
`RemoveDirectoryW`, a file symlink with `DeleteFileW` — pick the wrong one and
the operation either fails or, if the code "helpfully" falls back to a recursive
delete, destroys the target. That fallback is exactly what must never exist.

### 3.8 `Delete` (`store.go:812-865`) — the highest-severity operation

**Linux:** `openRealDir(segs, false, false)` (`store.go:835`) refuses a symlinked
project path; `Fstatat(…, AT_SYMLINK_NOFOLLOW)` + `S_IFLNK` (`store.go:842`)
routes watched folders to a bare `Unlinkat` (`store.go:844`) so the source is
untouched; otherwise `openDirAt` (`O_NOFOLLOW|O_DIRECTORY`, `store.go:848`) plus
`dirHasHTMLFD` confirms a real artifact and `removeTreeAt(parent, name)`
(`store.go:856`) walks *by descriptor*: `openDirAt` per child
(`annotationfs_linux.go:153`), `Fstatat(AT_SYMLINK_NOFOLLOW)` to classify
(`annotationfs_linux.go:169`), `Unlinkat` for non-directories,
`Unlinkat(AT_REMOVEDIR)` for the directory itself
(`annotationfs_linux.go:181,191`). Critically, a link encountered mid-tree hits
the `ENOTDIR`/`ELOOP` branch and is *unlinked, not descended*
(`annotationfs_linux.go:158-163`).

**Windows if emulated:** this is the classic Windows destruction primitive.
`RemoveDirectoryW` on a junction removes only the junction — but a recursive
delete that *opens* the child without `FILE_FLAG_OPEN_REPARSE_POINT` and finds
`FILE_ATTRIBUTE_DIRECTORY` set will happily enumerate and delete the junction's
**target contents**. Concretely against this code: A1 plants (or A2 plants at any
moment) a junction named `art` under the store root whose target is
`%USERPROFILE%\Documents`, containing any `.html` so `dirHasHTMLFD` passes; the
user clicks delete on the card; the port's `removeTreeAt` recurses through the
junction and destroys the target tree. There is no confirmation step and
`handleDelete` (`internal/web/server.go:894-909`) is reachable by A3 as well.
The Win32 property that defeats it: classify from a handle opened with
`FILE_FLAG_OPEN_REPARSE_POINT` (or `NtCreateFile` with `OBJ_DONT_REPARSE`) and
consult `FILE_ATTRIBUTE_TAG_INFO.ReparseTag`, never the directory attribute
alone; and issue deletes relative to a pinned parent handle.

### 3.9 `OpenDocument` (`storefs_linux.go:216-228`) / `openFileAt` (`128-142`)

**Linux:** `openat(parent, name, O_RDONLY|O_CLOEXEC|O_NOFOLLOW)`
(`storefs_linux.go:129`) then `Fstat` requiring `S_IFREG`
(`storefs_linux.go:134`). The parent came from `openBrowsableDir`, so at most one
store-owned link boundary was crossed. Tested by
`TestOpenDocumentRejectsArtifactAssetSymlink` (`store_test.go:434-460`).

**Windows if emulated:** the served path is the union of `validateSegment`-clean
segments. `validateSegment` (`store.go:107-120`) rejects `/`, `\`, control
characters, `.` and `..` — but **not `:`**. On NTFS a colon selects an alternate
data stream, so `GET /a/art/index.html:x` opens stream `x` of `index.html`, and
`GET /a/art/C:evil` is parsed by the filesystem as stream `evil` of a file named
`C`. Neither escapes the directory, but both defeat the store's one-name-one-file
model (see §4.6). `S_IFREG` has no exact analogue: the equivalent check is that
`FILE_ATTRIBUTE_TAG_INFO` reports no reparse tag and `FILE_ATTRIBUTE_DIRECTORY`
is clear; a `FILE_ATTRIBUTE_REPARSE_POINT` file with an unknown tag must be
refused, not read.

### 3.10 `ResolveDoc` (`store.go:490-507`)

**Linux:** suffix test on `.md`, `visibleSegments`, then `OpenDocument` for the
authoritative answer; the returned string is only used for display/logging.

**Windows if emulated:** the suffix test is `strings.HasSuffix(strings.ToLower(...), ".md")`
(`store.go:491`) — Go's `ToLower` is Unicode simple lowercasing, NTFS matching is
the volume's `$UpCase` table. These are not the same function **[MEASURE M11]**.
Also `notes.md.` and `notes.md ` name the same file as `notes.md` after Win32
path canonicalisation but fail the suffix test, and conversely
`notes.md::$DATA` is the same bytes but is not `.md`-suffixed. Any decision made
on the *string* and enforced on the *file* is a mismatch waiting to be exploited.

### 3.11 `WatchLinkFor` (`store.go:682-696`)

**Linux:** walks ancestors with `os.Lstat` and returns the first `ModeSymlink`
(`store.go:691`). It is advisory — it only chooses an error message and decides
whether the UI offers "unwatch" — but `Unwatch` uses it twice to tell
the user which link governs a path (`store.go:770,782`).

**Windows if emulated:** returns `""` for junction-governed paths, so a user
inside a junction-watched tree is told "not a watched folder" and, worse, the
card offers **delete** instead of **unwatch**. Combined with §3.8 that is the
full destruction chain: mis-classify, then recursively delete.

### 3.12 Annotation read — `annotationFS.readFile` (`annotationfs_linux.go:82-95`)

**Linux:** `.annotations` is pinned once at `openAnnotationFS`
(`annotationfs_linux.go:23-45`): root opened `O_NOFOLLOW|O_DIRECTORY`, then
`Mkdirat` + `Openat(…, O_DIRECTORY|O_NOFOLLOW)` for `.annotations` itself, with
an explicit error if it is not a real directory (`annotationfs_linux.go:36-40`).
Every subsequent segment is `openDirAt`; the file itself is
`Openat(…, O_RDONLY|O_NOFOLLOW)` (`annotationfs_linux.go:88`).

**Windows if emulated:** `.annotations` is created by us, so A2 is the only
attacker — but A2 can replace it with a junction between `MkdirAll` and the open.
The unavailability of a *directory* lock (see 3.16) means the window is real, not
theoretical.

### 3.13 Annotation write — `annotationFS.writeFile` (`annotationfs_linux.go:97-140`)

**Linux:** a random `.notes-<hex>.tmp` created `O_CREAT|O_EXCL|O_NOFOLLOW` in the
pinned parent (`annotationfs_linux.go:111`), written, chmod'd, then
`unix.Renameat(parent, tmp, parent, target)` (`annotationfs_linux.go:135`) —
**the same descriptor on both sides**, so the rename is atomic and cannot be
retargeted. The temp file is unlinked on any failure
(`annotationfs_linux.go:120-124`).

**Windows if emulated:** three distinct problems, and the spec only names one.
1. *Atomicity.* `MoveFileExW(..., MOVEFILE_REPLACE_EXISTING)` takes two path
   strings and re-resolves both. The handle-relative analogue is
   `SetFileInformationByHandle(FileRenameInfoEx)` with
   `FILE_RENAME_FLAG_REPLACE_IF_EXISTS`, whose `FILE_RENAME_INFO` carries a
   `RootDirectory` handle — **[MEASURE M9]**: confirm that `RootDirectory` is
   honoured through the Win32 `SetFileInformationByHandle` wrapper and not only
   through `NtSetInformationFile`, and confirm the minimum OS build.
2. *Sharing violations.* If any other opener (antivirus, Windows Search, an
   editor, Explorer preview) holds the destination without `FILE_SHARE_DELETE`,
   the replace fails with `ERROR_SHARING_VIOLATION` — "Delete access allows both
   delete and rename operations" (Microsoft Learn, `CreateFileW`, `dwShareMode`).
   This is a liveness problem that must not be "fixed" by deleting then renaming;
   that turns an atomic update into a window where the notes file does not exist.
3. *Revision guard.* `saveNotesRaw` (`annotations.go:310-327`) is a
   read-modify-write protected only by the two locks; see 3.16.

### 3.14 Annotation subtree removal — `removeSubtree` / `removeTreeAt` (`annotationfs_linux.go:152-223`)

**Linux:** identical discipline to `Delete`, plus ancestor pruning that reopens
each parent from the pinned annotation root so "a concurrent rename can only make
pruning stop, never redirect it" (`annotationfs_linux.go:209-221`).

**Windows if emulated:** same catastrophic recursive-delete-through-reparse-point
risk as §3.8, reached from `Delete`/`Unwatch` via `removeNotesFor`
(`annotations.go:493-505`, called at `store.go:794,864`). Additionally the
`ENOTDIR`/`ELOOP` → "unlink it, don't descend" branch (`annotationfs_linux.go:158-163`)
has no direct Windows error analogue: `OBJ_DONT_REPARSE` yields
`STATUS_REPARSE_POINT_ENCOUNTERED`, which Go maps to `ELOOP`
(`$GOROOT/src/internal/syscall/windows/at_windows.go:188`) **[MEASURE M2]** —
but a port that uses plain `CreateFileW` gets no error at all, it gets the target.

### 3.15 Annotation walk — `walk` / `walkAnnotationDir` (`annotationfs_linux.go:225-273`)

**Linux:** `Fstatat(AT_SYMLINK_NOFOLLOW)` per entry (`annotationfs_linux.go:244`),
recursion only into `S_IFDIR`, file reads only for `S_IFREG` `.json` with
`O_NOFOLLOW` (`annotationfs_linux.go:260`). Cycles are impossible because links
are never followed.

**Windows if emulated:** `OpenNoteCount` runs this walk **per card on every page
render** (`annotations.go:466-480`). A reparse cycle inside `.annotations` that is
followed would hang or crash the render path; unbounded recursion here is a
denial of service on the whole site, not just one card.

### 3.16 Annotation locking — `lockAnnotations` (`annotations.go:119-136`), `lockDocument` (`annotations.go:138-160`)

**Linux:** `unix.Flock` on the **store-root directory descriptor**, `LOCK_SH` for
normal work and `LOCK_EX` held by `Delete`/`Unwatch` across removing both content
and notes (`store.go:745,813`). The code states why the root and not
`.annotations`: the root inode is stable "even if a hostile process renames and
replaces `.annotations`" (`annotations.go:124-126`). Per-document exclusion uses
a real lock *file* under `.annotations/.locks/<sha256>.lock`
(`annotations.go:143-159`).

**Windows if emulated:** `LockFileEx` locks byte ranges of a *file*; directory
handles are not lockable **[MEASURE M14]**. There is therefore no direct
equivalent of the store-root rendezvous, and the natural substitute — a lock file
— must live *somewhere*, which reintroduces the replacement hazard the comment
says was avoided. Losing this lock is not cosmetic: it is what stops `Delete`
from removing an artifact while `SaveNotes` is mid-write, which is how a deleted
name can inherit resurrected notes (the thing `removeNotesFor` exists to
prevent). **The spec's "Annotation atomicity" section does not mention this lock
at all.** Also note `LockFileEx` on Windows is *mandatory*, not advisory: a held
lock actually fails other processes' I/O, so a crashed or hung holder has a
different (worse) failure mode than `flock`.

### 3.17 Watcher registration and reconciliation (`internal/watch/watch.go`)

**Linux:** `desiredDirs` (`watch.go:224-274`) walks with `os.ReadDir`, refuses to
descend a symlink once one was crossed (`watch.go:254-261`), keys everything on
`canonicalDir` = `filepath.EvalSymlinks` + `Abs` + `Clean` (`watch.go:288-297`),
and identifies each directory by `dev`+`ino` from `syscall.Stat_t`
(`watch.go:276-286`) so a replaced directory is detected and its stale watch
removed (`watch.go:196-206`). Any non-overflow error from `reconcile` is returned
from `Run` and is fatal to the web process by design.

**Windows if emulated:**
- `syscall.Stat_t` does not exist; `os.Stat` yields `*syscall.Win32FileAttributeData`,
  which carries **no file index at all**, so `identity` (`watch.go:281-285`)
  fails outright and the watcher refuses to start. This is the expected P2.2
  compile break, but note the *security* consequence: without stable identity,
  replacement detection (`w.registered[canonical] == want.id`, `watch.go:198`)
  degrades to a path-string comparison, and A2 can swap a watched directory for
  another without the watcher noticing.
- `FILE_ID_INFO` (`GetFileInformationByHandleEx(FileIdInfo)`) is the documented
  replacement: `VolumeSerialNumber` + 128-bit `FileId`, and "the file identifier
  and the volume serial number uniquely identify a file on a single computer"
  (Microsoft Learn, `FILE_ID_INFO`). It requires an **open handle**, i.e. the
  identity must come from the same handle the watch is registered on, or the
  identity is again a check-then-use.
- Fatality is an availability weapon. Because `reconcile` errors kill the
  process, any A1-controlled condition that makes `EvalSymlinks`/`ReadDir` return
  an unexpected error (a junction mid-path returning `ENOTDIR`, a cloud
  placeholder returning `ERROR_CLOUD_FILE_*`, an `ERROR_SHARING_VIOLATION` from
  a locked directory) becomes a persistent denial of service on the web binary.
  See R16.

## 4. Windows hazard catalogue

Each entry: (a) what it is, (b) the concrete attack against *this* code, (c) the
Win32-level property that defeats it.

### 4.1 Rename and replacement races on ancestors and targets

(a) Windows renames are cheap and unprivileged, and — unlike Unix — a directory
with an open handle *can* be renamed if every opener granted `FILE_SHARE_DELETE`
("Delete access allows both delete and rename operations", `CreateFileW`,
`dwShareMode`). Handles reference the file object, so the handle follows the
object to its new name.

(b) A2 renames `root\project` away and substitutes a junction between our
ancestor check and our create/delete. Every operation in §3.5–§3.8 and §3.13–§3.14
is affected. `TestPinnedMutationsIgnoreProjectSwap` (`store_test.go:403-432`) is
the existing proof for Linux and must have a Windows twin.

(c) Perform the mutation relative to a handle obtained *before* the check and
never re-derive a path. The NT-level primitive is `OBJECT_ATTRIBUTES.RootDirectory`
set to the parent handle plus a relative `ObjectName`; Go's own rooted
filesystem does exactly this (`$GOROOT/src/os/root_windows.go:155-166`,
`$GOROOT/src/internal/syscall/windows/types_windows.go:115-129`). A retained
handle plus `FILE_ID_INFO` re-verification is a weaker second-best because
identity can be checked but the *name* still has to be re-resolved.

### 4.2 The reparse-point family

(a) A reparse point is a tagged blob on a file or directory. `FILE_ATTRIBUTE_REPARSE_POINT`
says only "there is a tag"; the tag says what it means. The Microsoft-predefined
set includes `IO_REPARSE_TAG_SYMLINK`, `IO_REPARSE_TAG_MOUNT_POINT`,
`IO_REPARSE_TAG_APPEXECLINK`, `IO_REPARSE_TAG_CLOUD` through `CLOUD_F` plus
`IO_REPARSE_TAG_ONEDRIVE`/`STORAGE_SYNC`/`HSM`/`HSM2`, `IO_REPARSE_TAG_WCI`/
`WCI_1`/`WCI_LINK`/`WCI_LINK_1`/`WCI_TOMBSTONE`, `IO_REPARSE_TAG_PROJFS`/
`PROJFS_TOMBSTONE`, `IO_REPARSE_TAG_AF_UNIX`, `IO_REPARSE_TAG_DEDUP`,
`IO_REPARSE_TAG_WOF`/`WIM`, `IO_REPARSE_TAG_NFS`, `IO_REPARSE_TAG_DFS`/`DFSR`,
`IO_REPARSE_TAG_GLOBAL_REPARSE`, `IO_REPARSE_TAG_UNHANDLED` and more (Microsoft
Learn, *Reparse Point Tags*). The tag's high bits carry a Microsoft bit and a
**name-surrogate bit** (`IsReparseTagNameSurrogate`); the surrogate bit is set
for `SYMLINK` and `MOUNT_POINT` and means "this names another entity". Tags
outside the reserved ranges "are not reserved and are available for your
application" — i.e. third-party filter drivers invent their own, so the set is
*open*, and unknown tags will appear on real machines.

(b) Concrete, per tag class, against this code:

| Tag class | Where it lands | Attack |
|---|---|---|
| `SYMLINK` | anywhere | The Linux model, ported. Only defeats us if the port miscounts the single allowed boundary (§3.2). |
| `MOUNT_POINT` (junction) | store root, project dirs, watched source | Unprivileged to create, invisible to `ModeSymlink` checks. Drives §3.8 recursive delete and §3.2 unbounded traversal. **The single highest-value attacker primitive on Windows.** |
| `MOUNT_POINT` (volume mount point) | any directory | Same tag, substitute name `\??\Volume{GUID}\`. Crossing one moves to a different volume — different `VolumeSerialNumber`, different `FileId` space, possibly a non-NTFS filesystem where the security model is not specified. |
| `APPEXECLINK` | `%LOCALAPPDATA%\Microsoft\WindowsApps` | Zero-length "files" that fail to open normally. Reachable if a user watches their profile. Causes `openFileAt`-equivalent errors, and `Size`/`WalkDir` accounting to lie. |
| Cloud files (`CLOUD_*`, `ONEDRIVE`, `STORAGE_SYNC`, `HSM*`) | OneDrive-backed folders, which on default Windows 11 **includes `%USERPROFILE%\Documents` and `Desktop`** | Opening a dehydrated placeholder triggers a network fetch that can block for a long time or fail with `ERROR_CLOUD_FILE_*`. `loadArtifact`'s `filepath.WalkDir` (`store.go:380`) touches every file in an artifact on every listing; a watched OneDrive tree turns a folder page into a mass rehydration. This is a realistic availability and data-egress-cost issue, not a contrived one. |
| `WCI*` / `PROJFS*` (Windows Containers, VFS-for-Git) | dev machines | Directories that materialise on enumeration; tombstones that report as existing but fail to open. Non-deterministic `ReadDir` results. |
| `WOF` / `WIM` / `DEDUP` | compressed or deduplicated volumes | Files that *are* regular for our purposes. Go treats `DEDUP` as regular and everything else unknown as `ModeIrregular` (`$GOROOT/src/os/types_windows.go:210-227`). A blanket "reject all reparse points" rule would make a deduplicated Server volume unusable. |
| `AF_UNIX` | anywhere | Reported by Go as `ModeSocket`. Harmless if classified, an error source if not. |
| Unknown / third-party | anywhere | Must be refused, not guessed. |

(c) Two properties, both needed. First, **never traverse**: `NtCreateFile` with
`OBJ_DONT_REPARSE` fails the whole path with `STATUS_REPARSE_POINT_ENCOUNTERED`
if *any* component is a reparse point (`$GOROOT/src/internal/syscall/windows/at_windows.go:109-110,188`),
which is strictly stronger than `FILE_FLAG_OPEN_REPARSE_POINT`'s final-component
guarantee. Second, **classify from the handle**: `GetFileInformationByHandleEx`
with `FileAttributeTagInfo` returns `FILE_ATTRIBUTE_TAG_INFO{FileAttributes, ReparseTag}`
from an already-open handle, so classification and use share one object. The
policy must be an **allowlist of tags we created and understand**, as the spec's
Risks section already says; everything else is refused with an actionable error.
**[MEASURE M1]** the exact availability of `OBJ_DONT_REPARSE` on the minimum
supported build, and whether it is honoured by the SMB and ReFS redirectors.

### 4.3 Volume mount points

(a) A directory carrying `IO_REPARSE_TAG_MOUNT_POINT` whose target is a volume
GUID path rather than a directory path.

(b) Because the tag is identical to a junction's, any allowlist keyed on
`MOUNT_POINT` accidentally allows volume crossings. Crossing one invalidates the
watcher's identity model (`VolumeSerialNumber` changes,
`watch.go:276-286`'s successor) and can silently move the store's effective
storage onto FAT32/exFAT/ReFS/SMB, where the spec's security guarantees are
explicitly not claimed.

(c) Distinguish by inspecting the reparse *data*, not just the tag: a volume
mount point's substitute name begins `\??\Volume{`. Additionally compare
`FILE_ID_INFO.VolumeSerialNumber` against the root's and refuse a mismatch for
any mutation. **[MEASURE M3]**.

### 4.4 Directory hard links

(a) **They do not exist.** "Hard links can't reference directories, only files,
and they can't reference files on different volumes" (Microsoft Learn, *Hard
Links and Junctions*). `CreateHardLinkW` fails on a directory.

(b) None. Stated explicitly so the ADR does not spend effort defending against a
non-threat, and so nobody mistakes junctions for directory hard links — the same
Microsoft page confusingly says junctions "operate identically to hard links".

(c) N/A. Do not test for it; document the absence.

### 4.5 File hard links

(a) `CreateHardLinkW` creates a second directory entry for the same file on the
same volume. **No privilege is required**, unlike symbolic links.

(b) A1 places a hard link inside a watched source tree pointing at
`%USERPROFILE%\.ssh\id_rsa` or `.aws\credentials`, names it `secret.txt`, and the
unauthenticated web server serves it. There is **no link to detect**: the entry
is a perfectly ordinary regular file with no reparse tag. `openFileAt`'s
`S_IFREG` test (`storefs_linux.go:134`) passes, and its Windows equivalent will
too. Note this is a **pre-existing, cross-platform gap** — the same attack works
today on Linux — but Windows makes it materially worse in two ways: unprivileged
hard link creation is the *cheapest* remaining primitive once symlinks require
Developer Mode, and `defaultIgnores`' credential rules are case-sensitive
(§4.10) while NTFS lookup is not.

(c) There is no clean structural defence; a hard link is the file. Partial
mitigations for the ADR to consider and reject explicitly: refuse files whose
`FILE_STANDARD_INFO.NumberOfLinks > 1`, or refuse files whose
`FILE_ID_INFO.VolumeSerialNumber` differs from the watched root's (does not help
— hard links are same-volume by definition). Recommendation for P1.6: record it
as an accepted residual risk with parity to Linux (RR4), not as a new Windows
regression.

### 4.6 NTFS alternate data streams

(a) `name:stream` and `name:stream:$DATA` address a named stream of a file;
`name::$DATA` names the default stream (i.e. the file itself);
`dir::$INDEX_ALLOCATION` addresses a directory's index. Streams are **not
enumerated** by ordinary directory listing.

(b) `validateSegment` (`store.go:107-120`) rejects `/`, `\`, control characters,
`.`, `..` and over-long names — but permits `:`. Every lookup path uses it: URL
resolution (`ResolvePath`, `OpenDocument`), `existingDir` for
`Delete`/`Unwatch` (`store.go:227-238`), and `notesPath` for note doc paths
(`annotations.go:184-188`). Consequences on NTFS:
- `GET /a/art/index.html:x` opens a stream, not the file. Two distinct logical
  document paths, one underlying object — and note sidecars are keyed **per
  document path** (`annotations.go:174-197`), so `index.html` and
  `index.html::$DATA` get *separate* note sets for the same bytes.
- `GET /a/art/C:evil` is parsed by NTFS as stream `evil` of a file named `C`
  inside the artifact — a drive-letter-looking segment that is not a drive.
- Stream bytes are invisible to `loadArtifact`'s size/mtime walk
  (`store.go:380-386`) and therefore to `maxPreviewBytes`, so an artifact's real
  size can be understated without bound.
- `Publish` is safe here: `ValidateFilePath` → `validateName` → `nameRe`
  (`store.go:82`) excludes `:` from created names, and annotation temp names are
  generated (`annotationfs_linux.go:110`).

(c) Reject `:` in every path segment on Windows, in *lookup* validation as well
as create validation — `filepath.IsLocal` takes exactly this line ("Colons are
only valid when marking a drive letter. Rejecting any path with a colon is
conservative but safe", `$GOROOT/src/internal/filepathlite/path_windows.go:31-35`).
Belt and braces: after opening, confirm via `FILE_STREAM_INFO` or by re-deriving
the name that no stream was selected **[MEASURE M12]**.

### 4.7 8.3 short-name aliasing

(a) NTFS may store an 8.3 alias (`PROGRA~1`, `MYARTI~1`) alongside the long name.
"Windows may also create a short 8.3 form of the name … do not make the
assumption that the 8.3 alias already exists on-disk", and it can be disabled
per volume (Microsoft Learn, *Naming Files, Paths, and Namespaces*).

(b) Any decision made by comparing path *strings* is defeated. Specifically:
- `Watch`'s "already inside the scratchpad" check
  (`strings.HasPrefix(real, realRoot+sep)`, `store.go:610-616`) fails to fire when
  the target is spelled with a short name, letting the user watch the store into
  itself.
- `joinInRoot`'s `filepath.Rel` containment test (`store.go:241-248`) is a string
  test; short names produce a different string for the same object.
- `Visible`'s `filepath.Rel(root, dir)` (`ignore.go:366`) mis-computes the
  relative segment list, so ignore rules and the `.annotations` reserved-name
  check are evaluated against the wrong path.
- `WatchLinkFor` and the watcher's `canonicalDir` map keys likewise.

(c) Never make a security decision by string comparison. Where identity is the
question, use `FILE_ID_INFO` from two handles. Where the canonical spelling is
needed for display, `GetFinalPathNameByHandleW` is appropriate — for display
only. **[MEASURE M6]** whether `GetFinalPathNameByHandleW` returns the long name
when the handle was opened via a short name (expected: yes) and whether
`FILE_NAME_INFO` from the handle agrees.

### 4.8 `\\?\` extended-length and `\\.\` device namespace prefixes

(a) `\\?\` "tells the Windows APIs to disable all string parsing and to send the
string that follows it straight to the file system", which also re-enables `.`
and `..` as literal names and lifts `MAX_PATH`. `\\.\` reaches the device
namespace (`\\.\PhysicalDrive0`, `\\.\COM56`). Both from *Naming Files, Paths,
and Namespaces*.

(b) Direct injection through URL segments is already blocked: `validateSegment`
rejects `\` and the store never joins a user segment at position 0. The live
exposure is **`SCRATCHPAD_ROOT`**, which `Root()` (`store.go:60-69`) returns
unexamined. `SCRATCHPAD_ROOT=\\.\PhysicalDrive0` or `\\?\GLOBALROOT\Device\...`
is accepted today. Secondarily, if the port ever *builds* `\\?\` paths internally
to escape `MAX_PATH` (a very likely temptation, since watched repos have deep
trees), it silently disables the normalisation that other parts of the code
assume has happened — e.g. `filepath.Clean`-based containment.

(c) Validate and canonicalise `SCRATCHPAD_ROOT` once at startup: require a
rooted, non-device, non-UNC local path; open it; and derive everything else from
that handle. Never mix normalised and `\\?\` forms in one comparison.

### 4.9 UNC, drive-relative (`C:foo`), and slash equivalence

(a) `C:tmp.txt` "refers to a file named tmp.txt in the current directory on drive
C", and Windows tracks a separate current directory *per drive letter*
(*Naming Files*). `\foo` is current-drive-relative. `\\server\share\x` is UNC.
Forward and backslash are interchangeable in Win32 paths.

(b) `Root()` is re-evaluated on every call and never made absolute, so with a
drive-relative or bare-relative `SCRATCHPAD_ROOT` the "same" root resolves
differently as the process CWD changes — and the web server, the CLI, and the
watcher all resolve it independently. Slash equivalence means the store's own
`strings.Contains(p, "\\")` guards (`store.go:164`, `ValidateFilePath`
`store.go:512`) are load-bearing on Windows in a way they are not on Linux: a
segment containing `/` would be a *separator*, not a name. `Watch` does call
`filepath.Abs(target)` (`store.go:595`) but resolves it against the process CWD
at that instant, which for a long-running web process is not stable.

(c) Resolve `SCRATCHPAD_ROOT` to a fully-qualified path exactly once, at process
start, and pin a handle. Refuse UNC for security-sensitive mutations per the
spec's Compatibility Policy, with an explicit error rather than a warning.

### 4.10 Case insensitivity and Unicode case folding

(a) NTFS lookup is case-insensitive by default; `NtCreateFile` is issued with
`OBJ_CASE_INSENSITIVE` unless `FILE_FLAG_POSIX_SEMANTICS` is requested
(`$GOROOT/src/internal/syscall/windows/at_windows.go:113-115`). Folding uses the
volume's `$UpCase` table, fixed at format time — it is *not* Go's `strings.ToLower`
and *not* Unicode simple case folding.

(b) This is the most under-appreciated hazard in this codebase, because several
security decisions are exact-string comparisons against names that NTFS will fold:
1. **`.annotations` is reachable.** `Visible` refuses it with
   `rel == "." && name == AnnotationsDir` (`ignore.go:378`) — an exact,
   case-sensitive comparison, and the comment explains this check is deliberately
   *not* in `defaultIgnores` because it must not be `!`-overridable. On NTFS,
   `.Annotations` names the same directory and passes the check, so
   `VisiblePath`/`ResolvePath`/`OpenDocument` will serve note sidecars over HTTP.
   The reserved name is bypassed by changing one letter's case.
2. **Credential ignores are bypassable.** `defaultIgnores` (`ignore.go:42-90`)
   hides `.ssh/`, `.gnupg/`, `.aws/`, `.env`, `.env.*`, `.netrc`, `*.pem` — and
   matching goes through `path.Match` (`ignore.go:169`), which is
   case-**sensitive**. On NTFS a directory named `.SSH` or a file named
   `key.PEM` is the same object to the filesystem but does not match the rule.
   For A1 (a watched tree) this is a direct exposure of credential-shaped files
   through an unauthenticated server.
3. **Cost ignores likewise.** `.GIT/`, `Node_Modules/` are not matched, so a
   watched repo can be made ruinously expensive to walk and watch.
4. **Go-side maps diverge from the filesystem.** `List`'s `visited`
   (`store.go:398-436`), `Watches`' output, the watcher's `registered` and
   `desired` maps (`watch.go:186-220`), and `ignoreCache` (`ignore.go:300`) all
   key on strings. Two spellings of one directory become two entries.
5. **Turkish dotless-i class.** `strings.ToLower`/`ToUpper` are locale-invariant
   in Go, but `İ` (U+0130) lowercases to `i̇` (two runes) and `ı` (U+0131)
   uppercases to `I` in the invariant mapping. Whether NTFS's `$UpCase` folds
   U+0131→`I`, and whether the Kelvin sign U+212A folds to `K`, decides whether
   `notes.md` and `notes.mı` (or `.PEM` vs `.PEΜ`) collide. **[MEASURE M11]**.
6. **Full-width and homoglyph forms** (`ＩＮＤＥＸ.html`, Cyrillic `а`) do *not*
   fold to ASCII, so they are distinct files that render identically in the UI.
   That is a spoofing risk in the card list, not a collision risk.

(c) Do all name comparisons that gate security with a Windows-appropriate
comparison, or better, eliminate them: compare *objects* (`FILE_ID_INFO`) rather
than names. For the specific cases above, `Visible`'s reserved-name check and the
`defaultIgnores` credential rules need case-insensitive matching on Windows
(R11); and note that "case-insensitive" must mean the *filesystem's* folding, so
the only fully correct test is "does opening this name relative to the root yield
the same file id as opening `.annotations`".

### 4.11 Trailing dots and spaces

(a) "Do not end a file or directory name with a space or a period" — Win32 path
normalisation strips them from the final component unless the path is `\\?\`-
prefixed (*Naming Files*).

(b) `checkPortableName` (`names.go:39-51`, landed by P2.5) now rejects trailing
dot/space at **create** time, called from `validateName` (`store.go:96`). Good.
But `validateSegment` (`store.go:107-120`) still accepts them, and every lookup
uses it, so on Windows `GET /a/art.` and `GET /a/art` name the same directory,
and `DELETE /a/art.` deletes `art`. More sharply: the note sidecar path
`.annotations/<doc>.json` is built from a doc string (`annotations.go:174-197`);
`x.html.` and `x.html` are one file on disk but two logical docs. A
`\\?\`-prefixed internal path would make them two files, so the behaviour also
depends on which form the backend uses internally.

(c) Reject trailing dot/space in lookup segments on Windows too (R11), or
normalise them and treat the request as naming the normalised entry — but pick
one and prove it, because the two choices differ observably for `Delete`.

### 4.12 Reserved DOS device names

(a) `CON`, `PRN`, `AUX`, `NUL`, `COM1`–`COM9`, `LPT1`–`LPT9`, plus the ISO-8859-1
superscript forms `COM¹`/`COM²`/`COM³` and `LPT¹`–`LPT³`, are reserved **in every
directory**. Historically "also avoid these names followed immediately by an
extension; for example, NUL.txt and NUL.tar.gz are both equivalent to NUL"
(*Naming Files*). **Since Windows 11 this changed**: reserved names with
extensions are no longer reserved, and the authoritative test is
`RtlIsDosDeviceName_U` — Go's own `isReservedName` says exactly this
(`$GOROOT/src/internal/filepathlite/path_windows.go:98-126`). So the same name is
a device on Windows Server 2022 and a file on Windows 11.

(b) On this code:
- **Create** is covered: `checkPortableName` (`names.go:39-51`) rejects the
  basename before the first dot, case-insensitively, on every OS. Given `nameRe`
  is ASCII-only and excludes `$` and spaces, the superscript and `CONIN$` forms
  are unreachable, so the rule is adequate for created names. (It is slightly
  over-strict — it also rejects `COM0`/`LPT0`, which Microsoft does not list.)
- **Lookup is not covered**, by design (`names.go:36-38`). A URL segment `NUL`
  reaches `openFileAt`; opening `NUL` succeeds and yields a device that reads as
  empty and swallows writes. `GET /a/<watch>/NUL` would serve an empty 200 rather
  than a 404, and `DELETE`/`Unwatch` on a device name has undefined behaviour.
  Non-final segments matter too: `COM1\anything` fails, but with a device in the
  path the failure mode is `ERROR_INVALID_NAME`-shaped, not `ENOENT`-shaped, and
  the store's error mapping must not translate that into "not found" in a way
  that hides it.
- The version dependence means a store created on Windows 11 can contain
  `CON.txt`, which is unreachable on Server 2022. That is a data-portability
  bug, not a containment bug, but it belongs in release notes.

(c) `filepath.IsLocal`/`isReservedName` semantics applied to **lookup** segments
on Windows (R11), and the ADR should state which behaviour is normative — Go's
(which consults `RtlIsDosDeviceName_U`) is the right choice because it tracks the
running OS.

### 4.13 Sharing violations and `FILE_SHARE_DELETE`

(a) "You cannot request a sharing mode that conflicts with the access mode
specified in an existing request that has an open handle. CreateFile would fail
and GetLastError would return `ERROR_SHARING_VIOLATION`. … The sharing options
for each open handle remain in effect until that handle is closed, regardless of
process context." Opening without `FILE_SHARE_DELETE` blocks *others*' delete and
rename (`CreateFileW`, `dwShareMode`).

(b) Two directions, both real:
- *Against us.* Antivirus, Windows Search, Explorer's preview handler, or a
  synced-folder client holds an artifact file or a notes sidecar without
  `FILE_SHARE_DELETE`; our atomic replace (§3.13) or our recursive delete (§3.8)
  fails with `ERROR_SHARING_VIOLATION`. `Delete` currently has no partial-failure
  story — `removeTreeAt` aborts mid-tree (`annotationfs_linux.go:183-186`) and
  `Delete` returns the error after possibly having removed half the artifact.
- *By us.* If our long-lived handles (the pinned root, the pinned `.annotations`,
  the per-document lock files) omit `FILE_SHARE_DELETE`, we prevent the *user*
  from renaming or deleting their own store root in Explorer, which will be read
  as a bug. `.locks/<sha>.lock` files (`annotations.go:150`) are never cleaned up
  and would accumulate held handles.

(c) Open with `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE` everywhere, as
Go's rooted filesystem does (`$GOROOT/src/os/root_windows.go:176`). Define a
bounded retry with backoff for `ERROR_SHARING_VIOLATION` and
`ERROR_LOCK_VIOLATION` (spec: "define retryable error codes, backoff, total
bound"), and surface a distinct, actionable error after the bound — never a
silent skip. **[MEASURE M13]**: which of `ERROR_SHARING_VIOLATION`,
`ERROR_ACCESS_DENIED`, `ERROR_LOCK_VIOLATION`, `ERROR_USER_MAPPED_FILE`,
`ERROR_DELETE_PENDING`/`STATUS_DELETE_PENDING`, and `ERROR_DIR_NOT_EMPTY` are
transient in practice under AV.

### 4.14 Deferred deletion, delete-on-close, and POSIX-semantics delete

(a) Without POSIX semantics, "a file marked for deletion is not actually deleted
until all open handles for the file have been closed and the link count for the
file is zero"; with `FILE_DISPOSITION_POSIX_SEMANTICS`, "the link is removed from
the visible namespace as soon as the POSIX delete handle has been closed, but the
file's data streams remain accessible by other existing handles" (Microsoft
Learn, `FILE_DISPOSITION_INFORMATION_EX`). Between marking and completion the
name exists but new opens fail with `STATUS_DELETE_PENDING`, surfaced as
`ERROR_ACCESS_DENIED`.

(b) The store's create-only contract is built on `EEXIST` being the *only*
meaning of "name taken" (`store.go:561-566`, and its user-facing message "names
are not reusable until the user deletes the old artifact"). On Windows, right
after a delete, re-publishing the same name can fail with
`ERROR_ACCESS_DENIED` (delete pending) rather than `ERROR_ALREADY_EXISTS` — a
different error, a different message, and a transient one.
`TestPublishCreateOnly` (`store_test.go:48-66`) deletes then immediately
re-publishes and would be flaky. A2 can also *hold the window open* by keeping a
handle on any file in the artifact, indefinitely blocking name reuse. And a
`FILE_FLAG_DELETE_ON_CLOSE` handle held by A2 on our temp file turns a successful
annotation write into a silently vanished one.

(c) Use `SetFileInformationByHandle(FileDispositionInfoEx)` with
`FILE_DISPOSITION_DELETE|FILE_DISPOSITION_POSIX_SEMANTICS` so the name leaves the
namespace immediately (Windows 10 1709+ on NTFS/ReFS **[MEASURE M10]** for the
exact minimum build and for behaviour when the file is memory-mapped, where
`STATUS_CANNOT_DELETE` is documented). Map delete-pending explicitly and
retry-with-bound rather than reporting it as "already exists".

### 4.15 The store root itself being replaced mid-operation

(a) A directory handle survives the directory's rename; it does not survive the
directory being deleted and a *new* directory created under the same name.

(b) `Root()` is a string, recomputed per call, and `openRootedFS`/`openAnnotationFS`
each open it independently (`storefs_linux.go:20-35`, `annotationfs_linux.go:23-45`).
A2 that replaces the root between two calls has two different stores in flight in
one process: `visibleSegments` and `Visible` consult one, the handle-anchored
walk consults another. On Linux the pinned-descriptor discipline plus the
store-root `flock` (`annotations.go:119-136`) narrows this to a small window; on
Windows, with no directory lock available (§3.16), the window is wider.

(c) Pin one root handle for the process lifetime, derive every operation from it,
and re-verify `FILE_ID_INFO` against the recorded root identity before any
mutation. Report a mismatch as a hard error ("the scratchpad root was replaced")
rather than silently re-opening.

## 5. Mapping to the spec's Security Test Matrix

The spec's matrix, reproduced verbatim:

| Area | Required cases |
|---|---|
| Root | root missing, root file, root link/reparse point, root replaced during operation |
| Publish | concurrent same-name claim, ancestor replaced, ancestor link, artifact ancestor |
| Browse | one approved watch boundary, nested link, cycle, broken target, target replacement |
| Documents | file link, directory link, alternate stream syntax, case variation, rename race |
| Delete | target replaced, parent replaced, link target untouched, annotation subtree cleanup |
| Notes | annotation root link, intermediate link, concurrent revisions, sharing violation |
| Watch | same-target idempotence, different target collision, junction/reparse variants |
| Names | reserved devices, trailing dot/space, UNC/drive forms, Unicode and case collisions |

### Determinism: what the existing hook can and cannot do

`testStoreOpHook` (`store.go:24-32`) is called from exactly four places, each
placed *after* the parent descriptor is pinned:
`publish-claim` (`store.go:558`), `watch-link` (`store.go:632`), `unwatch`
(`store.go:776`), `delete` (`store.go:840`). Tests install it via
`setStoreOpHook` (`hook_test.go:9-13`) and the canonical use is
`TestPinnedMutationsIgnoreProjectSwap` (`store_test.go:403-432`): the hook
renames the pinned ancestor away and drops a symlink in its place, then the test
asserts the write landed in the *original* directory. That is a proof, not a
timing loop, and every Windows equivalent should be built the same way.

Six of the matrix's cells need a hook that **does not exist yet**. P1.5/P3.12
must add them, in shared code, on the same "after the handle is pinned" rule:

| Proposed hook | Site | Cells it makes deterministic |
|---|---|---|
| `root-open` | after the root handle is pinned in `openRootedFS` (`storefs_linux.go:30`) | Root: root replaced during operation |
| `browse-segment` | after each segment handle in `openBrowsableDir` (`storefs_linux.go:169-196`) | Browse: nested link, target replacement |
| `doc-open` | after the parent is pinned in `openPathFile` (`storefs_linux.go:207`) | Documents: rename race |
| `notes-replace` | between temp write and rename in `writeFile` (`annotationfs_linux.go:134`) | Notes: concurrent revisions, sharing violation |
| `notes-remove` | after the parent is pinned in `removeSubtree` (`annotationfs_linux.go:195`) | Delete: annotation subtree cleanup under race |
| `watch-reconcile` | after `desiredDirs` returns in `reconcile` (`watch.go:181`) | Watch/Browse: directory replaced between enumeration and registration |

Adding hooks in shared (untagged) code keeps the Linux tests and the Windows
tests structurally identical, which is the point.

### Cell-by-cell

**Root.**
- *root missing* — not an attack; assert `EnsureRoot` creates it and the pinned
  handle is re-validated after creation. Deterministic, no hook.
- *root file* — `SCRATCHPAD_ROOT` points at a regular file. On Linux `O_DIRECTORY`
  refuses it. Windows: `FILE_DIRECTORY_FILE` must produce `STATUS_NOT_A_DIRECTORY`.
  Deterministic.
- *root link/reparse point* — root is a symlink, a junction, **or a volume mount
  point**. Linux `O_NOFOLLOW` refuses. Windows must refuse on tag, not on Go's
  `ModeSymlink` (which misses junctions entirely, §3.4). Three separate tests,
  one per tag. Deterministic. Note: a user redirecting `%USERPROFILE%\.scratchpad`
  onto another drive with `mklink /J` is *normal behaviour*, so the ADR must say
  whether this is refused or is an accepted, identified configuration — refusing
  silently will read as a bug.
- *root replaced during operation* — needs the new `root-open` hook. Attack: A2
  renames the root away and creates a new directory of the same name; assert the
  operation completes against the original object or fails, never against the
  new one. Deterministic with the hook.

**Publish.**
- *concurrent same-name claim* — two `Publish` calls race on `CreateDirectoryW`
  relative to one pinned parent; exactly one must win with the other seeing
  "already exists". Deterministic via `publish-claim`. **Windows extra:** a third
  variant where the loser sees `ERROR_ACCESS_DENIED` because a delete is pending
  (§4.14) — must be mapped to a distinct, non-"already exists" error.
- *ancestor replaced* — the direct Windows twin of `store_test.go:403-432`, with
  the replacement being (i) a symlink, (ii) a junction, (iii) a real directory of
  the same name but a different `FileId`. Deterministic via `publish-claim`.
- *ancestor link* — publish under a project path that already contains a symlink,
  a junction, and an unknown-tag reparse point. All three must be refused with
  "project ancestor is a symlink"-equivalent messages. Deterministic.
- *artifact ancestor* — `rejectArtifacts` in `openRealDir` (`storefs_linux.go:81-84`);
  purely logical, already covered by `store_test.go:67-108`. No Windows-specific
  attack beyond case variation of the `.html` suffix test (`storefs_linux.go:50`
  uses `strings.ToLower`, which is not NTFS folding — §4.10).

**Browse.**
- *one approved watch boundary* — assert the counter is exactly one and that the
  boundary is only forgiven for a tag we created. Deterministic.
- *nested link* — the existing `store_test.go:313-347` ported, with a junction
  and an unknown-tag reparse point as extra variants. Deterministic.
- *cycle* — a junction inside a watched tree pointing at an ancestor. Two
  assertions: (i) traversal refuses it, (ii) `List` and the watcher terminate.
  The `List` cycle guard is string-keyed (`store.go:407`), so the case-varying
  cycle from §3.3 belongs here. Deterministic (build the cycle, then call).
- *broken target* — a watch link whose target no longer exists; assert the card
  disappears and nothing panics. Deterministic. **Windows extra:** a cloud-file
  placeholder target that fails with `ERROR_CLOUD_FILE_*` rather than
  "not found" (§4.2) — an error-mapping test, hard to produce deterministically
  without OneDrive; mark as a documented exclusion with a manual check.
- *target replacement* — needs `browse-segment`. Deterministic with the hook.

**Documents.**
- *file link* — `store_test.go:434-460` ported; plus a **hard link** variant,
  which will *pass* (be served) and must be recorded as accepted residual risk
  RR4, not silently omitted.
- *directory link* — a symlink/junction where a file is expected; must be refused
  by the no-reparse open, not by a stat.
- *alternate stream syntax* — `index.html:x`, `index.html::$DATA`, `C:evil`,
  `dir::$INDEX_ALLOCATION` as URL segments; all must 404. Requires NTFS
  (`testutil.RequireNTFS`). Deterministic.
- *case variation* — `/a/ART/index.html` vs `/a/art/index.html`; `.Annotations`
  vs `.annotations` (§4.10 item 1 — this is a live defect, and the test must fail
  before the fix); `.SSH`/`key.PEM` against `defaultIgnores`. Deterministic.
- *rename race* — needs `doc-open`. Deterministic with the hook.

**Delete.**
- *target replaced* — hook `delete`, swap the artifact directory for a junction
  to an external tree between the classification and the removal; assert the
  external tree is intact afterwards. This is the T1 test and it is the single
  most important row in the matrix. Deterministic.
- *parent replaced* — hook `delete`, swap the project directory. Deterministic.
- *link target untouched* — delete a watched folder; assert only the link went
  and the source is byte-identical. Must be run for symlink and, if the ADR
  admits them, junction. Deterministic.
- *annotation subtree cleanup* — publish, add notes, delete, re-publish the same
  name, assert zero inherited notes; plus the racing variant via `notes-remove`.
  Deterministic.

**Notes.**
- *annotation root link* — replace `.annotations` with a symlink/junction; assert
  `openAnnotationFS`'s equivalent refuses (`annotationfs_linux.go:36-40`).
  Deterministic.
- *intermediate link* — a link at `.annotations/<project>`; same. Deterministic.
- *concurrent revisions* — two writers at the same `rev`; exactly one wins with
  `ErrRevMismatch`. Deterministic today via the per-document lock; needs
  `notes-replace` to test the window *inside* the lock. **Windows extra:** with
  no store-root directory lock (§3.16), also test `Delete` racing `SaveNotes`.
- *sharing violation* — open the destination sidecar without `FILE_SHARE_DELETE`
  from a second goroutine/process, then attempt the atomic replace; assert
  bounded retry then a distinct, actionable error, and assert the destination
  still holds the *old* complete content (never truncated, never absent).
  Deterministic (we control the interfering handle). This cell **does not exist
  on Linux** — mark it Windows-only rather than inventing a Linux analogue.

**Watch.**
- *same-target idempotence* — `store_test.go:109-165` ported. The comparison must
  be object identity, not string equality (§3.6). Deterministic.
- *different target collision* — must error, must not overwrite. Deterministic.
- *junction/reparse variants* — for each of `MOUNT_POINT` (junction),
  `MOUNT_POINT` (volume mount point), `APPEXECLINK`, a cloud tag, and a
  synthetic unknown tag: assert `Watches` either lists it correctly or refuses
  it, and that `Unwatch` can always remove whatever `Watches` lists. The
  invariant to assert is **`Watches` ⊆ `Unwatch`-able**; today a junction breaks
  it (§3.4). Creating a synthetic unknown tag needs
  `FSCTL_SET_REPARSE_POINT` with a non-Microsoft tag — **[MEASURE M4]** whether
  that is possible unprivileged; if not, this cell is a documented exclusion.
- **Windows-only cell to add:** *no symlink privilege*. With Developer Mode off,
  `watch` must fail with an actionable `ERROR_PRIVILEGE_NOT_HELD` message and
  publish/serve must keep working (spec acceptance criterion 6). `testutil`
  already supports forcing this with `SCRATCHPAD_TEST_SYMLINKS=0`
  (`testutil.go:24-33`).

**Names.**
- *reserved devices* — create-time is covered by `checkPortableName`
  (`names_test.go`); the missing half is **lookup**: `GET /a/NUL`,
  `DELETE /a/CON`, `/a/COM1/x.html`, `/a/CON.txt` (which is a device on
  Server 2022 and a file on Windows 11 — §4.12), and a device name in a
  non-final segment. Deterministic; assert 404, never a hang or an empty 200.
- *trailing dot/space* — `GET /a/art.`, `DELETE /a/art `, and the notes-doc
  aliasing case `x.html.` vs `x.html`. Deterministic.
- *UNC/drive forms* — `SCRATCHPAD_ROOT` set to `\\server\share\x`, `C:foo`,
  `\foo`, `\\?\C:\x`, `\\.\PhysicalDrive0`. Assert an explicit refusal or an
  explicit unsupported warning, never silent operation. Deterministic for the
  syntactic cases; the live-SMB case is an exclusion (no share in CI).
- *Unicode and case collisions* — `Publish("Report")` after `Publish("report")`
  must fail create-only; `.Annotations` must be unreachable; the `$UpCase`
  questions in §4.10 need a probe test that *reports* the volume's folding
  rather than asserting a guess (**[MEASURE M11]**). Full-width/homoglyph names
  are a UI-spoofing note, not a containment test — record, do not block.
- **Concept that does not exist on Windows:** there is no case where a *Linux*
  store can hold a name Windows cannot address once `checkPortableName` is in
  place, so no reverse-direction test is needed. Conversely, a name containing
  `:` or a trailing dot can exist on Linux and not on Windows — but the store
  never creates such names, only watched sources do, and those are
  platform-native by construction.

## 6. Ranked residual risks

Severity is impact × reachability for the beta's stated posture (loopback
default, unauthenticated, single user).

> **Post-implementation status (added by P6.2/P6.3).** This table is the
> Phase 1 assessment and is left as written; the authoritative *current*
> disposition of every risk is the ranked register in
> [reviews/P6.2-threat-model-audit.md](reviews/P6.2-threat-model-audit.md) §6,
> which reconciles it against the shipped code. One row is added below —
> **RR14** — because §4.7(b) named `Visible` as defeated by an 8.3 alias and
> the ranked table never carried a row for it, so the risk was real,
> documented in prose, and dispositioned nowhere. That gap was found by P6.3
> F1 rather than by this document, three phases later.

| # | Risk | Severity | Acceptable for beta? |
|---|---|---|---|
| RR1 | **Recursive delete through a reparse point.** If `removeTreeAt`'s Windows twin classifies by directory attribute instead of reparse tag, `Delete` destroys an arbitrary tree outside the store. Reachable by A1 (junction in a watched source), A2, and A3 (viewer JS calling `DELETE /a/...`). Irreversible data loss. | **Critical** | **No.** Blocks the beta. Must have a passing deterministic test (Delete/target replaced) before any Windows binary ships. |
| RR2 | **Unbounded reparse traversal on the browse path.** `openBrowsableDir`'s single-boundary counter has no meaning if opens follow reparse points; the unauthenticated server becomes an arbitrary local file reader. | **Critical** | **No.** Blocks the beta. |
| RR3 | **Path-string re-resolution anywhere in a mutation.** Any surviving check-then-use turns every §4.1 race into an escape. | **High** | **No.** This is acceptance criterion 10; the P3.13 trace review must find zero. |
| RR4 | **Hard links inside a watched source are served.** No structural defence exists; identical on Linux today. | **Medium** | **Yes**, with an explicit note in the README and release notes: *watch only trees you trust*. Windows makes it cheaper (no privilege needed) but not newly possible. |
| RR5 | **Case-folding bypass of `Visible`'s `.annotations` guard and of `defaultIgnores` credential rules** (§4.10). Exposes note sidecars and credential-shaped files in watched trees over an unauthenticated server. | **High** | **No** for the `.annotations` guard (one-line class of fix, R11). **Yes, with documentation**, for the ignore-rule folding *if* the fix proves risky — but it is a small change and should land. |
| RR6 | **No Windows equivalent of the store-root `flock`** (§3.16). `Delete` can race `SaveNotes`; notes can be resurrected onto a re-used name. | **Medium-High** | **Yes, if** the ADR names the chosen substitute, bounds the window, and adds the racing test. **No** if the answer is "drop the lock". |
| RR7 | **`SCRATCHPAD_ROOT` accepts device, UNC and drive-relative forms** (§4.8, §4.9). Self-inflicted, needs the user to set it, but silently produces an unsupported configuration. | **Medium** | **Yes**, if it fails loudly (R18). |
| RR8 | **ADS aliasing via `:` in lookup segments** (§4.6). Splits one document into several logical paths, splits note sidecars, understates sizes. No containment escape found. | **Medium** | **Yes** short-term; the fix (R11) is cheap enough that it should land anyway. |
| RR9 | **Availability: watcher/`List` DoS from a hostile watched tree** (§3.3, §3.17). A case-varying junction cycle or an unexpected error kills the web process, which is fatal by design. | **Medium** | **Yes** for the beta — it is availability only, and the supervisor restarts — provided R16 (bounded recursion, error triage) lands. |
| RR10 | **Cloud-file placeholders cause mass rehydration** when a OneDrive-backed folder is watched (§4.2). Default Windows 11 puts Documents and Desktop there. | **Low-Medium** | **Yes**, documented, with a follow-up to make `loadArtifact`'s tree walk skip `FILE_ATTRIBUTE_RECALL_ON_*` files. |
| RR11 | **Reserved device names and trailing dots reachable through lookup paths** (§4.11, §4.12). Odd responses, no escape. | **Low** | **Yes**, if R11 lands; otherwise document. |
| RR12 | **8.3 alias defeats `Watch`'s "already inside the scratchpad" guard** (§4.7), allowing a self-watching store. | **Low** | **Yes** — the consequence is a confusing recursive listing, bounded by the cycle guard, not an escape. |
| RR13 | **A4 (shared/redirected store path).** Explicitly out of scope. | **N/A** | **Yes**, by scope. Document that the store root must be a directory only the user can write. |
| **RR14** | **8.3 alias defeats `Visible`'s `.annotations` reserved-name guard and every `defaultIgnores`/`.scratchpadignore` rule** for a requester-supplied path segment (§4.7(b), which named `Visible` but got no RR row of its own). Distinct from RR12: same aliasing mechanism, different consumer, and unlike RR12 this one is **not** bounded by the cycle guard. Exposure is the `.annotations` tree plus markdown and artifact **content** inside any ignore-hidden directory. | **Medium** (Medium-High under `LAN=1`) | **Added post-hoc and CLOSED, not accepted.** Raised as P6.3 F1; the 8.3 alias was measured to resolve through the store's own `RootDirectory`-relative open; fixed by `canonicalLookupName`. See P6.2 §10. |

### 6.1 Measured, not hypothetical — a deliberately weakened build escaped

Added after the AC5 migration (P6.2 §11.6). Every claim in §6 above is a
prediction about what *would* happen if a guard were absent. During the AC5
migration each new test was falsified against a build with that guard actually
removed, on real Windows — CI run
[32999441103](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32999441103),
on a scratch branch since deleted. Two results are worth promoting out of the
test file, because they convert §6 entries from reasoning into observation.

**1. The annotation backend escaped the store.** With `annotationFS.openDir`
made to follow reparse points — a raw `ntOpenAt` with neither
`FILE_OPEN_REPARSE_POINT` nor `OBJ_DONT_REPARSE` — all four notes verbs accepted
a junction as an `.annotations` path component, and the leak assertion fired:

> `the annotation backend wrote through the junction: [- index.html.json]`

The broken build **wrote a note sidecar into an external tree through a
junction.** This is §3.13's hazard demonstrated end to end rather than argued,
and it is the only place on this branch where a weakened build was observed
performing an escape rather than merely failing to refuse one. Note what it
implicates: not `Delete`, not the browse path, but the *annotation write* — the
operation §3.13 flagged and which the spec's "Annotation atomicity" section
does not discuss containment for at all (§9 item 4). Both link flavours
discriminate: the pre-existing `TestAnnotationSymlinkComponentsRejected` failed
under the same breakage.

**2. A mode-shaped guard misses both junction and unknown-tag roots.** With the
store-root check made mode-shaped instead of tag-shaped — i.e. refusing only
`IO_REPARSE_TAG_SYMLINK` — `TestRootMustNotBeAReparsePoint` failed on *both* the
junction and the unknown-tag flavour. The unknown-tag directory is invisible to
`os.Lstat`'s mode bits entirely: it reports plain `ModeDir`. That is §4.2's
table and §4.10(c)'s "compare objects, not names" argument shown directly, and
it is **RR1's second vector** — the one a reader is most likely to under-rate,
because the junction case is the memorable one and the unknown-tag case is the
one no mode-based check can ever see.

Neither result changes a disposition in §6. Both raise the confidence attached
to RR1 and RR2 from *argued* to *observed*, and both are the reason the tag-
versus-mode distinction (ADR §5.2, §7.4) should be treated as load-bearing
rather than stylistic.

## 7. Requirements on the backend

Numbered, falsifiable properties. P1.6's ADR is judged against this list; each
item is written so that a reviewer can point at a test, a code site, or a
measurement and say "met" or "not met".

**R1.** Every path resolution that precedes a mutation is performed one segment
at a time, each segment opened relative to a handle retained from the previous
segment. *Falsifier:* any mutation whose final Win32 call receives a path with
more than one component.

**R2.** No security decision is made by comparing path strings. *Falsifier:* any
`strings.HasPrefix`, `filepath.Rel`, or `==` on paths that gates a create,
delete, rename, or serve decision on Windows. (`joinInRoot` (`store.go:241-248`)
and `Watch`'s root-containment check (`store.go:610-616`) are the two existing
sites that must be re-expressed or demoted to advisory.)

**R3.** Reparse points are refused for every path component of every mutation,
using a mechanism that covers *intermediate* components, not only the final one.
*Falsifier:* a design that relies solely on `FILE_FLAG_OPEN_REPARSE_POINT`.

**R4.** Reparse tags are handled by an **allowlist** of tags the application
creates and understands. Every other tag — including tags the OS does not
document — is refused with a distinct error. *Falsifier:* any `default:` branch
that treats an unrecognised tag as a directory or a regular file.

**R5.** Link classification never uses Go's `fs.ModeSymlink` on Windows. The
tag comes from `FILE_ATTRIBUTE_TAG_INFO` on an open handle. *Falsifier:* a
`Mode()&ModeSymlink` test in any Windows-selected file, given
`$GOROOT/src/os/types_windows.go:206-230` excludes junctions.

**R6.** `Publish` and `Watch` claim a name with a single atomic create issued
relative to a pinned parent handle, and report collision distinguishably from
delete-pending and from permission failure. *Falsifier:* an existence check
followed by a create; or a design where `ERROR_ACCESS_DENIED` from
`STATUS_DELETE_PENDING` is surfaced as "already exists".

**R7.** `Unwatch` removes only a link, and only a link of an allowlisted tag, and
never falls back to a recursive removal. *Falsifier:* any code path from
`Unwatch` that can reach a tree walk.

**R8.** Recursive removal walks by handle, classifies each entry from that
handle, and unlinks (never descends) anything carrying a reparse tag.
*Falsifier:* a `RemoveDirectoryW`/`DeleteFileW` on a rebuilt path string, or a
descent decided by `FILE_ATTRIBUTE_DIRECTORY` alone.

**R9.** The annotation write remains a single atomic replacement issued relative
to a pinned parent handle. It is never decomposed into remove-then-rename, and on
any failure the destination still holds complete prior content. *Falsifier:* a
test that kills the writer between the two steps and finds no sidecar.

**R10.** Transient failures (`ERROR_SHARING_VIOLATION`, `ERROR_LOCK_VIOLATION`,
delete-pending) are retried with a **documented, bounded** backoff, then reported
with a distinct actionable error. *Falsifier:* an unbounded retry, a silent
skip, or a retry that also swallows a permanent error.

**R11.** Windows lookup-path validation rejects, in every segment: `:`, a
trailing dot or space, and reserved DOS device names as the running OS defines
them. Independently, `Visible`'s `.annotations` reserved-name check and
`defaultIgnores` matching are case-insensitive on Windows. *Falsifier:*
`GET /.Annotations/...` returning anything but 404; `GET /a/art/index.html:x`
returning content; a watched `.SSH` directory appearing in a folder page.

**R12.** The store root is resolved to a fully-qualified local path **once**, at
process start, and pinned as a handle for the process lifetime. Every operation
derives from that handle. *Falsifier:* any second call to `Root()` whose result
reaches a Win32 API.

**R13.** Before any mutation, the pinned root's `FILE_ID_INFO` is re-verified
against the identity recorded at pin time, and a mismatch is a hard error.
*Falsifier:* a mutation that proceeds after the root was replaced.

**R14.** Directory identity for the watcher is `VolumeSerialNumber` + 128-bit
`FileId` obtained **from the handle the watch is registered on**, not from a
separate path lookup. *Falsifier:* an `identity(path string)` signature surviving
into Windows code.

**R15.** All long-lived handles are opened with
`FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE`, and every handle is closed
on success and on every error path. *Falsifier:* a handle-count test that grows
under `go test -count=20`; a user unable to delete their own store root in
Explorer while the server runs.

**R16.** Every recursive walk (`List`, `desiredDirs`, `walkAnnotationDir`,
`removeTreeAt`) has an explicit depth bound and a visited set keyed on **file
identity**, not on a path string; and a `reconcile` failure is triaged into
"retry" versus "fatal" rather than being uniformly fatal. *Falsifier:* a
hostile watched tree that can crash or hang the web process.

**R17.** Every security-relevant race in the matrix is exercised through a
deterministic hook placed after the relevant handle is pinned, in shared
untagged code. Where only a timing loop is possible, the ADR names the cell and
says why. *Falsifier:* a matrix cell whose only test is `for i := 0; i < N; i++`.

**R18.** An unsupported storage configuration (non-NTFS volume, UNC/SMB path,
device namespace, drive-relative root) produces an explicit refusal or an
explicit unsupported warning before the first mutation — never silent operation.
*Falsifier:* `SCRATCHPAD_ROOT=\\server\share\x` working quietly.

**R19.** Watch failure never disables publish-only operation, and the failure
message names Developer Mode without recommending elevation as the default.
*Falsifier:* acceptance criterion 6 failing with `SCRATCHPAD_TEST_SYMLINKS=0`.

**R20.** No untagged (shared) Go file imports `x/sys/unix`, references
`syscall.Stat_t`, or embeds a mechanism that only one OS can satisfy. Shared
files hold policy; platform files hold mechanism. *Falsifier:* `grep` over
untagged files; also the reverse — a *policy* decision (what is an artifact, what
is visible, what names are allowed) duplicated into a platform file.

## 8. Open questions the P1.2 spike must measure

These are stated as measurements, not assumptions. Each should end in the ADR as
a recorded fact with the probe that produced it.

- **M1.** `OBJ_DONT_REPARSE` — minimum OS build on which it is honoured; whether
  `STATUS_REPARSE_POINT_ENCOUNTERED` is returned for *intermediate* components as
  well as the final one; behaviour on ReFS and over SMB.
- **M2.** The exact Win32/NT error returned when a no-reparse open meets each tag
  class (symlink, junction, volume mount point, APPEXECLINK, cloud placeholder,
  WCI, unknown), and how Go maps each (`ELOOP`? `ENOTDIR`? raw `Errno`?).
- **M3.** Whether a volume mount point can be distinguished from a junction
  without parsing reparse data — e.g. whether `FILE_ID_INFO.VolumeSerialNumber`
  from a handle opened *through* it differs from the parent's.
- **M4.** Whether an unprivileged process can set a **non-Microsoft** reparse tag
  with `FSCTL_SET_REPARSE_POINT` (needed to test the "unknown tag" cell at all).
- **M5.** Whether `filepath.EvalSymlinks` on Windows canonicalises **case** via
  `normBase`/`FindFirstFile`, and what it returns for a path traversing a
  junction. This decides whether `List`'s cycle guard (`store.go:407`) is sound.
- **M6.** Whether `GetFinalPathNameByHandleW` and `FILE_NAME_INFO` return long
  names for handles opened through an 8.3 alias, and which volume-name flag
  (`VOLUME_NAME_DOS` vs `VOLUME_NAME_GUID`) is stable across mount changes.
- **M7.** Whether a directory handle survives — and continues to name the same
  object after — a rename of that directory and of each of its ancestors, when
  opened with `FILE_SHARE_DELETE`.
- **M8.** Whether `CreateSymbolicLinkW` (and its handle-relative equivalent,
  `windows.Symlinkat` / `NtCreateFile` + `FSCTL_SET_REPARSE_POINT`) fails
  atomically when the name already exists, i.e. whether it is a true `O_EXCL`
  analogue; and the exact error.
- **M9.** Whether `SetFileInformationByHandle(FileRenameInfoEx)` honours
  `FILE_RENAME_INFO.RootDirectory` for a *relative* `FileName`, or whether only
  `NtSetInformationFile` does. Also the minimum build for
  `FILE_RENAME_FLAG_POSIX_SEMANTICS`. This decides whether §3.13's atomic replace
  can be made handle-relative at all.
- **M10.** Minimum build and filesystem support for
  `FILE_DISPOSITION_INFO_EX` / `FILE_DISPOSITION_POSIX_SEMANTICS`, and the
  behaviour when the target is memory-mapped (`STATUS_CANNOT_DELETE`).
- **M11.** The volume's actual case-folding behaviour: probe `i`/`I`, `ı`
  (U+0131), `İ` (U+0130), `K`/`K` (U+212A), `ß`/`ẞ`, and full-width `Ａ`. Report
  what NTFS folds; do **not** assume Go's `ToLower` agrees.
- **M12.** Whether an open that reached an alternate data stream can be detected
  after the fact from the handle (`FILE_STREAM_INFO`, `FILE_NAME_INFO`), as a
  defence in depth behind rejecting `:`.
- **M13.** Under a real antivirus and Windows Search, the observed distribution
  of transient errors on rename-over and on recursive delete, and how long a
  bounded retry must be to reach acceptable reliability.
- **M14.** Whether `LockFileEx` works on a directory handle opened with
  `FILE_FLAG_BACKUP_SEMANTICS` (expected: no). If not, what the replacement
  rendezvous for `lockAnnotations` (`annotations.go:119-136`) is, and how it
  preserves the property that the lock survives `.annotations` being replaced.
- **M15.** `fsnotify` on Windows: whether `ReadDirectoryChangesW`-based watches
  survive a rename of the watched directory; what happens on buffer overflow and
  whether an overflow is reported at all; whether a *populated* subtree created
  atomically (e.g. by a rename) produces one Create event or none.
- **M16.** Whether `os.File.ReadDir` on a handle opened with
  `FILE_LIST_DIRECTORY` behaves as `readDirFD` (`annotationfs_linux.go:142-150`)
  needs — in particular whether a duplicated handle can be enumerated
  independently.
- **M17.** Whether Go's `os.Root` (`OpenRoot`, `$GOROOT/src/os/root_windows.go`)
  provides the required properties out of the box — it already uses
  `OBJECT_ATTRIBUTES.RootDirectory` and `OBJ_DONT_REPARSE` — and, if so, exactly
  which of R1–R9 it satisfies and which it does not (it *follows* symlinks that
  stay inside the root, `$GOROOT/src/os/root.go:41-43`, which is **not** what
  `openBrowsableDir` wants). Measuring this is not the same as choosing it; the
  choice is P1.6's.
- **M18.** Whether `RtlIsDosDeviceName_U` behaviour for `CON.txt` differs across
  the supported OS matrix (Windows 11 vs Server 2019/2022), and what the store
  should do about a store created on the permissive one and read on the strict
  one.

## 9. Where the spec's stated approach already looks unsound

Raised here so P1.6 either fixes the spec or records why not.

1. **The spec's "Fallback" containment strategy cannot be made sound for
   creation.** It proposes "no-follow handle opens, stable file IDs, canonical
   final paths, and pre/post-operation identity checks … acceptable only if the
   security review demonstrates that rename/reparse races cannot redirect a
   destructive or write operation." Pre/post identity checking is a
   check-then-use pattern by construction: it can *detect* that something moved,
   but `Mkdirat`-style atomic name claiming has no verify-after equivalent — by
   the time you notice, the directory exists somewhere. The fallback should be
   struck for **mutations** and, if kept at all, restricted to read paths where a
   post-check can safely turn into "return not-found".

2. **`GetFinalPathNameByHandleW` is listed among the containment primitives.** It
   returns a *string* that must be re-resolved by the OS, so any use of it as an
   input to a subsequent operation reinstates the TOCTOU the handle removed. It
   is a diagnostics and display primitive. The spec should say so.

3. **The spec never mentions `fdPath`** (`storefs_linux.go:41`, `/proc/self/fd/N`),
   yet it is load-bearing at `store.go:475` and `store.go:580` — it is how
   handle-anchored code re-enters the path-based `loadArtifact`. Windows has no
   equivalent, and (2) rules out the obvious substitute. This is the single
   hardest porting constraint in the store and it is absent from "Current
   Portability Boundaries".

4. **The spec never mentions the annotation lock** (`annotations.go:119-136`).
   "Annotation atomicity" covers temp-file replacement and revision conflicts but
   not the `flock` on the **store-root descriptor** that makes `Delete` and
   `SaveNotes` mutually exclusive. Windows cannot lock a directory handle
   (**M14**), so this needs a design, and the naive substitute — a lock file
   inside `.annotations` — reintroduces exactly the replacement hazard the
   existing comment (`annotations.go:124-126`) says it avoids.

5. **"Name semantics" tightens create-time names only, and says lookup "remains
   looser".** That is right for reachability but leaves the entire Windows
   aliasing surface open, because every hazard that matters here — `:`/ADS,
   trailing dots, reserved devices, case folding — arrives through
   `validateSegment` (`store.go:107-120`), which is what URL resolution,
   `Delete`, `Unwatch` and note doc paths all use. P2.5 has correctly landed the
   create-time half (`names.go`); the matrix's "Names" row cannot be closed
   without the lookup half (R11).

6. **Invariant 5 and the reparse-tag policy are in tension over the watched
   source.** The spec says "prefer a directory symbolic link" for links *we*
   create and "treat junctions and other reparse points as untrusted". But real
   Windows checkouts contain junctions, cloud placeholders and (on dev machines)
   WCI/ProjFS reparse points that we did **not** create. The spec never states
   what browsing does when it meets one *inside* a watched tree. "Refuse" is
   defensible; "refuse silently" is not, because it will look like missing
   content. This needs an explicit rule and a user-visible explanation.

7. **The junction fallback is scoped too narrowly.** P1.4 frames junctions as a
   link-*creation* fallback. On Go ≥1.23 a junction is neither `ModeSymlink` nor
   `ModeDir` (`$GOROOT/src/os/types_windows.go:206-230`), so adopting junctions
   also requires rewriting `entryIsDir` (`store.go:316-325`), `annotate`
   (`store.go:328-337`), `List`'s link test (`store.go:417`), `Watches`
   (`store.go:722`), `WatchLinkFor` (`store.go:691`), and the watcher's link
   handling (`watch.go:252-261`) — otherwise a junction-based watch is created
   but is invisible to listing and unremovable by `Unwatch`. That cost belongs in
   the fallback decision.

8. **"NTFS local volumes are supported; other filesystems … return an explicit
   unsupported warning."** Two problems. First, the filesystem cannot be known
   before the root is opened, so the warning necessarily comes after the first
   handle exists — fine, but the ADR should say the *first mutation* is the gate,
   not the first open. Second, ReFS is not exotic: Dev Drive on Windows 11 is
   ReFS and is exactly where developers put source trees. Refusing ReFS outright
   will refuse a large fraction of watched sources. ReFS supports symbolic links
   (per `CreateSymbolicLinkW`'s technology table) but has different hard-link and
   file-ID characteristics; the ADR should decide ReFS explicitly rather than let
   it fall into "other".

9. **The matrix has no "no symlink privilege" row**, although acceptance
   criterion 6 requires that behaviour. Added in §5 as a Windows-only cell.

10. **The matrix's "sharing violation" cell has no Linux analogue** and its
    "annotation root link" cell means something different on Windows (a link is
    one of several reparse tags). The spec's framing "release blockers on both
    Linux and Windows **where the concept exists**" is right; §5 now says
    explicitly which cells are Windows-only and which are excluded.
