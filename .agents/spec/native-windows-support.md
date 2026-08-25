---
title: Native Windows Support Technical Spec
status: draft
created: 2026-08-25
links: [../plans/todo/native-windows-support/]
---

# Native Windows Support

## Objective

Ship `scratchpad.exe` and `scratchpad-web.exe` as genuine Windows binaries that
run without WSL, containers, Unix compatibility layers, or administrator
privileges. Preserve the existing filesystem-backed architecture, create-only
publishing, watch semantics, annotation guarantees, and path-containment
security while maximizing shared Go code.

Native support means:

- Windows 11 and supported Windows Server releases on NTFS.
- Native CLI, web server, filesystem notifications, installer, and startup.
- A default data root at `%USERPROFILE%\.scratchpad`, still overridable by
  `SCRATCHPAD_ROOT`.
- Native directory watches backed by Windows reparse points, with a documented
  capability check and actionable errors when link creation is unavailable.
- Release archives and checksums for `windows/amd64` and `windows/arm64`.
- Continuous compilation on Linux and behavioral/security tests on Windows.

WSL-only support, a desktop GUI, MSI packaging, code signing, and support for
FAT/exFAT or network filesystems are not required for the first release.

## Invariants

Windows support must not weaken these existing rules:

1. The filesystem remains the sole source of truth.
2. Publish and watch atomically claim a new name and never overwrite.
3. Artifact nesting and visibility rules remain platform-independent.
4. A path operation cannot be redirected outside its approved root by a rename,
   symlink, junction, mount point, or other reparse point during the operation.
5. Browsing may cross exactly one store-owned watch link and must reject nested
   links in the watched source.
6. Delete and annotation operations never traverse an untrusted link.
7. Unwatch removes only the store link and never modifies its target.
8. Annotation writes remain revision-guarded and atomically replace the prior
   file.
9. The web server binds to loopback unless LAN mode is explicitly selected.
10. Existing Linux behavior and container deployment remain unchanged.

## Current Portability Boundaries

Most of the application is already portable: command parsing, domain policy,
artifact discovery, ignore rules, HTTP handlers, templates, markdown, gzip,
SSE, and fsnotify orchestration. Platform coupling is concentrated in:

- `internal/store/storefs_linux.go`: descriptor-relative store traversal,
  creation, reads, deletion, and document opening.
- `internal/store/annotationfs_linux.go`: descriptor-relative annotation I/O,
  atomic replacement, recursive deletion, and walking.
- `internal/store/store.go` and `internal/store/annotations.go`: remaining
  direct `x/sys/unix` calls embedded in shared policy.
- `internal/watch/watch.go`: Unix `syscall.Stat_t` directory identity.
- Watch creation and classification: Unix symlink assumptions.
- Tests that unconditionally create Unix symlinks.
- `Makefile`, `scripts/install.sh`, and systemd/container deployment paths.

## Architecture

### Shared policy, platform mechanisms

Keep domain decisions in untagged files and isolate operating-system mechanisms
behind narrow package-private functions or types. Prefer the existing concrete
backend shape over introducing a broad interface used throughout the store.
Build tags select the implementation:

```text
internal/store/
  store.go                       shared policy
  annotations.go                 shared annotation policy
  storefs_linux.go               Linux handle-relative mechanisms
  storefs_windows.go             Windows handle-relative mechanisms
  annotationfs_linux.go          Linux annotation mechanisms
  annotationfs_windows.go        Windows annotation mechanisms
  link_linux.go                  Unix link create/read/classify helpers
  link_windows.go                reparse-point helpers

internal/watch/
  watch.go                       shared reconciliation and event handling
  identity_unix.go               device/inode identity
  identity_windows.go            volume/file identity
```

Small shared declarations may move to neutral files, but public APIs should not
change unless a platform requirement makes that unavoidable. Do not create a
generic virtual filesystem abstraction: it would obscure security properties
and increase maintenance without adding another real backend.

### Windows rooted filesystem

The Windows backend must use Win32 handles rather than validating a string path
and later reopening it. Candidate primitives include `CreateFileW` with
`FILE_FLAG_BACKUP_SEMANTICS` and `FILE_FLAG_OPEN_REPARSE_POINT`,
`GetFileInformationByHandleEx`, `GetFinalPathNameByHandleW`, and handle-relative
NT operations where documented Win32 APIs cannot provide race resistance.

The phase-zero spike must choose and document one of these strategies:

- **Preferred:** open each segment relative to an already validated directory
  handle, refusing unexpected reparse points and retaining parent handles
  through mutation.
- **Fallback:** combine no-follow handle opens, stable file IDs, canonical final
  paths, and pre/post-operation identity checks. This is acceptable only if the
  security review demonstrates that rename/reparse races cannot redirect a
  destructive or write operation.

Path-prefix checks alone are explicitly insufficient. Windows path comparison
must account for drive roots, UNC syntax, `\\?\` extended paths, alternate data
streams, case insensitivity, trailing dots/spaces, reserved device names, and
reparse-point substitution.

### Handle and path representation

Keep slash-separated logical paths at CLI, URL, and domain boundaries. Convert
segments to native paths only inside platform helpers. Avoid passing fully
joined native paths into security-sensitive helpers when segment traversal is
possible.

The Windows backend should own handles with a small wrapper that has explicit
`Close` behavior. Helpers should return `*os.File` only where callers require
Go file I/O or HTTP serving. Error translation should preserve `errors.Is`
behavior for existence, permission, and not-found cases.

### Reparse points and watch semantics

The user-facing `watch` behavior remains “link a live external directory into
the store.” On Windows:

- Prefer a directory symbolic link because it preserves the existing model.
- Support links created when Developer Mode grants unprivileged symlink
  creation; do not require running the whole application elevated.
- Detect and explain `ERROR_PRIVILEGE_NOT_HELD`, including how to enable
  Developer Mode.
- Treat junctions and other reparse points as untrusted unless created and
  identified by the application under an explicitly accepted design.
- Never infer safety from the reparse attribute alone; inspect the reparse tag
  and resolved target.
- Preserve idempotent watch behavior only when the existing link resolves to
  the same target.

The phase-zero spike may recommend application-created junctions as a fallback
if they can preserve all containment and unwatch guarantees without elevation.
That fallback must be explicit and tested, not silently selected for arbitrary
reparse points.

### Directory identity and notifications

`fsnotify` supports Windows and remains the shared event backend. Split only
directory identity into platform files:

- Unix: current device and inode values.
- Windows: volume serial number plus file ID from an open directory handle.

The reconciliation algorithm, debounce, overflow handling, visibility, and SSE
broadcast remain shared. Windows integration tests must cover recursive initial
registration, populated subtree creation, rename/replacement, watched-link
targets, overflow recovery where reproducible, and clean shutdown.

### Annotation atomicity

The Windows annotation backend must retain these semantics:

- `.annotations` and all ancestors are real directories, not reparse points.
- New directories and files are created relative to validated handles.
- Temporary files are unique, flushed as appropriate, and atomically replace
  the destination.
- Revision conflicts remain visible to callers.
- Recursive cleanup cannot escape through a reparse point.

Windows file replacement differs when a destination is open. The implementation
must define bounded retry behavior for transient sharing violations and return a
clear error after the bound. It must not turn an atomic update into remove then
rename.

### Name semantics

Current create-time names match a portable ASCII expression but still admit
Windows-problematic forms such as reserved device basenames. Add a shared
portable create-name rule so artifacts created on one platform can be moved to
another safely. At minimum reject case-insensitive `CON`, `PRN`, `AUX`, `NUL`,
`COM1`-`COM9`, and `LPT1`-`LPT9`, including names with extensions, plus names
ending in a dot or space.

Lookup remains looser for watched repositories. Such entries may be listed only
when Windows can address them safely; malformed or ambiguous native names must
be unreachable rather than normalized to another entry. Any shared validation
change is a behavior change and requires Linux regression tests and release
notes.

### Installation and lifecycle

Add `scripts/install.ps1` with separate operations matching the shell installer:

- Install `scratchpad.exe` and optionally `scratchpad-web.exe` under a
  user-writable directory, defaulting to `%LOCALAPPDATA%\scratchpad\bin`.
- Add that directory to the user PATH only with explicit consent or print the
  command needed to do so.
- Install the skill for detected Claude Code and GPT-agent-compatible skill
  locations where applicable; keep the repository skill contract canonical.
- Remove obsolete MCP registrations only where safe and format-preserving.
- Register/unregister `scratchpad-web.exe` as a per-user Scheduled Task running
  at logon, with loopback binding by default.

The first release should use a Scheduled Task instead of a Windows Service:
services typically require elevation and run under a different profile, which
conflicts with `%USERPROFILE%\.scratchpad`. Document foreground execution as a
fully supported alternative.

### Build and release

Add reproducible targets or scripts for:

- `GOOS=windows GOARCH=amd64` and `arm64` cross-compilation.
- Native Windows test execution in CI.
- Zip archives containing the two executables, README/license material, and
  PowerShell installer.
- SHA-256 checksums.

Cross-compilation proves source portability but does not replace native tests.
At least one Windows CI job must run the full suite with Developer Mode or an
equivalent symlink-capable test environment. Security tests that cannot obtain
the capability should report a skip with a reason; a separate required job must
exercise them.

## Security Test Matrix

The following cases are release blockers on both Linux and Windows where the
concept exists:

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

Race tests should use deterministic hooks around validated-handle acquisition,
as existing Linux tests do, rather than timing-only loops.

## Compatibility Policy

- Windows support is initially best-effort beta until two releases complete
  without a critical containment defect.
- NTFS local volumes are supported. Other filesystems and SMB shares return an
  explicit unsupported warning for security-sensitive mutations until tested.
- Windows path comparison is case-insensitive, but logical URL paths retain the
  actual directory spelling returned by the filesystem.
- Existing stores remain data-compatible. No migration is introduced.
- Linux containers and native systemd installation remain supported.

## Observability and Errors

Errors should identify the failed operation and remediation without exposing
unsafe implementation detail. Required actionable cases include:

- Developer Mode or symlink privilege unavailable.
- Store is on an unsupported filesystem.
- A reparse point appears where a real directory is required.
- A watched source contains a nested reparse point.
- Antivirus/indexer sharing violations exceed retry bounds.
- Scheduled Task creation fails or is blocked by policy.

No telemetry is added. Logs remain local process output.

## Acceptance Criteria

1. `go test ./...` passes natively on Windows amd64 and Linux.
2. `GOOS=windows GOARCH=amd64 go build ./cmd/...` succeeds from Linux.
3. Both Windows executables operate from PowerShell without WSL or elevation.
4. Publish, list, delete, notes, web previews, live refresh, and watch/unwatch
   pass end-to-end tests on NTFS.
5. Every security-matrix case has a test, documented exclusion, or accepted ADR.
6. Windows watch inability produces an actionable error and does not affect
   publish-only operation.
7. PowerShell install/uninstall is idempotent and leaves user data untouched.
8. Release archives for amd64 and arm64 are generated and smoke-tested.
9. README, CLI docs, internals, and `skill/SKILL.md` describe platform behavior
   consistently.
10. A final independent security review finds no known path-based TOCTOU escape.

## Risks and Trade-offs

- Handle-relative Windows traversal may require low-level `x/sys/windows` or
  carefully wrapped native calls. This is more code but preserves security.
- Symlink capability varies by machine policy. A clear degraded mode is better
  than silently copying watched content or requiring elevation.
- Antivirus software can transiently hold files. Bounded retries improve
  reliability but must not obscure persistent failures.
- Portable create-name restrictions slightly narrow Linux behavior. This avoids
  stores that cannot be moved to Windows and should be announced explicitly.
- Supporting every reparse tag creates an unbounded security surface. The first
  version should allow only the exact tags it creates and understands.
