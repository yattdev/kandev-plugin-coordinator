#!/usr/bin/env python3
"""Standalone validator for the Coordinator policy contract.

Purpose
-------
This script has NO runtime dependency on the Coordinator knowledge-repository
checkout: it only needs the contract JSON (and, for the optional checks, a
plugin-snapshot or workspace-overlay JSON) passed in via explicit paths. The
Coordinator plugin repository may vendor this single file plus its vendored
copy of ``coordinator-policy-contract.json`` and run it in CI without cloning
this repository.

Stdlib only. No third-party dependencies. Python >= 3.8.

Usage
-----
    validate_contract.py contract --contract path/to/contract.json
    validate_contract.py plugin-snapshot --contract path/to/contract.json \
        --snapshot path/to/plugin-snapshot.json
    validate_contract.py overlay --contract path/to/contract.json \
        --overlay path/to/workspace-overlay.json

Exit code 0 on success, 1 on any validation failure. Failures are printed one
per line to stderr, prefixed with the failing check name, so CI logs stay
legible (see FILESYSTEM_DOCKER_CONTRACT.md's "denials fail closed and must be
legible" convention, applied here to contract validation).
"""

import argparse
import copy
import hashlib
import json
import sys

# Bump this only when this validator itself changes what it can recognize.
# The contract's own compatibility.max_known_contract_version is the field
# the validator checks the contract against; VALIDATOR_SCHEMA_VERSION is
# reported in --version output only.
VALIDATOR_SCHEMA_VERSION = "1.1.1"

# The highest contract_version this validator BUILD understands, hardcoded in
# code rather than read from the contract document. A contract's own
# compatibility.max_known_contract_version is self-declared: a future or
# malicious contract could set contract_version, min_supported_contract_version,
# and max_known_contract_version all to e.g. "2.0.0" in the same document, and
# the same-document check in validate_contract() (which only compares the
# contract against itself) would find it internally consistent and pass it.
# This constant is the independent, non-forgeable ceiling: it is what THIS
# validator source file was actually written to understand, so it cannot be
# smuggled past by anything inside the JSON body. Bump it only when this
# validator is upgraded to actually understand a newer contract_version.
VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION = "1.1.0"

# The exact, ordered readiness/notification sequence required by the
# contract (see CONTRACT_MAPPING.md: "Order is fixed; reordering is
# breaking"). Checked as a full sequence, not just first/last elements, so a
# contract that drops or reorders the middle `refreshed_post_ready_gates`
# step (the post-ready-gate refresh between provider-ready confirmation and
# reviewer notification) is rejected.
REQUIRED_READINESS_NOTIFICATION_ORDER = [
    "provider_confirmed_ready_nondraft",
    "refreshed_post_ready_gates",
    "reviewer_notification",
]

# Mandatory plugin-snapshot `defaults` keys. A snapshot with a missing or
# empty `defaults` object -- or missing any one of these keys -- declares no
# verifiable invariants at all and must fail closed rather than silently
# pass (an empty object trivially satisfies "no field contradicts the
# contract").
REQUIRED_PLUGIN_SNAPSHOT_DEFAULT_KEYS = [
    "human_reserved_classes",
    "exact_head_gates",
    "workers_never_mutate",
    "cross_workspace_authority",
]

DIGEST_EXCLUDED_FIELDS = ("digest",)

REQUIRED_TOP_LEVEL_FIELDS = [
    "contract_id",
    "contract_version",
    "source_charter_effective_version",
    "source_decision_anchor",
    "generated_at",
    "digest_algorithm",
    "digest",
    "compatibility",
    "required_fields",
    "authority_boundaries",
    "workspace_lane_ownership",
    "queue_claim_identity",
    "worker_helper_receipts",
    "gates",
    "readiness_notification_order",
    "escalation_classes",
    "done_integrity",
    "exclusions",
]

REQUIRED_GATE_KEYS = ["review", "qa", "readiness", "done_integrity"]

# Review and QA are symmetric: both must run in an independent session, never
# the authoring/implementing session self-attesting its own work (see
# CONTRACT_MAPPING.md: "exact-head Review/QA" gates). Readiness and
# done_integrity have no independent-session requirement of their own.
REQUIRED_INDEPENDENT_SESSION_GATES = ["review", "qa"]

# The only coalescing rule this contract permits: identity-equivalent
# *pending routine wakes for the same target* may collapse to one. Anything
# broader (e.g. a value like "all_messages") would silently coalesce
# distinct Human/task/peer messages, which `coalescing_forbidden_for` exists
# specifically to prevent.
REQUIRED_QUEUE_COALESCING_RULE = "only_identity_equivalent_pending_routine_wakes_for_same_target"

# Canonical Host routine identity (contract_version 1.1.0 clarification): a
# routine wake's identity is exactly this tuple, independent of which
# sender (task/session/message ID) delivered it. Two wakes with an
# identical value across all four of these components are the *same*
# pending generation and may coalesce across senders; dropping any one
# component would let sender identity leak back into the coalescing key,
# silently narrowing cross-sender coalescing (duplicate full-board-scan
# risk) or, if implemented backwards, letting non-identical generations
# collapse together.
REQUIRED_ROUTINE_IDENTITY_COMPONENTS = {
    "workspace_id",
    "routine_type_or_name",
    "policy_or_prompt_version_generation",
    "semantic_scope_generation",
}

# A coalesced routine-wake receipt must be able to prove, after the fact,
# exactly what was absorbed into the one surviving canonical entry and
# under which leader authority -- these four are the safety-critical
# subset (full field list lives in the contract's
# `routine_wake_coalescing_receipt_fields`, see CONTRACT_MAPPING.md).
REQUIRED_ROUTINE_WAKE_COALESCING_RECEIPT_FIELDS = {
    "canonical_entry_id",
    "absorbed_source_entry_ids",
    "leader_fencing_token",
    "dirty_generation",
}

REQUIRED_HUMAN_RESERVED_CLASSES = {
    "destructive_or_irreversible",
    "security_or_trust_boundary",
}

# A helper receipt is evidence for the leader to act on, never itself a
# disposition of the queue entry (see PROMPT.md: "A helper receipt never
# proves that its source queue row was claimed, acknowledged, removed, or
# that capacity was released"). Dropping any one of these four from
# `receipt_is_not_proof_of` would let a receipt be silently treated as
# sufficient for that specific disposition claim.
REQUIRED_RECEIPT_IS_NOT_PROOF_OF = {
    "queue_row_claimed",
    "queue_row_acknowledged",
    "queue_row_removed",
    "capacity_released",
}

FORBIDDEN_EXCLUSION_LEAKS = ["secret", "credential", "password", "token"]

# Bare category names are the expected, safe shape of an `exclusions` entry
# (see CONTRACT_MAPPING.md: "Category names only ... never example values").
# Anything else that matches a FORBIDDEN_EXCLUSION_LEAKS term is treated as
# an embedded secret-shaped value, not a category reference.
ALLOWED_BARE_EXCLUSION_CATEGORIES = {"secrets", "credentials"}


class ValidationError(Exception):
    """Raised with a list of (check_name, message) failures."""

    def __init__(self, failures):
        self.failures = failures
        super().__init__("; ".join(f"{c}: {m}" for c, m in failures))


def _load_json(path):
    with open(path, "r", encoding="utf-8") as fh:
        return json.load(fh)


def canonical_bytes(contract, exclude=DIGEST_EXCLUDED_FIELDS):
    """Deterministic canonical serialization used for the digest.

    Rules (documented in CONTRACT_MAPPING.md so plugin CI can reproduce this
    independently without importing this script):
      1. Start from the top-level object only (this contract format is not
         recursively digested field-by-field; the whole document minus the
         excluded fields is canonicalized).
      2. Drop the excluded top-level fields (currently just ``digest`` -- the
         digest cannot include itself).
      3. Serialize with ``sort_keys=True``, no extra whitespace, UTF-8.
    """
    working = copy.deepcopy(contract)
    for field in exclude:
        working.pop(field, None)
    return json.dumps(working, sort_keys=True, separators=(",", ":")).encode("utf-8")


def compute_digest(contract, algorithm="sha256"):
    if algorithm != "sha256":
        raise ValidationError([("digest_algorithm", f"unsupported algorithm '{algorithm}'")])
    return hashlib.sha256(canonical_bytes(contract)).hexdigest()


def validate_contract(contract):
    """Validate a contract document. Returns list of (check, message) failures."""
    failures = []

    # 1. Required top-level fields present (schema/unknown-required-field check).
    for field in REQUIRED_TOP_LEVEL_FIELDS:
        if field not in contract:
            failures.append(("required_fields", f"missing required top-level field '{field}'"))

    # An unrecognized *additional* required_fields entry this validator does
    # not know about means the document declares invariants this validator
    # cannot check -- fail closed rather than silently pass a newer/foreign
    # schema.
    declared_required = contract.get("required_fields", [])
    if isinstance(declared_required, list):
        unknown = [f for f in declared_required if f not in REQUIRED_TOP_LEVEL_FIELDS]
        if unknown:
            failures.append((
                "unknown_required_field",
                f"contract declares required field(s) unknown to this validator: {unknown}",
            ))
        # The self-declared required_fields list must also *enumerate* every
        # field this validator treats as required. A contract that keeps the
        # actual top-level `done_integrity` object but quietly drops the
        # string "done_integrity" from its own required_fields list has
        # weakened what it claims to guarantee (a consumer trusting only the
        # self-declared list would no longer treat done_integrity as
        # mandatory) -- fail closed rather than only checking presence of
        # the underlying object.
        omitted = [f for f in REQUIRED_TOP_LEVEL_FIELDS if f not in declared_required]
        if omitted:
            failures.append((
                "required_fields",
                "contract's own required_fields list omits known required "
                f"field(s): {omitted}",
            ))
    else:
        failures.append(("required_fields", "'required_fields' must be a list"))

    if failures and not all(f in contract for f in ("digest", "digest_algorithm", "compatibility")):
        # Cannot safely continue version/digest checks without these fields.
        return failures

    # 2. Version support check.
    compat = contract.get("compatibility", {})
    max_known = compat.get("max_known_contract_version")
    min_supported = compat.get("min_supported_contract_version")
    version = contract.get("contract_version")
    if max_known and version and _version_tuple(version) > _version_tuple(max_known):
        failures.append((
            "stale_validator_or_future_contract",
            f"contract_version {version} exceeds this contract's own "
            f"declared max_known_contract_version {max_known}; treat as "
            "unsupported per compatibility.unsupported_version_behavior",
        ))
    if min_supported and version and _version_tuple(version) < _version_tuple(min_supported):
        failures.append((
            "stale_version",
            f"contract_version {version} is older than "
            f"min_supported_contract_version {min_supported}",
        ))
    # 2c. Unsupported-version behavior must be fail-closed, never a silent
    # "best effort" downgrade (see CONTRACT_MAPPING.md §4 point 4: a plugin
    # build with a mismatched vendored version "must fail closed ... never
    # silently downgrade to 'best effort'"). Weakening this field alone,
    # with every other invariant untouched, would let an out-of-range
    # contract/plugin pairing silently keep running instead of failing.
    if compat.get("unsupported_version_behavior") != "fail_closed":
        failures.append((
            "missing_required_invariant",
            "compatibility.unsupported_version_behavior must be "
            f"'fail_closed', got {compat.get('unsupported_version_behavior')!r}",
        ))
    # 2b. Independent, hardcoded ceiling (cannot be forged by the document
    # itself -- see VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION). A contract
    # that self-declares contract_version 2.0.0 alongside a matching
    # compatibility.max_known_contract_version of 2.0.0 passes check #2
    # above (it is internally consistent with itself) but must still be
    # rejected here, because this validator build was never actually written
    # to understand a 2.0.0 schema.
    if version and _version_tuple(version) > _version_tuple(VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION):
        failures.append((
            "stale_validator_or_future_contract",
            f"contract_version {version} exceeds "
            f"{VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION}, the maximum "
            "contract_version this validator build understands (hardcoded "
            "in validate_contract.py, independent of the contract's own "
            "self-declared compatibility fields); upgrade the validator "
            "before trusting a newer contract",
        ))

    # 3. Digest check.
    algorithm = contract.get("digest_algorithm", "sha256")
    declared_digest = contract.get("digest")
    try:
        recomputed = compute_digest(contract, algorithm)
    except ValidationError as exc:
        failures.extend(exc.failures)
        recomputed = None
    if recomputed is not None and declared_digest != recomputed:
        failures.append((
            "stale_digest",
            f"declared digest {declared_digest!r} does not match recomputed "
            f"digest {recomputed!r} over the canonical contract body",
        ))

    # 4. Required invariants: human-reserved escalation classes must be present
    #    and must not be narrowed (that would silently widen Coordinator
    #    authority into a Human-reserved class).
    authority = contract.get("authority_boundaries", {})
    reserved = set(authority.get("human_reserved_classes", []))
    missing_invariant = REQUIRED_HUMAN_RESERVED_CLASSES - reserved
    if missing_invariant:
        failures.append((
            "missing_required_invariant",
            f"authority_boundaries.human_reserved_classes is missing required "
            f"class(es): {sorted(missing_invariant)}",
        ))
    # 4a. cross_workspace_authority must be explicitly present AND false --
    # checking only "not True" (or using a `False` default on a missing
    # key) would let a contract that silently *removes* this field pass,
    # since a missing key would then read back as the same falsy default
    # the invariant requires. The key must be present in the document and
    # its value must be exactly `false`.
    if "cross_workspace_authority" not in authority or authority.get("cross_workspace_authority") is not False:
        failures.append((
            "missing_required_invariant",
            "authority_boundaries.cross_workspace_authority must be present "
            "and false",
        ))
    # 4a2. scope must stay 'same_workspace_only' -- weakening or removing it
    # would let a contract silently drop the same-workspace authority
    # boundary while approval_principal/cross_workspace_authority still
    # individually check out (see CONTRACT_MAPPING.md: authority_boundaries
    # is one mapped group but scope is its own independently-enforced
    # floor).
    if authority.get("scope") != "same_workspace_only":
        failures.append((
            "missing_required_invariant",
            "authority_boundaries.scope must be 'same_workspace_only', got "
            f"{authority.get('scope')!r}",
        ))
    # 4b. approval_principal must name an actual accountable principal
    # ('coordinator'), never 'none' -- a contract that keeps every other
    # authority-boundary invariant intact but sets approval_principal to
    # 'none' silently removes who is accountable for the coordinator-
    # decidable examples this same object lists, which is a floor-level
    # weakening even though nothing else in authority_boundaries changed.
    if authority.get("approval_principal") != "coordinator":
        failures.append((
            "missing_required_invariant",
            "authority_boundaries.approval_principal must be 'coordinator', "
            f"got {authority.get('approval_principal')!r}",
        ))

    # 5. Gates must all require exact-head evidence, and Review/QA must both
    #    require an independent session (self-attestation is not a gate).
    gates = contract.get("gates", {})
    for gate_name in REQUIRED_GATE_KEYS:
        gate = gates.get(gate_name)
        if not isinstance(gate, dict):
            failures.append(("missing_required_invariant", f"gates.{gate_name} is missing"))
            continue
        if gate.get("exact_head_required") is not True:
            failures.append((
                "missing_required_invariant",
                f"gates.{gate_name}.exact_head_required must be true",
            ))
        if gate_name in REQUIRED_INDEPENDENT_SESSION_GATES and gate.get("independent_session_required") is not True:
            failures.append((
                "missing_required_invariant",
                f"gates.{gate_name}.independent_session_required must be true",
            ))
        # Readiness must recheck gates after a draft-to-ready transition --
        # without this, a task that was gated while still draft could reach
        # Ready without its post-transition state ever being reconfirmed
        # (see PROMPT.md's strict readiness/notification ordering).
        if gate_name == "readiness" and gate.get("recheck_after_draft_to_ready_transition") is not True:
            failures.append((
                "missing_required_invariant",
                "gates.readiness.recheck_after_draft_to_ready_transition "
                "must be true",
            ))
        # gates.done_integrity.terminal_receipt_required must stay true --
        # without it, exact_head_required alone would let Done be reached
        # on a matching head with no terminal receipt at all, reopening the
        # Done terminal-integrity gate this floor exists to close (see
        # done_integrity.merged_pr_or_done_placement_alone_is_not_proof,
        # checked separately below, which this floor backs at the gate
        # level).
        if gate_name == "done_integrity" and gate.get("terminal_receipt_required") is not True:
            failures.append((
                "missing_required_invariant",
                "gates.done_integrity.terminal_receipt_required must be true",
            ))

    # 5b. Workspace/lane ownership: Done must stay a terminal-integrity lane,
    # never a silently-ignored archive (see CONTRACT_MAPPING.md).
    lane_ownership = contract.get("workspace_lane_ownership", {})
    if lane_ownership.get("done_is_terminal_integrity_lane") is not True:
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.done_is_terminal_integrity_lane must be true",
        ))
    # 5c. Done must actually be one of the monitored lanes -- otherwise
    # "done_is_terminal_integrity_lane: true" is a claim about a lane that
    # is never watched, which is equivalent in practice to not monitoring
    # Done at all (see PROMPT.md: "Monitor every task in ... AND Done").
    monitored_lanes = lane_ownership.get("monitored_lanes", [])
    if "done" not in monitored_lanes:
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.monitored_lanes must include 'done'",
        ))
    # 5d. unit must stay 'workspace' -- this is the granularity every other
    # workspace_lane_ownership/authority_boundaries field assumes (peer
    # workspaces, not e.g. per-task or per-repository lane ownership); see
    # docs/FILESYSTEM_DOCKER_CONTRACT.md §3.
    if lane_ownership.get("unit") != "workspace":
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.unit must be 'workspace', got "
            f"{lane_ownership.get('unit')!r}",
        ))
    # 5e. peer_model must be explicitly present and true -- workspaces have
    # no standing over each other (see docs/FILESYSTEM_DOCKER_CONTRACT.md
    # §3: "peers have no standing over each other"). As with
    # cross_workspace_authority above, checking only "not True" against a
    # falsy default would let a contract that removes this field pass, so
    # presence is checked explicitly.
    if "peer_model" not in lane_ownership or lane_ownership.get("peer_model") is not True:
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.peer_model must be present and true",
        ))
    # 5f. cross_workspace_standing must be explicitly present and false --
    # the workspace-lane-ownership-level twin of
    # authority_boundaries.cross_workspace_authority; dropping it would
    # silently reopen cross-workspace standing at the lane-ownership layer
    # even though the authority-boundaries floor above still holds.
    if "cross_workspace_standing" not in lane_ownership or lane_ownership.get("cross_workspace_standing") is not False:
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.cross_workspace_standing must be "
            "present and false",
        ))
    # 5g. auto_start_lane_move_requires_settled_lifecycle must stay true --
    # without it, an auto-start lane move could fire against a task whose
    # lifecycle has not yet settled, racing the same in-flight
    # gate/receipt state the other workspace_lane_ownership/gates floors
    # exist to protect.
    if lane_ownership.get("auto_start_lane_move_requires_settled_lifecycle") is not True:
        failures.append((
            "missing_required_invariant",
            "workspace_lane_ownership.auto_start_lane_move_requires_settled_lifecycle "
            "must be true",
        ))

    # 6. Queue claim identity must be per-entry, never a global watermark,
    # and coalescing must be limited to identity-equivalent pending routine
    # wakes for the same target -- never a broader rule (e.g.
    # "all_messages") that would coalesce distinct Human/task/peer messages.
    queue = contract.get("queue_claim_identity", {})
    if queue.get("audit_model") != "exact_entry_never_global_watermark":
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.audit_model must be "
            "'exact_entry_never_global_watermark'",
        ))
    if queue.get("identity_scope") != "per_entry":
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.identity_scope must be 'per_entry'",
        ))
    if queue.get("coalescing_rule") != REQUIRED_QUEUE_COALESCING_RULE:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.coalescing_rule must be "
            f"{REQUIRED_QUEUE_COALESCING_RULE!r}, got "
            f"{queue.get('coalescing_rule')!r}",
        ))
    # 6b. minimum_trusted_envelope must name entry_id -- without a
    # per-entry identifier in the trusted envelope, a claim cannot actually
    # be tied to one exact entry, which silently reopens the
    # exact-entry-never-global-watermark hole this floor exists to close.
    envelope = queue.get("minimum_trusted_envelope", [])
    if "entry_id" not in envelope:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.minimum_trusted_envelope must include "
            "'entry_id'",
        ))
    # 6b2. minimum_trusted_envelope must also name workspace_id -- without
    # it, a claim's trusted envelope cannot be scoped to one workspace,
    # which silently reopens the cross-workspace-authority hole
    # authority_boundaries.cross_workspace_authority: false exists to
    # close at the queue-claim layer (entry_id alone ties a claim to one
    # entry, but not to one workspace's entries only).
    if "workspace_id" not in envelope:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.minimum_trusted_envelope must include "
            "'workspace_id'",
        ))
    # 6c. coalescing_forbidden_for must keep human_input -- dropping it
    # would let a Human message silently coalesce with a pending routine
    # wake for the same target, which coalescing_forbidden_for exists
    # specifically to prevent.
    coalescing_forbidden = set(queue.get("coalescing_forbidden_for", []))
    if "human_input" not in coalescing_forbidden:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.coalescing_forbidden_for must include "
            "'human_input'",
        ))
    # 6d. claim_collision_check must be an actual deterministic check, not
    # 'none' -- without it, two claims against the same resource key could
    # both be granted concurrently, which is exactly the "zero claim
    # overlap" invariant PLUGIN_SCALE_RFC.md's harness exists to verify.
    if queue.get("claim_collision_check") != "deterministic_claim_set":
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.claim_collision_check must be "
            f"'deterministic_claim_set', got "
            f"{queue.get('claim_collision_check')!r}",
        ))
    # 6e. Canonical routine identity must name all four components
    # (workspace_id, routine_type_or_name, policy_or_prompt_version_generation,
    # semantic_scope_generation) -- this is what makes routine-wake identity
    # independent of sender task/session/message ID. Dropping any one
    # component reopens a way for sender identity (or a stale policy/scope
    # generation) to leak into the coalescing key.
    routine_identity = set(queue.get("routine_identity_components", []))
    missing_identity_components = REQUIRED_ROUTINE_IDENTITY_COMPONENTS - routine_identity
    if missing_identity_components:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.routine_identity_components must include "
            f"{sorted(REQUIRED_ROUTINE_IDENTITY_COMPONENTS)}; missing "
            f"{sorted(missing_identity_components)}",
        ))
    # 6f. Routine identity must explicitly exclude sender IDs -- a routine
    # wake's identity/generation must not depend on which task/session/
    # message delivered it, or cross-sender duplicates of the same
    # generation would never coalesce (reopening the duplicate-full-board-
    # scan defect this clarification exists to close).
    if queue.get("routine_identity_excludes_sender_ids") is not True:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.routine_identity_excludes_sender_ids must "
            "be true",
        ))
    # 6g. Cross-sender coalescing must be explicitly permitted -- without
    # this, an otherwise-identical routine generation delivered by two
    # different senders would never coalesce, defeating the point of a
    # sender-independent identity.
    if queue.get("cross_sender_coalescing_permitted") is not True:
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.cross_sender_coalescing_permitted must "
            "be true",
        ))
    # 6h. Coalescing must preserve exactly one successor state, never drop
    # the sole effective wake outright. "dropped"/"none" would mean a
    # cross-sender duplicate could vanish with no pending/freshness trace
    # at all, which is a stronger defect than the identity being wrong.
    if queue.get("coalescing_preserved_state") != "single_pending_successor_or_freshness_bit":
        failures.append((
            "missing_required_invariant",
            "queue_claim_identity.coalescing_preserved_state must be "
            "'single_pending_successor_or_freshness_bit', got "
            f"{queue.get('coalescing_preserved_state')!r}",
        ))

    # 7. Worker/helper receipts: workers must never mutate.
    receipts = contract.get("worker_helper_receipts", {})
    if receipts.get("workers_never_mutate") is not True:
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.workers_never_mutate must be true",
        ))
    # 7a2. mutation_serialized_by must stay 'single_writer_lane' -- this is
    # what actually serializes the mutations that workers_never_mutate
    # forbids workers themselves from making; weakening or removing it
    # would leave "workers never mutate" true in isolation while no single
    # lane is named as the serializing mutation path (see §2.5 of
    # PLUGIN_SCALE_RFC.md, "single-writer mutation lane").
    if receipts.get("mutation_serialized_by") != "single_writer_lane":
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.mutation_serialized_by must be "
            f"'single_writer_lane', got {receipts.get('mutation_serialized_by')!r}",
        ))
    # 7b2. receipt_is_not_proof_of must keep all four disposition classes a
    # receipt must never be mistaken for proof of -- dropping any one (e.g.
    # narrowing to just queue_row_claimed) would let a receipt be silently
    # treated as sufficient for the dropped disposition (see PROMPT.md: "A
    # helper receipt never proves that its source queue row was claimed,
    # acknowledged, removed, or that capacity was released").
    receipt_is_not_proof_of = set(receipts.get("receipt_is_not_proof_of", []))
    missing_receipt_is_not_proof_of = REQUIRED_RECEIPT_IS_NOT_PROOF_OF - receipt_is_not_proof_of
    if missing_receipt_is_not_proof_of:
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.receipt_is_not_proof_of must include "
            f"{sorted(REQUIRED_RECEIPT_IS_NOT_PROOF_OF)}; missing "
            f"{sorted(missing_receipt_is_not_proof_of)}",
        ))
    # 7c. Receipts must be tied to an actual claim/lease identity --
    # without claim_or_lease_id, a receipt cannot be correlated back to the
    # exact claim it attests to (see receipt_is_not_proof_of: a receipt
    # already isn't proof of claim/ack/removal; omitting the field that
    # would let it be checked against the claim makes that gap worse).
    receipt_fields = receipts.get("receipt_required_fields", [])
    if "claim_or_lease_id" not in receipt_fields:
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.receipt_required_fields must include "
            "'claim_or_lease_id'",
        ))
    # 7c2. A worker/helper report must be barred from reporting completion
    # before a freshness barrier confirms its read was current -- without
    # this, a worker could report against stale state it read before a
    # concurrent mutation, and that stale report would be indistinguishable
    # from a fresh one (receipt_is_not_proof_of already says a receipt
    # isn't proof of claim/ack/removal; a false freshness barrier removes
    # the one check that would catch a report based on stale state).
    if receipts.get("freshness_barrier_required_before_reporting") is not True:
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.freshness_barrier_required_before_reporting "
            "must be true",
        ))
    # 7d. A coalesced routine-wake receipt must name the canonical surviving
    # entry, every absorbed source entry it stands in for, the leader
    # fencing token in force, and the dirty generation it satisfies --
    # without these, "one effective wake survived" cannot be distinguished
    # from "N entries were silently merged with no audit trail" (see
    # REQUIRED_ROUTINE_WAKE_COALESCING_RECEIPT_FIELDS).
    coalescing_receipt_fields = set(receipts.get("routine_wake_coalescing_receipt_fields", []))
    missing_coalescing_receipt_fields = (
        REQUIRED_ROUTINE_WAKE_COALESCING_RECEIPT_FIELDS - coalescing_receipt_fields
    )
    if missing_coalescing_receipt_fields:
        failures.append((
            "missing_required_invariant",
            "worker_helper_receipts.routine_wake_coalescing_receipt_fields "
            f"must include {sorted(REQUIRED_ROUTINE_WAKE_COALESCING_RECEIPT_FIELDS)}; "
            f"missing {sorted(missing_coalescing_receipt_fields)}",
        ))

    # 7b. Done integrity: a merged PR or Done-column placement alone must
    # never be treated as proof (see PROMPT.md's Done terminal-integrity
    # gate). Weakening this to false would let a stale/incomplete task be
    # accepted as Done on placement alone.
    done_integrity = contract.get("done_integrity", {})
    if done_integrity.get("merged_pr_or_done_placement_alone_is_not_proof") is not True:
        failures.append((
            "missing_required_invariant",
            "done_integrity.merged_pr_or_done_placement_alone_is_not_proof "
            "must be true",
        ))
    # 7c. Done required_proof must keep no_unique_local_or_untracked_work --
    # without it, a task could be proven Done while local worktree changes
    # never made it into the accepted head (see PROMPT.md's Done
    # terminal-integrity gate: local head must be provably contained).
    required_proof = done_integrity.get("required_proof", [])
    if "no_unique_local_or_untracked_work" not in required_proof:
        failures.append((
            "missing_required_invariant",
            "done_integrity.required_proof must include "
            "'no_unique_local_or_untracked_work'",
        ))
    # 7c2. Done required_proof must keep
    # canonical_merged_identity_and_accepted_head -- without it, a task
    # could be proven Done with no requirement that the merged/accepted
    # head is ever identified at all, which is the most basic Done
    # terminal-integrity floor (every other required_proof entry checks a
    # *property of* that head; dropping this entry removes the head
    # identity itself).
    if "canonical_merged_identity_and_accepted_head" not in required_proof:
        failures.append((
            "missing_required_invariant",
            "done_integrity.required_proof must include "
            "'canonical_merged_identity_and_accepted_head'",
        ))
    # 7d. Done receipt_fields must keep local_head -- without it, the
    # receipt records what was merged/accepted but never what the local
    # worktree actually held, which is exactly what the
    # no_unique_local_or_untracked_work proof above needs to check against.
    done_receipt_fields = done_integrity.get("receipt_fields", [])
    if "local_head" not in done_receipt_fields:
        failures.append((
            "missing_required_invariant",
            "done_integrity.receipt_fields must include 'local_head'",
        ))

    # 8. Readiness/notification order must be the exact ordered sequence,
    # not merely start/end correctly. Checking only the first and last
    # elements would silently accept a contract that dropped or reordered
    # the required middle `refreshed_post_ready_gates` step (contradictory-
    # order check).
    order = contract.get("readiness_notification_order", [])
    if list(order) != REQUIRED_READINESS_NOTIFICATION_ORDER:
        failures.append((
            "contradictory_plugin_prompt_default",
            "readiness_notification_order must be the exact ordered "
            f"sequence {REQUIRED_READINESS_NOTIFICATION_ORDER}, got {order!r}",
        ))

    # 9. Exclusions must not themselves leak secret-shaped content. Check
    # each entry independently (never the whole list joined together, which
    # would mask a leaking entry among legitimate bare category names).
    exclusions = contract.get("exclusions", [])
    for entry in exclusions:
        normalized = str(entry).strip().lower()
        if normalized in ALLOWED_BARE_EXCLUSION_CATEGORIES:
            continue
        for leak_term in FORBIDDEN_EXCLUSION_LEAKS:
            if leak_term in normalized:
                failures.append((
                    "exclusion_leaks_secret",
                    f"exclusions entry {entry!r} looks like a secret-shaped "
                    f"value (matched {leak_term!r}); exclusions may only "
                    "name categories (e.g. 'secrets', 'credentials'), never "
                    "embed actual secret-shaped content",
                ))
                break

    return failures


def _version_tuple(version_str):
    parts = []
    for part in str(version_str).split("."):
        digits = "".join(ch for ch in part if ch.isdigit())
        parts.append(int(digits) if digits else 0)
    while len(parts) < 3:
        parts.append(0)
    return tuple(parts)


def validate_plugin_snapshot(contract, snapshot):
    """Validate a vendored plugin snapshot against the canonical contract.

    Expected snapshot shape:
        {
          "plugin_contract_version": "1.0.0",
          "vendored_digest": "<sha256>",
          "defaults": {
             "human_reserved_classes": [...],
             "exact_head_gates": true,
             "workers_never_mutate": true,
             "cross_workspace_authority": false
          }
        }
    """
    failures = []
    contract_version = contract.get("contract_version")
    plugin_version = snapshot.get("plugin_contract_version")
    if plugin_version != contract_version:
        failures.append((
            "stale_version",
            f"plugin_contract_version {plugin_version!r} does not match "
            f"contract_version {contract_version!r}",
        ))

    try:
        expected_digest = compute_digest(contract, contract.get("digest_algorithm", "sha256"))
    except ValidationError as exc:
        failures.extend(exc.failures)
        expected_digest = None
    vendored_digest = snapshot.get("vendored_digest")
    if expected_digest is not None and vendored_digest != expected_digest:
        failures.append((
            "stale_digest",
            f"snapshot vendored_digest {vendored_digest!r} does not match "
            f"contract digest {expected_digest!r}",
        ))

    defaults = snapshot.get("defaults")
    authority = contract.get("authority_boundaries", {})
    contract_reserved = set(authority.get("human_reserved_classes", []))

    if not isinstance(defaults, dict) or not defaults:
        failures.append((
            "missing_required_invariant",
            "plugin snapshot 'defaults' is missing or empty; a snapshot "
            "must explicitly declare all of "
            f"{REQUIRED_PLUGIN_SNAPSHOT_DEFAULT_KEYS} -- an absent or empty "
            "defaults object trivially satisfies 'no field contradicts the "
            "contract' and must not be treated as a valid snapshot",
        ))
        defaults = {}
    else:
        missing_default_keys = [
            key for key in REQUIRED_PLUGIN_SNAPSHOT_DEFAULT_KEYS if key not in defaults
        ]
        if missing_default_keys:
            failures.append((
                "missing_required_invariant",
                f"plugin snapshot 'defaults' is missing mandatory key(s): "
                f"{missing_default_keys}",
            ))

    snapshot_reserved = set(defaults.get("human_reserved_classes", []))
    if "human_reserved_classes" in defaults and not snapshot_reserved:
        failures.append((
            "missing_required_invariant",
            "plugin snapshot defaults.human_reserved_classes must not be "
            "empty; it must declare the full human-reserved floor",
        ))
    if contract_reserved - snapshot_reserved:
        failures.append((
            "contradictory_plugin_prompt_default",
            "plugin defaults.human_reserved_classes drops required class(es): "
            f"{sorted(contract_reserved - snapshot_reserved)}",
        ))
    if "exact_head_gates" in defaults and defaults["exact_head_gates"] is not True:
        failures.append((
            "contradictory_plugin_prompt_default",
            "plugin defaults.exact_head_gates contradicts contract "
            "(must be true)",
        ))
    if "workers_never_mutate" in defaults and defaults["workers_never_mutate"] is not True:
        failures.append((
            "contradictory_plugin_prompt_default",
            "plugin defaults.workers_never_mutate contradicts contract "
            "(must be true)",
        ))
    if "cross_workspace_authority" in defaults and defaults["cross_workspace_authority"] is not False:
        failures.append((
            "contradictory_plugin_prompt_default",
            "plugin defaults.cross_workspace_authority contradicts contract "
            "(must be false)",
        ))

    return failures


def validate_overlay(contract, overlay):
    """Validate a workspace overlay only narrows, never widens, the contract.

    Expected overlay shape:
        {
          "overlay_id": "workspace-x",
          "base_contract_version": "1.0.0",
          "narrows": {
             "human_reserved_classes": [...],   // may only be a superset
             "cross_workspace_authority": false, // may only stay false
             "coordinator_decidable_examples": [...] // may only be a subset
          }
        }
    """
    failures = []
    base_version = overlay.get("base_contract_version")
    contract_version = contract.get("contract_version")
    if base_version != contract_version:
        failures.append((
            "stale_version",
            f"overlay base_contract_version {base_version!r} does not match "
            f"contract_version {contract_version!r}",
        ))

    narrows = overlay.get("narrows", {})
    authority = contract.get("authority_boundaries", {})

    if "cross_workspace_authority" in narrows and narrows["cross_workspace_authority"] is not False:
        failures.append((
            "overlay_widens_authority",
            "overlay attempts to set cross_workspace_authority to true; "
            "overlays may never widen cross-workspace authority",
        ))

    if "human_reserved_classes" in narrows:
        base_reserved = set(authority.get("human_reserved_classes", []))
        overlay_reserved = set(narrows["human_reserved_classes"])
        removed = base_reserved - overlay_reserved
        if removed:
            failures.append((
                "overlay_widens_authority",
                "overlay removes required human_reserved_classes "
                f"{sorted(removed)}; an overlay may only add to, never "
                "remove from, human-reserved classes (removal widens "
                "Coordinator/plugin authority)",
            ))

    if "coordinator_decidable_examples" in narrows:
        base_decidable = set(authority.get("coordinator_decidable_examples", []))
        overlay_decidable = set(narrows["coordinator_decidable_examples"])
        added = overlay_decidable - base_decidable
        if added:
            failures.append((
                "overlay_widens_authority",
                "overlay adds coordinator_decidable_examples not present in "
                f"the base contract: {sorted(added)}; overlays may only "
                "narrow (subset), never add new decidable classes",
            ))

    return failures


def _print_failures(failures):
    for check, message in failures:
        print(f"FAIL [{check}] {message}", file=sys.stderr)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", action="version", version=VALIDATOR_SCHEMA_VERSION)
    sub = parser.add_subparsers(dest="mode", required=True)

    p_contract = sub.add_parser("contract", help="Validate a contract document standalone.")
    p_contract.add_argument("--contract", required=True)

    p_snapshot = sub.add_parser("plugin-snapshot", help="Validate a vendored plugin snapshot.")
    p_snapshot.add_argument("--contract", required=True)
    p_snapshot.add_argument("--snapshot", required=True)

    p_overlay = sub.add_parser("overlay", help="Validate a workspace overlay.")
    p_overlay.add_argument("--contract", required=True)
    p_overlay.add_argument("--overlay", required=True)

    args = parser.parse_args(argv)

    contract = _load_json(args.contract)

    if args.mode == "contract":
        failures = validate_contract(contract)
    elif args.mode == "plugin-snapshot":
        failures = validate_contract(contract)
        snapshot = _load_json(args.snapshot)
        failures += validate_plugin_snapshot(contract, snapshot)
    elif args.mode == "overlay":
        failures = validate_contract(contract)
        overlay = _load_json(args.overlay)
        failures += validate_overlay(contract, overlay)
    else:  # pragma: no cover - argparse enforces choices
        parser.error(f"unknown mode {args.mode}")
        return 2

    if failures:
        _print_failures(failures)
        print(f"{len(failures)} check(s) failed.", file=sys.stderr)
        return 1

    print("OK: all checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
