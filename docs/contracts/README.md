# Coordinator policy contract — vendoring and CI

The Coordinator plugin must not drift from the Coordinator policy contract's
authority boundaries, gate requirements, queue/receipt identity rules, and
Done terminal-integrity floor. Rather than hand-copying or reinterpreting
those invariants into this plugin's own prompt/config defaults (which can
silently drift from the source of truth), this plugin **vendors** the
contract and its standalone validator verbatim from the immutable source and
runs them in CI against this plugin's own defaults snapshot. See
[`PROVENANCE.md`](PROVENANCE.md) for exactly what is vendored, from where,
and the deterministic refresh procedure.

## Layout

```
docs/contracts/
  README.md                 # this file
  PROVENANCE.md              # source pin, digest/version/schema readback, refresh procedure
  upstream-manifest.json     # generated: sha256 of every vendored file + provenance table
  plugin-snapshot.json       # this plugin's own machine-readable `defaults` dump
  upstream/                  # exact, unmodified copy of the source's docs/contracts/
    coordinator-policy-contract.json
    validate_contract.py
    adversarial_sweep.py
    test_validate_contract.py
    CONTRACT_MAPPING.md
    fixtures/*.json
scripts/
  contract_pins.py                # independently pinned source_repository/branch/commit constants
  verify_contract_provenance.py   # hermetic (no network) manifest/checksum/pin proof
  generate_contract_manifest.py   # regenerates the manifest during a refresh (validated against contract_pins.py)
  test_verify_contract_provenance.py  # isolated regression tests for the two scripts above
```

Nothing under `upstream/` is hand-edited; it is replaced wholesale during a
refresh (see `PROVENANCE.md`). `plugin-snapshot.json` is this plugin's own
artifact and is the only file in this tree meant to be edited directly (to
add workspace-specific defaults, narrowing only — see below).

## What CI enforces (`make verify-contract`)

Run locally with:

```sh
make verify-contract
```

This single hermetic target (no network, no external services) runs, in
order, and fails on the first non-zero exit:

1. **Contract self-validation** — `validate_contract.py contract` recomputes
   the contract's own digest and checks every mandatory invariant
   (authority boundaries, gates, lane ownership, queue/receipt identity,
   done-integrity, readiness/notification order, exclusions). Also rejects a
   `contract_version` below the contract's own
   `min_supported_contract_version`, above its own
   `max_known_contract_version`, or above the validator's independent,
   hardcoded `VALIDATOR_MAX_SUPPORTED_CONTRACT_VERSION` ceiling (a
   self-declared future version cannot forge its way past that ceiling).
2. **Plugin snapshot validation** — `validate_contract.py plugin-snapshot`
   checks `docs/contracts/plugin-snapshot.json` against the vendored
   contract: exact `plugin_contract_version` / `vendored_digest` match
   (validator/contract version incompatibility fails closed, never "best
   effort"), all four mandatory `defaults` keys present and
   non-contradictory, and no missing/unknown required top-level fields.
3. **Overlay narrowing proof** — `validate_contract.py overlay` is run
   against both the upstream `fixtures/narrowing_overlay.json` (must pass)
   and `fixtures/widening_overlay.json` (must be rejected with
   `overlay_widens_authority`), proving workspace overlays can only narrow
   authority, never widen it.
4. **Full vendored test suite** — `python3 -m unittest test_validate_contract`
   (88 tests: canonical contract, every positive/negative fixture, plugin
   snapshot, and overlay cases).
5. **Complete adversarial sweep** — `python3 adversarial_sweep.py` asserts
   all 78/78 mandatory-invariant-weakening mutations are rejected, each
   against an independently recomputed valid digest, so a `stale_digest`
   false-positive can never mask a real invariant gap.
6. **Hermetic provenance check** — `scripts/verify_contract_provenance.py`
   independently re-checks the manifest's `provenance.source_repository`,
   `source_branch`, and `source_commit` against the hardcoded constants in
   `scripts/contract_pins.py` (never trusting the manifest's own assertion
   about itself — missing, malformed, mutable, or substituted values all
   fail closed); proves every file under `upstream/` is a real regular
   file (never a symlink or other non-regular file — a symlink whose
   current target happens to have byte-identical content is still
   rejected, because its content is not pinned) matching the sha256
   recorded in `upstream-manifest.json` at the last deliberate refresh
   (catches drift or partial re-vendor); rejects any manifest-recorded path
   that attempts to escape `docs/contracts/`; and confirms the manifest's
   recorded `contract_version`/`digest`/`validator_schema_version` agree
   with what is actually inside the vendored files.
7. **Isolated provenance regression tests** —
   `scripts/test_verify_contract_provenance.py` (run via
   `python3 -m unittest discover -s scripts -p "test_*.py"`) exercises the
   above checks and the manifest generator's input validation against
   throwaway fixture copies: missing/attacker/mutable/substituted source
   provenance, a symlink standing in for a vendored file, path traversal,
   an emptied provenance block, and a generator invocation given an
   arbitrary fork/branch/"main"-as-commit.

`.github/workflows/ci.yml`'s `contract-validation` job runs `make
verify-contract` on every pull request, plus a separate
`contract-vendor-provenance` job that checks out the pinned immutable source
commit (`yattdev/tasks-coordinator` at the exact SHA recorded in
`PROVENANCE.md`), rejects any symlink present in either the vendored or the
pinned-source tree, diffs it byte-for-byte (`--no-dereference`, so a symlink
can never be treated as the file it resolves to) against
`docs/contracts/upstream/`, and independently cross-checks that the exact
ref it just checked out matches `scripts/contract_pins.py` — the strongest
available proof that the vendored tree is exactly the immutable source's
`docs/contracts/` at that pin, not merely internally self-consistent.

## Workspace overlays

A workspace overlay may only **narrow** the contract's authority floor
(`docs/contracts/upstream/CONTRACT_MAPPING.md` §5): `cross_workspace_authority`
may stay `false` but never flip `true`; `human_reserved_classes` may only
grow; `coordinator_decidable_examples` may only shrink.
`upstream/fixtures/narrowing_overlay.json` /
`upstream/fixtures/widening_overlay.json` are the vendored positive/negative
proof of that rule and are exercised directly in CI (see above) rather than
duplicated into a plugin-specific example, to avoid a second copy drifting
from the upstream fixture it is supposed to mirror. This plugin does not yet
ship a runtime overlay-authoring UI; when it does, that UI must call
`validate_contract.py overlay` (or an equivalent same-invariant check) before
persisting any operator-authored overlay.

## Updating the snapshot or the vendored contract

- **Changing this plugin's own defaults** (`plugin-snapshot.json`): edit the
  file directly, keep all four mandatory keys, then run
  `make verify-contract`. This does not require a source-repository change.
- **Adopting a new contract version**: follow the refresh procedure in
  [`PROVENANCE.md`](PROVENANCE.md). Never hand-edit `upstream/`.
