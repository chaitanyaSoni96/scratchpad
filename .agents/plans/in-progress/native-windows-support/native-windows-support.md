---
title: Native Windows Support
status: todo
created: 2026-08-25
links: [../../../spec/native-windows-support.md]
---

# Native Windows Support Plan

## Scope

Implement the [native Windows technical specification](../../../spec/native-windows-support.md)
without weakening the Linux security model. Deliver native CLI and web binaries,
live watched folders, annotation support, PowerShell installation, startup
management, CI, release artifacts, and documentation.

The plan is deliberately gated. Phase 1 determines whether documented Win32
primitives can preserve the current handle-anchored containment model. Do not
start the full backend until that decision and threat model are reviewed.

## Model Assignment Policy

Every phase names one GPT 5.6 model and one Claude 5 model. The operator may run
the entire phase with either assignment; the paired model is an equivalent
alternative, not a requirement to switch tools. For security gates, use the
other family for independent review when both tools are available.

| Label | GPT assignment | Claude assignment | Use |
|---|---|---|---|
| Architecture | GPT 5.6 Luna | Claude Opus 5 | threat modeling, ADRs, security review |
| Systems | GPT 5.6 Sol | Claude Sonnet 5 | Win32 implementation, concurrency, difficult debugging |
| Delivery | GPT 5.6 Terra | Claude Fable 5 | bounded refactors, CI, packaging, docs |

Task labels indicate work mode, not a different model assignment: `CO` means
architecture, `CXH` means systems, and `CS`/`CX` mean delivery. A `review`
suffix requires a fresh context and should use the other model family when
available.

Rules for execution:

- Use the phase assignment for all tasks in that phase, including tasks whose
  existing bracketed label describes a narrower work mode.
- The author of a security-sensitive phase may not be its only reviewer. Pair
  Claude-authored implementation with GPT review or vice versa when possible.
- Avoid parallel agents editing the same files. Parallelize research, test
  design, docs, and isolated platform files only.
- Every agent must read the spec, current `CLAUDE.md`, and touched tests before
  editing. Agents must not weaken or skip tests to obtain a green build.

## Phase 0: Baseline and Harness

**Models:** GPT 5.6 Terra or Claude Fable 5. **Gate review:** GPT 5.6 Luna or
Claude Opus 5 from the other family when available.

**Goal:** establish repeatable Linux and Windows feedback before architectural
changes. **Gate:** current Linux tests pass and Windows compile failures are
captured as expected evidence.

- [x] **P0.1 [CX] Record baseline.** Run `make test` and capture `GOOS=windows`
  compiler failures in the execution tracker. Do not edit behavior.
- [x] **P0.2 [CX] Add Windows build job.** Add CI that cross-compiles both commands
  for `windows/amd64` and `windows/arm64`; initially allow only known failures,
  then make it required in Phase 2.
- [x] **P0.3 [CS] Add native Windows test job.** Run `go test ./...` on a pinned
  supported Windows runner. Separate ordinary tests from tests requiring
  symlink capability and print explicit skip reasons.
- [x] **P0.4 [CS] Classify tests.** Move Unix-only assumptions behind test helper
  files or build tags without changing assertions. Define reusable helpers for
  link capability, NTFS requirement, and race hooks.
- [x] **P0.5 [CO review] Baseline review.** Confirm CI covers Linux, Windows
  cross-build, native Windows, amd64, and at least compile coverage for arm64.

**Exit evidence:** baseline tracker entry, CI links, known-failure list, and no
Linux regression.

## Phase 1: Win32 Security Spike

**Models:** GPT 5.6 Luna or Claude Opus 5. Use maximum reasoning for the entire
phase. **Gate review:** the paired model from the other family.

**Goal:** prove a race-resistant Windows design before production implementation.
**Gate:** accepted ADR approved by one Claude and one GPT reviewer.

- [x] **P1.1 [CO] Write threat model.** Enumerate attacker-controlled paths,
  rename races, reparse tags, junctions, symlinks, mount points, UNC paths,
  alternate data streams, case folding, device names, and sharing violations.
- [x] **P1.2 [CXH] Prototype rooted traversal.** In an isolated spike or test-only
  package, open a root and child segments without following reparse points,
  retain stable identity, and demonstrate safe create/read/delete primitives.
- [x] **P1.3 [CXH] Prototype atomic replacement.** Demonstrate annotation temp
  creation and atomic replacement under realistic Windows sharing modes. Define
  retryable error codes, backoff, total bound, and durability expectations.
- [x] **P1.4 [CS] Probe link options.** Test unprivileged directory symlinks under
  Developer Mode and without it. Evaluate junctions only as a documented
  fallback; identify reparse tags and target-resolution behavior.
- [x] **P1.5 [CXH] Build adversarial spike tests.** Replace an ancestor or target
  between validation and use; assert writes/deletes cannot escape. Include
  nested reparse points and root replacement.
- [x] **P1.6 [CO] Author ADR.** Record chosen APIs, containment proof, supported
  filesystems/reparse tags, link policy, fallback behavior, rejected approaches,
  and remaining risk under `.agents/ADRs/`.
- [x] **P1.7 [CXH review] Red-team ADR and prototype.** Try to invalidate path
  comparison, identity, and race assumptions. Report findings before approval.
- [x] **P1.8 [CO review] Accept or stop.** If no credible race-resistant strategy
  exists, stop and rescope instead of shipping a path-based backend.

**Exit evidence:** accepted ADR, passing spike tests on NTFS, measured behavior
with and without symlink privilege, and a fixed backend API design.

## Phase 2: Extract Shared Platform Boundaries

**Models:** GPT 5.6 Sol or Claude Sonnet 5. **Gate review:** GPT 5.6 Luna or
Claude Opus 5 from the other family when available.

**Goal:** make shared domain code compile on Windows while preserving Linux
behavior. **Gate:** Linux suite passes and both Windows commands cross-compile.

- [x] **P2.1 [CO] Confirm minimal boundary.** Map each direct Unix operation in
  `store.go`, `annotations.go`, and `watch.go` to the narrowest platform helper.
  Reject broad filesystem interfaces unless the spike proves they are needed.
- [x] **P2.2 [CX] Split directory identity.** Move Unix `Stat_t` identity to
  `identity_unix.go`; add the Windows declaration or stub selected by the ADR.
  Keep reconciliation and debounce shared.
- [x] **P2.3 [CS] Split link mechanics.** Move create/read/classify/remove link
  mechanisms into OS files while retaining validation, idempotence policy, and
  user-facing errors in shared code.
- [x] **P2.4 [CX] Remove shared Unix imports.** Eliminate direct `x/sys/unix` and
  `syscall.Stat_t` references from untagged files. Keep Linux implementations
  mechanically equivalent.
- [x] **P2.5 [CS] Add portable name validation.** Implement accepted reserved-name
  and trailing-dot/space rules with table-driven tests on every OS. Document the
  small compatibility change.
- [x] **P2.6 [CX] Make cross-build required.** Remove temporary known-failure
  allowances once both commands compile.
- [x] **P2.7 [CO review] Boundary review.** Verify domain policy was not duplicated
  into platform files and security-sensitive mechanism was not made generic.

**Exit evidence:** green Linux tests, green Windows cross-build, shared files
free of Unix APIs, and a reviewed platform API inventory.

## Phase 3: Windows Store and Annotation Backends

**Models:** GPT 5.6 Sol or Claude Sonnet 5, with maximum reasoning enabled for
native calls and race tests. **Gate review:** GPT 5.6 Luna or Claude Opus 5 from
the other family.

**Goal:** implement complete Windows storage semantics. **Gate:** store and
annotation package tests pass natively, including deterministic race tests.

### Rooted store

- [x] **P3.1 [CXH] Implement handle wrapper and error mapping.** Add deterministic
  ownership, final-path/identity inspection, Win32-to-Go error translation, and
  no-follow directory opens according to the ADR.
- [x] **P3.2 [CXH] Implement real-directory traversal.** Support optional ancestor
  creation and artifact-ancestor rejection while retaining validated handles.
- [x] **P3.3 [CS] Implement create-only file and directory operations.** Preserve
  atomic EEXIST behavior under concurrent claims and return errors compatible
  with shared policy.
- [x] **P3.4 [CXH] Implement browsable traversal.** Permit exactly one approved
  store watch boundary, reject nested or unknown reparse points, and handle
  broken/cyclic targets safely.
- [x] **P3.5 [CXH] Implement safe document open and deletion.** Ensure file links,
  ancestor replacement, and target replacement cannot escape containment.
- [x] **P3.6 [CS] Implement pruning and directory reads.** Preserve empty-project
  cleanup and artifact discovery semantics without following reparse points.

### Annotations

- [x] **P3.7 [CXH] Implement annotation-root opening.** Create/open `.annotations`
  under the store handle and reject any reparse component.
- [x] **P3.8 [CXH] Implement read and atomic write.** Use unique temp files,
  bounded sharing-violation retry, atomic replacement, cleanup on failure, and
  existing revision semantics.
- [x] **P3.9 [CXH] Implement safe recursive removal.** Walk by handle, never follow
  reparse points, and prune empty annotation ancestors.
- [x] **P3.10 [CS] Implement annotation walk/report support.** Match Linux ordering,
  malformed-file handling, and callback behavior.

### Verification

- [x] **P3.11 [CS] Port behavior tests.** Run shared publish/list/delete/resolve
  tests on Windows and add platform-specific expectations only where OS behavior
  is genuinely different.
- [x] **P3.12 [CXH] Add deterministic attack tests.** Cover every applicable row
  of the security matrix, including case variants, reserved names, ADS syntax,
  root replacement, and intermediate reparse substitution.
- [x] **P3.13 [CO review] Security invariants review.** Trace each mutation from
  user input to final handle operation and confirm no validate-then-open path.
- [x] **P3.14 [CXH review] Implementation red team.** Independently inspect native
  call flags, handle lifetimes, error paths, and recursive deletion.

**Exit evidence:** green `internal/store` tests on Windows and Linux, attack-test
report, no leaked handles under repeated tests, and review sign-offs.

## Phase 4: Native Watch and Web Behavior

**Models:** GPT 5.6 Sol or Claude Sonnet 5. **Gate review:** GPT 5.6 Luna or
Claude Opus 5 from the other family when available.

**Goal:** make the full CLI/web workflow native and live. **Gate:** Windows
end-to-end tests pass under both link-capable and link-incapable configurations.

- [x] **P4.1 [CXH] Implement Windows directory identity.** Use volume serial and
  stable file ID from a directory handle; test replacement detection.
- [x] **P4.2 [CS] Validate fsnotify reconciliation.** Test initial recursive
  registration, populated subtree creation, rename/replacement, deletion,
  overflow recovery where supported, and clean shutdown.
- [x] **P4.3 [CXH] Implement Windows watch links.** Create only the reparse type
  accepted by the ADR, verify the target, preserve same-target idempotence, and
  reject collisions or unknown reparse tags.
- [x] **P4.4 [CS] Add degraded-mode errors.** Publish and web serving must work
  without link privilege; `watch` must provide precise remediation without
  suggesting elevation as the default.
- [x] **P4.5 [CS] Add CLI end-to-end tests.** Cover publish from HTML/stdin and
  directories, list JSON, watch/watches/unwatch, delete, notes report/reply/
  resolve, excess arguments, and exit status.
- [x] **P4.6 [CS] Add web end-to-end tests.** Cover folder browsing, sandboxed
  previews, viewer, markdown, notes, delete, SSE refresh, watched sources, and
  loopback default.
- [x] **P4.7 [CO review] Semantic parity review.** Compare Windows and Linux user
  behavior; document only unavoidable differences.

**Exit evidence:** native smoke-test transcript, end-to-end suite results, and
verified behavior without symlink capability.

## Phase 5: Installation, Startup, CI, and Releases

**Models:** GPT 5.6 Terra or Claude Fable 5. Escalate only Win32 lifecycle or
policy failures to GPT 5.6 Sol or Claude Sonnet 5. **Gate review:** GPT 5.6 Luna
or Claude Opus 5 from the other family when available.

**Goal:** make Windows installation and distribution routine. **Gate:** clean
Windows VM can install, run, update, and uninstall without touching user data.

- [x] **P5.1 [CS] Write `scripts/install.ps1`.** Support CLI-only, skill-only,
  full install, obsolete-MCP cleanup, and uninstall operations. Make each action
  idempotent and preserve `%USERPROFILE%\.scratchpad`.
- [x] **P5.2 [CS] Add per-user startup.** Register a logon Scheduled Task for
  `scratchpad-web.exe --addr 127.0.0.1:8737`; add start, stop, status, and removal
  commands with actionable policy errors.
- [x] **P5.3 [CX] Add Windows build commands.** Produce `.exe` binaries for amd64
  and arm64 without changing existing Linux/container targets.
- [x] **P5.4 [CX] Add release packaging.** Build zip archives, include installer
  and required docs/licenses, generate SHA-256 checksums, and smoke-test archive
  contents.
- [x] **P5.5 [CS] Test path and quoting edge cases.** Install from paths containing
  spaces and Unicode, run under a non-administrator user, and test an overridden
  `SCRATCHPAD_ROOT`.
- [x] **P5.6 [CX] Add CI artifact retention.** Upload binaries and test logs on
  pull requests; produce release assets only for tagged builds.
- [x] **P5.7 [CO review] Supply-chain and lifecycle review.** Verify downloaded
  binaries need no arbitrary script execution beyond the visible installer,
  checksums are published, and uninstall is non-destructive.

**Exit evidence:** clean-VM install/update/uninstall transcript, CI artifacts for
both architectures, checksum verification, and startup task validation.

## Phase 6: Hardening and Release Readiness

**Models:** GPT 5.6 Luna or Claude Opus 5 for audit and release decisions. Use
GPT 5.6 Sol or Claude Sonnet 5 for fixes discovered during the phase. The final
security review must use the other model family when both are available.

**Goal:** close remaining risks and publish beta-quality support. **Gate:** all
acceptance criteria in the spec are satisfied or explicitly deferred in an ADR.

- [x] **P6.1 [CXH] Run stress and race campaigns.** Repeat concurrent publish,
  notes writes, watcher reconciliation, replacement attacks, and shutdown under
  `go test -race` where Windows supports it. Investigate flaky tests; do not
  paper over them with broad retries.
- [x] **P6.2 [CO] Perform final threat-model audit.** Reconcile implementation
  against the Phase 1 threat model and security matrix. Record residual risks.
- [x] **P6.3 [CXH review] Independent code review.** Review all Windows native
  calls, unsafe usage, build tags, error translation, link handling, and delete
  paths without relying on the implementing agent's summary.
- [x] **P6.4 [CS] Update documentation.** Update README, CLI docs, internals,
  install instructions, troubleshooting, supported-filesystem policy, Developer
  Mode guidance, and LAN exposure warning.
- [x] **P6.5 [CS] Update agent contract.** Update `skill/SKILL.md` for PowerShell
  syntax and Windows watch behavior, then run the repository's skill install
  process where appropriate. Keep CLI flags and create-only semantics aligned.
- [x] **P6.6 [CX] Run full release matrix.** Linux vet/tests/build, container build,
  Windows amd64 native tests, Windows arm64 build/smoke test, archive tests, and
  documentation link checks.
- [x] **P6.7 [CO] Prepare beta release notes.** State supported Windows versions,
  NTFS restriction, link capability requirements, known limitations, security
  posture, and rollback steps.
- [ ] **P6.8 [Human gate] Approve beta.** Confirm no unresolved high-severity
  findings and retain Windows beta status for at least two releases.

**Exit evidence:** final review report, complete CI matrix, updated docs/skill,
release artifacts, and human approval.

## Verification Commands

Exact CI wrappers may change, but execution must cover these outcomes:

```text
# Linux
make test
make build

# Cross-compile from Linux
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/...

# Native PowerShell
go vet ./...
go test ./...
go test ./internal/store -count=20
go test ./internal/watch -count=20
go test -race ./internal/store ./internal/watch ./internal/web
```

Native test jobs should additionally run named security and end-to-end suites so
they cannot disappear behind package-level success after an accidental rename.

## Review Checklist

- [x] No shared untagged Go file imports `x/sys/unix` or assumes Unix `Stat_t`.
- [x] No Windows mutation relies only on a canonical string path or prefix test.
- [x] Reparse-point tags are allowlisted, not accepted generically.
- [x] Every native handle is closed on success and every error path.
- [x] Existing Linux race and symlink tests remain enabled.
- [x] Windows attack tests use deterministic hooks where possible.
- [x] Watch failure does not disable publish-only workflows.
- [x] Install and uninstall never delete the data root.
- [x] Loopback remains the default on every platform.
- [ ] CLI, README, internals, and skill documentation agree.

## Rollback Strategy

Keep platform additions isolated by build tags and additive scripts. If Windows
beta reveals a critical containment issue:

1. Stop publishing Windows release assets and mark the affected versions.
2. Leave Linux binaries and container releases unchanged.
3. Disable only the unsafe Windows operation if publish-only behavior remains
   demonstrably safe; otherwise withdraw the Windows binaries entirely.
4. Preserve store data and annotation format so repaired binaries can resume
   without migration.
5. Add the discovered case to the permanent security matrix before re-release.

## Definition of Done

The plan is complete only when all ten acceptance criteria in the technical spec
are met, required CI jobs are green, both independent security reviews are
resolved, documentation is synchronized, and a clean non-admin Windows machine
can install and exercise the complete supported workflow without WSL.

---

## Checkbox reconciliation (2026-08-27)

Ticking stopped at P2.7 while the work ran through P6.7, so this plan spent most
of the branch asserting less than was true. That is the inverse of the branch's
own systemic finding — *a gate with an owner and no recorded evaluation reads as
satisfied* — and the boxes above were therefore reconciled against shipped code
and CI evidence, not from memory. Evidence per phase:

| Phase | Evidence |
|---|---|
| P3.1–P3.14 | Windows store/annotation backends shipped; `reviews/P3.13-security-invariants.md`, `reviews/P3.14-implementation-red-team.md`, and the P3.14 remediation record in `EXECUTION.md` |
| P4.1–P4.7 | Native watch/web shipped; `reviews/P4.7-semantic-parity.md` and its four remediation records (P-1a/P-2/P-3/P-4/P-5/P-6/P-7/P-8) |
| P5.1–P5.6 | `scripts/install.ps1` + `scripts/verify-install.ps1`, `windows-installer` matrix green on **both** engines at 94/94 (run `32994820274`); `EXECUTION.md`'s P5.1/P5.2/P5.5 record |
| P5.7 | `reviews/P5.7-supply-chain-review.md` — FAIL AT ENTRY then PASS; the shipped archive could not install itself (fixed `0fb8b8b`), `windows-archive-install` added as a gating job proving install/upgrade/uninstall/reinstall from an extracted archive on both engines (24/24 each) plus `Get-FileHash` against `SHA256SUMS.txt` (8/8 each), all 23 actions SHA-pinned (F8 closed), three release-path holes closed; run `33017420341` |
| P6.6 | `EXECUTION.md`'s P6.6 record — cell-by-cell: Linux, container build, Windows amd64 native, and doc links all locally/CI-verified covered; archive tests covered by `windows-archive-install` (run `33017854855`); Windows arm64 build-covered but smoke-test **not** covered (feasibility recorded, blocked on an unverified symlink probe and Go's lack of `-race` support on `windows/arm64`) |
| P6.1 | `stress-linux` / `stress-windows` green (`-race`, `-count=20`) |
| P6.2 | `reviews/P6.2-threat-model-audit.md` |
| P6.3 | `reviews/P6.3-independent-code-review.md` |
| P6.4, P6.5 | `6cb7384`, `2a1cb6f` — `docs/windows.md` added; README, CLI docs, internals, ignore rules and `skill/SKILL.md` aligned |
| P6.7 | `beta-release-notes.md` |

Three caveats that a tick would otherwise hide:

- **P4.6 is ticked for its own deliverable** (web end-to-end tests), but three
  items were *deferred to* P4.6 by other dispositions and never shipped: RW13's
  `RECALL_ON_*` skip, RW15's unsupported-entry tile, RW19's folder-card delete
  affordance. P6.2's FD-1 caught them; they now carry P6.8 gates with named
  fallbacks in ADR §9.1. They are not covered by this tick.
- **"CLI, README, internals, and skill documentation agree" is left unticked**
  deliberately. The repository copies agree, but `make install-skill` has not
  been re-run, so the copies in `~/.claude/skills` and `~/.pi/agent/skills` are
  stale — and `CLAUDE.md` requires that re-run. P6.6 ran the documentation link
  check (145 links, 0 broken); the re-run this item is waiting on is `make
  install-skill` itself, not the link check.
- **P5.7 is now ticked on its own remediation, not on what it found.** The
  review entered on a release archive that could not install itself and a
  94/94-green installer job that had only ever tested the git-checkout layout.
  Three findings are recorded and deliberately **not** fixed (vendored-JS
  provenance, recursive skill-directory removal on uninstall, and a `v*` tag on
  any commit producing draft assets), and five things could not be verified —
  most importantly, **the tagged-release path has never executed.** See the
  review's "Could not verify" section before P6.8.
- **P6.6 is ticked for running the matrix and reporting on it honestly, not for
  every cell coming back green-and-complete.** Four cells are fully covered
  (Linux vet/tests/build, container build — verified for the first time on this
  branch, Windows amd64 native, documentation link checks). Archive tests went
  from a formality to a real gate mid-task (see P5.7's row above) and are now
  covered by `windows-archive-install`, run `33017854855`. **Windows arm64 is
  build-covered only** — no smoke/full-suite run has ever executed on that
  architecture, and Go's race detector cannot run there at all
  (`windows/arm64` is unsupported); EXECUTION.md's P6.6 record names the CI job
  this would take. **P6.8 is the human gate** and is not the agent's to tick.

Review-checklist items were each re-verified this session rather than assumed:
no untagged shared file imports `x/sys/unix` (the only match was a comment at
`internal/store/store.go:407`); `watchTags` in `link_windows.go:21` is a
two-element allowlist with everything else refused as `reparseRefusal`; the
Linux suite reports **0** skips; the default listen address is `127.0.0.1:8737`
with `0.0.0.0` reachable only via `LAN=1`. Handle-closure comes from P6.3's
trace of all 30 acquisition sites. One honest qualification on "no Windows
mutation relies only on a canonical string path or prefix test": no *mutation*
does, but `alreadyInsideRoot` (`storefs_windows.go:597`) does use a
`strings.HasPrefix` alongside its `FILE_ID_INFO` primary — it is a
deny-broadening guard that refuses watch targets inside the root, where
over-breadth is safe, not an authorisation path.

