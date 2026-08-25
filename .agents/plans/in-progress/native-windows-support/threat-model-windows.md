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
