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
`windows-latest` / `windows-11-arm` runners against pushes of this branch to
`origin`. Linux gates run locally.

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

Known-failure list at baseline:

| Site | Failure | Owning phase |
|---|---|---|
| `internal/store/annotations.go:96,138,252,276,310` | `undefined: annotationFS` (Linux-only type) | P2.1, P3.7–P3.10 |
| `internal/store/annotations.go:120` | `undefined: openAnnotationFS` | P3.7 |
| `internal/store/annotations.go:103,127,129,131,155` | `undefined: unix.Flock`, `unix.LOCK_*` — advisory lock in shared policy | P2.4 |
| `internal/store/annotations.go:147,150` | `undefined: unix.Close`, `unix.Openat`, `unix.O_*` in shared policy | P2.4 |
| `internal/store/store.go:173,457,542` | `undefined: openRootedFS` | P3.1 |
| `internal/store/store.go:182,467` | `undefined: dirHasHTMLFD` | P3.6 |
| `internal/store/store.go:469,574` | `undefined: fdPath` (`/proc/self/fd`) — no Windows analogue | P2.1, P3.1 |
| `internal/store/store.go:495` | `undefined: OpenDocument` | P3.5 |
| `internal/store/store.go:561` | `undefined: openDirAt` | P3.2 |
| `internal/store/store.go:567` | `undefined: removeTreeAt` | P3.9 |
| `internal/store/store.go:569` | `undefined: writeFileAt` | P3.3 |
| `internal/store/store.go:183,470,477,551,555,556,563,566,625,631,632,636,769,771,772,775,781,833,835,836,838,845,849` | direct `unix.*` calls embedded in shared policy | P2.3, P2.4 |
| `internal/watch/watch.go:281` | `syscall.Stat_t` directory identity; masked at baseline because `internal/store` fails first | P2.2, P4.1 |

`GOOS=windows go vet ./...` fails identically. `GOARCH=arm64` fails identically —
the failures are OS-level, not architecture-level.

Behaviour was not edited for this task.
