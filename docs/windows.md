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

Download the archive for your architecture, unzip it, and run the installer
from the unzipped folder:

```powershell
# CLI only
.\scripts\install.ps1 cli

# CLI + agent skill + cleanup of obsolete MCP registrations
.\scripts\install.ps1 all

# ...and register the web server to start at logon
.\scripts\install.ps1 install
```

Binaries go to `%LOCALAPPDATA%\scratchpad\bin`. Override with `-BinDir`.
Every operation is idempotent — re-running is how you upgrade.

The installer runs under both Windows PowerShell 5.1 (the `powershell` that
ships with Windows) and PowerShell 7 (`pwsh`) — CI verifies every operation
on both engines, so neither needs installing first.

The installer adds that directory to your **user** PATH only if you pass
`-AddToPath`; otherwise it prints the exact command to run yourself. It never
edits the machine PATH.

Verify the archive before installing:

```powershell
Get-FileHash .\scratchpad-windows-amd64.zip -Algorithm SHA256
# compare against SHA256SUMS.txt from the release
```

## Running the web server

```powershell
.\scripts\install.ps1 startup        # register a per-user logon Scheduled Task
.\scripts\install.ps1 start          # start it now
.\scripts\install.ps1 status         # is it registered, is it running
.\scripts\install.ps1 stop
.\scripts\install.ps1 remove-startup
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
.\scripts\install.ps1 uninstall
```

Existing stores are data-compatible in both directions. There is no migration.

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

Names the store merely **looks up** stay looser, so a watched repository that
already contains a folder called `CON` remains reachable and removable.

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

Loopback is the default on every platform. `.\scripts\install.ps1 install -Lan`
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
