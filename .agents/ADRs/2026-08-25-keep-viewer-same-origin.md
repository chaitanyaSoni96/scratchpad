---
title: Keep Viewer Artifacts Same-Origin
type: adr
status: accepted
created: 2026-08-25
links: [../plans/completed/codebase-review-remediation/codebase-review-remediation.md]
---

# Keep Viewer Artifacts Same-Origin

## Decision

Keep `allow-same-origin` on the sandboxed artifact viewer for now. Treat opening
an artifact in the viewer as an explicit decision to trust that artifact with
the Scratchpad origin's privileges.

## Context

The viewer's annotation integration currently relies on same-origin access.
Removing it would require a separate-origin or message-based design rather than
a small sandbox flag change.

Same-origin artifacts can potentially call Scratchpad's unauthenticated routes,
including routes that modify notes or delete content. Card previews remain
opaque-origin and do not receive this privilege; it applies only after a user
deliberately opens an artifact in the viewer.

## Consequences

- Existing viewer and annotation behavior remains unchanged.
- Users should open only artifacts they trust.
- Network exposure increases the impact of opening a malicious artifact because
  Scratchpad has no authentication or authorization boundary.
- A future change to isolate viewer artifacts requires a dedicated design for
  trusted communication between the viewer and artifact frame.
