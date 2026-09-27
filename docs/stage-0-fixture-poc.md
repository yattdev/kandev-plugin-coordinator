# Stage 0 synthetic-board POC

Run `go run ./cmd/fixture-poc`. The driver submits two normalized fixture
snapshots through `Plugin.HandleAction` and `coordinator.shadow-observe`, then
uses the existing shadow-governor recovery receipt verification for its typed
effect readback. The fixture contains a Blocked target, a Done dependency, and
an InProgress task. Its deterministic selection issues one exact
`fixture.unblock` grant for `blocked-target`; competing, revoked, and replayed
uses are denied by focused tests.

This is only a local deterministic fixture. It has no real Kandev board,
Host task mutation, provider, credential, scheduler, deployment, merge, or
release authority. The policy contract remains vendored under
`docs/contracts/upstream`; this POC relies on its existing normalized-snapshot
and receipt boundary rather than claiming a new production authority.
