# Contract field mapping, evolution rules, and validator conventions

Status: **agreed 2026-09-03**, implementing `docs/DECISIONS.md#coordinator-policy-is-contract-validated-not-hand-copied-2026-09-03-human-directed`.

This document is the canonical reference for `coordinator-policy-contract.json`.
It exists so a reviewer (or the plugin repository's CI) can answer, for any
field: *where does this come from, and what changes are compatible versus
breaking?* It is deliberately separate from the contract file itself, which
stays compact and machine-readable.

## 1. What the contract is, and is not

- The contract is a **compact, versioned, machine-readable extract** of stable
  cross-runtime invariants — not a serialized copy of `PROMPT.md`, and it never
  contains live board state (task/session IDs, board rows, in-flight queue
  entries).
- It captures **authority boundaries, ownership shape, identity/receipt
  requirements, gate requirements, notification ordering, and escalation
  classes** — the parts of the charter that the plugin's scheduler/prompt
  bundle must not silently drift from.
- It excludes secrets, credentials, and anything transient (`exclusions`
  field). If a future field would require a secret or a transient ID to be
  meaningful, it does not belong in this contract.
- Scale/load architecture, the burst-harness spec, and state-compaction
  mechanics are **delivery specifications**, not contract invariants (see the
  2026-09-03 decision's final paragraph). They live in `docs/rfcs/` and are
  referenced from the contract's mapping table below only where a contract
  field constrains them (e.g. `worker_helper_receipts.workers_never_mutate` is
  an invariant the scale RFC's read-only worker pool must satisfy).

## 2. Field-by-field source mapping

| Contract field | Canonical source | Notes |
| --- | --- | --- |
| `source_charter_effective_version` | `PROMPT.md` line 2 (`<!-- effective-version: ... -->`) | Bump whenever `PROMPT.md`'s effective-version header changes in a way that touches any mapped field below. |
| `source_decision_anchor` | `docs/DECISIONS.md#coordinator-policy-is-contract-validated-not-hand-copied-2026-09-03-human-directed` | The decision that authorizes this contract's existence. |
| `authority_boundaries.approval_principal`, `.scope`, `.cross_workspace_authority` | `PROMPT.md` "FULL COORDINATOR APPROVAL AUTHORITY" section (same-workspace approval principal); `docs/FILESYSTEM_DOCKER_CONTRACT.md` §3 (workspace-scoped authority, peers have no standing over each other) | Cross-workspace authority is always `false`; no evolution may set it `true`. `cross_workspace_authority` must be **present** and `false` — a contract that removes the key entirely must fail closed the same as one that weakens it to `true` (the validator checks presence explicitly, not just `!= true`, since a naive default-on-missing-key check would otherwise let removal silently pass; see `fixtures/missing_cross_workspace_authority_contract.json`). `scope` must stay exactly `"same_workspace_only"` — weakening or removing it is independently enforced even though `approval_principal`/`cross_workspace_authority` may still check out individually; see `fixtures/weakened_authority_scope_contract.json` and `fixtures/missing_authority_scope_contract.json`. `approval_principal` must stay exactly `"coordinator"` — weakening it to `"none"` removes the accountable principal for every `coordinator_decidable_examples` entry this same object lists, even though `scope`/`cross_workspace_authority` stay correct; enforced by the validator's `missing_required_invariant` check, see `fixtures/false_approval_principal_none_contract.json`. |
| `authority_boundaries.coordinator_decidable_examples` | `PROMPT.md` "Everything else is Coordinator-decidable" paragraph | Additive (new examples) is a **minor** change; removing an example without a corresponding Human-reserved reclassification is **breaking**. |
| `authority_boundaries.human_reserved_classes` | `PROMPT.md` "The only approval classes reserved for the Human are (1)... (2)..." | These two classes (`destructive_or_irreversible`, `security_or_trust_boundary`) are the floor. A contract or overlay may only add to this set, never remove from it — enforced by the validator's `missing_required_invariant` / `overlay_widens_authority` checks. |
| `workspace_lane_ownership.*` | `PROMPT.md` "Monitor every task in spec, work, review, qa, pr, ci-fixup, AND Done"; `docs/FILESYSTEM_DOCKER_CONTRACT.md` §3 (peer workspaces) | `done_is_terminal_integrity_lane` must stay `true` — Done is never an ignored archive — enforced by the validator's `missing_required_invariant` check; see `fixtures/false_done_terminal_integrity_lane_contract.json`. `monitored_lanes` must keep `done` — a `done_is_terminal_integrity_lane: true` claim about a lane that isn't even monitored is meaningless; see `fixtures/missing_done_monitored_lane_contract.json`. `unit` must stay exactly `"workspace"` — this is the granularity every other `workspace_lane_ownership`/`authority_boundaries` field assumes; see `fixtures/weakened_lane_unit_contract.json` and `fixtures/missing_lane_unit_contract.json`. `peer_model` must be **present** and `true` — workspaces have no standing over each other (`docs/FILESYSTEM_DOCKER_CONTRACT.md` §3); see `fixtures/weakened_peer_model_contract.json` and `fixtures/missing_peer_model_contract.json`. `cross_workspace_standing` must be **present** and `false` — the lane-ownership-level twin of `authority_boundaries.cross_workspace_authority`; see `fixtures/weakened_cross_workspace_standing_contract.json` and `fixtures/missing_cross_workspace_standing_contract.json`. `auto_start_lane_move_requires_settled_lifecycle` must stay `true` — an auto-start lane move must never fire against a task whose lifecycle has not yet settled; see `fixtures/weakened_auto_start_lane_move_contract.json` and `fixtures/missing_auto_start_lane_move_contract.json`. |
| `queue_claim_identity.*` | `PROMPT.md` "Queue identity and disposition are per-entry..." paragraph (queue helpers/coalescing section); `docs/DECISIONS.md#cross-sender-routine-wake-coalescing-is-identity-based-not-sender-based-2026-09-03` for the routine-identity fields specifically | `audit_model` must stay `exact_entry_never_global_watermark`; this is the specific defect class the 2026-09-01j decision closed. `coalescing_rule` must stay exactly `only_identity_equivalent_pending_routine_wakes_for_same_target` — a broader rule (e.g. `all_messages`) would coalesce distinct Human/task/peer messages, which `coalescing_forbidden_for` exists to prevent; enforced by the validator's `missing_required_invariant` check, see `fixtures/weakened_coalescing_rule_contract.json`. `minimum_trusted_envelope` must keep `entry_id` (see `fixtures/missing_entry_id_envelope_contract.json`) and `workspace_id` (see `fixtures/missing_workspace_id_envelope_contract.json` — without it a claim's trusted envelope cannot be scoped to one workspace, reopening the cross-workspace-authority hole at the queue-claim layer) and `coalescing_forbidden_for` must keep `human_input` (see `fixtures/missing_human_input_coalescing_forbidden_contract.json`) — all independently enforced floors, not implied by `audit_model`/`coalescing_rule` alone. `claim_collision_check` must stay `deterministic_claim_set`, never `"none"` — without an actual collision check, two claims against the same resource key could both be granted concurrently, which is exactly the scale RFC's "zero claim overlap" invariant; see `fixtures/false_claim_collision_check_none_contract.json`. Added at `contract_version` `1.1.0` (additive/MINOR): `routine_identity_components` must be a superset of `{workspace_id, routine_type_or_name, policy_or_prompt_version_generation, semantic_scope_generation}` — this is the canonical Host routine identity, deliberately independent of sender task/session/message ID; see `fixtures/missing_semantic_scope_generation_routine_identity_contract.json`. `routine_identity_excludes_sender_ids` must stay `true` — see `fixtures/false_routine_identity_excludes_sender_contract.json`. `cross_sender_coalescing_permitted` must stay `true` — without it, an identical generation delivered by two different senders would never coalesce, defeating the point of a sender-independent identity; see `fixtures/false_cross_sender_coalescing_permitted_contract.json`. `coalescing_preserved_state` must stay exactly `single_pending_successor_or_freshness_bit` — coalescing must never "drop" the sole effective wake outright; see `fixtures/weakened_coalescing_preserved_state_contract.json`. |
| `worker_helper_receipts.*` | `PROMPT.md` "helpers never mutate the board, provider, task worktrees..." paragraph; "A helper receipt never proves that its source queue row was claimed..."; `docs/rfcs/PLUGIN_SCALE_RFC.md` §2.5 (single-writer mutation lane) | `workers_never_mutate` is a floor invariant shared with the scale RFC's read-only worker pool design. `mutation_serialized_by` must stay exactly `"single_writer_lane"` — this is what actually serializes the mutations `workers_never_mutate` forbids workers from making; weakening or removing it would leave "workers never mutate" true in isolation with no named serializing lane; see `fixtures/weakened_mutation_serialized_by_contract.json` and `fixtures/missing_mutation_serialized_by_contract.json`. `receipt_is_not_proof_of` must keep all four disposition classes (`queue_row_claimed`, `queue_row_acknowledged`, `queue_row_removed`, `capacity_released`) — dropping any one would let a receipt be silently treated as sufficient for the dropped disposition; see `fixtures/weakened_receipt_is_not_proof_of_contract.json` (one entry removed) and `fixtures/missing_receipt_is_not_proof_of_contract.json` (whole field removed). `receipt_required_fields` must keep `claim_or_lease_id` — without it a receipt cannot be tied back to the exact claim it attests to; see `fixtures/missing_claim_or_lease_id_receipt_contract.json`. `freshness_barrier_required_before_reporting` must stay `true` — without it a worker could report against state it read before a concurrent mutation, indistinguishable from a fresh report; see `fixtures/false_freshness_barrier_contract.json`. Added at `contract_version` `1.1.0` (additive/MINOR): `routine_wake_coalescing_receipt_fields` names the full schema for a coalesced routine-wake receipt (`canonical_entry_id`, `absorbed_source_entry_ids`, `absorbed_source_entry_count`, `absorbed_source_entry_timestamps_without_bodies` — never the absorbed entries' bodies — `leader_fencing_token`, `dirty_generation`, `post_run_requeue_required`); the validator enforces the safety-critical subset `{canonical_entry_id, absorbed_source_entry_ids, leader_fencing_token, dirty_generation}` — see `fixtures/missing_leader_fencing_token_routine_wake_receipt_contract.json`. |
| `gates.review`, `gates.qa`, `gates.readiness`, `gates.done_integrity` | `PROMPT.md` "exact-head Review/QA/CI readiness" (multiple sections); "DONE TERMINAL-INTEGRITY GATE" | All four must keep `exact_head_required: true`. This is the single most load-bearing invariant in the contract — it is what stops a stale-head approval. `gates.review` and `gates.qa` must additionally keep `independent_session_required: true` (symmetric requirement, both checked identically by the validator) — self-attestation by the authoring session is not a gate; see `fixtures/false_review_independent_session_contract.json` and `fixtures/false_qa_independent_session_contract.json`. `gates.readiness.recheck_after_draft_to_ready_transition` must stay `true` — readiness must be rechecked after a draft-to-ready transition, not only evaluated once while still draft; see `fixtures/false_readiness_recheck_contract.json`. `gates.done_integrity.terminal_receipt_required` must stay `true`, independently of `exact_head_required` — without it, a matching head alone could reach Done with no terminal receipt at all; see `fixtures/false_done_terminal_receipt_required_contract.json`. |
| `readiness_notification_order` | `PROMPT.md` "universal reviewer-request ordering is strict: provider-confirmed ready/non-draft first, then refreshed post-ready gates, then and only then reviewer notification" | Order is fixed; reordering, or dropping the required middle `refreshed_post_ready_gates` step, is **breaking**. The validator checks the full ordered sequence (not just first/last elements) — see `fixtures/reordered_notification_contract.json`. |
| `escalation_classes.human_reserved[].includes` | `PROMPT.md` "Destructive includes deletion, reset/clean/discard..." / "Security includes secret/credential disclosure..." | Additive is **minor**; removing an item from `includes` without also removing it from the `PROMPT.md` prose is **breaking** (and would itself require a Human-directed decision, per the escalation-classes rule). |
| `escalation_classes.label_alone_does_not_escalate` | `PROMPT.md` "Labels such as production, protected/release branch, cost, or external communication do not by themselves create a second approval principal" | Advisory list; additive-only. |
| `done_integrity.required_proof`, `.receipt_fields`, `.merged_pr_or_done_placement_alone_is_not_proof` | `PROMPT.md` "Before any move to Done, explicitly prove..." and the terminal-receipt paragraph | Mirrors `docs/FILESYSTEM_DOCKER_CONTRACT.md`'s audit-trail convention (name the exact fields, never an aggregate claim). `merged_pr_or_done_placement_alone_is_not_proof` must stay `true` — a merged PR or Done-column placement alone is never sufficient; enforced by the validator's `missing_required_invariant` check, see `fixtures/false_done_placement_alone_not_proof_contract.json`. `required_proof` must keep `canonical_merged_identity_and_accepted_head` (see `fixtures/missing_canonical_merged_identity_done_proof_contract.json` — this is the base head-identity proof every other `required_proof` entry checks a property of) and `no_unique_local_or_untracked_work` (see `fixtures/missing_no_unique_local_done_proof_contract.json`), and `receipt_fields` must keep `local_head` (see `fixtures/missing_local_head_done_receipt_contract.json`) — the receipt field is what the proof is checked against, so all are independently enforced. |
| `exclusions` | `docs/DECISIONS.md#coordinator-policy-is-contract-validated-not-hand-copied-2026-09-03-human-directed` (second paragraph) | Category names only (e.g. `"secrets"`), never example values. |

## 3. Compatible vs. breaking evolution

The contract uses a `MAJOR.MINOR.PATCH` `contract_version`, checked by the
validator against `compatibility.min_supported_contract_version` and
`compatibility.max_known_contract_version`.

- **PATCH** — wording/clarification only; no field added, removed, or
  semantically changed. Digest still changes (any byte change moves the
  digest), but no invariant check result can change. Example: fixing a typo in
  an `includes` string.
- **MINOR** — backward-compatible addition: a new optional field, a new
  `coordinator_decidable_examples` entry, a new `human_reserved_classes` entry,
  a new `includes` item under an existing escalation class. A plugin vendored
  at the previous minor version remains valid (it simply doesn't yet know
  about the addition); a plugin vendored at the new version must reflect it.
- **MAJOR** — anything that changes what was previously required or allowed:
  removing a `required_fields` entry, weakening any `gates.*.exact_head_required`
  or `worker_helper_receipts.workers_never_mutate` or
  `queue_claim_identity.audit_model` invariant, removing a
  `human_reserved_classes` entry, reordering `readiness_notification_order`,
  or widening `cross_workspace_authority`. A **major** bump must cite the
  `PROMPT.md` effective-version change and, if it changes binding authority,
  a corresponding `docs/DECISIONS.md` entry — never a silent contract-only
  change to authority.

Bumping `contract_version` always requires recomputing `digest` (see §5) and
updating `source_charter_effective_version` if the underlying charter section
moved.

**Precedent**: `contract_version` `1.0.0` → `1.1.0` (this repository's first
bump) added `queue_claim_identity.routine_identity_components` /
`.routine_identity_excludes_sender_ids` / `.cross_sender_coalescing_permitted`
/ `.coalescing_preserved_state` and
`worker_helper_receipts.routine_wake_coalescing_receipt_fields` — five new
fields, all newly-mandatory-but-additive (no existing field removed, weakened,
or reordered), so it is **MINOR**, not MAJOR. `source_charter_effective_version`
was left at `2026-09-03a` because no `PROMPT.md` rule changed; the new floors
are a contract/RFC-level clarification only, recorded in
`docs/DECISIONS.md#cross-sender-routine-wake-coalescing-is-identity-based-not-sender-based-2026-09-03`.
Because `validate_plugin_snapshot()` requires exact `plugin_contract_version`
equality (not merely `>= min_supported`), a plugin still vendored at `1.0.0`
fails closed against this `1.1.0` contract until it re-vendors — consistent
with §4 point 3's "older deployed plugin fails observably" rule, even though
the bump itself is semantically backward-compatible.

## 4. Plugin vendoring

1. The plugin repository vendors an **exact copy** of
   `coordinator-policy-contract.json` (or the fields it actually consumes,
   still under the same `contract_version`/`digest`) plus a copy of
   `validate_contract.py`. Neither file has a runtime dependency on this
   checkout — that's why the validator only takes explicit `--contract`/
   `--snapshot`/`--overlay` paths.
2. Plugin CI runs a **plugin snapshot** through
   `validate_contract.py plugin-snapshot --contract <vendored contract>
   --snapshot <plugin's own defaults dump>` (see `validate_plugin_snapshot()`
   in the validator, and `fixtures/valid_plugin_snapshot.json` /
   `fixtures/contradictory_plugin_snapshot.json` for the expected snapshot
   shape). This is the CI gate the 2026-09-03 decision requires: a version or
   digest mismatch, missing invariant, or a contradictory default
   **must fail the plugin build**.
3. Updating the contract (this repository) and updating the plugin's vendored
   snapshot (the plugin repository) are **separate reviewed changes**. Do not
   bump both in the same PR across repositories — the whole point is that an
   older deployed plugin fails observably (wrong `vendored_digest` /
   `plugin_contract_version`) instead of silently claiming parity.
4. Unsupported version behavior: if the plugin's `plugin_contract_version` is
   older than this contract's `compatibility.min_supported_contract_version`,
   or newer than `compatibility.max_known_contract_version`, the build **must
   fail closed** (`compatibility.unsupported_version_behavior`), never
   silently downgrade to "best effort". The validator itself enforces that
   `compatibility.unsupported_version_behavior` is exactly `"fail_closed"`
   (`missing_required_invariant` if it is anything else, e.g.
   `"best_effort"`); see `fixtures/best_effort_unsupported_version_contract.json`.
5. A plugin `defaults` snapshot must explicitly declare every mandatory key
   (`human_reserved_classes`, `exact_head_gates`, `workers_never_mutate`,
   `cross_workspace_authority`) with a non-empty value where applicable. An
   absent or empty `defaults` object is rejected
   (`missing_required_invariant`) rather than silently treated as "no
   contradiction found" — see `fixtures/empty_defaults_plugin_snapshot.json`
   and `fixtures/missing_defaults_plugin_snapshot.json`.

### 4.1 The validator's own version ceiling is hardcoded, not self-declared

`validate_contract()` checks `contract_version` against two independent
things, and a contract must pass both:

1. The contract's own `compatibility.max_known_contract_version` /
   `min_supported_contract_version` (a same-document consistency check).
2. `VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION`, a constant hardcoded in
   `validate_contract.py` itself, independent of anything the contract
   document declares.

Check (1) alone is insufficient: a contract can self-declare
`contract_version: "2.0.0"` alongside a matching
`compatibility.max_known_contract_version: "2.0.0"`, which is internally
consistent and would pass check (1) even though this validator build was
never written to understand a 2.0.0 schema. Check (2) is the check that
actually protects against that case — see
`fixtures/future_version_contract.json` and
`test_self_declared_future_version_is_rejected`. Bump
`VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION` only when the validator source is
actually upgraded to understand a new `contract_version`.

## 5. Workspace-overlay narrowing

A workspace overlay (`fixtures/narrowing_overlay.json` shows a valid shape)
may only ever **narrow** behavior relative to its `base_contract_version`:

- `cross_workspace_authority` may stay `false`; it may never be set `true`.
- `human_reserved_classes` may only **grow** (a workspace can decide more
  classes need Human sign-off than the floor requires; it can never shrink
  the floor).
- `coordinator_decidable_examples` may only **shrink** (a workspace can
  decide fewer things are Coordinator-decidable; it can never add a new
  decidable example the base contract didn't already list).

`validate_overlay()` enforces exactly these three checks and reports
`overlay_widens_authority` for any violation. An overlay is validated against
the same `--contract` path as everything else; it is never self-certifying.

## 6. Digest computation (reproducible without importing the validator)

`digest` is `sha256` over the contract's own JSON body, minus the `digest`
field itself, serialized with `json.dumps(doc, sort_keys=True,
separators=(",", ":"))` (compact, UTF-8, sorted keys — no field ordering or
whitespace dependency). Any tool in any language can reproduce this by:

1. Parsing the contract JSON.
2. Deleting the top-level `digest` key.
3. Re-serializing with keys sorted and no insignificant whitespace.
4. Hashing the UTF-8 bytes with SHA-256.

This is implemented once in `validate_contract.py:compute_digest` — the
canonical, tested implementation — but the algorithm above is intentionally
simple enough to reimplement in the plugin's own language/toolchain if
preferred, as long as it is round-trip tested against this contract's
published `digest` before being trusted.

## 7. Failure behavior for unsupported contract versions

- A contract whose `contract_version` is below its own
  `compatibility.min_supported_contract_version`, a `required_fields` list
  containing an entry this validator's `REQUIRED_TOP_LEVEL_FIELDS` doesn't
  recognize, **or a `required_fields` list that omits an entry this
  validator does recognize** (e.g. the top-level `done_integrity` object is
  still present but the string `"done_integrity"` is missing from the
  `required_fields` array itself), is invalid — **fail closed**, never "pass
  with a warning". See `fixtures/required_fields_omits_done_integrity_contract.json`.
- A plugin build that cannot resolve a contract file at all (missing vendor
  copy, path not passed) must treat that the same as a failing validation run
  — it must not fall back to trusting its own hardcoded prompt defaults.
- See `docs/rfcs/PLUGIN_SCALE_RFC.md` and `docs/rfcs/STATE_COMPACTION_SPEC.md`
  for how this contract's invariants (`workers_never_mutate`, exact-head
  gates, single-writer mutation lane, exact-entry queue identity) are
  satisfied by the scale/compaction designs; those documents do not
  reintroduce or relax any invariant defined here.

## 8. Related documents

- [`coordinator-policy-contract.json`](coordinator-policy-contract.json) — the contract itself.
- [`validate_contract.py`](validate_contract.py) — the standalone validator.
- [`test_validate_contract.py`](test_validate_contract.py) — the test suite (`python3 -m unittest docs/contracts/test_validate_contract.py -v`).
- [`fixtures/`](fixtures/) — one fixture per required validator scenario (valid contract, stale version, stale digest, missing invariant, unknown required field, exclusion-leaking-secret-shaped-value, self-declared future contract version, reordered/incomplete notification sequence, contradictory plugin snapshot, empty/missing plugin snapshot defaults, widening overlay, plus positive controls). Isolated adversarial fixtures for each individually-mapped floor invariant, each with an independently recomputed valid digest so a weakened value cannot hide behind a stale-digest failure instead of its own check: `false_done_terminal_integrity_lane_contract.json` (`workspace_lane_ownership.done_is_terminal_integrity_lane: false`), `false_review_independent_session_contract.json` / `false_qa_independent_session_contract.json` (`gates.review`/`gates.qa` `.independent_session_required: false`, checked symmetrically), `weakened_coalescing_rule_contract.json` (`queue_claim_identity.coalescing_rule: "all_messages"`), `false_done_placement_alone_not_proof_contract.json` (`done_integrity.merged_pr_or_done_placement_alone_is_not_proof: false`), `required_fields_omits_done_integrity_contract.json` (the top-level `done_integrity` object is present and unchanged, but the self-declared `required_fields` list omits the string `"done_integrity"` — the validator now checks that the list itself enumerates every field this validator treats as required, not just that the object is physically present), `missing_entry_id_envelope_contract.json` (`queue_claim_identity.minimum_trusted_envelope` missing `"entry_id"`), `missing_workspace_id_envelope_contract.json` (`queue_claim_identity.minimum_trusted_envelope` missing `"workspace_id"`), `missing_human_input_coalescing_forbidden_contract.json` (`queue_claim_identity.coalescing_forbidden_for` missing `"human_input"`), `false_claim_collision_check_none_contract.json` (`queue_claim_identity.claim_collision_check: "none"`), `missing_claim_or_lease_id_receipt_contract.json` (`worker_helper_receipts.receipt_required_fields` missing `"claim_or_lease_id"`), `false_freshness_barrier_contract.json` (`worker_helper_receipts.freshness_barrier_required_before_reporting: false`), `missing_no_unique_local_done_proof_contract.json` (`done_integrity.required_proof` missing `"no_unique_local_or_untracked_work"`), `missing_canonical_merged_identity_done_proof_contract.json` (`done_integrity.required_proof` missing `"canonical_merged_identity_and_accepted_head"`), `missing_local_head_done_receipt_contract.json` (`done_integrity.receipt_fields` missing `"local_head"`), `false_readiness_recheck_contract.json` (`gates.readiness.recheck_after_draft_to_ready_transition: false`), `false_done_terminal_receipt_required_contract.json` (`gates.done_integrity.terminal_receipt_required: false`), `best_effort_unsupported_version_contract.json` (`compatibility.unsupported_version_behavior: "best_effort"`), `false_approval_principal_none_contract.json` (`authority_boundaries.approval_principal: "none"`), and `missing_done_monitored_lane_contract.json` (`workspace_lane_ownership.monitored_lanes` missing `"done"`). Added for the `contract_version` `1.1.0` routine-wake-identity clarification: `missing_semantic_scope_generation_routine_identity_contract.json` (`queue_claim_identity.routine_identity_components` missing `"semantic_scope_generation"`), `false_routine_identity_excludes_sender_contract.json` (`queue_claim_identity.routine_identity_excludes_sender_ids: false`), `false_cross_sender_coalescing_permitted_contract.json` (`queue_claim_identity.cross_sender_coalescing_permitted: false`), `weakened_coalescing_preserved_state_contract.json` (`queue_claim_identity.coalescing_preserved_state: "dropped"`), and `missing_leader_fencing_token_routine_wake_receipt_contract.json` (`worker_helper_receipts.routine_wake_coalescing_receipt_fields` missing `"leader_fencing_token"`). Added to close 8 previously-unguarded floors (each with a weaken and/or remove variant, all independently recomputed valid digests): `weakened_authority_scope_contract.json` / `missing_authority_scope_contract.json` (`authority_boundaries.scope`), `missing_cross_workspace_authority_contract.json` (`authority_boundaries.cross_workspace_authority` removed — the validator now checks presence explicitly, not just `!= true`, since a naive default-on-missing-key check let removal silently pass), `weakened_lane_unit_contract.json` / `missing_lane_unit_contract.json` (`workspace_lane_ownership.unit`), `weakened_peer_model_contract.json` / `missing_peer_model_contract.json` (`workspace_lane_ownership.peer_model`), `weakened_cross_workspace_standing_contract.json` / `missing_cross_workspace_standing_contract.json` (`workspace_lane_ownership.cross_workspace_standing`), `weakened_auto_start_lane_move_contract.json` / `missing_auto_start_lane_move_contract.json` (`workspace_lane_ownership.auto_start_lane_move_requires_settled_lifecycle`), `weakened_mutation_serialized_by_contract.json` / `missing_mutation_serialized_by_contract.json` (`worker_helper_receipts.mutation_serialized_by`), and `weakened_receipt_is_not_proof_of_contract.json` / `missing_receipt_is_not_proof_of_contract.json` (`worker_helper_receipts.receipt_is_not_proof_of`).
- [`adversarial_sweep.py`](adversarial_sweep.py) — the standalone, reproducible complete adversarial sweep: enumerates every mandatory invariant the validator enforces and applies every weaken/remove mutation that is semantically distinct for that field (78 mutations total as of this writing), each against an independently recomputed valid digest, and asserts every one is rejected without being masked by a `stale_digest` false-positive. Run directly (`python3 adversarial_sweep.py`) or via the wired-in test (`TestAdversarialSweep` in `test_validate_contract.py`).
- [`../rfcs/PLUGIN_SCALE_RFC.md`](../rfcs/PLUGIN_SCALE_RFC.md) — scale/load architecture and the 70-task/50-message burst harness.
- [`../rfcs/STATE_COMPACTION_SPEC.md`](../rfcs/STATE_COMPACTION_SPEC.md) — safe state compaction.
