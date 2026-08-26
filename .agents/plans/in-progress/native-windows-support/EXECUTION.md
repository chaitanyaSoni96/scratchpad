---
title: Native Windows Support — Execution Tracker
status: in-progress
created: 2026-08-26
links: [./native-windows-support.md, ../../../spec/native-windows-support.md]
---

# Execution Tracker

Branch: `feat/native-windows-support`.

Verification environment agreed with the operator on 2026-08-26: there is no
local Windows machine. Native Windows gates are verified on GitHub Actions
`windows-2025` / `windows-11-arm` runners (pinned images — the workflow
deliberately does not use the floating `windows-latest` label) against pushes
of this branch to `origin`. Linux gates run locally.

## Phase 0 — Baseline and Harness

### P0.1 Baseline record

Toolchain: `go1.27.0 linux/amd64`, module `go 1.26.5`.

Linux baseline, commit `3a7b7b6` (`make test`) — **pass**:

```
go vet ./... && go test ./... && scripts/check-make.sh
ok  	scratchpad/cmd/scratchpad
ok  	scratchpad/cmd/scratchpad-web
ok  	scratchpad/internal/store
ok  	scratchpad/internal/watch
ok  	scratchpad/internal/web
check-make: ok
```

Windows cross-build baseline (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...`)
— **fail, as expected**. Every package fails through the same root cause:
`internal/store` has no Windows backend, so all five packages report
`internal/store` errors and nothing downstream is reached.

Known-failure list at baseline. Line numbers are as of commit `30c34a5` (P2.5
has since shifted `store.go`); the table was regenerated mechanically per
review finding F3 by extracting that commit cleanly (`git archive 30c34a5 |
tar -x -C <tmpdir>`) and running
`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -gcflags=-e ./...` there —
run it on a clean extraction, never the live tree, or shifted line numbers
will make the table look wrong when it is not:

| Site | Failure | Owning phase |
|---|---|---|
| `internal/store/annotations.go:96,138,252,276,310` | `undefined: annotationFS` (Linux-only type) | P2.1, P3.7–P3.10 |
| `internal/store/annotations.go:120` | `undefined: openAnnotationFS` | P3.7 |
| `internal/store/annotations.go:103,127,129,131,155` | `undefined: unix.Flock`, `unix.LOCK_*` — advisory lock in shared policy | P2.4 |
| `internal/store/annotations.go:147,150` | `undefined: unix.Close`, `unix.Openat`, `unix.O_*` in shared policy | P2.4 |
| `internal/store/store.go:173,457,542,616,757,824` | `undefined: openRootedFS` | P3.1 |
| `internal/store/store.go:182,467,843` | `undefined: dirHasHTMLFD` | P3.6 |
| `internal/store/store.go:469,574` | `undefined: fdPath` (`/proc/self/fd`) — no Windows analogue | P2.1, P3.1 |
| `internal/store/store.go:495` | `undefined: OpenDocument` | P3.5 |
| `internal/store/store.go:561,842` | `undefined: openDirAt` | P3.2 |
| `internal/store/store.go:567,850` | `undefined: removeTreeAt` | P3.9 |
| `internal/store/store.go:569` | `undefined: writeFileAt` | P3.3 |
| `internal/store/store.go:784,854` | `undefined: pruneAt` — empty-project/annotation ancestor pruning | P3.6 |
| `internal/store/store.go:183,470,477,551,555,556,563,566,625,631,632,636,769,771,772,775,781,833,835,836,838,845,849` | direct `unix.*` calls embedded in shared policy | P2.3, P2.4 |
| `internal/watch/watch.go:281` | `syscall.Stat_t` directory identity; masked at baseline because `internal/store` fails first | P2.2, P4.1 |

`pruneAt` is assigned to **P3.6** ("Implement pruning and directory reads")
because it is the empty-directory pruning primitive that task exists to
implement — its two call sites are the empty-project cleanup after delete and
the annotation-ancestor pruning, both named in P3.6's "preserve empty-project
cleanup" language. It was previously omitted from this table with no owning
task (review finding F3).

`GOOS=windows go vet ./...` fails identically. `GOARCH=arm64` fails identically —
the failures are OS-level, not architecture-level.

Behaviour was not edited for this task.

### P0.2 Windows build jobs in CI

`.github/workflows/ci.yml` added (commit `30c34a5`): a required Linux gate
(`linux-test`: vet, test, `check-make`, `make build`) plus cross-compile jobs
for `windows/amd64` and `windows/arm64` from Linux, and a native
`windows-11-arm` build job. All Windows jobs carry `continue-on-error` with a
comment naming the task that removes the allowance (P2.6 for the builds,
P3.11/P4 for native tests, P4.4 for the degraded-mode job added later).

CI evidence — run 1, commit `30c34a5`:
<https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32898457159>

| Job | Runner | Conclusion | Expected? |
|---|---|---|---|
| Linux amd64 — vet, test, check-make, build | `ubuntu-latest` | success | yes — required gate |
| Cross-build windows/amd64 | `ubuntu-latest` | failure | yes — `internal/store` has no Windows backend |
| Cross-build windows/arm64 | `ubuntu-latest` | failure | yes — same cause |
| Windows amd64 native — vet, test | `windows-2025` | failure | yes — same cause |
| Windows arm64 native — build | `windows-11-arm` | failure | yes — same cause |

Every Windows failure was confirmed from the logs to be the recorded
known-failure list (`undefined: annotationFS`, `undefined: unix.Flock`, …),
not a harness defect. Go `1.26.5` resolves and installs from
`go-version-file: go.mod` on all four runners, and all four `runs-on` labels
are valid.

CI evidence — run 2, commit `1ed3992` (P2.5 name change in place):
<https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32898956148> —
same pattern: Linux still green, Windows jobs still failing on the same known
cause.

Not yet in CI, deliberately: `-count=20` repetition and `-race`, which the
plan places in P6.1/P6.6.

### P0.3 Native Windows test jobs

Native Windows amd64 testing runs on the pinned `windows-2025` image (not the
floating `windows-latest` label). The job probes symlink capability at runtime
and exports `SCRATCHPAD_TEST_SYMLINKS=1|0`, which `testutil.RequireSymlinks`
reads to separate symlink-dependent tests from ordinary ones with greppable
`SKIP(symlink-capability)` reasons.

In run 32898457159 the probe reported capability **present** and exported
`SCRATCHPAD_TEST_SYMLINKS=1`, so the security tests will not silently skip on
that runner once the code compiles. Vet failed on the expected known-failure
list; per review finding F2 the `go test` step originally never executed
(GitHub skips steps after a failure), which the P0.5 follow-up below fixed
with `if: always()` on the test steps.

Per review finding F1, native testing is now **two** jobs (see the P0.5
follow-up section for the mechanics):

- `windows-symlink-required` — full suite; the capability probe hard-fails if
  the runner cannot create a directory symlink, and the job fails on any
  `SKIP(symlink-capability)` line or on output containing no test result
  lines at all. Becomes the spec's REQUIRED symlink-capable job at P3.11/P4.
- `windows-degraded-test` — deliberately sets `SCRATCHPAD_TEST_SYMLINKS=0`
  and asserts the suite still passes with the symlink tests visibly
  skipping (P4.4's degraded-mode evidence in embryo).

### P0.4 Test classification

Commit `30c34a5` moved Unix-only assumptions behind `internal/testutil`
helpers and build tags without changing assertions:

- `testutil.RequireSymlinks` / `SymlinkCapable` (env override
  `SCRATCHPAD_TEST_SYMLINKS`, one-per-process directory-symlink probe),
  `testutil.RequireNTFS` (Windows-only volume check, no call sites yet),
  `testutil.RequireUnix`, and the race-hook helper extraction in
  `internal/store/hook_test.go`.
- The FIFO test moved to `cmd/scratchpad/main_unix_test.go` under
  `//go:build unix`, assertions unchanged.

Verified by the P0.5 review (its E1–E3): the top-level test-name set is
byte-identical before and after (84/84), Linux runs with **zero skips**
(93 PASS / 0 FAIL / 0 SKIP), every symlink-creating or `store.Watch`-using
test carries `RequireSymlinks`, none is over-guarded, and no `if`, expected
value, or error substring changed. `internal/testutil` itself compiles for
`windows/amd64` and `windows/arm64`.

### P0.5 Baseline review

Fresh-context gate review of commit `30c34a5`:
[reviews/P0.5-baseline-review.md](reviews/P0.5-baseline-review.md).
Verdict: **PASS WITH FINDINGS** — the Phase 0 gate ("Linux tests pass,
Windows compile failures captured as expected evidence") is met; nine
findings F1–F9 recorded. Phase 1 may proceed. F1/F2 are blocking
pre-conditions on removing any `continue-on-error` allowance.

### P0.5 review follow-up (this commit)

Closed now:

- **F1 (HIGH)** — the single advisory-capability Windows test job was split
  into `windows-symlink-required` and `windows-degraded-test` (see P0.3
  above). The capable job's probe is mandatory (`exit 1` when absent —
  GitHub-hosted runners are capable, so absence means a broken harness, not
  optional tests) and the job fails on any `SKIP(symlink-capability)` line in
  the `-v` output. The assertion cannot pass vacuously: the job also fails
  when the output contains no `--- PASS/FAIL/SKIP:` result lines at all, so a
  suite that never ran (today: every package fails to build) can never
  satisfy "zero skips". The degraded job asserts the mirror image: suite
  passes **and** at least one `SKIP(symlink-capability)` line appears, so
  `SCRATCHPAD_TEST_SYMLINKS=0` can only ever move tests into counted,
  asserted skips — never silently erase them. Both jobs keep
  `continue-on-error` (the code does not compile on Windows yet), each with a
  comment naming the removing task: P3.11/P4 for the capable job, P4.4 for
  the degraded job. The review's further suggestion of a zero-skip assertion
  on `linux-test` was not implemented here and remains open under F1's
  recommendation.
- **F2 (MED-HIGH)** — both native Windows test steps now run under
  `if: always()` (guarded on probe success in the capable job), so the test
  step executes and its skip machinery is observable even while vet fails on
  the missing backend.
- **F3 (MED)** — known-failure table regenerated from a clean
  `git archive 30c34a5` extraction (see the P0.1 caption); the four
  undercounted rows corrected and `pruneAt` added with owner P3.6.
- **F4 (MED)** — this tracker's P0.2–P0.5 sections and CI run URLs added;
  Phase 0 checkboxes (P0.1–P0.5) and P2.5 ticked in the plan.
- **F9 (LOW)** — tracker now says `windows-2025`, matching the workflow.
- **F8 (LOW), partially** — added a `concurrency` group
  (`${{ github.workflow }}-${{ github.ref }}`) so superseded runs on the same
  ref cancel, with `cancel-in-progress: ${{ github.ref != 'refs/heads/main' }}`
  so pushes to `main` are never cancelled; added `go mod tidy -diff` to
  `linux-test`. Action SHA-pinning was **not** done — that is a supply-chain
  decision deferred to P5.7.

Deferred, with owners (do not lose these):

- **F5 → P4.4 and P1.6** — whole-test `RequireSymlinks` guards currently hide
  degraded-mode and collision-rejection assertions
  (`TestWatchCreateOnly`, `TestUnwatch`,
  `TestPublishAndNestedWatchRejectSymlinkProject`) that must be split into
  unguarded tests when the Windows watch backend lands (P4.4). And the probe
  asks "can `os.Symlink`?", which is the wrong question if the Phase 1 ADR
  selects junctions — `testutil.SymlinkCapable` must then be re-expressed in
  terms of the store's own link primitive (P1.6).
- **F6 → P3.12** — `RequireNTFS` treats "cannot determine filesystem" as a
  skip, which is a vacuous-pass hole in a required security job; fix when it
  acquires call sites (make "cannot determine" a failure in the required job),
  and soften its doc comment.
- **F7 → P6.1/P6.6** — `-race`, `-count=20`, and the named-suite `-run`
  invocations from the plan's Verification Commands are absent from CI by
  design at this phase.
- **SHA-pinning of actions → P5.7** (see F8 above).

> **OPERATOR ACTION REQUIRED (from the review's "could not verify" #4):**
> `main` has **no branch protection rule** (`gh api
> .../branches/main/protection` → 404). Every job, `linux-test` included, is
> therefore advisory at the repository level, and "make the job required"
> (P2.6, P3.11/P4) is a **no-op** until a protection rule naming the checks
> exists. Configuring branch protection is a repo-admin action that no agent
> should perform; the operator must add a rule on `main` requiring at least
> `linux-test` now, and the Windows checks as their allowances are removed.

## Phase 2 — Extract Shared Platform Boundaries

### P2.5 Portable name validation

Added `internal/store/names.go` (shared, untagged — behaves identically on
every OS) with `checkPortableName`, wired into `validateName` only.
Rejects, case-insensitively and keyed on the portion before the first dot so
an extension does not hide the match: `CON`, `PRN`, `AUX`, `NUL`,
`COM0`-`COM9`, `LPT0`-`LPT9` (Windows' own documented device-name list
includes `COM0`/`LPT0` alongside `COM1`-`COM9`/`LPT1`-`LPT9`). Also rejects
names ending in a trailing dot or trailing space.

Verified the existing `nameRe` (`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,99}$`) already
excludes every Windows-forbidden character (`<>:"/\|?*`, control chars),
leading dots/spaces, and embedded spaces, and — because the character class
is ASCII-only — the Unicode superscript device forms (`COM¹`, `COM²`,
`COM³`) can never reach `validateName` at all, so no code was added for
them. Trailing space was already unreachable through `nameRe`; the trailing
check was still written explicitly so the rule doesn't silently depend on
that staying true.

Deliberately **not** added to `validateSegment`/`existingDir` (lookup,
delete, unwatch): tightening those would make a watched repository's own
`CON`-or-similar-named entries unreachable and undeletable through
scratchpad, which is a regression, not a hardening. Locked down by
`TestValidateSegmentAcceptsDeviceNames` in
`internal/store/names_test.go`.

Compatibility change: this narrows what Linux users can name a new artifact,
watch link, or asset file going forward (previously-legal names like `NUL`,
`nul.html`, `COM1`, or a name ending in `.` are now rejected at create time
only). Existing entries are unaffected — lookup/delete stayed untouched.
Docs updated to match: `skill/SKILL.md`, `docs/cli.md`, `docs/internals.md`,
and the `CLAUDE.md` name-validation paragraph. `README.md` does not state
the naming rule directly (it links out to `docs/cli.md`), so no change was
needed there. `make install-skill` was **not** run (writes outside the repo
into `~/.claude` and `~/.pi`) — pending for whoever ships this.

Verification: `make test` and `go test ./... -count=1` both green, plus
`go test ./internal/store -run 'Name|Publish|Watch|Validate' -v -count=1`
green including the new `TestValidateNamePortable`,
`TestValidateFilePathRejectsDeviceNames`, and
`TestValidateSegmentAcceptsDeviceNames`. No existing test or fixture used a
now-rejected name.

(P0.2/P0.3 CI evidence lives in the Phase 0 sections above.)

### P2.6 — cross-build allowances removed

After P2.1–P2.4 (`36bb133`) both commands compile for `windows/amd64` and
`windows/arm64`, so `cross-build-windows` and `windows-arm64-build` lost their
`continue-on-error` and are now required checks. The two native Windows test
jobs keep theirs — the platform files are stubs that return "not implemented"
until Phase 3, so the suite cannot pass there yet (P3.11/P4, P4.4).

Compiling is not working. A green cross-build proves source portability only.

### Open gap carried into Phase 3/4: `/proc/self/fd` in the web layer

P2.4 removed every Unix API from shared *store* code, but `internal/web/server.go`
still formats Linux `/proc/self/fd/%d` paths at four sites (296, 555, 605, 648)
to turn a pinned directory handle back into a readable path. These **compile on
Windows and silently misbehave** — they are not caught by the cross-build gate,
which is exactly the failure mode P2.4's grep was meant to prevent.

This is the same `fdPath` problem the threat model calls the hardest porting
constraint in the store, surfacing a second time in the web layer. The P1.2
spike measured that `os.NewFile(dup(handle)).ReadDir` works on Windows, which is
the natural replacement.

Owner: **P3.6** for the store-side `fdPath` removal, and **P4.6** for these four
web call sites. Neither may be closed by making the string portable — the point
of the pin is that it is not re-resolved.

### Doc drift repaired

P2.1–P2.4 deleted four confirmed-dead functions (`ensureProjectDir`,
`rejectSymlinkParents`, `pruneEmpty`, `linksTo`). `CLAUDE.md` still described
`Watch`'s same-target idempotence in terms of `linksTo`; it now describes the
behaviour without naming the removed helper.

### P2.7 gate review — PASS WITH FINDINGS

Full record in `reviews/P2.7-boundary-review.md`. The boundary itself is sound:
no interface or VFS was introduced, no helper takes a flags parameter, every
security-carrying flag (`O_NOFOLLOW`, `O_DIRECTORY`, `O_CLOEXEC`,
`AT_SYMLINK_NOFOLLOW`, `AT_REMOVEDIR`) survives verbatim, no descriptor-relative
operation became path-based, and the passing test-name set is byte-identical to
`36bb133^`.

Closed immediately:

- **F1 (high, latent)** — four Windows stubs whose signatures have no error
  channel returned benign constants. `dirHasHTMLFD → false` is fail-**open** in
  two of its three callers, disabling both the artifact-nesting rejection and
  the single-watch-boundary guard; `fdPath → ""` is CWD-relative because
  `filepath.Join("", x) == x`. There is no correct constant, so they now panic
  via `unreachableOnWindows`. `OpenDocument` keeps returning false — there,
  false is unambiguously fail-closed (a 404).
- **F6 (low)** — CI only ran `GOOS=windows go build ./cmd/...`, which never
  compiles test files. Added `GOOS=windows go vet ./...` to the cross-build job.

### Preconditions carried into Phase 3

**Ordering constraint (F4) — binding on P3.1.** `IsLinkEntry`/`IsLinkInfo` have
a real but junction-blind Windows implementation. Today that is fail-closed: a
junction reports `ModeIrregular`, so both `IsLinkEntry(e)` and `e.IsDir()` are
false and `entryIsDir` skips it *before* its follow-through `os.Stat`. Fixing
the classifier to report junctions **without** simultaneously fixing
`entryIsDir`'s follow-through `os.Stat` would convert that skip into a
path-based descent through the junction. A partial fix is worse than none, and
the plan imposes no ordering. Both must change together.

**Vacuous Windows tests (F3) — binding on P3.11.** Some security tests already
appear in the windows-2025 PASS list while asserting nothing: they assert only
`err != nil` and "nothing was created", both trivially true because the stub
refused at line 1. Confirmed for `TestPublishAndNestedWatchRejectSymlinkProject`,
`TestSaveNotesRequiresDocExists`, `TestNotesReadHiddenPath404s`,
`TestListFragmentRejectsInvalidFolders`. These must be made to assert the
*reason* for the failure before P3.11 removes the `continue-on-error` allowance,
or the native job will go green while testing nothing.

Also open: F2 (domain policy inherited on the platform side — `dirHasHTMLFD`
encodes the artifact definition, `openRealDir` owns two user-facing error
strings, `openBrowsableDir` implements invariant 5) → P3.2/P3.6. F5, F7, F8, F9
are recorded in the review.

## Security fix — A11.ancestor_swapped closed on Linux (out-of-band)

`internal/store/storefs_linux.go`'s `openBrowsableDir` re-opened the
readlink(2) result of the store's one permitted watch-link boundary as a
whole path string with `O_NOFOLLOW`. That flag protects only the final path
component; every intermediate component was still resolved by the kernel
following symlinks normally. `A11.ancestor_swapped` (spike-findings.md
§10.1; P1.7 red-team finding F2) measured this on the Windows spike and
confirmed it is platform-independent and already live in the Linux code
that ships today — not a Phase 3 concern. Fixed out of Phase 1/2/3 sequence
because it is a real defect in shipping code, reachable by watched-source
content (a `git checkout` swapping a tracked directory for a symlink), with
payoff being read disclosure over the unauthenticated HTTP endpoint.

Fix: `openAbsoluteDirNoFollow` walks the link target's path components
handle-by-handle from the filesystem root, refusing a symlink (or anything
but a plain directory) at ANY component — the same structural fix both
documents named. `Watch` now resolves the target with
`filepath.EvalSymlinks` at creation time and stores that resolved,
symlink-free path as the actual link target: a legitimately symlinked
ancestor (a moved `/home`, a convenience symlink) is handled once, there;
anything the browse-time walk refuses is therefore a path that changed
*since* the watch was created, which is exactly the attack this closes. The
cost: a convenience symlink repointed after `Watch` is not followed by the
existing watch (it keeps serving the original resolved directory) — re-run
`scratchpad watch` to pick it up.

The Windows backend (Phase 3 stub) has the handle-relative primitive to do
the same thing (the strict `FILE_OPEN_REPARSE_POINT` open from P1.3/P1.5,
per spike-findings.md §10.3), applied to every component of the resolved
target rather than just the final one — noted in `storefs_windows.go`'s
`openBrowsableDir` stub; not implemented here.

## Phase 1 — Win32 Security Spike

### P1.8 — Accept or stop: **ACCEPT**

The gate asks whether a credible race-resistant strategy exists, and whether to
stop and rescope if it does not. It does. Recorded 2026-08-26.

Evidence chain: threat model (P1.1) → prototype and 369 measurements on real
`windows-2025` and `windows-11-arm` runners across nine CI runs (P1.2–P1.5) →
ADR (P1.6) → independent red team (P1.7, verdict ACCEPT WITH CONDITIONS) → ADR
revision 2 closing every blocking condition.

Authoritative run: [32908643117](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32908643117)
at `145583a` — 369 measurement lines (251 YES / 70 INFO / 24 NO / 13
NOT-MEASURED / 11 PARTIAL), 91 REQUIRED properties, zero `SECURITY-FAIL`.

What makes this an accept rather than a hope: every mechanism the Linux backend
relies on has a *measured* Windows twin. The two questions that could have
killed the project — whether intermediate-component containment is reachable
(M1/§2.1) and whether handle-relative atomic replacement exists at all (M9) —
both resolved favourably. The two that resolved against the obvious approach —
`LockFileEx` on a directory handle (M14) and `os.Root` (M17) — each have a
replacement that preserves the property rather than trading it away.

Two blocking findings were closed by *changing the design*, and P1.8 approves
them as design commitments rather than accepting them as established facts:

- **F1** — `OBJ_DONT_REPARSE` is inert for non-Microsoft tags
  (`A5.obj_dont_reparse_inert_for_unknown_tags`): the refusal is "no filter
  driver claimed the tag", not containment, so on a machine running Docker
  (WCIFS) or VFS for Git (ProjFS) the walk traverses. The containment primitive
  is now the strict open — `FILE_OPEN_REPARSE_POINT` plus a
  `FILE_ATTRIBUTE_TAG_INFO` read from the same handle — with
  `OBJ_DONT_REPARSE` demoted to necessary-but-not-sufficient. Six REQUIRED
  properties hold **in the prototype**, not yet in `internal/store`.
- **A11** — still a `NO` in the authoritative run. The Linux half shipped in
  `113cbb2`; the Windows half is a P3 commitment.

Operator decision recorded 2026-08-26: **junctions are accepted** as watch
links at the same tier as symlinks. The spike measured that a junction is the
only link an unprivileged user with Developer Mode off can create
(`P14.devmode_off.*`), and the red team confirmed that everything an attacker
gains from junction acceptance is closed by tag-aware classification, which the
design requires regardless. The spec's "created and identified by the
application" test is unachievable — a store-created junction is byte-identical
to an attacker's — so §10.1 of the ADR retargets that clause rather than
claiming to satisfy it.

Not accepted, carried as conditions on later phases: the three unmeasured beta
dependencies (ReFS/Dev Drive, the antivirus transient-error distribution, and a
genuinely non-elevated session) block *claiming the beta is validated*, not
implementation. Owners are recorded in the ADR's remaining-risk table.

### Out-of-sequence security fix: A11 on Linux

`A11.ancestor_swapped` was measured on the Windows spike and proved
platform-independent: `openBrowsableDir` crossed the one permitted watch
boundary by re-opening the link target as a **path string** with `O_NOFOLLOW`,
which protects only the final component. It is not a race — ancestors were
never validated, at watch time or browse time — and it is reachable by watched
source content (a `git checkout` swapping a directory for a symlink), with the
payoff being read disclosure over the unauthenticated HTTP endpoint.

Fixed in `113cbb2`, out of phase order, because it is a live defect in shipping
Linux code rather than a Phase 3 concern. The demonstrated leak (HTTP 200
serving a planted marker) is now a regression test at both the store and web
layers.

## Phase 3 — Store Backend Implementation (P3.1–P3.6)

Implemented from the ADR revision 2, in three commits: `61f334d` (shared
refactor removing `fdPath`, handle-anchoring `List`/`Watches`/`Resolve`),
`347c9b0` (the Windows backend itself — `win32_windows.go`,
`storefs_windows.go`, `link_windows.go`), `4d87801` (two test fixes surfaced
by the first native run, plus the three exported handle helpers P4.6 needs).

### The three preconditions (§11 Pre-1/Pre-2/Pre-3)

**Pre-1** (`IsLinkEntry`/`IsLinkInfo` + `entryIsDir`'s two traps) could not be
landed as an isolated, narrow fix ahead of P3.6 the way the ADR describes for
a multi-agent handoff: `entryIsDir`'s job — deciding whether `List` should
descend into an entry, and whether that descent crosses the one permitted
link boundary — is inseparable from the walk that calls it. Fixing the
classifier in isolation while `List` stayed path-based would have reproduced
exactly the hazard Pre-1 exists to prevent (a fixed `IsLinkEntry` plus an
unfixed path-based follow-through). Since this agent implemented P3.1–P3.6 as
one continuous body of work rather than as a handoff between separate agents,
Pre-1 was folded directly into the `List`/`Watches`/`WatchLinkFor` rewrite
(§6.8 item 4, §6.9 rows 6–7): `classifyEntry` is now the single, handle-based,
tag-aware classification point every one of those three functions uses, and
`entryIsDir` no longer exists. **Pre-3** (`unreachableOnWindows` panics)
disappeared naturally — every stub it guarded now has a real implementation.
**Pre-2**'s domain-policy hoist was done partially: `dirHasHTMLFD`'s
`.html`-suffix predicate and `openBrowsableDir`'s invariant-5 policy are both
expressed once conceptually (the same shape on both platforms) but are not
literally shared code — see "What was scoped down" below.

### What is implemented

Every function in the ADR's §3.2/§3.3 API table for `storefs_windows.go` and
`link_windows.go`, ported from `internal/winspike/winfs.go` and `links.go`:
the strict open (`openStrictAt`/`openRealDirAt`/`openRealFileAt`), the
create-only claim (`mkdirClaim`) with the three-way taken-name error map
(`translateClaim`), `statAt`/`statLinkTarget`/`classifyEntry` (tag-aware,
handle-based classification — never `fs.FileMode`), `rootedFS` with the
process-level root-identity cache (§4.1, closing F9), `openRealDir` and
`openBrowsableDir` (the latter with the handle-by-handle ancestor walk from
the volume root that closes `A11.ancestor_swapped`, §4.3), `readlinkAt` with
the `SYMLINK_FLAG_RELATIVE` refusal and the `\??\Volume{` refusal, `symlinkAt`
with the two-step-window self-heal (rule 1 only — see below),
`canonicalizeWatchTarget`/`alreadyInsideRoot`/`sameWatchTarget` (§4.3/§4.7/
§7.1/§7.2), `readDirFD`/`dupFD`/`closeFD`, `pruneAt`, `openPathFile`/
`OpenDocument`, and the error-translation table (§3.7) as `winError` chaining
to `fs.ErrExist`/`fs.ErrPermission`/`fs.ErrNotExist`/`errExists`. `fdPath` is
gone from the tree entirely (not stubbed — deleted), on both platforms.

**Verified natively**, not just by cross-compilation: run `32928654626` on
`windows-2025` (commit `4d87801`) passed, with no `SECURITY-FAIL` and no
skipped-for-annotations substitute — `TestBrowseRefusesWatchAncestorSymlinkSwap`
(the store-level A11 regression), `TestArtifactHandlerRefusesWatchAncestorSymlinkSwap`
(the HTTP-level A11 regression), `TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix`,
`TestPinnedMutationsIgnoreProjectSwap`, `TestPublishAndNestedWatchRejectSymlinkProject`,
`TestOpenDocumentRejectsArtifactAssetSymlink`, `TestIsLinkFalseForPlainArtifact`,
`TestIsLinkTruePositiveThroughResolvePath`, `TestListAndResolvePath`,
`TestResolveFolderContainmentAndWatchedTrees`, `TestWatchCreateOnly`,
`TestWatchResolvesSymlinkedAncestorAtCreation` and `internal/winspike`'s own
suite (no `SECURITY-FAIL`) all passed on real `windows-2025` hardware.

### What was scoped down, named rather than silently dropped

- **`removeTreeAt`** (P3.9) is untouched — still `errWindowsUnimplemented`.
  Publish's rollback path and Delete's real-directory branch call it and will
  fail until P3.9 lands; this is the expected, correct boundary, not a gap in
  P3.1–P3.6.
- **`annotationfs_windows.go`** (P3.7–P3.10) is untouched. Every `internal/store`
  test failure in the native run traces to one `errWindowsUnimplemented` from
  that file (confirmed by grepping the failure text in the CI log, not
  assumed).
- **§6.8 item 5's three exported helpers** (`ReadDirHandle`, `EntryIsDirAt`,
  `StatEntryAt`) were added to `store.go` so P4.6 does not also have to invent
  platform mechanism for `internal/web/server.go`'s four remaining
  `/proc/self/fd` sites (`docCount`, `buildCards`/`folderExtras`, `siblings`,
  `hasRenderable` — confirmed by inspection; not modified). `StatEntryAt`
  returns primitive fields rather than the unexported `entryMeta`, so it is
  usable from `internal/web` without exporting that type.
- **Pre-2's typed `pathError{Kind, Seg}`** was not introduced. `openRealDir`'s
  two user-facing error strings are reproduced with matching wording directly
  in `storefs_windows.go` rather than shared through a typed error the Linux
  side also adopts — a real, intentional scope cut (not an oversight) to avoid
  touching `storefs_linux.go`'s already-passing error paths under this
  deadline. A future pass can introduce the shared type without behavior
  change.
- **`watchLinkFlavour`** is written (probes by claiming and immediately
  removing a throwaway name) but not called from any production path —
  `symlinkAt`'s own try-symlink-then-junction fallback is what actually
  matters at runtime. Kept for P3.11/P3.12's `WatchLinkCapable`/degraded-mode
  tests.
- **R18's ReFS/non-NTFS warning** fires once per process, gated on `create`
  (Publish/Watch's entry point) rather than precisely "before the first
  mutation" — an approximation, not the exact gate text.

### Two test fixes required by real hardware, not by design changes

The first native run (`32928256806`) found two `internal/store` test
failures that were real, in-scope bugs surfaced only by running on actual
Windows: `TestUnwatch` and `TestWatchResolvesSymlinkedAncestorAtCreation`
compared a `WatchLink.Target` string for **byte-exact equality** against a
path built by `t.TempDir()`. The `windows-2025` runner's `TEMP` resolves to
an 8.3 short-name alias (`RUNNER~1`), while `canonicalizeWatchTarget`'s
`GetFinalPathNameByHandleW` call — required by the ADR specifically because
`filepath.EvalSymlinks` cannot resolve junctions — returns the long name
(`runneradmin`). Both name the same directory. Fixed by comparing directory
identity (`os.SameFile`) instead of spelling (commit `4d87801`), which does
not weaken either assertion (both still fail if the watch resolves to the
wrong directory) and is correct on both platforms. Re-run (`32928654626`)
confirmed both pass.

A pre-existing Linux bug was also found and fixed while building this:
`readDirFD` (`annotationfs_linux.go`) `dup()`s the caller's fd, but a
duplicated fd **shares** the original's directory-read position, so a second
`readDirFD` call against the same original fd — newly possible once
`dirHasHTMLFD` and `loadArtifactAt` can both be called against one handle in
the same operation — silently returned zero entries. Fixed by rewinding
(`Seek(0, SEEK_SET)`) before every read; confirmed the Windows equivalent
does not need this (`M16`'s "each duplicate restarts enumeration
independently" measurement holds — the native run's passing `TestWatch`/
`TestListAndResolvePath`/etc., which all exercise `loadArtifactAt` after
`dirHasHTMLFD` on the same handle, is the confirmation).

### Disagreements with the ADR, recorded rather than silently resolved

- **§3.2's `statLinkTarget` and `entryMeta.IsDir`'s Windows semantics collide
  with what `Watches()`'s listing decision needs.** The ADR defines
  `entryMeta.IsDir` as "`FILE_ATTRIBUTE_DIRECTORY` and no reparse tag" and
  `statLinkTarget` as "never follows... a reparse-tagged entry answers
  `isDir=false`". Taken literally, `Watches()` could never learn whether a
  watch link points at a directory without a separate mechanism, because the
  no-follow answer is unconditionally `false` for any link. This is not a
  contradiction in the mechanism — a directory-type `SYMLINK`/`MOUNT_POINT`
  **does** carry `FILE_ATTRIBUTE_DIRECTORY` on its own reparse-point entry,
  readable without following anything — but the ADR's own field definition
  (IsDir excludes anything tagged) hides that fact. Resolved with a third,
  narrower function, `linkTargetIsDir`, that reads the raw attribute bit
  directly rather than reusing `entryMeta.IsDir`. Recorded because a
  literal-minded implementation of §3.2 alone would have missed this.
- **§6.6's `watchLinkFlavour() linkFlavour` (no arguments) cannot answer the
  question it is named for without side effects.** Whether a symlink or a
  junction will be created depends on live privilege/Developer-Mode state
  that can only be observed by attempting a real claim (`FILE_CREATE` then
  `FSCTL_SET_REPARSE_POINT`) — a true no-op probe does not exist. Implemented
  as `watchLinkFlavour(parent int, probeName, target string) (linkFlavour, error)`,
  which claims and immediately removes a throwaway name. `symlinkAt` itself
  does not call it — it has its own inline try-then-fallback — so this exists
  purely as a capability query for tests. The ADR's zero-argument signature is
  not implementable as an actual probe; recorded rather than forced.
- **§8.4's "P3.1 ... windows.Write must gain a short-write loop" does not
  apply to anything P3.1–P3.6 actually calls.** Every write in this package's
  scope goes through `os.NewFile(handle, ...).Write(data)`
  (`writeFileAt`), and `os.File.Write` already loops internally on a short
  write — the same guarantee Linux's `os.File.Write` provides, which is
  exactly why the ADR calls the Linux behavior out as the thing to match. A
  raw `windows.Write` call only appears in the annotation atomic-replace path
  (P3.8), which this agent did not implement. The checklist item is real but
  is P3.8's to close, not P3.1's — noted so it is not assumed already done.

### Verification run IDs

- `32928256784`/`32928256806` — first native push (commit `347c9b0`). Both
  native jobs failed as expected; failures triaged to exactly two categories:
  genuine bugs in this agent's scope (the `Target` string comparisons above)
  and out-of-scope stubs (`annotations_test.go`, `internal/watch`'s identity
  stub, `internal/web`'s two `ListFragment` tests).
- `32928654619`/`32928654626` — second native push (commit `4d87801`), after
  fixing the two in-scope test failures. Remaining native-job failures are
  now *entirely* attributable to `errWindowsUnimplemented` from
  `annotationfs_windows.go` (P3.7–P3.10), `identity_windows.go`'s stub
  (P4.1/P4.2), and `internal/web/server.go`'s four `/proc/self/fd` sites
  (P4.6) — confirmed by grepping the failure text for each, not assumed from
  the task list. `internal/winspike`'s own suite passed with zero
  `SECURITY-FAIL` in both runs. Local gates (`make test`;
  `go test ./... -count=1`; `go test ./internal/store -race -count=3`;
  `GOOS=windows GOARCH={amd64,arm64} CGO_ENABLED=0 go build ./...`;
  `GOOS=windows go vet ./...`; `go mod tidy -diff`) all clean at `4d87801`;
  Linux passing-test count is 105 (up from 96 at the start of this branch),
  0 skips.

## Phase 3 — Store Backend Implementation (P3.7–P3.10)

Implemented from the ADR revision 2, on top of P3.1–P3.6, in four commits:
`43cf095` (the Windows annotation backend itself — `annotationfs_windows.go`
rewritten in full, `annotationfs_linux.go`'s `lockRendezvous`/depth bound,
`annotations.go`'s lock refactor, `ignore.go`/`storefs_{linux,windows}.go`'s
`nameEquals`/`lockFileName`), `ece9391` (the P3.7–P3.10 permanent tests
migrating ADR §11.1's required properties out of `internal/winspike`),
`f6db3b1` (a real bug the first native run found: `annotations.go` used
`os.IsNotExist`, not `errors.Is(_, fs.ErrNotExist)`).

### What is implemented

- **P3.7 annotation root.** `openAnnotationFS` pins the store root (reusing
  `openRootedFS`, so the process-level root-identity cache and R18's warning
  apply here too), then the new reserved rendezvous file
  `<root>\.scratchpad-lock` (`openRendezvousLockFile`, `FILE_OPEN_IF`,
  identity recorded in a lock-specific cache), then `.annotations`
  (`mkdirClaim` + `openRealDirAt`, the strict open), in that order and before
  any lock is taken — the §6.7 ordering rule. `flockFile`/`funlockFile`/
  `openLockFileAt` (the per-document locks `lockDocument` uses) are real
  `LockFileEx` implementations over an ordinary file, unlike the rendezvous:
  LockFileEx refuses a directory handle outright (`ERROR_INVALID_PARAMETER`,
  confirmed live in this run's own `internal/winspike` suite,
  `M14.dir_readhandle`), so `lockRendezvous`/`unlockRendezvous` is the F5
  rework — a byte-range lock on the new lock file, `LOCKFILE_FAIL_IMMEDIATELY`
  plus a bounded retry (reusing the write path's measured bound; there is no
  separate measurement for the lock, and the code says so). The second
  reserved name is wired into `Visible` through a new `nameEquals` platform
  pair (`==` on Linux, `strings.EqualFold` on Windows, per M11's case-folding
  measurement). `annotations.go`'s `annotationLock`/`lockAnnotations`/
  `lockDocument` were refactored to close through a closure rather than fixed
  fields, since the two lock constructors close structurally different things
  per platform.
- **P3.8 atomic write.** `atomicWriteFileAt`, ported from
  `internal/winspike/atomicwrite.go`'s `AtomicWriteFile`: a unique create-only
  temp (`.notes-<hex>.tmp`), `NtSetInformationFile(FileRenameInformationEx,
  REPLACE|POSIX)` with the class-65→10 fallback restricted to the
  allowlisted "class unsupported" statuses (never a blanket retry), the
  measured bound (10 attempts, 2ms→256ms, 2s ceiling), and cleanup through
  the temp's own handle on every failure path (`deleteByHandlePosix`, never a
  name-based unlink). `Flush` defaults to `false` (`P13.flush_cost`'s 5.7×
  cost, matching Linux's own no-`fsync` behaviour).
- **P3.9 safe recursive removal — the release gate.** `removeTreeAt` /
  `removeTreeAtDepth` is open-then-classify-from-the-handle: the attempted
  `openRealDirAt` (the STRICT primitive) is the classification, and its
  failure (a `*reparseRefusal` or `errNotDir`) routes to `deleteEntryAt`
  (no `FILE_DIRECTORY_FILE`/`FILE_NON_DIRECTORY_FILE` constraint, so it
  covers a junction, either symlink flavour, an unknown-tag directory or a
  plain file in one call) rather than a descent. `statAt`/`classifyEntry`
  play no role in this function at all. Also closes the ADR-noted
  carried-forward depth-bound gap on **both** platforms in the same change:
  `removeTreeAtDepth` (Windows) and `annotationfs_linux.go`'s twin now both
  bound recursion at `maxArtifactWalkDepth` (store.go), which the ADR's §4.5
  named as pre-existing and unfixed on Linux too.
- **P3.10 annotation walk.** `walk`/`walkAnnotationDir` mirrors the Linux
  walk's shape over `readDirFD` + `classifyEntry`: directories entered
  depth-first, `.json` files read and handed to `visit`, anything else
  (an allow-listed link, an unrecognised tag) silently skipped exactly as
  Linux's `switch` on `S_IFMT` skips anything that is not `S_IFDIR`/`S_IFREG`.
  Malformed-JSON handling is unchanged, shared code (`WalkNotes`).

### The bug the first native run found, and the fix

Run `32931073624`/`32931073638` (commit `ece9391`) failed every test in
`internal/store`'s `annotations_test.go` that reads through a fresh sidecar
(`TestLoadSaveNotesLifecycle`, `TestConcurrentSameRevisionExactlyOneWins`,
`TestSaveNotesRevMismatch`, `TestResolveAndReplyNote`,
`TestConcurrentResolveAndReplyPreserveBoth`,
`TestStaleSaveCannotRecreateAfterFinalDeletion`, `TestWalkNotesAndOpenCount`),
plus every `internal/web` notes test that seeds through `SaveNotes`
(`TestNotesWriteConflict`, `TestNotesWriteHappyPathBumpsRev`,
`TestConcurrentNotesWritesExactlyOneSucceeds`,
`TestConcurrentNotesPutAndDeleteCannotRecreateStaleNotes`,
`TestNotesFormatNegotiation`, `TestNotesFolderShadowing`, `TestNotesDelete`,
`TestNotesReportGzipped`, `TestNotesStatusFilter`), all with the same
message: `open: file does not exist (NTSTATUS 0xC0000034)` surfacing as a
hard error instead of "no notes yet".

Root cause: `annotations.go`'s `loadNotesRaw`/`WalkNotes` used
`os.IsNotExist(err)`, which predates Go's `errors.Is`/`Unwrap` convention.
`os.IsNotExist` only recognises a fixed set of concrete shapes
(`*PathError`/`*LinkError`/`*SyscallError`, or a type with its own `Is`
method) — it never walks an arbitrary `Unwrap` chain. Windows's `*winError`
(`win32_windows.go`) chains to `fs.ErrNotExist` through `Unwrap` exactly as
ADR §3.7 specifies, and `errors.Is(err, fs.ErrNotExist)` correctly says
`true`, but `os.IsNotExist` never follows that chain and said `false`. Linux
never surfaced this because raw `unix.ENOENT` (`syscall.Errno`) implements
its own `Is(error) bool` method, which `os.IsNotExist`'s internal check does
recognise — so the same predicate happened to work by accident on one
platform and not the other. Fixed by switching both call sites to
`errors.Is(err, fs.ErrNotExist)` (commit `f6db3b1`): a strict fix, not a
behaviour change, on Linux.

### Disagreements with, and clarifications of, the ADR

- **§3.4's `flockFile(f *os.File, exclusive bool) error` is kept for
  per-document locks only, exactly as the ADR's own text says** ("NEW —
  replaces `flockFile(ann.storeRoot, …)`" for the rendezvous;
  `lockRendezvous`/`unlockRendezvous` are the new pair for that). This is not
  a disagreement, but the earlier Phase-3-stub comment in
  `annotationfs_windows.go` read as if `flockFile` itself needed to become
  the rendezvous primitive; it is now a straightforward `LockFileEx` over an
  ordinary file, unrelated to the redesign.
- **The retry bound for `lockRendezvous`/`flockFile` reuses
  `defaultReplacePolicy()`** (10 attempts, 2ms→256ms, 2s ceiling) rather than
  a bound of its own. The ADR does not specify separate numbers for the lock,
  and no run-9 measurement targets lock acquisition specifically (only the
  annotation write's replace loop, `P13.bound`/`P13.retry.*`). Reusing the
  measured replace-loop bound is the least arbitrary available choice; the
  code says so explicitly rather than implying a second measurement exists.
  If a future stress campaign (P6.1) finds the lock needs different numbers,
  this is the line to change.
- **§11.1's migration table assigns `A6.delete.unknowntag.depth{0,2}`
  jointly to P3.9/P3.12; this pass ships only the junction and symlink
  flavours** (`TestRemoveTreeAtLeavesJunctionTargetIntact`). Planting an
  "unknown tag" reparse point needs raw `FSCTL_SET_REPARSE_POINT` buffer
  construction with a non-Microsoft tag, which `internal/store` does not
  expose (only `setSymlinkReparse`/`setMountPointReparse`, both tag-fixed).
  `removeTreeAtDepth`'s containment argument does not depend on which tag is
  present — a strict-open failure of *any* shape routes to `deleteEntryAt`
  identically — but the unknown-tag case is not independently exercised by a
  permanent test yet. Left for P3.12, which already owns the harder
  hook-driven matrix.
- **The `A6.swap_midwalk` determinism hook (`"annotation-tree-entry"`) is a
  new call site through the existing shared `testStoreOpHook`/
  `runStoreOpHook` mechanism (`store.go`), not one of P3.12's six named
  hooks** (`root-open`, `browse-segment`, `doc-open`, `notes-replace`,
  `notes-remove`, `watch-reconcile`). It was necessary to make P3.9's own
  release-gate property deterministic rather than sleep-based, and it does
  not collide with any of the six names P3.12 is scoped to add.
- **The namespace-removal audit** (`writeAuditStart`/`writeAuditStop`/
  `recordNamespaceRemoval` in `annotationfs_windows.go`) is new test-only
  instrumentation migrating `P13.audit`/`P13.no_dest_removal`/
  `P13.audit_control` permanently, mirroring `internal/winspike`'s
  `AuditStart`/`AuditStop`. It hooks `deleteEntryAt` only (the one name-based
  removal primitive a "remove-then-rename" degradation would call); the
  write path's own temp cleanup goes through `deleteByHandlePosix` directly
  and so never appears in the log during a correct replace. Off by default
  (a single untaken boolean check on the production path).
- **Not attempted, and named rather than silently dropped**: the six shared
  race hooks and the hook-driven `A1`/`A2`/`A4`/`A7`/`A8` matrix (P3.12);
  `WatchLinkCapable`/`SymlinkCapable` re-expression (P3.11); the full
  migration-inventory sign-off (P3.11/P3.12 own it jointly, P3.13 verifies).

### Verification run IDs

- `32931073624`/`32931073638` (commit `ece9391`) — first native push of
  P3.7–P3.10. Both native jobs failed as expected, but triage (grepping the
  actual failure text, not assuming) showed every `internal/store` failure
  and every annotation-related `internal/web` failure shared one root cause
  (the `os.IsNotExist` bug above); `internal/watch`'s failures were all
  `identity_windows.go`'s pre-existing stub (P4.1/P4.2); `internal/web`'s
  remaining two (`TestListFragmentBrowsesWatchWithoutFollowingNestedLinks`,
  `TestListFragmentAppliesFolderIgnoreRules`) trace to the `/proc/self/fd`
  sites P4.6 owns. `internal/winspike`'s own suite passed with zero
  `SECURITY-FAIL` in both jobs, and `MATRIX.Delete.target_replaced` (the
  release gate) held `YES`.
- `32931416952`/`32931416953` (commit `f6db3b1`, after the fix) —
  `scratchpad/internal/store` reports **`ok`** on both native jobs
  (`Windows amd64 native — degraded mode, no symlinks` and `Windows amd64
  native — full suite, symlink-capable`), including every new P3.7–P3.10
  test (`TestRemoveTreeAtSwapMidWalkAndNegativeControl` and its two
  subtests, `TestRemoveTreeAtLeavesJunctionTargetIntact` at depth 0 and 2,
  every `TestAtomicWrite*`, `TestAnnotationLockRendezvousSharedAndExclusive`,
  `TestAnnotationLockIdentitySwapDetected`, `TestVisibleHidesLockFileName`)
  and the entire pre-existing `annotations_test.go`/`store_test.go` suite.
  **No annotation-related failure remains on either native job** — the only
  remaining native failures are `internal/watch` (P4.1/P4.2's stub) and
  `internal/web`'s two `/proc/self/fd`-dependent tests (P4.6), exactly the
  set this task's brief named as out of scope. `internal/winspike` again
  passed with zero `SECURITY-FAIL`; `MATRIX.Delete.target_replaced` again
  held `YES`. Both native jobs are still `continue-on-error`
  (`[allowed to fail until P3.11/P4]`/`[allowed to fail until P4.4]`), which
  this task did not touch, per instruction. Windows arm64 native build and
  both Windows cross-builds (amd64/arm64, `go vet` included) passed, as did
  the Linux job. Local gates before each push
  (`make test`; `go test ./... -count=1`; `go test ./internal/store -race
  -count=3`; `GOOS=windows GOARCH={amd64,arm64} CGO_ENABLED=0 go build
  ./...`; `GOOS=windows go vet ./...`; `go mod tidy -diff`) were clean at
  every commit in this range; Linux skip count stayed at 0.
