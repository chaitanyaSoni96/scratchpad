# Windows

Native Windows support: `scratchpad.exe` and `scratchpad-web.exe` are real
Windows binaries. No WSL, no container, no Unix compatibility layer, and no
administrator rights.

**Beta.** Windows support stays beta until two releases complete without a
critical containment defect. Linux behaviour and the container deployment are
unchanged.

## Requirements

- Windows 11 or a supported Windows Server release, on a **local NTFS volume**.
- `windows/amd64` or `windows/arm64`.
- No elevation. Nothing here needs an administrator prompt.

Other filesystems are not supported yet. ReFS (including **Dev Drive**), SMB
shares and FAT/exFAT are untested — see [Unsupported filesystems](#unsupported-filesystems)
below, which matters more than it sounds, because a Dev Drive is exactly where
many people keep source trees.

## Install

Download `scratchpad_<version>_windows_<arch>.zip`, verify it (below), unzip
it, and run the installer from the unzipped folder. `install.ps1` sits at the
top of the archive next to the two `.exe`s — there is no `scripts\`
subdirectory in a release:

```powershell
# CLI only
.\install.ps1 cli

# CLI + agent skill + cleanup of obsolete MCP registrations
.\install.ps1 all

# ...and register the web server to start at logon
.\install.ps1 install
```

From a git checkout the same script lives at `scripts\install.ps1` and reads
the binaries you built into `bin\`; it accepts either layout.

Windows blocks unsigned scripts that carry the download mark, so the first run
may fail with *"cannot be loaded"* or *"running scripts is disabled on this
system"*. Clear the mark on the extracted files and run in a session that
allows local scripts — no permanent policy change, no elevation:

```powershell
Get-ChildItem -Recurse -File | Unblock-File
powershell -ExecutionPolicy Bypass -File .\install.ps1 install
```

Nothing else needs to run. The installer is the only script involved: it copies
the two `.exe`s and `SKILL.md` out of the folder you unzipped and registers a
Scheduled Task. It downloads nothing, and there is no remote bootstrap command
— be suspicious of any instruction to pipe a URL into PowerShell.

Binaries go to `%LOCALAPPDATA%\scratchpad\bin`. Override with `-BinDir`.

**Upgrading** is re-running the installer over the top from a newer archive;
every operation is idempotent. Use the verb that covers what you have
installed: `cli` replaces `scratchpad.exe` only, `all` adds the skill, and
`install`/`startup` are the only verbs that stop the task and replace
`scratchpad-web.exe`. Upgrading a machine that runs the web server with `all`
alone leaves the old server binary in place.

The installer runs under both Windows PowerShell 5.1 (the `powershell` that
ships with Windows) and PowerShell 7 (`pwsh`) — CI verifies every operation
on both engines, so neither needs installing first.

The installer adds that directory to your **user** PATH only if you pass
`-AddToPath`; otherwise it prints the exact command to run yourself. It never
edits the machine PATH.

Verify the archive before unzipping it. `SHA256SUMS.txt` is published as a
release asset next to the archives and lists one line per archive:

```powershell
Get-FileHash .\scratchpad_<version>_windows_amd64.zip -Algorithm SHA256
# compare the hash against the matching line in SHA256SUMS.txt
```

`SHA256SUMS.txt` is not signed, so it proves the download was not corrupted or
substituted in transit — not that the release itself is authentic. The
binaries are unsigned too: SmartScreen and Defender will warn on first run,
and that warning is expected rather than a sign of tampering. Code signing is
out of scope for the beta.

## Running the web server

```powershell
.\install.ps1 startup        # register a per-user logon Scheduled Task
.\install.ps1 start          # start it now
.\install.ps1 status         # is it registered, is it running
.\install.ps1 stop
.\install.ps1 remove-startup
```

The task runs `scratchpad-web.exe --addr 127.0.0.1:8737` as **you**, at logon.
A per-user Scheduled Task is deliberate: a Windows Service would typically need
elevation and would run under a different profile, which conflicts with a data
root under `%USERPROFILE%`.

Running it in the foreground is equally supported and needs no task at all:

```powershell
scratchpad-web.exe --addr 127.0.0.1:8737
```

## Data root

`%USERPROFILE%\.scratchpad`, overridable with `SCRATCHPAD_ROOT`.

**No installer operation ever touches it — uninstall included.** Uninstall
removes only what the installer created: the Scheduled Task, the binaries, the
user-PATH entry and the installed skill copies.

```powershell
.\install.ps1 uninstall
```

Artifacts, notes and `.scratchpadignore` files are portable in both
directions, with no migration step. Two things do not round-trip:

- **Watch links are machine-local.** A link stores an absolute path on the
  machine that created it, so after moving a store, re-create each one with
  `scratchpad watch`. A link whose target no longer resolves is silently
  dropped from listings, on both platforms.
- **A name only one platform accepts.** A folder Linux allowed but Windows
  reserves — `CON`, a trailing-dot name, two names differing only in case —
  does not behave the same after the move: see [Naming](#naming) and
  [Case-insensitivity](#case-insensitivity). Ignore rules also *match* more
  on Windows than on Linux, because matching folds case there.

## Watching a folder

`watch` links an external folder into the store so it is hosted live. On
Windows that link is a reparse point, and which kind you get depends on your
machine's policy:

| Your situation | What `watch` creates |
|---|---|
| Developer Mode **on** (or you hold `SeCreateSymbolicLinkPrivilege`) | a directory **symbolic link** |
| Developer Mode **off**, ordinary user | a **junction** |

Both work, and you do not have to choose — `watch` tries a symbolic link and
falls back to a junction on `ERROR_PRIVILEGE_NOT_HELD`. A junction is the only
link an unprivileged user with Developer Mode off can create, which is why it
is supported rather than refused.

Turning on Developer Mode (Settings → System → For developers) gets you
symbolic links, but it is **not required** and you should not run scratchpad
elevated to get them.

Only links the store creates are trusted. A reparse point of a kind scratchpad
does not create is refused rather than followed, and a nested reparse point
inside a watched source tree is never browsed or listed.

`unwatch` removes only the link. The source folder is never modified, for both
link kinds.

**A name that "already exists" but was never a link.** `watch` claims the
name and applies the reparse point in two steps; a crash between them can
leave an ordinary empty directory behind. A later `watch` of that name fails
and tells you it is a directory, not a watch link — `scratchpad delete
<name>` clears it (an empty leftover is always safe to remove) and you can
retry. `unwatch` deliberately does not do this — only `delete` recovers a
bare directory, so the distinction between "remove a link" and "remove
content" stays with the command a human runs.

### If watch fails

Watch failure never affects anything else. `publish`, `list`, `delete`, `notes`
and the web site all keep working — a store you only publish into needs no link
capability at all.

## Naming

Names the store **creates** — published artifact names, watch link names and
the project directories made for them — reject reserved Windows device
basenames regardless of extension (`CON`, `PRN`, `AUX`, `NUL`, `COM0`-`COM9`,
`LPT0`-`LPT9`, case-insensitive, so `nul.html` and `Com1.tar.gz` fail too) and
any name ending in a dot or a space.

This rule applies **on every platform**, including Linux, so a store created on
one machine stays movable to the other. It is a small narrowing of what Linux
used to accept.

Names the store merely **looks up** — URL segments, `delete`, `unwatch` and
notes document paths — stay looser than the create rule, because a watched
repository names its own folders. How much looser is a platform split:

- **On Linux** the portable-name rule does not apply to lookups at all, so a
  watched repository that already contains a folder called `CON` remains
  reachable and removable.
- **On Windows** lookups additionally refuse the forms Win32 cannot address
  safely: a reserved device basename, a trailing dot or space (Windows strips
  them on resolution, so two spellings would collide), and any segment
  containing `:` (NTFS reads it as an alternate-data-stream selector). A
  watched repository's own `CON`-named entry therefore still appears in
  listings on Windows but cannot be opened or deleted through scratchpad.

## Case-insensitivity

NTFS folds case, and scratchpad follows the filesystem rather than fighting
it. Compared to Linux:

- **Names collide across case.** `publish -name Report` then
  `publish -name report` fails on Windows (both succeed on Linux). The error
  quotes the spelling you typed, even when the folder on disk spells it
  differently.
- **URLs resolve across case.** `/a/REPORT/` finds an artifact whose folder
  is `report`; on Linux it 404s. The page for a hand-typed case variant
  echoes the spelling from the URL — links scratchpad generates always carry
  the on-disk spelling.
- **Ignore rules fold.** `.SSH` matches the built-in `.ssh/` rule and
  `key.PEM` matches `*.pem`, so case variants of credential files stay
  hidden. Negations fold too — `!readme` un-hides `README`. See
  [ignore-rules.md](ignore-rules.md).
- **Reserved names fold.** A top-level directory whose name folds to
  `.annotations` (notes storage) or `.scratchpad-lock` is treated as reserved
  in any case spelling.
- **Notes fold with the document path.** Two case-variant document paths that
  would keep separate note sets on Linux share one note set on Windows,
  because the sidecar path folds the same way the document does.

## Unsupported filesystems

Only local NTFS is supported for the first release.

- **ReFS / Dev Drive** — untested. The containment primitives are documented
  for ReFS but were never verified there, because CI runners expose only NTFS.
  If you keep source trees on a Dev Drive, watch them from an NTFS store rather
  than putting the store itself there.
- **SMB / UNC paths** — refused for mutations rather than warned about.
- **FAT/exFAT** — the POSIX-semantics delete and rename this design relies on
  are documented as unsupported there.

## Troubleshooting

**`watch` says a required privilege is not held.** You should not see this —
the junction fallback handles it. If you do, enable Developer Mode (Settings →
System → For developers) and retry. Do not run elevated.

**A reparse point appears where a real directory is required.** A project
ancestor, an `.annotations` component, or a delete/unwatch parent is a link
rather than a real directory. Scratchpad refuses rather than following it. Move
or recreate that directory as a real one.

**A watched source contains a nested reparse point.** It is skipped, not
followed. Only one link boundary is crossed: the watch link itself.

**Sharing violations / "the file is in use".** Antivirus and Windows Search can
hold a file transiently. Annotation writes retry a bounded number of times and
then report a real error rather than retrying forever. A persistent failure is
a real failure — check for a process holding the file.

**The web server will not start.** Check `status`. If the Scheduled Task was
blocked, Group Policy on a managed machine is the usual cause; run
`scratchpad-web.exe --addr 127.0.0.1:8737` in the foreground instead.

**OneDrive, Docker Desktop or VFS for Git in the store tree.** Entries backed by
a filter driver that is not servicing them are **skipped and logged**, never
fatal. If the whole store root is unreadable that is still fatal, because there
is nothing to watch.

## LAN exposure

Loopback is the default on every platform. `.\install.ps1 install -Lan`
(or running the binary with `--addr 0.0.0.0:8737`) opts into LAN exposure, and
**the site has no authentication**: that exposes artifact
contents plus the unauthenticated delete and notes-write endpoints to every
host that can reach the port. Only do it on a network you trust.

## Security posture

The Windows backend is not a path-string port of the Linux one. Every mutation
is issued against a directory **handle** pinned before the check that
authorised it, so renaming or substituting an ancestor cannot redirect it. Path
prefix comparison is not used anywhere for containment — 8.3 short names alone
make it unsound, since `GetFinalPathNameByHandleW` and `FILE_NAME_INFO` can
return different spellings for the same object.

The design and its evidence are recorded in
[`.agents/ADRs/2026-08-26-windows-rooted-store-backend.md`](../.agents/ADRs/2026-08-26-windows-rooted-store-backend.md).

## Known limitations

- ReFS/Dev Drive, SMB and FAT/exFAT untested or refused (above).
- Cloud placeholder files (OneDrive) are skipped, not rehydrated.
- No code signing and no MSI in the first release; SmartScreen may warn.
- `ReadDirectoryChangesW` overflow recovery is implemented but was never
  reproduced deterministically in CI.
