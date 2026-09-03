#!/usr/bin/env python3
"""Hermetic provenance check for the vendored Coordinator policy contract.

Verifies, without any network access, that:

1. Every file listed in ``docs/contracts/upstream-manifest.json`` is present
   on disk with the exact sha256 checksum recorded at vendor time (proves no
   local edit or partial re-vendor has drifted the tree since the last
   deliberate refresh -- see docs/contracts/PROVENANCE.md).
2. No extra, unlisted file exists under ``docs/contracts/upstream/`` (proves
   nothing was added to the vendored tree outside the recorded manifest).
3. The vendored contract's own ``contract_version`` and ``digest`` match the
   manifest's recorded values (catches a manifest that was never updated
   after a vendor refresh).
4. The vendored validator's ``VALIDATOR_SCHEMA_VERSION`` matches the
   manifest's recorded value (catches a validator refresh whose manifest
   entry silently kept the old schema version).

This is deliberately independent of validate_contract.py's own checks (which
validate the contract's *content*); this script only proves the vendored
*files* are exactly what the manifest says was vendored -- the "did the
bytes on disk match what we vendored, and only what we vendored" half of
fail-closed vendoring. Stdlib only. Exit 0 on success, 1 on any mismatch.
"""
import hashlib
import json
import os
import sys

REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
CONTRACTS_DIR = os.path.join(REPO_ROOT, "docs", "contracts")
UPSTREAM_DIR = os.path.join(CONTRACTS_DIR, "upstream")
MANIFEST_PATH = os.path.join(CONTRACTS_DIR, "upstream-manifest.json")


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def list_upstream_files():
    found = []
    for root, dirs, files in os.walk(UPSTREAM_DIR):
        dirs[:] = [d for d in dirs if d != "__pycache__"]
        for name in files:
            if name.endswith(".pyc"):
                continue
            full = os.path.join(root, name)
            rel = os.path.relpath(full, CONTRACTS_DIR).replace(os.sep, "/")
            found.append(rel)
    return sorted(found)


def main():
    failures = []

    if not os.path.isfile(MANIFEST_PATH):
        print(f"FAIL [manifest_missing] {MANIFEST_PATH} does not exist", file=sys.stderr)
        return 1

    with open(MANIFEST_PATH, encoding="utf-8") as fh:
        manifest = json.load(fh)

    provenance = manifest.get("provenance", {})
    files = manifest.get("files", {})

    if not files:
        failures.append(("manifest_empty", "manifest 'files' map is empty"))

    manifest_paths = set(files.keys())
    disk_paths = set(list_upstream_files())

    missing_on_disk = sorted(manifest_paths - disk_paths)
    for rel in missing_on_disk:
        failures.append(("file_missing", f"manifest lists {rel!r} but it is not on disk"))

    unlisted_on_disk = sorted(disk_paths - manifest_paths)
    for rel in unlisted_on_disk:
        failures.append((
            "unlisted_file",
            f"{rel!r} exists under docs/contracts/upstream/ but is not in the manifest "
            "-- every vendored file must be recorded",
        ))

    for rel in sorted(manifest_paths & disk_paths):
        expected = files[rel]
        actual = sha256_file(os.path.join(CONTRACTS_DIR, rel))
        if actual != expected:
            failures.append((
                "checksum_mismatch",
                f"{rel!r} sha256 {actual!r} does not match manifest-recorded {expected!r} "
                "-- the vendored file has drifted since the last deliberate vendor refresh",
            ))

    # Cross-check the manifest's recorded contract_version/digest/validator
    # schema against what is actually inside the vendored files themselves,
    # so a manifest that was hand-edited (or never updated after a refresh)
    # cannot silently disagree with the files it describes.
    contract_path = os.path.join(UPSTREAM_DIR, "coordinator-policy-contract.json")
    if os.path.isfile(contract_path):
        with open(contract_path, encoding="utf-8") as fh:
            contract = json.load(fh)
        if contract.get("contract_version") != provenance.get("contract_version"):
            failures.append((
                "provenance_mismatch",
                "manifest provenance.contract_version "
                f"{provenance.get('contract_version')!r} does not match vendored "
                f"contract_version {contract.get('contract_version')!r}",
            ))
        if contract.get("digest") != provenance.get("contract_digest"):
            failures.append((
                "provenance_mismatch",
                "manifest provenance.contract_digest "
                f"{provenance.get('contract_digest')!r} does not match vendored "
                f"digest {contract.get('digest')!r}",
            ))

    validator_path = os.path.join(UPSTREAM_DIR, "validate_contract.py")
    if os.path.isfile(validator_path):
        sys.path.insert(0, UPSTREAM_DIR)
        import validate_contract as vc  # noqa: E402

        if vc.VALIDATOR_SCHEMA_VERSION != provenance.get("validator_schema_version"):
            failures.append((
                "provenance_mismatch",
                "manifest provenance.validator_schema_version "
                f"{provenance.get('validator_schema_version')!r} does not match vendored "
                f"VALIDATOR_SCHEMA_VERSION {vc.VALIDATOR_SCHEMA_VERSION!r}",
            ))

    if failures:
        for check, message in failures:
            print(f"FAIL [{check}] {message}", file=sys.stderr)
        print(f"{len(failures)} provenance check(s) failed.", file=sys.stderr)
        return 1

    print(f"OK: {len(files)} vendored file(s) match recorded provenance.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
