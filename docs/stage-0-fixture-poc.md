# Stage 0 synthetic-board POC

Run `go run ./cmd/fixture-poc`. A fixture-only typed `pluginsdk.Host` exposes
the local task adapter through `Host.Tasks()`; it lists and updates the three
synthetic task rows, which are normalized through `Plugin.HandleAction` and
`coordinator.shadow-observe`. The typed JSON report
contains before/after inventories, the Blocked target's Done dependency,
selected action and exact grant, independent effect readback, and actual
competing-target, revoked-grant, non-exact-action, and stale-evidence denial
receipts. Durable
operation intent is durable before the local task update. Injected
post-governor/pre-operation-record interruption leaves no fixture mutation;
the fixture task projection independently rehydrates an interruption after the
effect, reconciling to one verified readback without a second update.
Malformed or non-pending unverified projections report an unknown outcome
rather than a verified effect.

This is only a local deterministic fixture. It has no real Kandev board,
Host task mutation, provider, credential, scheduler, deployment, merge, or
release authority. The policy contract remains vendored under
`docs/contracts/upstream`; this POC relies on its existing normalized-snapshot
and receipt boundary rather than claiming a new production authority.
