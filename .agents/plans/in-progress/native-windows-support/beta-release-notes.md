---
title: Native Windows Support — Beta Release Notes (draft)
status: draft
created: 2026-08-26
links: [./native-windows-support.md, ../../../spec/native-windows-support.md, ../../../ADRs/2026-08-26-windows-rooted-store-backend.md]
---

# Native Windows Support — Beta

Draft for task P6.7. Not published until the P6.8 human gate.

`scratchpad.exe` and `scratchpad-web.exe` are native Windows binaries. No WSL,
no container, no Unix compatibility layer, and no administrator rights.

## Supported

- **Windows 11** and supported **Windows Server** releases.
- **`windows/amd64`** and **`windows/arm64`**.
- **Local NTFS volumes only.**
- Data root `%USERPROFILE%\.scratchpad`, overridable with `SCRATCHPAD_ROOT`.
- Loopback (`127.0.0.1:8737`) by default, as on every platform.

Everything the Linux build does, the Windows build does: publish, list, watch,
unwatch, delete, notes, the web site with sandboxed previews, live SSE refresh,
markdown rendering, and `.scratchpadignore`.

## Beta status

Windows support is **best-effort beta until two releases complete without a
critical containment defect**. Linux behaviour and the container deployment are
unchanged by this work — except where it fixed defects that affected Linux too
(see *Security fixes*, which are not Windows-specific).

## Link capability — you do not need Developer Mode

`watch` links an external folder into the store using a Windows reparse point:

- **Developer Mode on** (or you hold `SeCreateSymbolicLinkPrivilege`) → a
  directory **symbolic link**.
- **Developer Mode off**, ordinary user → a **junction**.

`watch` chooses automatically and needs no elevation. This was measured on a
real runner with the privilege genuinely removed from the process token and
Developer Mode genuinely cleared in the registry, not simulated: row 2 produced
`IO_REPARSE_TAG_SYMLINK`, row 3 produced `IO_REPARSE_TAG_MOUNT_POINT`, and the
"no link privilege" error path was never reached.

**Do not run scratchpad elevated.** Nothing requires it.

Only links the store itself creates are trusted. Any other reparse tag is
refused rather than followed, and a nested reparse point inside a watched
source is never browsed or listed.

## Security posture

The Windows backend is not a path-string port. Every mutation is issued against
a directory **handle pinned before the check that authorised it**, so renaming
or substituting an ancestor cannot redirect it. An independent review traced all
nine mutating entry points and found **no validate-then-open path**.

Two things worth stating plainly because they are counter-intuitive:

- **`OBJ_DONT_REPARSE` is not the containment primitive.** It is inert for
  non-Microsoft reparse tags — the refusal comes from no filter driver claiming
  the tag, not from containment. The primitive is a strict open:
  `FILE_OPEN_REPARSE_POINT` plus a `FILE_ATTRIBUTE_TAG_INFO` read from the same
  handle, refusing by tag value against an allowlist.
- **No path-prefix comparison is used for containment anywhere.** 8.3 short
  names alone make it unsound: `GetFinalPathNameByHandleW` and `FILE_NAME_INFO`
  can return different spellings for the same object.

The design and its measured evidence are in
`.agents/ADRs/2026-08-26-windows-rooted-store-backend.md`.

## Security fixes in this release — these affected Linux too

Porting forced every containment invariant to be re-derived, which surfaced
defects in code that had already shipped. **These are not Windows bugs**; they
were live on Linux and are fixed for both platforms.

- **Ignore rules could be disabled entirely.** A single `.html` file placed
  directly in the store root made every lookup bypass the hard-coded
  `.annotations` guard, all built-in ignores (`.env`, `*.pem`, `.ssh/`, `.git`,
  `node_modules`) and every `.scratchpadignore`. Reproduced end to end: an
  unauthenticated `GET /a/.git/secret.md` returned the file, and an
  unauthenticated recursive `DELETE` succeeded. Listing was unaffected, so
  nothing looked wrong in the UI.
- **Browsing could escape a watched folder.** Crossing the watch boundary
  re-opened the link target as a path string with `O_NOFOLLOW`, which guards
  only the final component. Replacing an *ancestor* of the watch target — for
  example a `git checkout` swapping a tracked directory for a symlink inside a
  watched repository — redirected browsing into the attacker's tree, readable
  over the unauthenticated HTTP endpoint. Not a race: ancestors were never
  validated at all.
- **Credential ignore rules did not match on NTFS.** Ignore matching was
  byte-exact, so `.SSH`, `key.PEM` and `Node_Modules` did not match their
  built-in rules on a case-folding volume.
- **Deep artifacts were permanently undeletable.** Removal was depth-bounded
  while creation was not, so the store could create trees it then refused to
  delete.
- **One unreadable directory could stop the server from starting**, on both
  platforms, in a supervisor restart loop.

Anyone running scratchpad on Linux should treat the first two as the reason to
upgrade.

## Behaviour differences from Linux

All unavoidable, all documented in `docs/windows.md`:

- **Case-insensitivity.** NTFS folds case, so `Report` and `report` collide,
  `/a/REPORT/` resolves, ignore rules and the `.annotations` reservation fold,
  and case-variant document paths share one note set.
- **Lookup refuses more.** Reserved device basenames, trailing dot or space,
  and `:` (an NTFS stream selector) are refused when *looking up* a name on
  Windows, not only when creating one. An entry a watched repository named
  `CON` is reachable on Linux and not on Windows.
- **Watch links may be junctions**, which Go reports as `ModeIrregular` rather
  than `ModeSymlink`.
- **UNC paths and the device namespace are refused** for mutations rather than
  warned about.

The create-time name rule — reserved device basenames with any extension, and
names ending in a dot or space — now applies on **every** platform, including
Linux, so a store stays movable. That is a small narrowing of what Linux
previously accepted.

## Known limitations

- **ReFS, including Dev Drive: untested.** The containment primitives are
  documented for ReFS but were never verified there — CI runners expose only
  NTFS. This is the most significant gap, because a Dev Drive is exactly where
  many developers keep source trees. Keep the store on NTFS; watching a source
  tree that lives on a Dev Drive is a different question and also unverified.
- **SMB / UNC: refused** for mutations.
- **FAT/exFAT: unsupported** — the POSIX-semantics delete and rename this design
  relies on are documented as unavailable there.
- **Antivirus interference is bounded, not measured.** Transient sharing
  violations are retried within a bound and then reported as a real error. The
  retry set comes from documentation, not from a measured distribution — a CI
  runner's Defender state is not representative.
- **Cloud placeholder files (OneDrive) are skipped, not rehydrated**, and were
  never exercised on a runner.
- **A genuinely non-elevated interactive session** has not been exercised. CI
  runners execute elevated; the privilege dimension was simulated faithfully,
  the full ACL environment was not. In particular, **per-user Scheduled Task
  registration by a standard user — the logon task that `install.ps1
  install`/`startup` registers — is not verified anywhere**: hosted runners
  deny the registration to CI's secondary non-admin account (`Access is
  denied`), so CI proves the installer's documented denial path (actionable
  error, foreground fallback, clean exit) rather than the registration
  itself. A pre-beta manual check on a real machine is owed — see the
  P5.1/P5.2/P5.5 record in `EXECUTION.md` for what "pass" looks like.
  Everything else about the task (register/start/status/stop/remove, exe-lock
  retry, loopback binding) is CI-verified for the runner's primary user.
- **`ReadDirectoryChangesW` overflow recovery** is implemented but was never
  reproduced deterministically.
- **No code signing and no MSI.** SmartScreen may warn. Verify archives against
  `SHA256SUMS.txt`.

## Install

```powershell
.\scripts\install.ps1 install    # CLI + skill + a per-user logon Scheduled Task
.\scripts\install.ps1 uninstall  # removes only what it installed
```

Startup is a **per-user logon Scheduled Task**, not a Windows Service: a service
would typically need elevation and run under a different profile than the
`%USERPROFILE%` data root. Foreground execution is fully supported.

**No installer operation touches the data root, uninstall included.**

## Rollback

Windows additions are isolated behind build tags and additive scripts. If a
critical containment issue appears:

1. Stop publishing Windows release assets and mark the affected versions.
2. Linux binaries and container releases are unaffected and stay published.
3. Disable only the unsafe Windows operation if publish-only behaviour remains
   demonstrably safe; otherwise withdraw the Windows binaries entirely.
4. Store data and the annotation format are unchanged, so a repaired binary
   resumes without migration.
5. Add the case to the permanent security matrix before re-release.

Note that the security fixes above are **not** Windows-specific and should not
be rolled back with the Windows binaries.

## Verifying a download

```powershell
Get-FileHash .\scratchpad-windows-amd64.zip -Algorithm SHA256
```

Compare against `SHA256SUMS.txt` in the release. Binaries are unsigned.
