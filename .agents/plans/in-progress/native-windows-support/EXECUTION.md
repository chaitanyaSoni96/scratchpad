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

## Phase 4 — Web Layer (P4.6)

### What is implemented

The four `/proc/self/fd` sites ADR §6.8 item 5 named — `docCount` (was line
296), `buildListView`/`buildCards`/`folderExtras` (was line 555), `siblings`
(was line 605), `hasRenderable` (was line 648), all in
`internal/web/server.go` — are replaced with the handle-taking primitives
`internal/store` exported for exactly this (`ReadDirHandle`, `EntryIsDirAt`,
`StatEntryAt` — the last was not needed, see below). None of the four sites
now builds a path string from a directory handle; each reads and classifies
directly off the `*os.File` `store.ResolveFolder` already returns:

- `docCount` and `hasRenderable` drop their `dir` string entirely, exactly as
  §6.8 item 5 anticipated — they already held the handle. `dirCount`'s
  per-child `dirHasHTML(sub)` check (which also consumed the `/proc` path)
  is replaced by a new `dirHasHTMLAt(rel string)`, a fresh
  `ResolveFolder`+`ReadDirHandle` resolution keyed on the store-relative
  child path rather than a path derived from the parent's handle — consistent
  with `docCount`'s own recursion, which already re-resolves each child from
  the root rather than threading handles down.
- `buildCards`/`folderExtras` now take folder `f`'s pinned `*os.File`
  directly instead of a `dir string` built from its fd. This also fixes the
  latent bug the ADR called out: `folderExtras`'s `pageCard(...,
  filepath.Join(dir, name), ...)` call for a loose markdown tile now joins
  against `logicalFolderPath(f)` (an ordinary, already-computed absolute
  path used elsewhere in the same function for visibility checks) instead of
  the `/proc`-derived string, so the loose-markdown tile's size/mtime stat
  resolves on Windows instead of silently failing.
- `siblingItems` replaces `os.ReadDir(dirPath)` with `store.ReadDirHandle`
  and its `dirHasHTML(filepath.Join(dirPath, name))` call with
  `dirHasHTMLAt(rel)`, reusing the `rel` it already computes.

`entryIsDirFS` (the old path-based classifier: `e.IsDir()` first, then an
`os.Stat` fallback for link entries) and the old path-based `dirHasHTML` are
both deleted — every call site now goes through `store.EntryIsDirAt` or the
handle-based `dirHasHTMLAt`, so nothing in the package still builds an
"is this a directory" answer from a path string. No `GetFinalPathNameByHandleW`
or any other handle-to-path emulation was introduced; each comment at the
four sites says explicitly why that would reinstate the TOCTOU the pin
exists to remove (ADR §6.8, §2 table).

`store.StatEntryAt` was exported for this refactor but is unused by it: none
of the four sites need per-entry size/mtime, only the directory/link
classification `EntryIsDirAt` gives. Left unused rather than removed, since
`internal/store` was out of scope for this task and the helper is generically
useful. No additional `internal/store` export was needed beyond the three
already provided.

### Preserved, and where the tests prove it

- **Preview cap / `card.Heavy`.** `maxPreviewBytes` and `previewBytes` (which
  measure the artifact's entry document via `os.Stat(filepath.Join(a.Dir,
  a.Entry))`) are untouched — that call is §6.9 row 3, accepted, not one of
  the four sites. `artifactCard`/`withPreview`/`pageCard` are unchanged;
  `folderExtras`'s new `pageCard(..., filepath.Join(visibleDir, name), ...)`
  call still goes through `pageCard`, so the cap still applies to loose
  markdown tiles. No new card-construction path was added.
- **Sandbox model.** Nothing in `templates/*.tmpl` or the iframe/`sandbox`
  attributes was touched; this task only changed how folder contents are
  enumerated and classified server-side.
- **Notes shadowing rule.** `handleFolderPage`'s `/p/{path...}/notes`
  fallback (resolved only after `resolveView`, `resolveCollection` and
  `folderExists` all fail) is unchanged; `TestNotesFolderShadowing` passes.
- **Visibility / collapse / collections / SSE.** Unchanged logic, same
  `store.Visible` call sites (now fed `isDir` from `EntryIsDirAt` instead of
  `entryIsDirFS`, which is the same classification `List` already uses via
  `classifyEntry` — a junction reads as browsable identically in both
  places). `handleEvents`/`gzip.go` untouched.

Tests proving the specific regression is fixed:
`TestListFragmentBrowsesWatchWithoutFollowingNestedLinks` and
`TestListFragmentAppliesFolderIgnoreRules` (`internal/web/server_test.go`) —
the exact two tests EXECUTION.md's Phase 3 entries traced to these four sites
— both pass locally, along with the full `internal/web` suite (24 tests) at
`-race -count=3`, `go test ./... -count=1`, both Windows cross-builds
(amd64/arm64), `GOOS=windows go vet ./...`, and a Linux skip count of 0. Only
`internal/web/server.go` was modified; no test was weakened, skipped, or
deleted.

### Other Linux-only assumptions checked for and not found live

Grepped `internal/web` for `/proc`, `/dev`, `/tmp`, hardcoded path
separators, and `filepath` vs `path` confusion:

- `strings.Split(path, "/")` / `strings.Split(rel, "/")` call sites
  (`crumbs`, `resolveRequest`, `resolveView`, `handleSiblings`, etc.) all
  operate on store-relative/URL paths, which are a "/"-separated convention
  independent of OS (`store.Artifact.RelPath()` and the `/p/`, `/a/` URL
  space are always "/"-joined) — not native filesystem paths. Correct as-is.
- `handleArtifact` already converts the URL-style suffix to a native path
  correctly (`filepath.Clean(filepath.FromSlash(file))`) before
  `filepath.Join(a.Dir, clean)`.
- `folderUnwatch`'s `os.Lstat(filepath.Join(root, filepath.FromSlash(rel)))`
  already uses `FromSlash` and classifies via `store.IsLinkInfo`, which ADR
  §3.3 confirms is the measured-correct cross-platform classifier (junction →
  `ModeIrregular`, no separate Windows path). Not a `/proc` bug and not one
  of the four named sites; left as is.
- `markdown.go`'s path-taking `serveMarkdown(w, r, path, title)` (as opposed
  to the handle-taking `serveMarkdownFile`) has no live caller in
  `server.go`/`notes.go` — dead code, unrelated to this task, not removed
  since removing unrelated dead code was not asked for.
- No other `/proc`, `/dev`, or `/tmp` reference exists anywhere in
  `internal/web`.

### Verification run IDs

- Baseline (before this task's push): `32931416952`/`32931416953` — the two
  `internal/web` failures on both native jobs were confirmed to be
  `TestListFragmentBrowsesWatchWithoutFollowingNestedLinks` and
  `TestListFragmentAppliesFolderIgnoreRules`, matching Phase 3's trace.
- `32932302339` (commit `3b69d43`, this task's push) — `scratchpad/internal/web`
  reports **`ok`** on both native jobs (`Windows amd64 native — full suite,
  symlink-capable` and `Windows amd64 native — degraded mode, no symlinks`),
  confirmed by grepping the raw log for the package line, not the job's
  overall conclusion. Both jobs still report an overall `failure` because
  `scratchpad/internal/watch` still `FAIL`s there — that is P4.1/P4.2's
  concurrent stub, explicitly out of this task's scope, and unrelated to
  `internal/web`. Both native jobs remain `continue-on-error`
  (`[allowed to fail until P3.11/P4]` / `[allowed to fail until P4.4]`),
  untouched by this task. Windows arm64 native build and both Windows
  cross-builds (amd64/arm64) passed, as did the Linux job
  (`vet`/`test`/`check-make`/`build`, `internal/web` again `ok`). Local gates
  before push (`make test`; `go test ./... -count=1`; `go test internal/web
  -race -count=3`; `GOOS=windows GOARCH={amd64,arm64} go build ./...`;
  `GOOS=windows go vet ./...`) were clean; Linux skip count stayed at 0; the
  passing `internal/web` test-name set (24 tests) is unchanged from before
  this task — none renamed, skipped, or removed.

## Phase 4 — Watcher Identity and Reconciliation (P4.1/P4.2)

### What is implemented

**P4.1 — `identity(*os.File)`.** `identity_windows.go` replaces the Phase 2
stub with `GetFileInformationByHandleEx(FileIdInfo)` read from the
already-open handle — volume serial (`uint64`) plus the 128-bit `FileId`
(`[16]byte`), never `ByHandleFileInformation`'s 64-bit file index (ADR §3.5,
survey Finding 6: insufficient on ReFS). `dirIdentity`'s field layout, which
P2.3 deliberately deferred, moved out of `watch.go` entirely and into the two
platform files — `identity_unix.go` keeps `dev, ino uint64`;
`identity_windows.go` gets `vol uint64, id [16]byte`. The two platforms do
not share one struct shape (Windows's identity is one opaque 16-byte object
id plus a separate volume serial, not two independent numbers the way
dev+ino are), so rather than force a common shape this is P2.3's decision,
made now: each platform owns its own type, and shared code only ever
compares values with `==` or stores them as map values, which needs nothing
more than that.

No `internal/store` export was needed for this, and none was added.
`ntOpenAt`/`attrTagOf`/`fileIDOf`/`openStrictAt` are all package-private to
`internal/store`, and `store.ReadDirHandle`/`EntryIsDirAt`/`StatEntryAt`
(P4.6's exports) are root-pinned-handle-relative — they answer questions
about a path already resolved through the store's containment walk, which
does not describe what `desiredDirs` does: it walks arbitrary absolute
filesystem paths, including ones outside `SCRATCHPAD_ROOT` entirely (a
watched project tree reached through a symlink target). `identity_windows.go`
calls `windows.GetFileInformationByHandleEx`/`windows.FileIdInfo` directly —
both already exported by `golang.org/x/sys/windows` — and declares its own
24-byte `fileIDInfo` struct mirroring `FILE_ID_INFO`'s layout, because that
struct itself (not the call, not the constant) is the one thing
`x/sys/windows` v0.41.0 doesn't export and `internal/store`'s copy
(`fileIDInfoRaw` in `win32_windows.go`) is package-private. This is an
unavoidable two-line restatement of a public, stable Win32 ABI shape, not a
duplication of any containment mechanism — nothing NT-call-shaped
(`ntOpenAt`'s marshaling, the strict-open tag check, error translation) is
reimplemented. If a single source of truth for this struct is wanted later,
the fix is trivial (`internal/store` exports a `DirFileID(*os.File) (vol
uint64, id [16]byte, err error)` wrapping the existing `fileIDOf`), but it
was not added unasked, per the brief's instruction to report rather than
restructure `internal/store`.

**P4.2 / F6 / §6.11 — the reconcile-triage boot loop.** This was treated as
the most important part of this task, per the brief. Traced exactly as the
ADR describes: `desiredDirs`' `walk` (`watch.go`) returned a hard
`fmt.Errorf` from any `os.Open`/identify/`ReadDir` error that was not
`os.IsNotExist`; on Windows an unserviced reparse tag — `APPEXECLINK`, a
OneDrive placeholder, a ProjFS entry — returns `STATUS_IO_REPARSE_TAG_NOT_HANDLED`
→ Win32 `ERROR_CANT_ACCESS_FILE` (1920), which is not `IsNotExist`; that
error propagated out of `reconcile()`, out of `newWatcher()` (called
synchronously at startup), and `cmd/scratchpad-web/main.go`'s
`log.Fatalf` turned a single such directory anywhere under the store root
into a permanent failure to start — confirmed live on this run's baseline
(`32932994845`): every `internal/watch` test failed with `identify
directory ...: scratchpad: Windows directory identity is not implemented
yet`, and, once identity landed, a dedicated regression test reproduced the
exact 1920 failure end to end before the triage fix (see below).

The fix is a narrow carve-out, not a relaxation: a new per-platform
`skipWalkError(err) bool` (in `identity_unix.go`/`identity_windows.go`)
classifies an error from resolving/opening/identifying/reading one directory
during the walk as either "this one entry is unreachable" (skip, log once,
via `watch.go`'s new `skipEntry`) or everything else, which still returns a
hard error and is still fatal through the same
`reconcile`→`newWatcher`→`main.go` chain as before. On Linux
`skipWalkError` is unchanged behaviour: `os.IsNotExist` only. On Windows the
skip set is explicit and named, not a numeric range (the Win32 error space
interleaves unrelated codes — thread/process background-mode, GDI handle
leaks, SMB1 — among the `ERROR_CLOUD_FILE_*` codes, so a range check would
misclassify them): `os.IsNotExist`, `fs.ErrPermission`,
`ERROR_CANT_ACCESS_FILE` (1920, the boot-loop trigger itself),
`ERROR_SHARING_VIOLATION`/`ERROR_LOCK_VIOLATION`, the five
`ERROR_FILE_SYSTEM_VIRTUALIZATION_*` codes (ProjFS/Windows Containers — the
ADR's third named example alongside `APPEXECLINK` and OneDrive), and the
full `ERROR_CLOUD_FILE_*` family by name. Root's own open/read (`required =
true` in `walk`) is deliberately **not** given this leniency: root failing
to open at all means there is nothing to watch, which is a real "the watch
subsystem cannot start" condition, not a single-entry problem — the ADR's
boot-loop scenario is specifically about an ordinary entry *inside* the
tree, and item 3's "startup gets the same triage as steady state" is
satisfied for free, because `reconcile()` (which item 3 is about) is the
same function whether called from `newWatcher` at startup or from `Run` at
steady state.

**RW24 — `desiredDirs`' share mode.** `os.Open` is replaced by a new
`openWatchDir(path string) (*os.File, error)`, per platform.
`identity_unix.go`'s version is a passthrough (the share-mode concept does
not exist on Linux). `identity_windows.go`'s version is a direct
`windows.CreateFile` granting `FILE_SHARE_READ|FILE_SHARE_WRITE|FILE_SHARE_DELETE`
plus `FILE_FLAG_BACKUP_SEMANTICS` (required to open a directory at all) —
measured as the fix `P13.go_share_mode` calls for: Go's `syscall.Open` (what
`os.Open` uses) and `golang.org/x/sys/windows.Open` both hard-code
`FILE_SHARE_READ|FILE_SHARE_WRITE` only, confirmed by reading both
implementations, not just the ADR's citation. This does not reuse
`internal/store`'s `ntOpenAt`/`openStrictAt`: those are handle-relative,
single-component, root-pinned primitives built for the store's containment
proof, and `desiredDirs` walks arbitrary absolute paths that can be outside
the root entirely — the root-anchored primitive does not apply. What ships
is a plain, path-based `CreateFile`, the same shape as `os.Open`, with the
one flag that matters fixed.

**`M15.overflow`.** No code change was made or needed. Reading
`github.com/fsnotify/fsnotify@v1.10.1`'s `backend_windows.go` (the version
this module already pins) shows its completion-port loop already detects
the `ReadDirectoryChangesW` buffer-overflow condition as a 0-byte
`GetQueuedCompletionStatus` return — exactly the shape
`internal/winspike/dirnotify.go`'s `DirObserver` uses and the shape the ADR
says to copy — and reports it as the same cross-platform
`fsnotify.ErrEventOverflow` sentinel Linux's inotify backend uses.
`watch.go`'s `Run` already special-cases `errors.Is(err,
fsnotify.ErrEventOverflow)` and routes it to `reconcile()` + broadcast,
never treating it as fatal, identically on both platforms — this predates
this task and needed no change. This satisfies the ADR's ask ("map the
Windows overflow error explicitly") because the mapping already happens one
layer down, inside fsnotify itself, into a value `internal/watch` already
handles correctly. **Verified by source inspection, not by CI
reproduction** — consistent with `M15.overflow` being recorded as not
deterministically reproducible on a runner; the existing
`TestOverflowReconcilesAndBroadcastsImmediately` (fake backend, synthetic
`fsnotify.ErrEventOverflow`) is the only executable coverage and passes
natively on both Windows jobs, which shows `internal/watch`'s side of the
contract is correct but does not exercise real `ReadDirectoryChangesW`
overflow.

### New tests

`reparse_windows_test.go` (Windows-only): stamps an empty directory with a
non-Microsoft reparse tag via `FSCTL_SET_REPARSE_POINT`/
`REPARSE_GUID_DATA_BUFFER` — the wire format mirrors
`internal/winspike/links.go`'s `SetUnknownTag`, measured working in this
repo's own CI evidence, but is reimplemented rather than imported, since
`internal/winspike` is scheduled for deletion (ADR §11.1) and `internal/watch`
must not depend on it.

- `TestDesiredDirsSkipsUnservicedReparseTagInsteadOfFailing` — the tagged
  directory is excluded from the desired watch set; its sibling and the root
  are not.
- `TestNewWatcherStartsDespiteUnservicedReparseTag` — the end-to-end
  regression test for the boot loop: `newWatcher` (the startup path) must
  not fail because of the tagged directory.
- `TestOpenWatchDirGrantsFileShareDelete` — a directory held open by
  `openWatchDir` does not veto a concurrent rename of it.

All three passed on the native run cited below. A fourth defensive posture —
skip the whole test via `t.Skip` if `FSCTL_SET_REPARSE_POINT` with a
non-Microsoft tag is refused on some future runner policy — mirrors
`testutil.RequireSymlinks`'s existing pattern for a missing OS capability;
not exercised on either evidence runner (the write succeeded on both).

### A latent test bug this task's fix surfaced, and fixed

Before `identity()` worked, every `internal/watch` test failed at the first
`os.Open`/identify call, so four tests' actual assertions were never
reached natively. Once identity worked, native CI (run `32932994845`) showed
`TestReconcileReplacesWatchWhenDirectoryIdentityChanges`,
`TestReconcileReplacesWatchWhenLinkTargetIdentityChanges`,
`TestReconcileRemovesTargetWatchAfterUnwatch`, and
`TestRealBackendWatchesReplacementDirectory` all failing — not because
identity or reconciliation were wrong, but because each test compared a raw
`t.TempDir()`-built path against backend bookkeeping keyed on
`canonicalDir`'s output. On the `windows-2025` runner `TEMP` resolves to an
8.3 short-name alias (`RUNNER~1`) while `canonicalDir`'s
`filepath.EvalSymlinks` normalizes to the long name (`runneradmin`) — the
identical symptom, and identical fix, already applied once in this branch to
`internal/store`'s tests for an analogous comparison (commit `4d87801`,
`TestUnwatch`/`TestWatchResolvesSymlinkedAncestorAtCreation`'s `sameTarget`).
Fixed with a `mustCanonical(t, path)` test helper built on the package's own
`canonicalDir`, used at each comparison site. This also caught a masked bug
in `TestRealBackendWatchesReplacementDirectory` itself: it read
`w.registered[dir]` with the raw (uncanonicalized) key, which silently
missed the map and returned the zero `dirIdentity` both before and after the
replacement — passing for the wrong reason on any platform where the string
mismatch is large enough to always miss. Fixed by reading under the
canonical key and asserting the pre-replacement identity is not the zero
value. No test's assertion was weakened; each still checks exactly what it
checked before, with a comparison that is correct on both platforms.

### Verification run IDs

- Baseline: run `32932994845` (commit `2ef00a8`, this task's first push,
  identity implemented, triage/share-mode fix present, new tests present) —
  `scratchpad/internal/watch` **passed 10 of 14 tests** on the
  `full suite, symlink-capable` job (the three new boot-loop/share-mode
  tests included), confirming F6/RW23/RW24 fixed; the four canonicalization
  tests above failed, root-caused as above from this run's raw log rather
  than guessed.
- `32933382724` (commit `c74db88`, this task's second push, test-only fix)
  — **`scratchpad/internal/watch` reports `ok` on both native jobs**
  (`Windows amd64 native — full suite, symlink-capable` and
  `Windows amd64 native — degraded mode, no symlinks`), confirmed by
  grepping each job's raw log for the package result line and for zero
  `--- FAIL` occurrences in either log. Both jobs report an overall
  **`success`** conclusion (not merely `continue-on-error` masking a
  failure) — the first time either native job has been fully green.
  `scratchpad/internal/store` and `scratchpad/internal/web` also `ok` on
  both native jobs. Windows arm64 native build and both Windows cross-builds
  (amd64/arm64, `go vet` included) passed, as did the Linux job. Local gates
  before each push (`make test`; `go test ./... -count=1`; `go test
  ./internal/watch -race -count=3`; `go test ./internal/watch -count=20`;
  `GOOS=windows GOARCH={amd64,arm64} go build ./...`; `GOOS=windows go vet
  ./...`; `go mod tidy -diff`) were clean; Linux skip count stayed at 0. One
  unrelated, non-reproducing flake (`TestPublishAndNestedWatchRejectSymlinkProject`
  in `internal/store`, a package this task did not touch) was observed once
  locally under load and did not reproduce across five standalone reruns or
  three full-suite reruns; not investigated further as out of scope.
  `continue-on-error` was left on both native jobs untouched, per
  instruction — only `internal/web`'s two `/proc/self/fd` tests (P4.6,
  already landed by another agent on this branch) and this task's own watch
  fixes were in scope, and both are now clean; the only thing keeping either
  native job's allowance in place is that removing it is explicitly owned by
  other tasks (P3.11/P4 and P4.4), not this one.

### Under-specified in the ADR

- §6.11 names the skip set as "`errReparse` (including 1920), `errSharing`,
  `fs.ErrPermission`, and the `ERROR_CLOUD_FILE_*` family" but does not
  mention the `ERROR_FILE_SYSTEM_VIRTUALIZATION_*` codes, even though §6.11's
  own prose names ProjFS as a boot-loop trigger alongside `APPEXECLINK` and
  OneDrive. Included them anyway (five named constants), since a ProjFS
  provider that is not running or not installed produces exactly one of
  these, not `ERROR_CANT_ACCESS_FILE`.
- Root's own leniency is left to the implementer. §6.11 says "a single
  unreadable entry anywhere under the store root" and §11's P4.2 row says
  nothing about the root directory itself; this report states the decision
  made (root stays strict) and the reasoning, since the ADR does not settle
  it explicitly.
- "Logged once" (§6.11 item 1) is not defined precisely — once ever, once
  per process, once per reconcile pass, or once per occurrence. Implemented
  as "log a line every time `skipEntry` is called", i.e., once per walk
  visit to that entry per `reconcile()` invocation; no cross-reconcile
  deduplication state was added. If a persistently-unreadable entry logging
  on every 250 ms–1 s reconcile cycle is judged too noisy in practice, adding
  a `map[string]bool` on `*Watcher` to deduplicate across calls is a small,
  self-contained follow-up.
- M15.overflow's Windows-side mapping turned out to already be handled by
  the fsnotify dependency rather than needing new code in this package; the
  ADR's phrasing ("must be mapped explicitly in Phase 3/4") reads as if
  `internal/watch` itself needs new mapping logic, when in fact the mapping
  is fsnotify's and was already consumed correctly. Worth a note for anyone
  auditing this against the ADR literally.

## Phase 4 — CLI End-to-End Tests (P4.5)

### What is implemented

New file `cmd/scratchpad/main_e2e_test.go`. Every test drives the actual
compiled CLI as a subprocess against a real temporary `SCRATCHPAD_ROOT`
(`t.TempDir()`) — not the internal helper functions `main_test.go` already
unit-tests (`publishFiles`, `filesFromDir`).

`TestMain` lets the compiled test binary double as `scratchpad` itself:
re-exec'd with `SCRATCHPAD_CLI_E2E_HELPER=1` (set only by this file's
`runCLI`/`runCLINoArgs` helpers), the process calls `main()` on its own
`os.Args` and exits exactly as the production binary would — the
`testing.M` + `os.Executable()` re-exec pattern named in the task, chosen
over `go build`: it needs no build step and no `.exe`-suffix bookkeeping,
and it was confirmed to type-check on Windows by running `GOOS=windows
GOARCH={amd64,arm64} go vet ./...`, which compiles test files too.
`SCRATCHPAD_URL` is pointed at a closed local port (`http://127.0.0.1:1`) in
every invocation so `webAlive()`'s liveness probe fails on immediate
connection refusal instead of blocking its 700ms timeout — keeps the suite
fast (`-race -count=3` ~141s for ~100 subprocess spawns) without touching
`main.go`'s networking code at all.

Coverage, by area (exit codes asserted as literal integers throughout, per
the task's "assert the actual codes" instruction):

- **publish**: `-html`, `-html -css -js`, stdin (`-`), `-dir` with nested
  assets; the create-only guarantee (a second publish of a taken name fails
  exit 1 with the actionable "already exists" message, and the original
  bytes on disk are unchanged); the P2.5 portable-name rule — `CON` as
  `-name`, `COM1` as a `-project` segment, trailing dot, trailing space,
  `nul.html` and `COM1.tar.gz` as file paths inside `-dir` (whole publish
  fails, nothing partial lands on disk), plus a negative control
  (`console`/`nul2.html` are correctly NOT rejected); exactly-one-of-`-dir`/
  `-html`; `-css`/`-js` require `-html`; excess positional arguments (exit
  2).
- **list**: `-json` shape parsed back into `[]store.Artifact` (checks
  `Name`, `Entry`, `Size`), plain-text output, excess arguments (exit 2),
  and the empty-store shape for both forms.
- **watch/watches/unwatch**: full lifecycle (watch → watches → list →
  unwatch → watches empty → source content unchanged); same-target
  idempotence (re-watching is a no-op, not an error — exactly one entry
  remains); different-target collision under the same name (exit 1,
  "already exists", the original watch's target is provably unchanged);
  excess arguments on both `watch` and `unwatch` (exit 2); empty `watches`.
- **delete**: normal delete, delete of a nonexistent name (exit 1), excess
  arguments (exit 2), and delete of a *watched* top-level entry — CLAUDE.md
  documents "`Delete` unlinks watched entries without touching the source",
  confirmed here by reading the source file's content back unchanged and
  confirming the link vanishes from both `watches` and `list`.
- **notes**: markdown report and `-json`; `reply` (appends without
  closing) and `resolve` (closes with a summary) verified both by CLI
  stdout and by re-reading the sidecar via `store.LoadNotes`; `-all` vs the
  default open-only view; `resolve` on a missing id (exit 1, "no such
  note"); `resolve`/`reply` without `-m` (exit 2); the `userOnlyVerbs`
  rejection for `create`/`edit`/`delete`/`reopen` (exit 2, the real
  explanation text, and explicitly asserted to NOT contain "No open notes"
  — the text a silently-read-as-a-path regression would produce instead);
  a negative control confirming a real document path that merely starts
  with a rejected verb's letters (`deleteme-report/index.html`) is still
  read as a path; excess positional arguments on read/resolve/reply.
  Notes are authored directly through `store.SaveNotes` in test setup
  (`t.Setenv(store.RootEnv, root)`) since the CLI deliberately has no way to
  create one — the exact asymmetry these tests protect.
- **general**: no arguments, unknown command, `-h`/`--help`/`help`, and the
  web-down warning on stderr (every command that prints a hosted URL warns
  there without failing when the server is unreachable).

### Windows specifics

- No new `testutil.RequireSymlinks` call was added, deliberately, after
  checking `symlinkAt` (`internal/store/link_windows.go`) rather than
  assuming: it tries a real symlink first and, on
  `ERROR_PRIVILEGE_NOT_HELD` (or any reparse-set failure), falls back to a
  junction — the file's own comment: "Junctions are accepted at the same
  trust tier as symlinks... rejecting them would only remove `watch` from
  every machine with Developer Mode off." `scratchpad watch` is therefore
  expected to succeed on a Developer-Mode-off machine via that fallback,
  and gating the CLI's watch/watches/unwatch tests behind
  `RequireSymlinks` would have wrongly skipped exactly the scenario P4.4/
  P4.5 exist to cover. Every watch-lifecycle test in this file
  (`TestCLIWatchListUnwatch`, `TestCLIWatchSameTargetIsIdempotent`,
  `TestCLIWatchDifferentTargetCollision`) is ungated and asserts success
  purely by CLI exit code and `watches`/`list` output, never by inspecting
  the link's raw reparse type.
- Also confirmed CI's "degraded mode" job (`windows-degraded-test`,
  `SCRATCHPAD_TEST_SYMLINKS=0`) is a test-harness-only switch:
  `testutil.SymlinkCapable`/`RequireSymlinks` are the only readers of that
  env var anywhere in the tree (`grep -rn SCRATCHPAD_TEST_SYMLINKS internal/
  cmd/` finds nothing else); production `symlinkAt` never consults it. An
  ungated CLI watch test therefore still exercises the real OS symlink path
  in both native jobs (both `windows-2025` runners are genuinely
  symlink-capable at the OS level; only the test suite's *belief* differs
  between the two jobs) — correct for CLI-level tests, since whether
  `watch` truly falls back to a junction with the privilege genuinely
  absent is `internal/store`'s own concern (P1.4/P3.11/P3.12), not
  re-verified at this layer.
- Path-identity trap (8.3 short names / canonicalization, the trap already
  hit twice per the task brief): `store.Watch` canonicalizes its target
  (`canonicalizeWatchTarget`) before storing the link, so the path a test
  passes to `watch` and the path `watches` prints back can legitimately
  differ in spelling on Windows. Every comparison against a watch target in
  this file goes through a local `sameDir(t, a, b)` helper (`os.Stat` +
  `os.SameFile`) rather than a raw string comparison — the same fix class
  as commit `4d87801` (`internal/store`) and `internal/watch`'s
  `mustCanonical`, applied at the CLI layer.
- `runCLI`/`os.Executable()` re-exec avoids a separate `go build` step and
  any `.exe`-suffix handling entirely: the already-running test binary IS
  the CLI under test on every platform `go test` itself runs on.

### CLI bug found and fixed

`list -json` on an empty store encoded the literal JSON `null` (a nil Go
slice marshals that way) instead of `[]`, while `notes -json` already
normalizes the equivalent nil-slice case via `openOnly`. Fixed in
`cmd/scratchpad/main.go`'s `list` case: `artifacts` is defaulted to
`[]store.Artifact{}` before encoding when `store.List()` returns nil.
`TestCLIListJSONEmpty` pins the corrected shape. This is the only
production-code change in this task; no existing test's assertions were
touched or weakened.

Also noted, not changed (a small independent behavior/exit-code decision,
out of this task's scope): `watch`'s missing-target and `unwatch`'s
missing-name paths exit 1 via `fatal()` even though their message text is a
usage string ("usage: scratchpad watch <folder>..."), while every
*excess-argument* case in the same two commands exits 2 via
`usageFatal()`. This reads as an existing, intentional-looking asymmetry
(missing a required value vs. a malformed argument shape) rather than a
bug, so it was left alone; the new tests assert the current real exit code
(1) for the missing-argument cases rather than assuming 2, per the task's
"assert the actual codes, not merely non-zero" instruction.

### Verification (local, before push)

```
go build ./...                                              clean
go vet ./...                                                clean
go test ./... -count=1                                      ok (all 5 packages)
go test ./cmd/... -race -count=3                             ok, 141s
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go vet ./...          clean
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go vet ./...          clean
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./cmd/...    scratchpad.exe + scratchpad-web.exe produced
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build ./cmd/...    scratchpad.exe + scratchpad-web.exe produced
go test ./... -v -count=1 | grep -c '^--- SKIP'               0
```

Test-name set is a pure superset of before: all four pre-existing
`cmd/scratchpad` tests (`TestPublishFilesValidation`,
`TestPublishFilesReadsStdin`, `TestFilesFromDirRejectsNonRegularEntries`,
`TestFilesFromDirRejectsNamedPipe`) are present and pass unmodified, plus
25 new top-level `TestCLI*` functions (several table-driven with subtests)
added by `main_e2e_test.go`.

### Verification run IDs

- CI run [`32934858837`](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32934858837)
  (commit `eff57e1`, this task's push) — **overall conclusion `success`**,
  all six jobs green: Linux amd64 (vet/test/check-make/build), both
  Windows cross-builds, Windows arm64 native build, and — the two jobs this
  task cares about — both native Windows amd64 jobs report `ok` for
  `scratchpad/cmd/scratchpad` and `scratchpad/cmd/scratchpad-web`:
  - **`Windows amd64 native — full suite, symlink-capable (windows-2025)`**
    (job `98074035096`): probe logs "symlink capability: PRESENT"; **all 33
    new `TestCLI*` functions report `PASS`, zero `FAIL`**; the job's own
    non-vacuous guard reports "symlink-capability skips: 0 (as required)" —
    none of this task's new tests appear in any skip list, confirming they
    were not accidentally gated.
  - **`Windows amd64 native — degraded mode, no symlinks (windows-2025)`**
    (`SCRATCHPAD_TEST_SYMLINKS=0`, job `98074035144`): **the same 33
    `TestCLI*` functions report `PASS`, zero `FAIL`**, including all five
    watch-lifecycle tests (`TestCLIWatchListUnwatch`,
    `TestCLIWatchSameTargetIsIdempotent`,
    `TestCLIWatchDifferentTargetCollision`, `TestCLIWatchExcessArguments`,
    `TestCLIWatchesEmpty`) — none of them appear among this run's 26
    `SKIP(symlink-capability)` lines (all 26 belong to pre-existing
    `internal/store`/`internal/watch` tests, verified by name). This is the
    task's "measured on CI rather than assumed" answer for whether `watch`
    works without symlink capability: on this runner the OS-level privilege
    is genuinely present regardless of the job's name (`windows-2025`
    GitHub-hosted runners are symlink-capable, and `SCRATCHPAD_TEST_SYMLINKS`
    only steers this repo's own test-side `RequireSymlinks` gate — see
    "Windows specifics" above), so what this run actually demonstrates is
    that the CLI watch tests do not themselves depend on that test-side
    belief; it does not by itself measure the real junction fallback with
    the OS privilege genuinely revoked, which remains `internal/store`'s
    own scope (P1.4/P3.11/P3.12/the winspike measurements).
  - Windows arm64 native build (`98074035136`) and the separate Winspike
    workflow run (`32934858903`) are also green on this commit, unaffected
    by this task (no files it owns were touched).

## Phase 3 — Verification (P3.11–P3.12)

Implemented on top of P3.1–P3.10 in four commits: `f1f12ec` (checkpoint after
an interrupted first pass — `checkLookupSegmentPlatform`, the `RequireNTFS`
F6 fix, and the initial F3 fixes/audit), `51bfa0e` (the shared hook-driven
attack matrix), `dda2e77` (the Windows-only reparse-tag attack matrix, the
deferred unknown-tag helper, and the additive `WatchLinkCapable`).

### P3.11 — the F3 audit

P2.7 finding F3's binding precondition: several tests in the pre-P3 PASS list
asserted only `err != nil`/`ok == false`, which the old `errWindowsUnimplemented`
stub satisfied trivially. All four named examples were re-verified against the
now-real backend and fixed to assert the *reason*:

- `TestPublishAndNestedWatchRejectSymlinkProject` (`store_test.go`) now
  requires the error to name the symlinked segment and call it a
  symlink/link/reparse point, on both the Publish and the Watch half.
  **This surfaced a real, independent bug while fixing it**: on this
  environment's kernel, `O_DIRECTORY|O_NOFOLLOW` against a directory-target
  symlink returns `ENOTDIR`, not `ELOOP` (confirmed with a 20-line C
  reproduction, not just the Go wrapper) — `openRealDir`
  (`storefs_linux.go`) checked `ELOOP` only, so the friendly "project
  ancestor is a symlink" message was silently dead code on that kernel and
  every caller saw a bare "not a directory" instead. Fixed by checking both
  errnos and classifying the entry (via `statAt`, still handle-relative, no
  re-resolution) before wording the message, so a plain non-directory file
  blocking the same path is not misreported as a symlink either. This is a
  message-quality bug, not a containment break: Publish/Watch still refused
  the operation correctly either way.
- `TestSaveNotesRequiresDocExists` (`annotations_test.go`) now asserts the
  error names the doc "no such document".
- `TestNotesReadHiddenPath404s` and `TestListFragmentRejectsInvalidFolders`
  (`internal/web`) were read and audited but **not modified**: `internal/web`
  is outside this task's constraints. Both assert a specific HTTP status
  (404) across several distinct scenarios (hidden `.annotations` path;
  `..`, percent-encoded traversal, `.git`, a missing folder, and an
  escaped-then-broken symlink target for the list fragment), which is a
  stronger check than a bare `err != nil` but is still satisfiable by "the
  backend always 404s" in the F3 sense. Flagging for whoever next touches
  `internal/web` (P4.6 already landed; this would be a small follow-up) to
  strengthen with a positive control the way the store-side fixes below did.

Broader audit of `internal/store`'s test suite for the same pattern found and
fixed five more instances that were reachable through the real backend and
would have passed vacuously against the old stub:

- `TestAnnotationSymlinkComponentsRejected` (`annotations_test.go`): added a
  positive control (an identically-shaped "control" doc with no symlink must
  succeed at the same four calls) and an explicit check that nothing was
  written into the symlink's external target — proving the failure is caused
  by the planted symlink, not a general backend defect. (The actual OS error
  text here is the same generic "not a directory" as above; a positive
  control was used instead of string-matching for exactly that reason.)
- `TestPublishFilesAndRules`'s artifact-nesting case, `TestWatch`'s
  delete-inside-watched-tree case, `TestUnwatch`'s missing-entry case, and
  `TestWatchCreateOnly`'s publish-over-watch-link case (all `store_test.go`):
  each now asserts the specific error text instead of only `err == nil`.

Not found to be vacuous (audited, left as-is, with the reasoning): every
pure-string-validation test (`validateName`, `validateSegment`,
`ValidateFilePath`, `notesPath`) never touches the backend at all, so a
broken backend cannot make them pass; `TestResolveFolderContainmentAndWatchedTrees`
and the `ResolvePath`/`OpenDocument` boolean-only assertions elsewhere in
`store_test.go` already carry positive controls in the same test function;
the two `annotationfs_windows_test.go` lock-conflict assertions
(`TestAnnotationLockRendezvousSharedAndExclusive`,
`TestAnnotationLockIdentitySwapDetected`) are preceded by successful lock
acquisitions in the same test, so a broken lock implementation could not
produce the asserted failure by accident.

### P3.12 — the attack matrix

**Hooks.** Six named in the ADR (§11/R17): `root-open`, `browse-segment`,
`doc-open`, `notes-replace`, `notes-remove`, `watch-reconcile`. Before this
task, only `publish-claim`, `watch-link`, `unwatch`, `delete` (pre-existing,
Phase 3.1–3.6) and `annotation-tree-entry` (P3.7–P3.10, `A6.swap_midwalk`)
existed. This task added the five in its own scope — `root-open` (fires at
the end of `openRootedFS`, both platforms), `browse-segment` (fires once per
resolved component inside `openBrowsableDir`, both platforms), `doc-open`
(fires in `openPathFile` after the parent is pinned, both platforms),
`notes-replace` (fires in the annotation write path after the temp file is
written and before the first rename attempt, both platforms), `notes-remove`
(fires in `removeSubtree` after the parent is pinned and before removal,
both platforms) — each mirrored byte-for-byte at the same logical point on
Linux and Windows so a shared test can install one hook and run on both.
`watch-reconcile` was **not** added: it belongs in `internal/watch`, which is
explicitly out of this task's scope (owned by the concurrent P4.1/P4.2 work).

**New tests**, shared unless noted:

- `TestPublishConcurrentClaimExactlyOneWins` (A8.concurrent_claim, shared):
  16 goroutines racing one `Publish` name — exactly one winner, 15 clean
  losers, no merged/partial content.
- `TestDeleteRacingSaveNotesNeverLeavesOrphanedNotes` (RW5/RW6, shared): the
  `notes-remove` hook races a concurrent `SaveNotes` against `Delete`'s
  cleanup, proving the annotation rendezvous lock actually serializes them.
- `TestSaveNotesDestinationReplacedBeforeReplace` (A2.dest_replaced,
  realfile/realdir, shared) and `TestSaveNotesDestinationReplacedWithLinkNeverEscapes`
  (A2.dest_replaced, junction/dirsymlink, Windows-only — the genuine escape
  attempt): via `notes-replace`.
- `TestPublishSurvivesRootRenamedAwayMidOperation` (shared) and
  `TestPublishSurvivesRootReplacedWithJunctionMidOperation` +
  `TestRootIdentityCacheDetectsCrossOperationReplacement` (Windows-only):
  A4.root_replaced's within-operation half (both platforms, F-b) and the
  Windows-only cross-operation identity-cache detector (§4.1/F9), which has
  no Linux analogue.
- `TestBrowseSegmentSwappedForLinkMidWalkStillRefused` and
  `TestDocOpenSubstitutedAfterParentPinnedStillRefused` (shared): mid-walk
  timing variants of the existing static A3/A10 coverage, via
  `browse-segment` and `doc-open`.
- `TestRejectArtifactAncestorCaseInsensitiveHTMLExtension` (shared): closes
  the spike's "Partial" Publish/artifact-ancestor row with a **passing**
  demonstration rather than an assumed defect — `dirHasHTMLFD`'s
  `strings.ToLower` suffix check folds a plain ASCII `.HTML` extension
  identically to NTFS's `$UpCase` table; the M11 case-folding concern is
  about non-ASCII disagreement elsewhere in a filename, which this
  suffix-only probe never inspects.
- `TestPublishAncestorReplacedWithJunctionOrUnknownTag` (Windows-only): the
  two reparse flavours of A1.ancestor_replaced the shared
  `TestPinnedMutationsIgnoreProjectSwap` doesn't cover (realdir/symlink only).
- `TestRemoveTreeAtLeavesUnknownTagTargetIntact` (Windows-only): the deferred
  `A6.delete.unknowntag.*` flavour (depth 0 and 2), built on the new
  `makeUnknownTagReparseAt` helper — see below.
- `TestWatchViaJunctionIsListedAndUnwatchable` and
  `TestUnknownTagEntryIsInvisibleAndInert` (Windows-only): "Watches ⊆
  Unwatch-able" for the junction flavour end-to-end, and confirmation that an
  unrecognised tag is inert (Scope C) rather than crashing or
  misclassifying, across `List`/`Watches`/`Delete`.
- `TestTwoStepCrashResidueCurrentlyLeavesNameStuck` (Windows-only,
  informational): documents A7's *current* pre-rule-3 behaviour rather than
  asserting a verdict the code doesn't earn yet — see "Deliberately not
  implemented" below.
- `TestDirectoryHardLinksDoNotExist` (Windows-only): empirically confirms
  `MATRIX.EXCLUDED.directory_hard_links` rather than only citing the spike's
  measurement of the same fact.
- `TestRequireNTFSWiredIntoAlternateStreamTest` (Windows-only): the first
  call site for `testutil.RequireNTFS`.

**The deferred raw reparse-buffer helper.** P3.7–P3.10 deferred
`A6.delete.unknowntag.*` because "no such helper existed" for planting a
non-Microsoft reparse tag. Built: `makeUnknownTagReparseAt`
(`storefs_windows_attack_test.go`), a direct port of
`internal/winspike/links.go`'s `SetUnknownTag` (a `REPARSE_GUID_DATA_BUFFER`:
tag + length + reserved + a fixed GUID + an 8-byte inert payload), adapted to
this package's `ntOpenAt`/`put16`/`put32`. Used by the unknown-tag delete and
listing tests above.

**`checkLookupSegmentPlatform` (`names_windows.go`/`names_linux.go`,
`store.go`).** Closes the "Documents / alternate stream syntax" Partial row
for real, not just with a test: the ADR (§7.5, R11) says `validateSegment`
should refuse `:`, a trailing dot/space, and reserved device names in a
*lookup* segment on Windows — and the code did not do this at all before this
task (`M12.C_stream`/`M12.relative_open`: a `RootDirectory`-relative open of
`doc.html:hidden` does not 404, it opens a second hidden stream). Implemented
as a platform-pair no-op-on-Linux extension (":" is a legal Linux filename
byte watched repositories use, per the ADR's own explicit instruction not to
reject it there). **This required correcting an existing test's assumption**:
`TestValidateSegmentAcceptsDeviceNames` asserted, unconditionally, that
lookup must never reject a reserved device name or trailing dot/space — true
on Linux, and true before this task on Windows too, but the ADR (written
after that test) deliberately narrows it on Windows. Split the test by
`runtime.GOOS` rather than forking it, since the *doctrine* (lookup stays
loose) is shared and only Windows's answer changed; added
`TestValidateSegmentADSSyntax` alongside it for the colon case specifically.

**`testutil.RequireNTFS` (F6).** Was `t.Skipf` on "cannot determine the
filesystem", which the review correctly identified as a vacuous pass at the
CI-job level (no job here counts skips as failures). Changed to `t.Fatalf`.
Given its first call site (`TestRequireNTFSWiredIntoAlternateStreamTest`)
above.

**`testutil.WatchLinkCapable`/`RequireWatchLinks` (ADR §8.5), additive
only.** `SymlinkCapable`/`RequireSymlinks` are kept unchanged and still have
their existing call sites in `internal/web` and `internal/watch` — both
explicitly out of this task's scope, so migrating them (and the
`SCRATCHPAD_TEST_WATCH_LINKS` CI wiring the ADR also describes, which touches
`.github/`) was not attempted. `WatchLinkCapable` probes a symlink first,
then a self-contained junction creation (deliberately not importing
`internal/store`, to keep `testutil` dependency-free), so a Developer-Mode-off
account that can still make a junction is correctly reported capable — the
gap a bare `os.Symlink` probe has.

### Deliberately not implemented, and why

- **A7 rule 3** (widen `Delete`/`Unwatch` to remove an empty non-artifact
  directory left behind by the two-step link-creation crash window) is P4.3's
  deliverable per the ADR's own consequences table, not P3.11/P3.12's, and is
  genuinely absent from `internal/store` as of this task — verified by
  writing the test that would need it and watching it correctly fail to find
  the widening (`TestTwoStepCrashResidueCurrentlyLeavesNameStuck`, kept as an
  **informational** test that documents current behaviour, not a
  HELD/BROKEN verdict, matching the spike's own framing for this item). Rule
  1 (self-heal on a synchronous FSCTL failure, inside `symlinkAt`) was
  already implemented by P3.7–P3.10 and is not independently retestable here
  without a privilege-token-manipulation harness this package does not have
  (`internal/winspike/privilege.go` built one; `internal/store` does not).
- **The `internal/web` F3 instances** (`TestNotesReadHiddenPath404s`,
  `TestListFragmentRejectsInvalidFolders`) were audited, not fixed — outside
  this task's constraints. See the F3 audit above.
- **`watch-reconcile`**, the sixth named hook, was not added — it belongs in
  `internal/watch`, out of scope here and under concurrent work.
- **A9 (rename-failure-status matrix)** already has permanent coverage from
  P3.7–P3.10's atomic-write tests (`TestAtomicWriteRetryBoundExhaustedPreservesDestination`,
  `TestAtomicWriteRidesOutTransientSharingViolation`); not duplicated here.
- **MATRIX.EXCLUDED rows** (`smb`, `refs_devdrive`, `fat32`,
  `cloud_placeholders`, `antivirus_distribution`, `readdirchanges_overflow`,
  `non_elevated_session`, `32bit`) are recorded verbatim, with owners, in a
  doc comment in `storefs_windows_attack_test.go` rather than invented as
  tests — per the task's own instruction not to fabricate coverage for rows
  the spike could not measure on a GitHub-hosted runner.

### `internal/winspike` migration status

Per the ADR §11.1 inventory, checked against what now has a permanent home
outside `internal/winspike`:

| Spike property | Status |
|---|---|
| `A5.*`, `A3.nested_strict.*` (strict-open proof) | Migrated implicitly — these are properties of `openRealDirAt`/`openBrowsableDir` themselves, exercised by every test in this task and P3.1–P3.10 that walks a real or reparse-tagged path; no separate `A5.*`-named test exists, by design (the property is structural, not a single measurement) |
| `A6.swap_midwalk` + negative control | Migrated (P3.7–P3.10): `TestRemoveTreeAtSwapMidWalkAndNegativeControl` |
| `A6.delete.{junction,symlink}.*`, `A6.parent_replaced`, `A6.unlink_watch.*` | Migrated (P3.7–P3.10) |
| `A6.delete.unknowntag.*` | **Migrated by this task**: `TestRemoveTreeAtLeavesUnknownTagTargetIntact` |
| `A11.target_swapped`/`A11.ancestor_swapped` | Migrated (Phase 2/3, out-of-band Linux fix + `openBrowsableDir` rewrite): `TestBrowseRefusesWatchAncestorSymlinkSwap` (shared), `TestOpenAbsoluteDirNoFollowRefusesSymlinkAncestor` (Linux) |
| `A10.rename_race`, `A10.file_link_refused`, `A10.dir_link_refused` | Migrated (pre-existing `TestOpenDocumentRejectsArtifactAssetSymlink` et al.) plus this task's `TestDocOpenSubstitutedAfterParentPinnedStillRefused` (mid-open timing variant) |
| `A12.concurrent_writers`/`.concurrent_temp_residue` | Migrated (P3.7–P3.10) |
| `P13.continuous_existence` + control, `P13.no_dest_removal`/`.audit`/`.audit_control`, `P13.sharing_never_truncates`, `P13.bound_preserves_dest`, `P13.retry_integrity.*`, `P13.go_share_mode` | Migrated (P3.7–P3.10) |
| `A1.ancestor_replaced.*` | Migrated: realdir/symlink pre-existing (`TestPinnedMutationsIgnoreProjectSwap`), junction/unknowntag **by this task** |
| `A2.dest_replaced*` | Migrated **by this task**: realfile/realdir (shared) + junction/dirsymlink (Windows) |
| `A4.root_replaced.*`, `A4.root_reparse_refused.*` | Migrated **by this task** (realdir/junction + cross-operation cache); `.root_reparse_refused` was already covered structurally by `openRootedFS`'s own tag check, exercised incidentally by every test that opens a root, not by a dedicated test |
| `A7.two_step_*` | Characterised (not "held") **by this task**: `TestTwoStepCrashResidueCurrentlyLeavesNameStuck` — the fix (rule 3) is still P4.3's, not shipped |
| `A8.concurrent_claim` | Migrated **by this task**: `TestPublishConcurrentClaimExactlyOneWins` |
| `MATRIX.Delete.target_replaced` | Migrated (P3.7–P3.10, the RW1 release gate) |
| `M1`–`M18` micro-measurements (e.g. `M11` case-folding, `M12` ADS syntax) | Migrated where they have a policy consequence: M11 via `TestVisibleHidesLockFileName` (P3.7–P3.10) and `TestRejectArtifactAncestorCaseInsensitiveHTMLExtension` (this task); M12 via `checkLookupSegmentPlatform`'s tests (this task) |

**Still only in `internal/winspike`, with no permanent home**: the
micro-measurement functions themselves (`M1`–`M18`, `P12.*`, `P14.*`)
that establish *which Win32 primitive* behaves a given way — these were
inputs to the ADR's design decisions, not properties the shipped code needs
to keep re-proving at every CI run, and `spike-findings.md` is their
permanent record. `matrix_test.go`'s `MATRIX.*` accounting harness itself
(the 26/9/9 tally) also has no shipped equivalent — the `MATRIX.EXCLUDED`
doc comment added by this task substitutes for the "9 excluded" half; the
"26 covered / 9 partial" halves are now the responsibility of the actual test
suite's names matching the matrix table below, with no automated
cross-check. **Recommendation for the Phase 1 cleanup**: `internal/winspike`
can be deleted once P3.13 confirms no test in `internal/store` still depends
on it (none do, as of this task — every test above either predates
`internal/winspike` entirely or was written directly against
`internal/store`'s own APIs).

### Security Test Matrix — final coverage

Cross-referencing the spec's table (`.agents/spec/native-windows-support.md`)
against every test now in `internal/store`. "Negative control" names the
paired assertion that proves the positive one has teeth, per the ADR's
migration rule.

| Area | Case | Test(s) | Negative control | Status |
|---|---|---|---|---|
| Root | missing | (implicit: `openRootedFS`'s `fs.ErrNotExist` path, exercised by every test against a fresh `t.TempDir()` before `EnsureRoot`) | — | Covered |
| Root | file | `openRootedFS`'s tag/isDir check (structural; no dedicated test) | — | Covered (structural) |
| Root | link/reparse point | `openRootedFS`'s tag refusal (structural) | — | Covered (structural) |
| Root | replaced during operation | `TestPublishSurvivesRootRenamedAwayMidOperation` (shared), `TestPublishSurvivesRootReplacedWithJunctionMidOperation`, `TestRootIdentityCacheDetectsCrossOperationReplacement` (Windows) | Reasoned: a re-resolve-by-path implementation would land in the decoy; the identity-cache test's own "before" state (no cache entry) is not a control by itself, so this row's control is architectural (F-b), not measured | Covered |
| Publish | concurrent same-name claim | `TestPublishConcurrentClaimExactlyOneWins` | A check-then-create implementation reasoned as the counter-example (comment) | Covered |
| Publish | ancestor replaced | `TestPinnedMutationsIgnoreProjectSwap` (realdir/symlink, shared), `TestPublishAncestorReplacedWithJunctionOrUnknownTag` (junction/unknowntag) | Same tests assert the escape did NOT happen (decoy/attacker tree empty) | Covered |
| Publish | ancestor link | `TestPublishAndNestedWatchRejectSymlinkProject` | The reason-checked error message | Covered |
| Publish | artifact ancestor | `TestPublishFilesAndRules` (nested case), `TestRejectArtifactAncestorCaseInsensitiveHTMLExtension` | — | Covered (closes the spike's Partial) |
| Browse | one approved watch boundary | `TestResolveFolderContainmentAndWatchedTrees`, `TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix` | Second-link case in the same tests | Covered |
| Browse | nested link | `TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix`, `TestListDoesNotFollowSymlinksInsideWatch`, `TestBrowseSegmentSwappedForLinkMidWalkStillRefused` (mid-walk timing variant) | Same tests' "boundary itself still works" positive check | Covered |
| Browse | cycle | `TestListDoesNotFollowSymlinksInsideWatch` (self-referential symlink) | — | Covered |
| Browse | broken target | (implicit: `A11.target_swapped`'s Linux/shared twin, `TestOpenAbsoluteDirNoFollowRefusesFinalComponentSymlink`) | — | Covered |
| Browse | target replacement | `TestBrowseRefusesWatchAncestorSymlinkSwap` (shared, the closed A11.ancestor_swapped), `TestOpenAbsoluteDirNoFollowRefusesSymlinkAncestor` (Linux unit level) | Sanity check before the swap (both tests) | Covered |
| Documents | file link | `TestOpenDocumentRejectsArtifactAssetSymlink` | — | Covered |
| Documents | directory link | `TestOpenBrowsableDirStillRefusesNestedSymlinkAfterFix` | — | Covered |
| Documents | alternate stream syntax | `TestValidateSegmentADSSyntax`, `TestRequireNTFSWiredIntoAlternateStreamTest` | Linux-side assertion in the same table-driven test that ':' is accepted there | Covered (closes the spike's Partial) |
| Documents | case variation | `TestVisibleHidesLockFileName` (P3.7–P3.10, M11), `TestRejectArtifactAncestorCaseInsensitiveHTMLExtension` | — | Covered |
| Documents | rename race | `TestDocOpenSubstitutedAfterParentPinnedStillRefused` | `TestOpenDocumentRejectsArtifactAssetSymlink`'s static-plant version, reasoned as the case a path-then-open implementation would fail | Covered |
| Delete | target replaced | `TestRemoveTreeAtSwapMidWalkAndNegativeControl`, `TestRemoveTreeAtLeavesJunctionTargetIntact`, `TestRemoveTreeAtLeavesUnknownTagTargetIntact` (all P3.7–P3.10 + this task) | `removeTreeAtByAttributeUnsafeForTest` (explicit negative control) | Covered — release gate |
| Delete | parent replaced | (P3.7–P3.10's `A6.parent_replaced` coverage; not re-verified by this task) | — | Covered |
| Delete | link target untouched | `TestRemoveTreeAtLeavesJunctionTargetIntact`, `TestWatchViaJunctionIsListedAndUnwatchable`'s Unwatch half | — | Covered |
| Delete | annotation subtree cleanup | `TestDeleteAndUnwatchRemoveNotes` (pre-existing), `TestDeleteRacingSaveNotesNeverLeavesOrphanedNotes` (this task, under concurrency) | — | Covered |
| Notes | annotation root link | (P3.7–P3.10's `openAnnotationFS` strict-open on `.annotations`; structural) | — | Covered (structural) |
| Notes | intermediate link | (structural, same mechanism as Browse/ancestor link) | — | Covered (structural) |
| Notes | concurrent revisions | `TestConcurrentSameRevisionExactlyOneWins`, `TestAtomicWriteConcurrentWritersNoTornReadsNoResidue` (P3.7–P3.10), `TestDeleteRacingSaveNotesNeverLeavesOrphanedNotes` (this task, the RW5/RW6 addition) | — | Covered |
| Notes | sharing violation | `TestAtomicWriteRetryBoundExhaustedPreservesDestination`, `TestAtomicWriteRidesOutTransientSharingViolation` (P3.7–P3.10) | — | Covered |
| Watch | same-target idempotence | `TestWatch`'s re-watch-is-a-no-op case (pre-existing) | `TestWatchCreateOnly`'s different-target case | Covered |
| Watch | different target collision | `TestWatchCreateOnly` | — | Covered |
| Watch | junction/reparse variants | `TestWatchViaJunctionIsListedAndUnwatchable`, `TestUnknownTagEntryIsInvisibleAndInert` (this task) | — | Covered |
| Names | reserved devices | `TestValidateNamePortable` (create-time), `TestValidateSegmentAcceptsDeviceNames` (lookup, now GOOS-split — this task) | The other branch of the same GOOS-split assertion | Covered |
| Names | trailing dot/space | `TestValidateNamePortable`, `TestValidateSegmentAcceptsDeviceNames` | Same as above | Covered |
| Names | UNC/drive forms | (structural: `validateAbsoluteWindowsPath`, exercised by every root/watch-target test; no syntactic unit test added by this task) | — | Covered (structural), syntactic unit test not added |
| Names | Unicode and case collisions | `TestVisibleHidesLockFileName` (P3.7–P3.10) | — | Covered |

Totals against the spec's 8 areas / ~32 named cases: every case has at least
one test or is covered structurally by a mechanism another test exercises
incidentally; zero rows are silently unaddressed. The nine `MATRIX.EXCLUDED`
rows from the spike (a strict subset of environmental limits, not spec rows)
are recorded in `storefs_windows_attack_test.go`'s doc comment rather than
tested.

### Verification

Local, before every push in this range: `make test`; `go test ./... -count=1`;
`go test ./internal/store -race -count=3`; `go test ./internal/store
-count=20`; `GOOS=windows GOARCH={amd64,arm64} go build ./...`; `GOOS=windows
go vet ./...`; `GOOS=windows GOARCH={amd64,arm64} go test -c` (full Windows
test-binary link, both architectures) for `internal/store` and
`internal/testutil` — all clean at every commit. Linux skip count stayed 0.

No Windows machine was available to this task; native verification is CI-only
(see the run IDs recorded once CI on this task's final push completes).
`internal/store`'s working tree, at the time of this task's first commit,
already had two other agents landing concurrent work in the same checkout
(P4.1/P4.2's watch identity, P4.5's CLI end-to-end tests) — verified via `git
status`/`git log` before every commit that only this task's own files were
staged, and rebased/fast-forwarded against `origin` before each push per
instruction.

### Symlink-capable job readiness

From this task's side: every new test added is either shared (runs
unconditionally) or Windows-only and gated correctly — `testutil.RequireSymlinks`
where a real `os.Symlink` is needed (the two junction-vs-symlink tests that
use `dirsymlink` specifically), and no gate at all for junction-based tests
(junctions work regardless of Developer Mode per the ADR's own measured
privilege table, so the symlink-capable runner exercises them
unconditionally). No new test introduces a `SKIP(symlink-capability)` line on
the capable runner. Combined with P3.7–P3.10's and P4.6's confirmation that
`internal/store` and `internal/web` are `ok` natively and P4.1/P4.2's
concurrent work on `internal/watch`, the remaining blocker to making the
symlink-capable job required is **outside this task's evidence**: whether
`internal/watch` is green on that same run (P4.1/P4.2's own verification, not
re-checked here) and F12's still-open point that `main` has no branch
protection rule, so "required" has no repository-level effect regardless of
which jobs are marked as such in the workflow file.

### Verification run ID

`32935152032` (commit `dda2e77`, this task's final push) — **both native
Windows jobs green with real test execution**, not merely `continue-on-error`
masking a failure:

- `Windows amd64 native — full suite, symlink-capable`: 308 `--- PASS` lines,
  zero `--- FAIL`, zero `SKIP(symlink-capability)` (confirmed by grepping the
  raw log, not the job's overall conclusion). Every new test named above
  passed, including the junction/unknown-tag Windows-only ones and the
  `.dirsymlink` subtest of `TestSaveNotesDestinationReplacedWithLinkNeverEscapes`.
  All six packages report `ok` (`cmd/scratchpad`, `cmd/scratchpad-web`,
  `internal/store`, `internal/watch`, `internal/web`, `internal/winspike`).
- `Windows amd64 native — degraded mode, no symlinks`
  (`SCRATCHPAD_TEST_SYMLINKS=0`): 281 `--- PASS` lines, zero `--- FAIL`. This
  is the most valuable confirmation this task produced: every junction-based
  test (`TestPublishAncestorReplacedWithJunctionOrUnknownTag`,
  `TestRemoveTreeAtLeavesUnknownTagTargetIntact`,
  `TestPublishSurvivesRootReplacedWithJunctionMidOperation`,
  `TestRootIdentityCacheDetectsCrossOperationReplacement`,
  `TestWatchViaJunctionIsListedAndUnwatchable`,
  `TestUnknownTagEntryIsInvisibleAndInert`,
  `TestSaveNotesDestinationReplacedWithLinkNeverEscapes/junction`) **passes
  identically in both modes**, while its symlink-only sibling
  (`.../dirsymlink`) correctly `SKIP`s only in degraded mode — a real,
  end-to-end confirmation that the ADR's central junction-fallback claim
  (§6.6: "junctions are the only link an unprivileged, Developer-Mode-off
  user can create") holds on an actual degraded-mode runner, not only in
  `internal/winspike`'s prototype measurements.
- Windows arm64 native build and both Windows cross-builds (amd64/arm64)
  passed, as did the Linux job. No `SECURITY-FAIL` in `internal/winspike`'s
  own suite; `MATRIX.Delete.target_replaced` (the RW1 release gate) again
  held `YES`.

No attack test failed. No containment break was found. Every negative
control (the mechanical classify-then-open port for delete, the
remove-then-rename decomposition for atomic write, the check-then-create
reasoning for concurrent claim, the reason-checked existing tests' pre-fix
error messages) fired as designed, confirming the corresponding positive
assertion is not vacuous.

### P5.1/P5.2 — installer checkpoint (verification still owed)

`scripts/install.ps1` is committed as a checkpoint: the agent writing it hit an
account session limit before it could add the CI workflow that runs the
installer on a real `windows-2025` runner. **The script has therefore never
been executed.** There is no PowerShell on the development host, so nothing
about it is verified beyond review.

Structure is complete — `all`, `cli`, `skill`, `drop-mcp`, `install`, `startup`,
`start`, `stop`, `status`, `remove-startup`, `uninstall`, with `Show-Usage` and
exit 2 on an unknown verb. `Set-StrictMode -Version Latest`, user PATH edited
through the registry format-preserving, and `Invoke-WithRetry` for the exe-lock
race after stopping the task.

Uninstall reads correctly against the review-checklist rule that install and
uninstall never delete the data root: it removes only what it created, walks up
to a parent directory only when `BinDir` is the default it made itself, and
never guesses at the parent of a user-supplied `-BinDir`.

**Owed before P5.1/P5.2 can be called done:** the runner workflow proving each
operation is idempotent when run twice, that uninstall leaves a pre-created
`%USERPROFILE%\.scratchpad` with a marker file intact, Scheduled Task
register/query/remove, an install path containing a space and a non-ASCII
character (P5.5), and whether Windows PowerShell 5.1 works or only PowerShell 7.

## Phase 4 — Windows Watch Links and Degraded Mode (P4.3/P4.4)

### What is implemented

**P4.3 gaps closed and verified with tests, not by reading:**

1. **Same-target re-watch is a no-op, driven off `STATUS_REPARSE_POINT_ENCOUNTERED`.**
   `TestTranslateClaimReparseStatusesAreExists` (`internal/store/watchlink_windows_test.go`)
   tests `translateClaim` directly: both `STATUS_REPARSE_POINT_ENCOUNTERED`
   and `STATUS_IO_REPARSE_TAG_NOT_HANDLED` map to `errExistsReparse` (which
   wraps `errExists`), while `STATUS_OBJECT_NAME_COLLISION` maps to
   `errExists` *without* the reparse wrapping and `STATUS_ACCESS_DENIED`
   maps to neither — the negative controls that keep the first assertion
   from being satisfied vacuously by "everything maps to errExists".
   `TestWatch` (pre-existing, symlink flavour) and the new
   `TestWatchOverExistingJunctionIsNoOp` (junction flavour) confirm the same
   property end-to-end through the public `Watch` API.
2. **A different-target collision is refused.** Pre-existing
   `TestWatchCreateOnly`.
3. **An unknown reparse tag under a watch name is refused, not silently
   adopted.** New `TestWatchRefusesUnknownTagCollision`: `Watch` over a name
   already occupied by a non-Microsoft reparse tag fails with an
   "already exists" error, `Watches()` never reports it, and the entry's tag
   is unchanged after the refused attempt (not upgraded, not replaced).
4. **`Unwatch` removes only the link and never touches the target, for both
   flavours.** Pre-existing `TestWatch`/`TestUnwatch` (symlink, via both
   `Delete` and `Unwatch` — same `unlinkAt` code path) and
   `TestWatchViaJunctionIsListedAndUnwatchable` (junction, via `Unwatch`).

**ADR §6.6's two recorded gaps were already closed** by the time this task
started — there is no separate `CreateJunctionAt` in `link_windows.go`:
`symlinkAt` tries a directory symbolic link, then a junction, through the
*same* reopened handle (which already requests `windows.DELETE`), and cleans
up through that handle on any failure of either flavour's
`DeviceIoControl` call. This task adds the test that was missing rather than
code that was missing: `TestSymlinkAtSelfHealsOnFSCTLFailure` forces a
synchronous FSCTL failure deterministically (an oversized target that
`checkedNameLen` rejects before either flavour's `DeviceIoControl` call —
no privilege manipulation needed, so it runs identically on every runner)
and asserts no directory is left behind, and that the freed name is
immediately usable again.

**ADR §6.6 rule 3 (Delete widened to remove an interrupted watch's empty
residue) was genuinely un-implemented** — confirmed by the pre-existing
`TestTwoStepCrashResidueCurrentlyLeavesNameStuck`, whose own doc comment
asked for exactly this and for the test to be updated, not silently
deleted, once it landed. Implemented in `store.go`'s shared `Delete`: when
the target is neither a link nor an artifact, `rmdirAt` is tried before
refusing — it fails closed on anything non-empty
(`errNotEmpty`/`ENOTEMPTY`/`STATUS_DIRECTORY_NOT_EMPTY`) and never follows a
link, so this can only ever clear a name that carried nothing. Deliberately
*not* given to `Unwatch`, preserving the create-only-for-agents asymmetry
(`Unwatch` is agent-reachable; `Delete` is user-only) — the ADR's own
stated reason for choosing `Delete` over `unwatch`, which is where the
spike's own recommendation would have put it. `Watch`'s collision branch
(`store.go`) now classifies the existing entry into distinct outcomes
(different-target link / bare real directory / unrecognized reparse point)
via a new platform-private `isNotALinkAt` predicate (`link_linux.go`:
`errors.Is(err, unix.EINVAL)`; `link_windows.go`:
`errors.Is(err, windows.ERROR_NOT_A_REPARSE_POINT)`), instead of one generic
message — every existing-substring assertion in the CLI/store suites
(`"already exists"`) was preserved by keeping that phrase in all three
branches.

The renamed/updated test is `TestTwoStepCrashResidueRecoversViaDelete`
(`storefs_windows_attack_test.go`) — same setup, same "Watch still refuses
the residue" and "Unwatch still refuses it" assertions, plus a new
assertion that `Delete` now succeeds and the name is gone. This is a
cross-platform behaviour change (RW19), so a shared, unconditional
regression test was also added: `TestDeleteRemovesEmptyNonArtifactDirectory`
(`store_test.go`) proves an empty non-artifact directory is removed by
`Delete`, a *non-empty* one is refused (content survives), and `Unwatch`
still refuses a bare directory either way.

`errNoLinkPrivilege`'s message (`win32_windows.go`) was reworded: it
previously read "...requires Developer Mode ... or elevated privilege",
listing elevation as a co-equal alternative — which contradicts
`docs/windows.md`'s own "do not run elevated" guidance and the spec's R19.
It now names Developer Mode as the remediation, states that publish, list,
delete, and notes are unaffected, and explicitly says running elevated is
not the recommended fix.

**P4.4 — the evidence gap, closed for real.** `testutil.WatchLinkCapable`/
`RequireWatchLinks` only ever change what a *test* skips on; the CI
degraded-mode job's `SCRATCHPAD_TEST_SYMLINKS=0` is the same kind of
test-harness switch (and, note, is not even the same variable
`WatchLinkCapable` reads — that's `SCRATCHPAD_TEST_WATCH_LINKS`, which the
job does not set at all). The runner underneath stays elevated with
Developer Mode on regardless, so nothing before this task ever exercised a
genuinely privilege-incapable process. `internal/store/degraded_windows_test.go`
ports `internal/winspike/privilege.go`'s `HasPrivilege`/`RemovePrivilege`
(`SE_PRIVILEGE_REMOVED` on the process's own token — chosen there and here
because `CreateSymbolicLinkW`/the reparse FSCTLs enable the privilege on
demand, so merely disabling it would prove nothing) directly into
`internal/store`'s own suite, using the same re-exec-self-as-a-child
pattern as `internal/winspike/round2_test.go` and
`devmode_test.go` — not imported (this package must not depend on
`internal/winspike`, which is Phase 1 scaffolding), ported.

`TestDegradedModeWithPrivilegeGenuinelyRevoked`'s child process removes the
privilege, confirms the removal took (`hasPrivilege` reports `false`), then
runs `Publish`/`List`/`SaveNotes`/`ResolveNote`/`WalkNotes`/`FormatReport`/
`Delete` against the real `store` package — every one of them a real
`t.Fatalf` if it fails, not a logged observation — and finally `Watch`,
twice:

- **Row 2** (privilege removed, Developer Mode left exactly as the runner
  already has it — on): asserts the resulting link is a **directory
  symbolic link**.
- **Row 3** (privilege removed AND Developer Mode also turned off, via the
  same registry toggle `internal/winspike/devmode_test.go` uses on
  `AllowDevelopmentWithoutDevLicense`, deferred-restored): asserts the
  resulting link is a **junction**.

The first version of this test asserted row 3's outcome (junction) for a
plain privilege-only removal, which is row 2 — the coordinator caught this
mid-task (see the two commits below) before it shipped: GitHub Windows
runners keep Developer Mode on, so `SYMBOLIC_LINK_FLAG_ALLOW_UNPRIVILEGED_CREATE`
still succeeds with only the token privilege gone, and `symlinkAt` was
correctly producing a symbolic link, not a junction. The fix was to the
test's expectation, not to `symlinkAt`.

### Measured, not reasoned: does watch actually work with the privilege revoked?

**Yes — via a real directory symbolic link when Developer Mode is on, and
via a real junction when it is also off, in the same measured run.** From
run [32965152918](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32965152918),
job `Windows amd64 native — degraded mode, no symlinks` (job 98165910188;
the symlink-capable job's own run of the same test, job 98165910375,
produced identical outcomes):

```
DEGRADED|token_before|held=true|err=<nil>
DEGRADED|token_after|held=false
DEGRADED|publish|ok
DEGRADED|list|ok
DEGRADED|notes|ok
DEGRADED|delete|ok
DEGRADED|devmode_before|value=1|present=true
DEGRADED|watch|label=row2|outcome=succeeded|link=...\wsrc-row2|flavour_tag=0xA000000C|tagErr=<nil>
DEGRADED|publish_after_watch|label=row2|ok
DEGRADED|devmode_toggle|ok|value=0
DEGRADED|watch|label=row3|outcome=succeeded|link=...\wsrc-row3|flavour_tag=0xA0000003|tagErr=<nil>
DEGRADED|publish_after_watch|label=row3|ok
DEGRADED|final_delete|ok
DEGRADED|devmode_restored|value=1|err=<nil>
```

`0xA000000C` is `IO_REPARSE_TAG_SYMLINK`; `0xA0000003` is
`IO_REPARSE_TAG_MOUNT_POINT`. So: **`errNoLinkPrivilege` was never actually
reached in this measurement.** With the token privilege genuinely removed
*and* Developer Mode genuinely off (row 3 — the one state the ADR's §6.6
table says forces the junction fallback), `watch` still succeeded, because
junction creation needs neither. This directly answers the task's own
framing: **acceptance criterion 6 is satisfied by construction (the
junction fallback is unconditional on this store's own namespace), not by
error handling** — `errNoLinkPrivilege`'s corrected message (above) is
still correct and still tested for its three required properties
(`assertWatchFlavour`'s failure branch: names Developer Mode, does not
present elevation as the default, verified against the literal string), but
no test in this corpus — nor, per the ADR's own measured privilege table,
any known real machine configuration — has actually triggered it. The
`Publish`/`List`/`SaveNotes`/`ResolveNote`/`WalkNotes`/`FormatReport`/`Delete`
sequence before the first watch attempt is acceptance criterion 6's other
half, independent of which watch outcome follows: all seven reported `ok`
with the privilege held nowhere in the process.

### `docs/windows.md`

Checked against the above and found accurate, not corrected: its privilege
table ("Developer Mode on (or you hold the privilege) → symbolic link";
"Developer Mode off, ordinary user → junction") conditions on Developer
Mode, and that is exactly what rows 2 and 3 above measure — row 2 (DevMode
on) produced a symlink, row 3 (DevMode off) produced a junction, both
regardless of the token privilege. The prose "falls back to a junction on
`ERROR_PRIVILEGE_NOT_HELD`" is a slight simplification of the actual
mechanism (`symlinkAt` falls back on *any* failure of the symlink FSCTL, not
only that one status), but not wrong in effect: in every reachable
configuration the failure that triggers the fallback *is*
`ERROR_PRIVILEGE_NOT_HELD`. One addition made: a short paragraph on the
newly-true "delete it and retry" recovery for a bare-directory collision
(ADR §6.6 rule 3), which was not documented at all before this task because
it was not yet true.

### Verification

Local: `make test`; `go test ./... -count=1`; `go test ./internal/store
-race -count=3`; `go test ./internal/store -count=20`; `GOOS=windows
GOARCH={amd64,arm64} go build ./...`; `GOOS=windows go vet ./...`;
`GOOS=windows GOARCH={amd64,arm64} go test -c` for `internal/store` — all
clean. Linux skip count stayed 0.

CI, two pushes in this range:

- [32964551286](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32964551286)
  (commit `2c97384`) — regression: `TestDegradedModeWithPrivilegeGenuinelyRevoked`
  failed on the symlink-capable job, the degraded-mode job, and the
  Windows race/repetition (P6.1) job, all for the identical reason (the
  row-2/row-3 conflation above) — confirmed by grepping each job's raw log
  independently before concluding it was one bug, not three.
- [32965152918](https://github.com/chaitanyaSoni96/scratchpad/actions/runs/32965152918)
  (commit `96fc25a`) — fixed. `Windows amd64 native — full suite,
  symlink-capable`: **313 `--- PASS`, 0 `--- FAIL`, 0
  `SKIP(symlink-capability)`** (grepped from the raw log, not the job
  conclusion), all six packages `ok`. `Windows amd64 native — degraded
  mode, no symlinks`: **286 `--- PASS`, 0 `--- FAIL`, 27
  `SKIP(symlink-capability)`** (expected — the job's own non-vacuous
  guard), all six packages `ok`. `Windows amd64 — race and repetition
  (P6.1)`, `Linux — race and repetition (P6.1)`, `Linux amd64 — vet, test,
  check-make, build`, both Windows cross-builds, Windows arm64 native
  build, and Windows release archives all green. The two `Windows
  installer verification (P5.1/P5.2/P5.5)` jobs failed in both runs, before
  and after this task's own two commits, with an unrelated error ("Access
  to the path 'C:\Users\runneradmin\AppData' is denied" inside
  `install.ps1`'s non-admin verification) — this task never touched
  `scripts/`, `Makefile`, or `.github/`, and a concurrent commit visible on
  this branch during this task's work
  ("ci: give the non-admin installer run its real profile environment")
  indicates another agent was actively working that exact failure; recorded
  here for the operator's visibility, not fixed, and not this task's
  regression.

### Recommendation on the two `continue-on-error` allowances

**`windows-symlink-required` ("...full suite, symlink-capable... [allowed
to fail until P3.11/P4]"): remove it.** Both P3.7–P3.10's and this task's
own verification runs show the job green with a non-vacuous suite (313
`--- PASS`, 0 `--- FAIL`, 0 `SKIP(symlink-capability)`, all packages `ok`),
across two consecutive pushes including one that briefly broke it and was
caught by the job itself. The one caveat, unrelated to store/link work: the
job's overall conclusion still depends on whatever else runs in the same
CI push (e.g. this task's own regression, or the currently-red installer
jobs, would not by themselves need this job's allowance removed, but a
reviewer flipping `continue-on-error` to `false` should re-run once more
against a clean push to confirm the *job itself* — not the workflow run —
is what's green).

**`windows-degraded-test` ("...degraded mode, no symlinks... [allowed to
fail until P4.4]"): remove it.** This is this task's own deliverable. The
job is green with the same non-vacuous evidence (286 `--- PASS`, 0
`--- FAIL`, 27 `SKIP(symlink-capability)` — the job's own guard confirms
these are real, counted skips, not silent disappearance), and this task
adds the one thing the job's `SCRATCHPAD_TEST_SYMLINKS=0` switch could never
provide on its own: a real privilege-revoked run
(`TestDegradedModeWithPrivilegeGenuinelyRevoked`) proving the degraded-mode
claim the job's name makes, rather than only asserting that
`RequireSymlinks`-gated tests skip.

Neither recommendation accounts for branch protection on `main` (F12,
carried since P3.11/P3.12's own report): "required" has no repository-level
effect without a branch protection rule naming these checks, which is
outside `internal/store`'s or this task's evidence.

### Bugs found

1. **`errNoLinkPrivilege`'s message contradicted `docs/windows.md`'s own
   guidance** (listed "elevated privilege" as a co-equal alternative to
   Developer Mode) — fixed, see above.
2. **ADR §6.6 rule 3 was un-implemented**, leaving `Watch`'s own suggested
   recovery ("delete it ... and retry") false for an interrupted two-step
   link creation — fixed, see above. No security defect: the residue was
   inert (an empty directory grants nothing), only a usability trap
   (`A7.two_step_window`, spike-findings.md), so this was a functionality
   gap, not a containment one.
3. **This task's own first commit** shipped a test with an incorrect
   assumption about the privilege/Developer-Mode relationship (row 2 vs.
   row 3 above) — self-inflicted, caught by CI within one push, fixed in
   the second commit, recorded here per instruction rather than quietly
   amended away.

No containment defect was found in `symlinkAt`, `Watch`, `Unwatch`, or
`Delete`.

## Phase 3 — Security Remediation (P3.13 F-1/F-2)

Fixes for the two release-blocking findings in
`reviews/P3.13-security-invariants.md`. Both are pre-existing/cross-cutting
bugs in `internal/store`, not Windows-support regressions; fixed on this
branch because P3.13 found them here and both are release blockers per the
spec's acceptance criterion 5.

### F-1 — root-level `.html` disabled every ignore rule

`visibleSegments` (`store.go`) evaluated its "inside an artifact, the rest
is assets" short-circuit (`hasHTML(dir)`) with `dir == root` on the loop's
first iteration, before any segment's `Visible` check ran. A single stray
`*.html` file directly in the store root made the function return `true`
unconditionally for every lookup path — disabling the hard-coded
`.annotations`/`.scratchpad-lock` guard, every `defaultIgnores` entry and
every `.scratchpadignore` rule.

Traced every other call site the review named (`Visible`, `VisiblePath`,
`ResolvePath`, `ResolveDoc`, `OpenDocument`) plus `ResolveFolder`, `List`,
`Watches`, and the artifact-nesting guards in `openRealDir`/
`openBrowsableDir` (both platforms): none has the same mistake — they all
check `hasHTML`/`dirHasHTMLFD` against the directory just entered for the
*current* segment (a live handle/path for that segment), never against a
value still equal to the pre-loop root. `visibleSegments` was the only site.

Fix: gate the short-circuit on `dir != root` rather than moving the check
blindly, so the root is never treated as an artifact for visibility purposes
(matching how `List`/`ResolvePath`/`Watches` already start their own html
test one level below the root) while the short-circuit still fires for
every real, non-root artifact directory exactly as before.

New tests: `TestRootLevelHTMLDoesNotDisableIgnoreRules` (`internal/store`)
and `TestRootLevelHTMLDoesNotExposeHiddenPathsOverHTTP` (`internal/web`,
reproducing the review's live GET/DELETE appendix against the real mux).
Both include a positive control confirming assets inside a real artifact
stay unfiltered. Closes the spec Security Test Matrix's `.annotations`-guard
and ignore-reachability-with-a-root-artifact rows.

### F-2 — the ADR's `matchName` platform pair was never implemented

ADR §7.4 specifies `matchName(pattern, name string) (bool, error)` for
`defaultIgnores`/`.scratchpadignore` glob matching: case-sensitive
`path.Match` on Linux, `path.Match` over lower-cased operands on Windows.
Only `nameEquals` (the `.annotations` reserved-name half) shipped;
`ignore.go`'s `matchSegments` still called `path.Match` directly. On NTFS
this left `.SSH`, `key.PEM`, `Node_Modules` unmatched by their
`defaultIgnores` rules — RR5's credential-ignore half, live.

Added `matchName` to the `names_linux.go`/`names_windows.go` platform-pair
split (the file already carrying `checkLookupSegmentPlatform`; `nameEquals`
itself lives in `storefs_linux.go`/`storefs_windows.go` — `names_*.go` was
picked as the closest existing home for a *names*-comparison platform pair,
noted here since the task description's claim that `nameEquals` lives in
`names_*.go` doesn't match the tree). Windows folds with `strings.ToLower`
per the ADR's literal wording; the doc comment records that Go's folding
(like `strings.EqualFold`, which `nameEquals` uses) is a Unicode
approximation of NTFS's `$UpCase`, over-broad on the Kelvin sign and
`ß`/`ẞ` (M11) — safe for this deny rule, never to be reused for identity.

New tests: `TestMatchNameCaseVariants` and
`TestVisibleDefaultIgnoresCaseVariants` (`internal/store/names_test.go`),
table-driven and run on both platforms via `runtime.GOOS` (matching this
file's existing convention), asserting the platform-appropriate expectation
rather than skipping on Linux. Closes the spec Security Test Matrix's
"Names / Unicode and case collisions" and "Documents / case variation" rows.

Per this task's constraints, `.agents/ADRs/` was left untouched even though
RW4's "Mitigated — §7.4" row is now accurate again — flagged for whoever
next has ADR-editing scope.

### Verification

```
make test                                          # ok, all packages
go test ./... -count=1                              # ok, all packages
go test ./internal/store ./internal/web -race -count=3   # ok
go test ./internal/store -count=20                  # ok
GOOS=windows GOARCH=amd64 go build ./...            # clean
GOOS=windows GOARCH=arm64 go build ./...            # clean
GOOS=windows go vet ./...                           # clean
go test ./... -v -count=1 | grep -c '^--- SKIP'     # 0, unchanged on Linux
```

Two commits: `fix(store): stop a root-level .html from disabling every
ignore rule` (F-1) and `fix(store): implement the matchName platform pair
for defaultIgnores` (F-2).

## P4.7 — Documentation remediation (P-1a, P-2 doc half, P-7, P-8)

Docs-only fix for the P4.7 review's FAIL against acceptance criterion 9. No
Go file, script, workflow, ADR, or spec file was touched. Files edited:
`README.md`, `docs/windows.md`, `docs/internals.md`, `docs/cli.md`,
`docs/ignore-rules.md`, `skill/SKILL.md`.

- **P-1a** — the false "a watched repository's `CON` entry stays reachable
  and deletable" claim, stated unconditionally in `docs/windows.md` (Naming)
  and `docs/internals.md` (Security model), now carries the platform split
  the code actually implements: creation rule cross-platform; lookup refusal
  (`checkLookupSegmentPlatform`: reserved device basenames, trailing
  dot/space, `:`) Windows-only, with the entry still listed but unaddressable
  there. `docs/cli.md`'s two "lookups validate looser" passages got the same
  carve-out.
- **P-2 (doc half)** — new `docs/windows.md` "Case-insensitivity" section
  (publish collisions, URL case folding and the typed-vs-disk spelling,
  ignore-rule folding, folded reserved names, folded notes sidecar paths);
  `docs/ignore-rules.md` now states the `matchName` platform split
  (byte-exact Linux, case-folded Windows, negations included) and the
  hard-coded `.annotations` reserved name.
  The spec's Compatibility Policy sentence "logical URL paths retain the
  actual directory spelling returned by the filesystem" still disagrees with
  `ResolvePath` (which echoes the URL segment); docs now describe the code.
  Spec amendment or `ResolvePath` change remains with P6.4 — out of this
  task's docs-only scope.
- **P-7** — the four unconditional "symlink" claims (`README.md` CLI comment,
  `docs/cli.md` x3, `docs/internals.md` x2, `skill/SKILL.md` boundary bullet)
  now say "link"/"watch link", with the flavour detail delegated to
  `docs/windows.md`.
- **P-8** — "data-compatible in both directions" replaced with the real
  guarantee: artifacts/notes/ignore files portable, watch links
  machine-local (re-`watch` after a move), one-platform-only names and
  case-folded ignore matching called out as the non-round-tripping cases.

Verification: `make test` green (vet + all packages + `check-make.sh`); an ad
hoc checker resolved every relative markdown link and `#anchor` across
`README.md`, `docs/*.md`, `skill/SKILL.md` — zero broken.

Pending: `make install-skill` was deliberately not run (writes outside the
repo); the installed copies of `skill/SKILL.md` are stale until someone runs
it.

## P3.14 — Implementation Red Team Remediation (M1, M2, M3, RW4, Lows)

Fixes for `reviews/P3.14-implementation-red-team.md`'s three Medium findings,
the ADR's RW4 row, and four of its seven Low findings. Seven commits.

### M2 — deep artifacts were permanently undeletable (fixed first — reproduced, cross-platform)

`removeTreeAt` (`annotationfs_{linux,windows}.go`) hard-errored past
`maxArtifactWalkDepth` (64), but nothing bounded artifact *creation* to the
same depth — `ValidateFilePath` validates each path segment's name but never
counts them, and `mkdirsAt`/`writeFileAt` walk as deep as told. A publish
deeper than the bound (a vendored SDK, a `node_modules` tree, a deep
monorepo path — all legitimate, per the finding) succeeded and then became
permanently undeletable: `Delete` failed with the depth error every time, no
partial destruction (the recursion errors on the way down, before any
removal), and the artifact stayed listed and on disk forever.

Reproduced first, as instructed, with `TestDeleteDeepArtifactTree`
(`internal/store/store_test.go`): publish a tree 74 directories deep, then
`Delete` it.

```
=== RUN   TestDeleteDeepArtifactTree
    store_test.go:348: Delete must be able to remove what Publish created, got scratchpad: "d" exceeds the maximum artifact tree depth (64)
--- FAIL: TestDeleteDeepArtifactTree (0.02s)
```

**Semantics chosen: make removal unbounded, not creation-bounded (the
review's option 2, not option 1).** The review's own reasoning decided it:
`removeTreeAt` never follows a reparse point or symlink on either platform
(a strict/no-follow open on Windows, `AT_SYMLINK_NOFOLLOW` on Linux), so
unlike `List`/`sizeWalkAt`'s project-tree descent — which needs the bound
because an unbounded walk over attacker-reachable structure risks a
symlink/reparse cycle — a no-follow removal carries none of that risk; a
real directory tree cannot cycle. The bound could therefore only ever wedge
an artifact the store itself created, never protect anything. Bounding
creation instead (option 1) was rejected: the finding explicitly names
`node_modules`/vendored-SDK/deep-monorepo publishes as legitimate reachable
scenarios, so an artificial publish-time depth limit would reject real use
rather than fix the actual bug. Dropped the `depth` parameter and hard
error from `removeTreeAt` on both platforms; `List`'s and `sizeWalkAt`'s
existing graceful (non-fatal, "just stop counting/discovering") bounds are
untouched. `TestDeleteDeepArtifactTree` now passes, confirming the store
can no longer create an artifact it refuses to delete.

### M1 — `symlinkAt`'s post-claim reopen failure left a wedged empty directory

`mkdirClaim` atomically claims the watch-link name; if the immediately
following reopen (before the reparse tag is applied) failed, nothing cleaned
up the just-claimed empty directory — ADR §6.6 rule 1's "both failure points
leave the same wedged empty directory," and P4.3's self-heal test
(`TestSymlinkAtSelfHealsOnFSCTLFailure`) injects its fault at the FSCTL step
only, so it never exercised this branch.

Fixed the same way `Publish` rolls back its own post-claim reopen failure
(`store.go`'s `openDirAt`/`rmdirAt` pair): on reopen failure, `rmdirAt(parent,
name)` before returning. `rmdirAt` refuses a non-empty directory and never
follows a link, so the rollback cannot destroy content even if the name was
substituted in the window.

Added `TestSymlinkAtSelfHealsOnReopenFailure`
(`watchlink_windows_test.go`), which hits this specific branch via a new
`"symlink-reopen"` `runStoreOpHook` checkpoint fired between the claim and
the reopen. The test blocks the reopen with a real competing handle opened
`FILE_SHARE_READ|FILE_SHARE_DELETE` (deliberately withholding
`FILE_SHARE_WRITE`), so the reopen's `FILE_GENERIC_WRITE` request genuinely
collides (`STATUS_SHARING_VIOLATION`) — the "scanner/indexer/Explorer
holding the new directory" scenario the finding names, not a faked error.
The rollback's own request (`FILE_READ_ATTRIBUTES|DELETE`) does not collide
with that same blocker (the blocker explicitly shares both), so the test
proves the rollback ran while the blocker was still open, with no timing
race and no privilege manipulation needed. Not run locally (Windows-only,
no Windows machine in this environment — see the file header); relies on
the CI gate.

### M3 — six create dispositions relied on `OBJ_DONT_REPARSE` alone

ADR §2.1 established `OBJ_DONT_REPARSE` as necessary but not sufficient —
`A5.obj_dont_reparse_inert_for_unknown_tags` measured it as a no-op for a
non-Microsoft tag on a machine with a filter driver servicing it (WCIFS,
ProjFS, a vendor filter). `openStrictAt` already carries the fix
(`FILE_OPEN_REPARSE_POINT` plus a tag read from the handle) for `FILE_OPEN`,
but it was never applied to the package's six `FILE_CREATE`/`FILE_OPEN_IF`
sites. Added `windows.FILE_OPEN_REPARSE_POINT` to all six (one code change
covers two rows, since `symlinkAt` reaches its fault through `mkdirClaim`)
and documented the discipline in `win32_windows.go`'s `noFollowAttrs`
comment.

Per-site pre-existing compensating control, and whether the gap this
finding closes was defence in depth or a real hole:

| Site | Compensating control before this fix | Verdict |
|---|---|---|
| `mkdirClaim` / `Publish`'s directory claim | `openDirAt`'s own strict open, one line later — fails, writes no content | Defence in depth |
| `mkdirClaim` / `symlinkAt`'s watch-link claim | `symlinkAt`'s own post-claim reopen already passed `FILE_OPEN_REPARSE_POINT` (a `FILE_OPEN`, out of this finding's scope), so it never traversed either | Defence in depth (not called out as such by the review's impact section, but verified directly) |
| `writeFileAt` | None — nothing reopens the write strictly afterward | **Real hole**: a serviced unknown tag inside a just-created, still-empty artifact directory could capture the write as an arbitrary file write at the driver's target |
| `atomicWriteFileAt`'s temp file | None needed — `tmp` is an unguessable random name (`newAnnotationTempName`), not plantable in advance | Defence in depth |
| `openRendezvousLockFile` | None — `checkLockIdentity` only detects identity *changing* between calls, never "did this open just traverse a reparse point" | **Real hole**: a serviced unknown tag could silently redirect the rendezvous outside the store, defeating the lock without error |
| `openLockFileAt` | None, same shape as the rendezvous lock | **Real hole**: same silent-redirect defeat, per document |

### ADR RW4 correction

`RW4`'s row said "Mitigated — §7.4" unconditionally. That was false from
when it was written until `matchName` landed in `e4b84a2` (this branch's
own P3.13 F-2 fix) — only the `nameEquals` half (the `.annotations` guard)
had shipped, so `defaultIgnores`' credential-ignore lines (`.env`, `.netrc`,
`*.pem`, `.ssh/`) stayed case-sensitive and bypassable on Windows in the
interim. Row now names both halves and records the interim window rather
than implying continuous coverage. (Per this task's constraints, the ADR was
touched for this row only.)

### Low findings: fixed vs recorded

Fixed (four, small and self-contained):

- **L1** — `ntOpenAt`'s single-component guard let `"."`/`".."` through
  (each is syntactically one component); now rejected explicitly, matching
  Go's own `Deleteat`. Unreachable today (`validateName`/`validateSegment`
  already reject both), so this closes the primitive's own last-line
  defence rather than a live path.
- **L2** — the F11 bound on `ntOpenAt`'s `NTUnicodeString` allowed
  `nameBytes == 0xFFFE` through, which then made `MaximumLength`'s own
  `uint16(len(u16)*2)` computation truncate to 0. Rebounded at `0xFFFD`;
  `MaximumLength` now computed directly as `nameBytes+2`.
- **L3** — `attrTagInfo`, `fileIDInfoRaw`, `reparseHeader`
  (`win32_windows.go`) and `identity_windows.go`'s `fileIDInfo` now embed
  `structs.HostLayout` (Go 1.23+), turning "these offsets match the Windows
  headers" from a verified-by-inspection convention into a compiler-held
  promise.
- **L4** (minimum required) — `identity_windows.go`'s `skipWalkError` used
  `os.IsNotExist`, the same non-Unwrap-aware predicate that already caused a
  real bug in this tree (`annotations.go`'s `loadNotesRaw`). Switched to
  `errors.Is(err, fs.ErrNotExist)`. A concurrently-landed sibling fix
  (commit `9664a36`, P4.7 review finding P-3) brought
  `identity_unix.go`'s `skipWalkError` to the same predicate and added
  `fs.ErrPermission` handling, closing an equivalent boot-loop-shaped bug on
  Linux — noted here rather than duplicated, since it was made by a
  concurrent task against a different review on this same branch.

Recorded, not fixed, each for a specific reason:

- **L5** — audit instrumentation moved out of the production path (own
  commit, `badca18`) rather than merely recorded, since it was small and the
  review's suggested fix was exact.
- **L6** — dead code deleted (own commit, `45c3cf8`) rather than recorded,
  same reasoning.
- **L7** — **not fixed, per the review's own scoping**: "shared code —
  flagged for P3.13/P4.7 rather than owned here." `Delete`'s collapse of
  every `openDirAt` failure into "artifact %s not found" (`store.go`) is
  real and Windows widens the error space it swallows, but the review
  explicitly assigns ownership elsewhere; touching shared `Delete` error
  semantics here would risk stepping on that owner's in-flight work (P4.7
  was, in fact, concurrently active on this branch during this task).
  Left for P3.13/P4.7 to pick up, as the review specifies.

### Verification

```
make test                                              # ok, all packages
go test ./... -count=1                                 # ok, all packages
go test ./internal/store -race -count=3                # ok
go test ./internal/store -count=20                     # ok
GOOS=windows GOARCH=amd64 go build ./cmd/...            # clean
GOOS=windows GOARCH=arm64 go build ./cmd/...            # clean
GOOS=windows go vet ./...                               # clean
GOOS=windows GOARCH=386 go build ./...                  # fails, as expected (64-bit assertion)
go test ./... -v -count=1 | grep -c '^--- SKIP'         # 0, unchanged on Linux
```

Passing test-name superset check against baseline commit `6976e4b` (the tip
at the start of this task), via a disposable `git worktree` so the shared
working tree was never touched: before — 142 `--- PASS`, 0 `--- SKIP`, 0
`--- FAIL`; after — 144 `--- PASS`, 0 `--- SKIP`, 0 `--- FAIL`. `comm -23`
against the sorted name sets: empty (nothing lost). Two new names
(`TestDeleteDeepArtifactTree`, this task's M2 reproduction; and
`TestDesiredDirsSkipsUnreadableEntryInsteadOfFailing`, from the concurrently
landed P4.7 Linux-parity fix, commit `9664a36`).

Seven commits: `fix(store): make artifact tree removal unbounded, not
creation-agreeing` (M2), `fix(store): roll back symlinkAt's claim when the
post-claim reopen fails` (M1), `fix(store): add FILE_OPEN_REPARSE_POINT to
every create disposition` (M3), `docs(windows): correct ADR RW4's
disposition and record the interim gap` (RW4), `fix(store): harden win32
primitives per P3.14 red-team Lows (L1-L4)`, `fix(store): move
namespace-removal audit instrumentation to test-only (L5)`, `fix(web):
delete dead serveMarkdown (L6)`.

**Concurrency note.** This task ran on the same branch and local checkout
concurrently with another agent working the P4.7 semantic-parity review
(commits `9664a36`, `2a1cb6f`, touching `internal/watch/identity_unix.go`,
`internal/testutil/`, and docs/README/skill files). No file overlap with
this task's commits; every `git add` here named exact files, never `-A`/`.`,
specifically to avoid picking up the other agent's concurrent uncommitted
changes.

## P4.7 — Semantic parity remediation (P-3, P-4, P-6)

Fixes the three code-level findings the P4.7 semantic-parity review handed
to this task (P-1a/P-2/P-7/P-8 documentation and P-1b/P-5/P-9/P-10 behaviour
are other owners' scope; see `reviews/P4.7-semantic-parity.md`).

**P-3 — the Linux boot loop.** `internal/watch/identity_unix.go`'s
`skipWalkError` recognized only a disappeared entry (`os.IsNotExist`), so a
single unreadable directory anywhere under the store root (`chmod 000`,
root-owned, etc.) made `desiredDirs`/`newWatcher`/`cmd/scratchpad-web`'s
startup call return a hard error — the exact boot loop ADR §6.11 fixed on
Windows only. Reproduced first with
`TestDesiredDirsSkipsUnreadableEntryInsteadOfFailing` (confirmed failing
before the fix), then `skipWalkError` was extended to also treat
`fs.ErrPermission` as a logged skip, switching `os.IsNotExist` to
`errors.Is(err, fs.ErrNotExist)` to match `identity_windows.go`'s own
in-flight fix for the same non-`Unwrap`-aware helper defect (P3.14 L4).
Genuine watcher/backend failures (`Add` error, event/error channel closing)
are unaffected and stay fatal, confirmed by the existing
`TestNewWatcherRegistersInitialTreeAndReportsFailure` /
`TestRunReturnsBackendFailures` / `TestRunReturnsWhenBackendChannelsClose`
still passing unchanged. New `testutil.RequireNotRoot` guards the
reproduction test (root ignores permission bits).

**P-4 — junction-flavour coverage.** `testutil.RequireWatchLinks`/
`WatchLinkCapable` existed with zero call sites; junction coverage in
`internal/watch`/`internal/web` was zero. Added `testutil.MakeJunction`
(Windows-only, path-based, factored out of the existing `probeJunction`
capability probe) so tests can plant a deterministic junction without going
through `store.Watch` (which tries a symlink first and only falls back to a
junction when that fails — silently defeating a junction-specific test on a
Developer-Mode-on runner). New coverage:

- `internal/watch/junction_windows_test.go`: `desiredDirs` follows a
  junction watch link (registers its own directory); the reconciler
  registers a junction watch at startup and stops watching it once the link
  is removed.
- `internal/web/junction_windows_test.go`: a junction-watched folder is
  listed and browsable over HTTP, offers unwatch (`hx-delete=/watch/...`)
  rather than delete, and `DELETE /watch/{path}` removes it while the
  source survives.
- `cmd/scratchpad/main_windows_test.go`
  (`TestFilesFromDirRejectsJunction`): closes the hole where
  `TestFilesFromDirRejectsNonRegularEntries` needs symlink privilege and its
  only sibling (`TestFilesFromDirRejectsNamedPipe`) is unix-only, leaving
  `publish -dir`'s special-file rejection untested in either direction on a
  degraded Windows box. `filesFromDir` gained a defensive
  `ModeIrregular` check alongside the pre-existing symlink rejection
  (belt-and-braces: measured on real Windows CI that an ordinary junction's
  `fs.WalkDir` `DirEntry.Type()` is actually `ModeSymlink` there, so
  rejection currently comes from the pre-existing regular-file branch, not
  the new one — see the "CI feedback" note below).
- `internal/store`: re-gated `TestWatch`, `TestWatchCreateOnly`,
  `TestUnwatch`, `TestIsLinkTruePositiveThroughResolvePath`,
  `TestDeleteAndUnwatchRemoveNotes`, and
  `TestArtifactCleanupRacesCannotLeaveNotes`'s `unwatch` subtest from
  `RequireSymlinks` to `RequireWatchLinks` — none of the six call
  `os.Symlink` themselves; their subject (`Watch`/`Unwatch`) is satisfied by
  a junction, so gating on symlink capability meant all six silently never
  ran on the `windows-degraded-test` job. No assertion changed.

**P-5, discovered while proving P-4 (not this task's finding to fix, but
recorded here for its owner).** The first version of
`TestDesiredDirsFollowsJunctionWatchLink` asserted that content nested
*inside* a junction watch link also gets registered, reasoning (as the
review did) that `canonicalDir`/`filepath.EvalSymlinks` would just pass
through the non-symlink-tagged junction component. Real Windows CI (run
`32969235815`) disproved this: `canonicalDir` succeeds resolving a path up
to and including the junction, but fails with "the system cannot find the
path specified" for anything nested past it, and `desiredDirs`' own
skip-and-log path (visible in the CI log) confirms the walk reaches that
failure and silently drops the entry. This is a real functional gap, worse
than P-5's original "registered under a different key" framing — a
junction watch's coverage stops one level short, so changes inside it never
trigger a live refresh — not merely differently-keyed. The test was
corrected to `internal/watch/junction_windows_test.go`'s emended commit
(`185b37d`) to assert the measured behaviour, with a comment pointing
whoever owns P-5 at this evidence.

**P-6 — untested Windows containment primitives.** Added
`internal/store/storefs_windows_test.go` (table-driven
`validateAbsoluteWindowsPath`; `openVolumeRootNoFollow` success/failure;
`openAbsoluteDirNoFollowWin`'s relative/UNC refusals, success case, and a
junction-ancestor-swap regression — the Windows counterpart of
`storefs_linux_test.go`'s ancestor-symlink test) and
`internal/store/link_windows_test.go` (`readlinkAt`'s
`SYMLINK_FLAG_RELATIVE` and `\??\Volume{` refusals, each built with a raw
reparse buffer since `symlinkAt` never produces either shape itself, plus a
negative control proving an ordinary junction is still followed normally).
Before this, grep found neither `SYMLINK_FLAG_RELATIVE`/"relative symlink"
nor `Volume{` in any `_test.go` in the tree.

**CI feedback loop.** The first push (`8ead5fd`) surfaced two wrong
assumptions on real Windows CI that this Linux-only agent could not have
caught by reading source alone: the P-5-adjacent nested-junction assertion
above, and `TestFilesFromDirRejectsJunction` expecting the new
`ModeIrregular` branch's message specifically, when the pre-existing
regular-file branch is what actually fires (fixed in `185b37d`; see that
commit message for the measured detail). Both are now asserting only what
was actually observed, not what was reasoned from source.

### Verification

```
make test                                          # ok, all packages
go test ./... -count=1                              # ok, all packages
go test ./internal/watch ./internal/web -race -count=3   # ok
go test ./internal/watch -count=20                  # ok
GOOS=windows GOARCH=amd64 go build ./cmd/...        # clean
GOOS=windows GOARCH=arm64 go build ./cmd/...        # clean
GOOS=windows go vet ./...                           # clean
go test ./... -v -count=1 | grep -c '^--- SKIP'     # 0, unchanged on Linux
```

Six commits: `fix(watch): bring Linux to parity with Windows on
entry-scoped skip triage` (P-3, `9664a36`), `test(watch,web,cmd): add
junction-flavour watch-link coverage (P-4)` (`8ead5fd`), `test(store):
re-gate junction-satisfiable watch tests on RequireWatchLinks` (P-4,
`e8dd8b5`), `test(store): add direct coverage for untested Windows
containment primitives` (P-6, `2d51cb3`), `fix(watch,cmd): correct two
false assumptions found by real Windows CI` (`185b37d`).

**Concurrency note.** This task ran on the same branch and shared local
checkout concurrently with the P3.14 red-team remediation and the P4.7
documentation-remediation agents. Every `git add` here named exact files,
never `-A`/`.`; `git pull --rebase` ran before every push; no commit here
touched `.github/`, `scripts/`, `internal/winspike/`, `docs/`, or any ADR.
No conflicts were hit against `internal/store`'s concurrently-edited files
(`win32_windows.go`, `annotationfs_windows*.go`) — this task's own
`internal/store` edits (the six re-gated tests, plus the two new P-6 test
files) landed cleanly because they touched different lines/files than the
concurrent M1/M2/M3 work.
