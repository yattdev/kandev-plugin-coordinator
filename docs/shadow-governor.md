# Shadow governor pilot

This opt-in pilot evaluates **normalized snapshots supplied by a caller**. The
plugin manifest grants workspace and workflow reads only; it has no task/event
feed permission. Consequently live board collection is unavailable, and this
pilot must never claim that a supplied snapshot represents current Host state.

`coordinator.shadow-observe` is the callable entry point. Its verified
workspace context must match `workspace_id` in a `shadow-governor/v1` payload.
The payload includes an event identity, provenance, completeness, observation
time, and bounded task facts (lane/state/owner/dependencies/blocker/head/plan
version/progress/result). Unknown, malformed, stale, incomplete, or oversized
evidence is rejected or escalated to attention; it never means “nothing
changed.”

The default plugin does not wire an observer, so the action returns
`unavailable`. An embedding runtime must explicitly provide `ShadowStoreObserver`
backed by the existing plugin SQLite durable-state store. Its checkpoint stores
the last observation, replay identities, and last successful Astra strategic
watermark. SQLite fencing protects that checkpoint transaction only; it does
not fence Host workers or turn the internal journal into a board event feed.

Results are recommendations only. They never dispatch a model, alter a task or
session, change a workflow, suppress monitoring, or start a timer. The routing
ladder is deterministic: no tier for normal unchanged evidence; Sol for bounded
blocking/health attention; Astra for incomplete or conflicting evidence,
multiple blocked work, or clock rollback. Luna and Terra remain documented
future recommendations for extraction and bounded execution; the current
workflow profile is authoritative.

The default active health watchdog is three hours (clamped to one through 24
hours). Digests are ordered and carry provenance, completeness, changed count,
and reason codes. Cost fields are intentionally absent: the snapshot format has
no Host usage or price feed, so this pilot makes no savings claim.

`Contract` is versioned and its validator rejects expired or mismatched
workspace/task/head/generation values before a proposed significant action.
This is a stale-plan guard for future adapters, not authority to execute an
action. Roll back by removing the observer wiring; durable checkpoints are
isolated under the `shadow_governor` record kind and do not affect scheduler
records.
