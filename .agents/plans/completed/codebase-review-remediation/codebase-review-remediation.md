---
title: Codebase Review Remediation
status: completed
created: 2026-08-25
links: [../../../spec/artifact-annotations.md, ../../../ADRs/2026-08-25-keep-viewer-same-origin.md]
---

# Codebase Review Remediation

## Execution Log

### 2026-08-25

- [x] Started execution and confirmed the accepted decision to retain viewer
  `allow-same-origin` for deliberately opened, trusted artifacts.
- [x] Implemented Linux descriptor-relative containment for store and annotation
  operations, including swap-safe reads, writes, deletion, and folder browsing.
- [x] Serialized annotation mutations across CLI and web processes and retained
  durable revisions after deleting the final note.
- [x] Made loopback the deployment default and added explicit `LAN=1` modes.
- [x] Hardened CLI input handling, generated shell commands, gzip negotiation,
  ignore caching, and filesystem watcher startup/recovery.
- [x] Updated documentation and refreshed installed agent skill copies.
- [x] Passed `make test`, `go test -race ./...`, container image build,
  Makefile deployment checks, and three independent security reviews.

## Scope

Resolve the security, correctness, concurrency, and reliability findings from the
August 2026 whole-codebase review. The work covers store path containment,
annotation revision integrity, HTTP exposure, generated shell commands,
filesystem watching, CLI input handling, and HTTP content negotiation.

The viewer's same-origin sandbox is an acknowledged design risk rather than an
unconditional implementation change. It requires a separate trust-model decision
before changing annotation injection or artifact behavior.

## Priorities

1. Close paths that can read or write outside `SCRATCHPAD_ROOT`.
2. Restore annotation compare-and-swap guarantees under concurrency and deletion.
3. Make the default HTTP deployment match the unauthenticated local-tool model.
4. Make filesystem watching fail visibly and recover from stale or lost watches.
5. Complete lower-risk CLI and HTTP protocol hardening.

## Approach

Keep containment rules centralized in `internal/store` instead of duplicating
path validation in handlers. Prefer filesystem operations that reject symlinked
ancestors over lexical prefix checks. Add regression tests before or with every
fix, including race-oriented tests where deterministic barriers are practical.

For annotation writes, serialize the revision check and commit per document and
retain revision state after the final annotation is removed. The chosen locking
mechanism must cover viewer writes, CLI reply/resolve operations, and deletion.

For the watcher, separate initial registration from the event loop so startup can
report failure. Reconciliation must calculate both additions and removals and be
triggered after queue overflow as well as ordinary event bursts.

## Task Breakdown

### Phase 1: Store And HTTP Containment

- [x] Add a store-level resolver for visible project folders that validates every
  segment and rejects paths escaping the root or crossing unauthorized symlinks.
- [x] Route `/fragments/list` project lookup through the validated resolver and
  return `404` for traversal, hidden, nonexistent, or invalid paths.
- [x] Ensure `docCount`, `folderExtras`, and related recursive helpers cannot walk
  outside the validated folder or recurse through symlink cycles.
- [x] Add web tests for `..`, encoded traversal, absolute-looking paths, hidden
  folders, symlink escapes, and cycles on `/fragments/list`.
- [x] Make `Publish` and nested `Watch` reject any project ancestor that is a
  symlink, including watched project trees.
- [x] Add store tests proving publish/watch beneath a watched tree fails without
  creating or changing anything in the external source.
- [x] Audit lookup and delete operations for symlink-swap windows; replace lexical
  checks where needed with containment-safe filesystem operations.
- [x] Reject symlink components beneath `.annotations` for reads, writes, walks,
  cleanup, and temporary-file replacement.
- [x] Add annotation tests proving sidecar symlinks cannot read from or write to
  paths outside `.annotations`.

### Phase 2: Annotation Revision Integrity

- [x] Introduce per-document synchronization covering revision load, validation,
  write/remove, and pruning for all annotation mutation paths.
- [x] Ensure two concurrent writes at revision `N` produce exactly one success at
  `N+1` and one `ErrRevMismatch`.
- [x] Preserve revision history when the last annotation is removed, using an
  empty sidecar/tombstone or another durable revision record.
- [x] Define and test revision behavior for `DeleteNotes`, artifact deletion,
  unwatch cleanup, and republishing a reused artifact name.
- [x] Add deterministic concurrent store and HTTP tests for PUT/PUT, PUT/delete,
  CLI reply/resolve races, and stale recreation after deleting the final note.
- [x] Run annotation concurrency tests under `go test -race`.

### Phase 3: Deployment And Trust Boundaries

- [x] Change the native default listen address to `127.0.0.1:8737`.
- [x] Bind the container port as `127.0.0.1:8737:8737` by default.
- [x] Add `make install LAN=1` as the explicit native LAN-access installation
  mode; keep plain `make install` loopback-only and make repeated installs switch
  the installed service cleanly between modes.
- [x] Document `make install LAN=1`, the equivalent explicit `-addr` or container
  deployment change, and the unauthenticated read/write/delete implications.
- [x] Replace `http.ListenAndServe` with an `http.Server` configured with a
  `ReadHeaderTimeout` and appropriate idle timeout without breaking SSE.
- [x] Add startup/configuration tests for the default address where practical.
- [x] Add installation tests or checks covering both the default and `LAN=1`
  generated systemd service addresses.
- [x] Keep `allow-same-origin`: opening an artifact explicitly trusts it with
  Scratchpad origin privileges. See the linked ADR.
- [x] No isolation follow-up is required now: opened artifacts remain explicitly
  trusted under the accepted same-origin ADR.

### Phase 4: Safe Reports And CLI Publishing

- [x] Shell-quote document paths in generated note instructions using one tested,
  POSIX-compatible quoting implementation.
- [x] Add report tests for spaces, single quotes, semicolons, command substitution,
  newlines, and leading hyphens.
- [x] Update `filesFromDir` to reject symlinks and non-regular entries rather than
  dereferencing them or attempting to read FIFOs/devices.
- [x] Add CLI/package tests proving files outside the source tree are not copied
  and special files do not block publication.
- [x] Replace `/dev/stdin` reads with `io.ReadAll(os.Stdin)` for portability.
- [x] Reject surplus positional arguments and mutually specified `-dir`/`-html`
  publish modes with usage errors.

### Phase 5: Watcher Reliability

- [x] Refactor initial recursive watch registration to return actionable root and
  descendant errors instead of leaving a silently inert watcher.
- [x] Make web startup fail when the root cannot be watched; define behavior when
  the watcher exits after startup.
- [x] Track the desired canonical watch set during reconciliation and remove
  watches no longer reachable after unwatch, rename, or symlink replacement.
- [x] Detect `fsnotify.ErrEventOverflow`, immediately reconcile the full tree,
  and broadcast because exact changes are unknown.
- [x] Add bounded refresh latency during sustained event streams while retaining
  trailing-edge debounce behavior.
- [x] Stop active timers on watcher shutdown.
- [x] Add watcher tests for populated subtree creation, symlink cycles, unwatch,
  target replacement, registration failure, overflow recovery, sustained events,
  descriptor cleanup, and cancellation.

### Phase 6: Cache And HTTP Protocol Correctness

- [x] Remove the ignore-cache data race by reading and updating cache entries
  under the mutex or replacing immutable entries atomically.
- [x] Add a concurrent expired-cache regression test that is run with `-race`.
- [x] Decide whether ignore-file freshness needs content hashing to detect
  same-size, same-mtime replacement; document the performance/security trade-off.
- [x] Parse gzip quality values numerically and compress only when the effective
  gzip quality is greater than zero.
- [x] Add gzip tests for `q=0.0`, `q=0.00`, malformed and out-of-range values,
  mixed-case parameters, duplicate offers, and wildcard interactions.

### Phase 7: Verification And Documentation

- [x] Run `gofmt` on changed Go files.
- [x] Run `make test`.
- [x] Run `go test -race ./internal/store ./internal/web ./internal/watch`.
- [x] Confirm the default native and container listeners are loopback-only.
- [x] Verify SSE refresh, artifact viewing, annotation editing, delete,
  unwatch, CLI publish, and watched-project browsing.
- [x] Update `README.md`, `skill/SKILL.md`, and `docs/internals.md` for any changed
  user-facing semantics or trust assumptions.
- [x] Run `make install-skill` after changing the agent-facing contract.

## Acceptance Criteria

- No HTTP query, store operation, annotation operation, or CLI directory publish
  can read or write outside its authorized root through traversal or symlinks.
- Annotation mutations provide real compare-and-swap behavior under concurrency,
  and deleting all annotations cannot reset a document to revision zero.
- Default installations listen only on loopback; remote exposure is explicit and
  documented as unauthenticated.
- Generated report commands remain a single safe command for every accepted
  document path.
- Watch registration failures are observable, overflow triggers recovery, and
  removed external trees no longer generate events or retain watches.
- Ignore-cache access is race-free and gzip negotiation honors numeric zero
  quality values.
- New regression tests pass with the race detector and the existing behavior
  tests remain green.

## Risks And Decisions

- Strong symlink containment should preserve deliberate top-level watched links
  while refusing mutation beneath them; over-broad rejection would break the
  core watch feature.
- Durable note revision tombstones affect cleanup and name-reuse semantics. A
  deleted/unwatched artifact must still clear its entire annotation history so a
  genuinely republished name starts clean.
- Filesystem race elimination may require Linux-specific directory-descriptor
  operations. If portability prevents a complete solution, document the residual
  race and isolate platform-specific implementations behind a small API.
- Watch overflow and descriptor-leak tests may need an injectable watcher
  interface because reliably exhausting host inotify limits is unsuitable for
  normal unit tests.
- Removing same-origin viewer access would require redesigning annotation
  injection and is intentionally gated on an explicit trust-model decision.
