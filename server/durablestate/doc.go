// Package durablestate implements the Coordinator plugin's durable
// materialized-state storage engine: a plugin-owned SQLite schema for
// current-state records, an append-only mutation log, full-content
// snapshots, hash-anchored compaction (rollup) receipts, and deterministic
// restore/replay — all funneled through a single fencing-token-checked
// storage boundary.
//
// This package implements
// docs/rfcs/STATE_COMPACTION_SPEC.md (contract v1.1.0, digest
// 00bc80871b666c17abacb96382f3e01291421cc8fe4b726f539fc613cdfb84a4) from
// yattdev/tasks-coordinator @ 2ca27d00477dc298fc91187274968f1fc3970fef, and
// its portable reference implementation/tests
// (docs/rfcs/replay_reference.py, docs/rfcs/test_replay_reference.py).
//
// Scope: this package owns the plugin's SQLite schema/migrations,
// materialized state, append-only audit/mutation history, full snapshots,
// hash-anchored compaction, deterministic restore/replay, single-writer
// fencing, and a narrow set of health surfaces. It does NOT implement the
// Kandev Host queue transport, leader-election/scheduling policy (when to
// call Compact, how leases are renewed), contract-vendoring CI, UI, or the
// full burst harness — those are owned by other tasks and consume this
// package's public API.
//
// Terminology mirrors the spec exactly: a "record" is one materialized
// current-state entry (a follow-up, lease, dirty-task, escalation, or
// done-receipt body); a "mutation" is one add/update/remove against that
// surface, always logged; a "snapshot" is a full, self-contained copy of
// current state at one instant; a "compaction" (rollup) moves resolved
// records from current state to the append-only archive, producing a
// receipt that hash-anchors exactly which records moved.
package durablestate
