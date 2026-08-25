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
