#!/usr/bin/env python3
"""Tests for validate_contract.py against fixtures in fixtures/.

Stdlib-only (unittest). Run with:
    python3 -m unittest docs/contracts/test_validate_contract.py -v
or, from this directory:
    python3 test_validate_contract.py
"""

import json
import os
import subprocess
import sys
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)

import validate_contract as vc  # noqa: E402
import adversarial_sweep  # noqa: E402

CANONICAL = os.path.join(HERE, "coordinator-policy-contract.json")
FIXTURES = os.path.join(HERE, "fixtures")


def load(name):
    with open(os.path.join(FIXTURES, name), encoding="utf-8") as fh:
        return json.load(fh)


def load_canonical():
    with open(CANONICAL, encoding="utf-8") as fh:
        return json.load(fh)


class TestCanonicalContract(unittest.TestCase):
    def test_canonical_contract_is_valid(self):
        failures = vc.validate_contract(load_canonical())
        self.assertEqual(failures, [], f"canonical contract must be valid, got: {failures}")

    def test_canonical_digest_matches_recomputation(self):
        contract = load_canonical()
        self.assertEqual(contract["digest"], vc.compute_digest(contract))


class TestFixtureContracts(unittest.TestCase):
    def test_valid_contract_fixture_passes(self):
        failures = vc.validate_contract(load("valid_contract.json"))
        self.assertEqual(failures, [])

    def test_stale_version_is_rejected(self):
        failures = vc.validate_contract(load("stale_version_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("stale_version", checks)

    def test_stale_digest_is_rejected(self):
        failures = vc.validate_contract(load("stale_digest_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("stale_digest", checks)

    def test_missing_required_invariant_is_rejected(self):
        failures = vc.validate_contract(load("missing_invariant_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_unknown_required_field_is_rejected(self):
        failures = vc.validate_contract(load("unknown_required_field_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("unknown_required_field", checks)

    def test_self_declared_future_version_is_rejected(self):
        # Regression guard: a contract that self-declares contract_version
        # 2.0.0 alongside a matching compatibility.min/max_known of 2.0.0 is
        # internally self-consistent (the same-document check alone would
        # pass it). The validator's own hardcoded
        # VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION ceiling must still reject
        # it, since this v1 validator was never written to understand a
        # 2.0.0 schema.
        contract = load("future_version_contract.json")
        self.assertEqual(contract["contract_version"], "2.0.0")
        self.assertEqual(contract["compatibility"]["max_known_contract_version"], "2.0.0")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("stale_validator_or_future_contract", checks)

    def test_reordered_notification_sequence_is_rejected(self):
        # Regression guard for the old first/last-only check: dropping the
        # required middle 'refreshed_post_ready_gates' step while keeping
        # correct first/last elements must still fail.
        failures = vc.validate_contract(load("reordered_notification_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("contradictory_plugin_prompt_default", checks)

    def test_exclusion_leaking_secret_shaped_value_is_rejected(self):
        failures = vc.validate_contract(load("exclusion_leak_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("exclusion_leaks_secret", checks)

    def test_bare_exclusion_category_names_are_not_flagged(self):
        # Regression guard for the opposite failure mode: legitimate bare
        # category names ("secrets", "credentials") must never themselves
        # trip the leak check.
        failures = vc.validate_contract(load_canonical())
        checks = [c for c, _ in failures]
        self.assertNotIn("exclusion_leaks_secret", checks)

    def test_false_done_is_terminal_integrity_lane_is_rejected(self):
        failures = vc.validate_contract(load("false_done_terminal_integrity_lane_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_review_independent_session_required_is_rejected(self):
        failures = vc.validate_contract(load("false_review_independent_session_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_qa_independent_session_required_is_rejected(self):
        # Symmetric guard: QA must be checked the same way Review is, not
        # just Review alone.
        failures = vc.validate_contract(load("false_qa_independent_session_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_weakened_coalescing_rule_is_rejected(self):
        # Regression guard: a coalescing_rule value like "all_messages"
        # would silently coalesce distinct Human/task/peer messages, which
        # coalescing_forbidden_for exists specifically to prevent.
        contract = load("weakened_coalescing_rule_contract.json")
        self.assertEqual(contract["queue_claim_identity"]["coalescing_rule"], "all_messages")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_routine_identity_component_is_rejected(self):
        # contract_version 1.1.0 clarification: canonical routine identity
        # is workspace_id + routine_type_or_name +
        # policy_or_prompt_version_generation + semantic_scope_generation,
        # independent of sender task/session/message ID. Dropping any one
        # component (here semantic_scope_generation) must fail closed.
        contract = load("missing_semantic_scope_generation_routine_identity_contract.json")
        self.assertNotIn(
            "semantic_scope_generation",
            contract["queue_claim_identity"]["routine_identity_components"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_routine_identity_excludes_sender_is_rejected(self):
        # Routine identity must not depend on which sender (task/session/
        # message ID) delivered the wake -- otherwise cross-sender
        # duplicates of the same generation could never coalesce.
        failures = vc.validate_contract(load("false_routine_identity_excludes_sender_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_cross_sender_coalescing_permitted_is_rejected(self):
        # Cross-sender coalescing must be explicitly permitted, or a
        # sender-independent identity is meaningless in practice.
        failures = vc.validate_contract(load("false_cross_sender_coalescing_permitted_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_weakened_coalescing_preserved_state_is_rejected(self):
        # Coalescing must preserve exactly one pending successor or
        # freshness bit, never "drop" the sole effective wake outright.
        contract = load("weakened_coalescing_preserved_state_contract.json")
        self.assertEqual(contract["queue_claim_identity"]["coalescing_preserved_state"], "dropped")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_leader_fencing_token_in_routine_wake_receipt_is_rejected(self):
        # A coalesced routine-wake receipt must name the leader fencing
        # token in force, or the receipt cannot be tied back to the single
        # serializing leader that authorized the coalescing decision.
        contract = load("missing_leader_fencing_token_routine_wake_receipt_contract.json")
        self.assertNotIn(
            "leader_fencing_token",
            contract["worker_helper_receipts"]["routine_wake_coalescing_receipt_fields"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_done_placement_alone_not_proof_is_rejected(self):
        failures = vc.validate_contract(load("false_done_placement_alone_not_proof_contract.json"))
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_entry_id_in_envelope_is_rejected(self):
        # Regression guard: dropping entry_id from
        # minimum_trusted_envelope silently reopens the
        # exact-entry-never-global-watermark hole even though audit_model
        # and identity_scope are both still correct.
        contract = load("missing_entry_id_envelope_contract.json")
        self.assertNotIn(
            "entry_id",
            contract["queue_claim_identity"]["minimum_trusted_envelope"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_human_input_in_coalescing_forbidden_is_rejected(self):
        # Regression guard: dropping human_input from
        # coalescing_forbidden_for would let a Human message silently
        # coalesce with a pending routine wake for the same target.
        contract = load("missing_human_input_coalescing_forbidden_contract.json")
        self.assertNotIn(
            "human_input",
            contract["queue_claim_identity"]["coalescing_forbidden_for"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_claim_or_lease_id_in_receipt_fields_is_rejected(self):
        # Regression guard: without claim_or_lease_id, a worker/helper
        # receipt cannot be correlated back to the exact claim it attests
        # to.
        contract = load("missing_claim_or_lease_id_receipt_contract.json")
        self.assertNotIn(
            "claim_or_lease_id",
            contract["worker_helper_receipts"]["receipt_required_fields"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_no_unique_local_work_in_done_proof_is_rejected(self):
        # Regression guard: without no_unique_local_or_untracked_work in
        # required_proof, a task could reach Done while local worktree
        # changes never made it into the accepted head.
        contract = load("missing_no_unique_local_done_proof_contract.json")
        self.assertNotIn(
            "no_unique_local_or_untracked_work",
            contract["done_integrity"]["required_proof"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_local_head_in_done_receipt_fields_is_rejected(self):
        # Regression guard: without local_head in receipt_fields, the Done
        # receipt has nothing to check the
        # no_unique_local_or_untracked_work proof against.
        contract = load("missing_local_head_done_receipt_contract.json")
        self.assertNotIn(
            "local_head", contract["done_integrity"]["receipt_fields"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_readiness_recheck_is_rejected(self):
        # Regression guard: readiness must recheck gates after a
        # draft-to-ready transition; flipping this to false while
        # exact_head_required stays true must still fail.
        contract = load("false_readiness_recheck_contract.json")
        self.assertFalse(
            contract["gates"]["readiness"]["recheck_after_draft_to_ready_transition"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_best_effort_unsupported_version_behavior_is_rejected(self):
        # Regression guard: compatibility.unsupported_version_behavior
        # must stay fail_closed, never silently downgrade to best_effort.
        contract = load("best_effort_unsupported_version_contract.json")
        self.assertEqual(
            contract["compatibility"]["unsupported_version_behavior"],
            "best_effort",
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_done_in_monitored_lanes_is_rejected(self):
        # Regression guard: done_is_terminal_integrity_lane: true is
        # meaningless if 'done' is not itself in monitored_lanes -- that
        # would claim Done is a terminal-integrity lane while never
        # actually watching it.
        contract = load("missing_done_monitored_lane_contract.json")
        self.assertNotIn(
            "done", contract["workspace_lane_ownership"]["monitored_lanes"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_required_fields_omitting_done_integrity_is_rejected(self):
        # Regression guard: the top-level done_integrity object can still be
        # present while the self-declared required_fields *list* quietly
        # drops the "done_integrity" entry. A consumer trusting only the
        # list would no longer treat it as mandatory, so this must fail
        # closed even though every other top-level field is unchanged.
        contract = load("required_fields_omits_done_integrity_contract.json")
        self.assertNotIn("done_integrity", contract["required_fields"])
        self.assertIn("done_integrity", contract)
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("required_fields", checks)

    def test_approval_principal_none_is_rejected(self):
        # Regression guard: authority_boundaries.approval_principal must
        # name an accountable principal ('coordinator'), never 'none' --
        # every other authority_boundaries invariant is untouched in this
        # fixture, so only this one floor must be the cause of failure.
        contract = load("false_approval_principal_none_contract.json")
        self.assertEqual(contract["authority_boundaries"]["approval_principal"], "none")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_workspace_id_in_envelope_is_rejected(self):
        # Regression guard: dropping workspace_id from
        # minimum_trusted_envelope while keeping entry_id, audit_model, and
        # identity_scope intact would silently reopen cross-workspace
        # authority at the queue-claim layer even though
        # cross_workspace_authority itself still reads false.
        contract = load("missing_workspace_id_envelope_contract.json")
        self.assertNotIn(
            "workspace_id",
            contract["queue_claim_identity"]["minimum_trusted_envelope"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_freshness_barrier_required_before_reporting_is_rejected(self):
        # Regression guard: flipping freshness_barrier_required_before_
        # reporting to false lets a worker/helper report against state it
        # read before a concurrent mutation, with nothing else in
        # worker_helper_receipts changed.
        contract = load("false_freshness_barrier_contract.json")
        self.assertFalse(
            contract["worker_helper_receipts"]["freshness_barrier_required_before_reporting"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_canonical_merged_identity_in_done_proof_is_rejected(self):
        # Regression guard: dropping
        # canonical_merged_identity_and_accepted_head from
        # done_integrity.required_proof removes the base head-identity
        # proof entry while no_unique_local_or_untracked_work and every
        # other required_proof entry stay intact.
        contract = load("missing_canonical_merged_identity_done_proof_contract.json")
        self.assertNotIn(
            "canonical_merged_identity_and_accepted_head",
            contract["done_integrity"]["required_proof"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_false_done_terminal_receipt_required_is_rejected(self):
        # Regression guard: gates.done_integrity.terminal_receipt_required
        # must stay true independently of exact_head_required -- flipping
        # only this one gate field to false, with exact_head_required
        # still true, must still fail.
        contract = load("false_done_terminal_receipt_required_contract.json")
        self.assertTrue(contract["gates"]["done_integrity"]["exact_head_required"])
        self.assertFalse(contract["gates"]["done_integrity"]["terminal_receipt_required"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_claim_collision_check_none_is_rejected(self):
        # Regression guard: queue_claim_identity.claim_collision_check must
        # be an actual deterministic check, not 'none' -- audit_model,
        # identity_scope, and coalescing_rule all stay correct in this
        # fixture, so only this one floor must be the cause of failure.
        contract = load("false_claim_collision_check_none_contract.json")
        self.assertEqual(contract["queue_claim_identity"]["claim_collision_check"], "none")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    # --- Blocker fixups: 15 previously-unguarded weaken/remove cases -----
    # Each fixture below carries an independently recomputed valid digest
    # (asserted via assertNotIn("stale_digest", ...)) so a weakened/removed
    # value cannot hide behind a stale-digest failure instead of tripping
    # its own dedicated invariant check.

    def test_weakened_authority_scope_is_rejected(self):
        contract = load("weakened_authority_scope_contract.json")
        self.assertEqual(contract["authority_boundaries"]["scope"], "cross_workspace_allowed")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_authority_scope_is_rejected(self):
        contract = load("missing_authority_scope_contract.json")
        self.assertNotIn("scope", contract["authority_boundaries"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_cross_workspace_authority_is_rejected(self):
        # Regression guard for the original masking bug: the old check used
        # `authority.get("cross_workspace_authority", False) is not False`,
        # so a *removed* key would read back as the same falsy default the
        # invariant requires and silently pass. Presence is now required
        # explicitly.
        contract = load("missing_cross_workspace_authority_contract.json")
        self.assertNotIn("cross_workspace_authority", contract["authority_boundaries"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_lane_unit_is_rejected(self):
        contract = load("weakened_lane_unit_contract.json")
        self.assertEqual(contract["workspace_lane_ownership"]["unit"], "task")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_lane_unit_is_rejected(self):
        contract = load("missing_lane_unit_contract.json")
        self.assertNotIn("unit", contract["workspace_lane_ownership"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_peer_model_is_rejected(self):
        contract = load("weakened_peer_model_contract.json")
        self.assertFalse(contract["workspace_lane_ownership"]["peer_model"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_peer_model_is_rejected(self):
        contract = load("missing_peer_model_contract.json")
        self.assertNotIn("peer_model", contract["workspace_lane_ownership"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_cross_workspace_standing_is_rejected(self):
        contract = load("weakened_cross_workspace_standing_contract.json")
        self.assertTrue(contract["workspace_lane_ownership"]["cross_workspace_standing"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_cross_workspace_standing_is_rejected(self):
        contract = load("missing_cross_workspace_standing_contract.json")
        self.assertNotIn("cross_workspace_standing", contract["workspace_lane_ownership"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_auto_start_lane_move_is_rejected(self):
        contract = load("weakened_auto_start_lane_move_contract.json")
        self.assertFalse(
            contract["workspace_lane_ownership"]["auto_start_lane_move_requires_settled_lifecycle"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_auto_start_lane_move_is_rejected(self):
        contract = load("missing_auto_start_lane_move_contract.json")
        self.assertNotIn(
            "auto_start_lane_move_requires_settled_lifecycle",
            contract["workspace_lane_ownership"],
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_mutation_serialized_by_is_rejected(self):
        contract = load("weakened_mutation_serialized_by_contract.json")
        self.assertEqual(contract["worker_helper_receipts"]["mutation_serialized_by"], "none")
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_mutation_serialized_by_is_rejected(self):
        contract = load("missing_mutation_serialized_by_contract.json")
        self.assertNotIn("mutation_serialized_by", contract["worker_helper_receipts"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_weakened_receipt_is_not_proof_of_is_rejected(self):
        contract = load("weakened_receipt_is_not_proof_of_contract.json")
        self.assertNotIn(
            "capacity_released", contract["worker_helper_receipts"]["receipt_is_not_proof_of"]
        )
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)

    def test_missing_receipt_is_not_proof_of_is_rejected(self):
        contract = load("missing_receipt_is_not_proof_of_contract.json")
        self.assertNotIn("receipt_is_not_proof_of", contract["worker_helper_receipts"])
        failures = vc.validate_contract(contract)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)
        self.assertNotIn("stale_digest", checks)


class TestPluginSnapshot(unittest.TestCase):
    def test_valid_plugin_snapshot_passes(self):
        contract = load_canonical()
        snapshot = load("valid_plugin_snapshot.json")
        failures = vc.validate_plugin_snapshot(contract, snapshot)
        self.assertEqual(failures, [])

    def test_contradictory_plugin_snapshot_is_rejected(self):
        contract = load_canonical()
        snapshot = load("contradictory_plugin_snapshot.json")
        failures = vc.validate_plugin_snapshot(contract, snapshot)
        checks = [c for c, _ in failures]
        self.assertIn("contradictory_plugin_prompt_default", checks)

    def test_empty_defaults_plugin_snapshot_is_rejected(self):
        # Regression guard: an empty `defaults` object trivially satisfies
        # every "if key present and contradicts" check, and previously
        # passed. It must fail closed as missing mandatory invariants.
        contract = load_canonical()
        snapshot = load("empty_defaults_plugin_snapshot.json")
        failures = vc.validate_plugin_snapshot(contract, snapshot)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)

    def test_missing_defaults_key_plugin_snapshot_is_rejected(self):
        # Regression guard: a snapshot that omits `defaults` entirely must
        # fail the same way an empty `defaults` object does.
        contract = load_canonical()
        snapshot = load("missing_defaults_plugin_snapshot.json")
        failures = vc.validate_plugin_snapshot(contract, snapshot)
        checks = [c for c, _ in failures]
        self.assertIn("missing_required_invariant", checks)


class TestWorkspaceOverlay(unittest.TestCase):
    def test_narrowing_overlay_passes(self):
        contract = load_canonical()
        overlay = load("narrowing_overlay.json")
        failures = vc.validate_overlay(contract, overlay)
        self.assertEqual(failures, [])

    def test_widening_overlay_is_rejected(self):
        contract = load_canonical()
        overlay = load("widening_overlay.json")
        failures = vc.validate_overlay(contract, overlay)
        checks = [c for c, _ in failures]
        self.assertIn("overlay_widens_authority", checks)
        # Both the cross-workspace flip and the added decidable-example
        # should each independently trip the same check category.
        self.assertGreaterEqual(checks.count("overlay_widens_authority"), 2)


class TestCli(unittest.TestCase):
    """Exercises the actual CLI entry point (subprocess) for a couple of
    cases, so the argparse wiring itself is covered, not just the importable
    functions."""

    def _run(self, *args):
        return subprocess.run(
            [sys.executable, os.path.join(HERE, "validate_contract.py"), *args],
            capture_output=True,
            text=True,
        )

    def test_cli_contract_valid_exit_zero(self):
        result = self._run("contract", "--contract", CANONICAL)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("OK", result.stdout)

    def test_cli_contract_invalid_exit_one(self):
        result = self._run(
            "contract", "--contract", os.path.join(FIXTURES, "stale_digest_contract.json")
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("stale_digest", result.stderr)

    def test_cli_contract_exclusion_leak_exit_one(self):
        result = self._run(
            "contract", "--contract", os.path.join(FIXTURES, "exclusion_leak_contract.json")
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("exclusion_leaks_secret", result.stderr)

    def test_cli_plugin_snapshot_contradictory_exit_one(self):
        result = self._run(
            "plugin-snapshot",
            "--contract", CANONICAL,
            "--snapshot", os.path.join(FIXTURES, "contradictory_plugin_snapshot.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("contradictory_plugin_prompt_default", result.stderr)

    def test_cli_overlay_widening_exit_one(self):
        result = self._run(
            "overlay",
            "--contract", CANONICAL,
            "--overlay", os.path.join(FIXTURES, "widening_overlay.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("overlay_widens_authority", result.stderr)

    def test_cli_future_version_contract_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "future_version_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("stale_validator_or_future_contract", result.stderr)

    def test_cli_empty_defaults_plugin_snapshot_exit_one(self):
        result = self._run(
            "plugin-snapshot",
            "--contract", CANONICAL,
            "--snapshot", os.path.join(FIXTURES, "empty_defaults_plugin_snapshot.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_done_terminal_integrity_lane_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_done_terminal_integrity_lane_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_weakened_coalescing_rule_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "weakened_coalescing_rule_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_required_fields_omits_done_integrity_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "required_fields_omits_done_integrity_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("required_fields", result.stderr)

    def test_cli_missing_entry_id_envelope_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "missing_entry_id_envelope_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_human_input_coalescing_forbidden_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_human_input_coalescing_forbidden_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_claim_or_lease_id_receipt_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_claim_or_lease_id_receipt_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_no_unique_local_done_proof_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_no_unique_local_done_proof_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_local_head_done_receipt_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_local_head_done_receipt_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_readiness_recheck_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "false_readiness_recheck_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_best_effort_unsupported_version_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "best_effort_unsupported_version_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_done_monitored_lane_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "missing_done_monitored_lane_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_approval_principal_none_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_approval_principal_none_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_workspace_id_envelope_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_workspace_id_envelope_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_freshness_barrier_exit_one(self):
        result = self._run(
            "contract",
            "--contract", os.path.join(FIXTURES, "false_freshness_barrier_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_canonical_merged_identity_done_proof_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "missing_canonical_merged_identity_done_proof_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_done_terminal_receipt_required_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_done_terminal_receipt_required_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_claim_collision_check_none_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_claim_collision_check_none_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_routine_identity_component_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(
                FIXTURES,
                "missing_semantic_scope_generation_routine_identity_contract.json",
            ),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_routine_identity_excludes_sender_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_routine_identity_excludes_sender_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_false_cross_sender_coalescing_permitted_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "false_cross_sender_coalescing_permitted_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_weakened_coalescing_preserved_state_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(FIXTURES, "weakened_coalescing_preserved_state_contract.json"),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)

    def test_cli_missing_leader_fencing_token_routine_wake_receipt_exit_one(self):
        result = self._run(
            "contract",
            "--contract",
            os.path.join(
                FIXTURES,
                "missing_leader_fencing_token_routine_wake_receipt_contract.json",
            ),
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("missing_required_invariant", result.stderr)


class TestAdversarialSweep(unittest.TestCase):
    """Wires the standalone adversarial_sweep.py into the pytest/unittest
    suite: every mandatory invariant must reject every weaken/remove
    mutation defined for it, each judged against an independently
    recomputed valid digest (see adversarial_sweep.py's module docstring).
    """

    def test_all_78_mutations_are_rejected(self):
        results, all_passed = adversarial_sweep.run_sweep()
        total = len(results)
        self.assertEqual(total, 78, f"expected exactly 78 mutations in the sweep, got {total}")
        failed = [(name, failures) for name, failures, ok in results if not ok]
        self.assertTrue(
            all_passed,
            f"{len(failed)}/{total} mutations were NOT correctly rejected: {failed}",
        )

    def test_sweep_never_masks_behind_stale_digest(self):
        # Regression guard for the exact defect class this task fixes: a
        # mutation must never be "caught" only by an incidental
        # stale_digest failure -- each fixture/mutation carries a
        # correctly recomputed digest, so its own targeted invariant check
        # must be the one that fires.
        results, _ = adversarial_sweep.run_sweep()
        for name, failures, _ in results:
            if name == "digest_algorithm.unsupported":
                continue  # digest cannot be recomputed under an unsupported algorithm by design
            checks = [c for c, _ in failures]
            self.assertNotIn("stale_digest", checks, f"{name} was masked by stale_digest: {failures}")


if __name__ == "__main__":
    unittest.main()
