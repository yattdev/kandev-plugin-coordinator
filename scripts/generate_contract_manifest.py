#!/usr/bin/env python3
"""Generate docs/contracts/upstream-manifest.json from the vendored tree.

Run this only as part of a deliberate vendor refresh (see
docs/contracts/PROVENANCE.md's refresh procedure), immediately after
replacing docs/contracts/upstream/ wholesale with a fresh copy from the
immutable source. It records the sha256 of every file currently under
docs/contracts/upstream/ plus the contract_version/digest/validator schema
read back from those same files, so scripts/verify_contract_provenance.py
can later prove nothing drifted since this snapshot was taken.

This script does not fetch anything over the network; it only reads files
already on disk. Usage:

    python3 scripts/generate_contract_manifest.py \
        --source-repository yattdev/tasks-coordinator \
        --source-branch feature/codify-coordinator-p-8eu \
        --source-commit 2ca27d00477dc298fc91187274968f1fc3970fef
"""
import argparse
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


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-repository", required=True)
    parser.add_argument("--source-branch", required=True)
    parser.add_argument("--source-commit", required=True)
    args = parser.parse_args()

    sys.path.insert(0, UPSTREAM_DIR)
    import validate_contract as vc  # noqa: E402

    with open(os.path.join(UPSTREAM_DIR, "coordinator-policy-contract.json"), encoding="utf-8") as fh:
        contract = json.load(fh)

    files = {}
    for root, dirs, names in os.walk(UPSTREAM_DIR):
        dirs[:] = [d for d in dirs if d != "__pycache__"]
        for name in sorted(names):
            if name.endswith(".pyc"):
                continue
            full = os.path.join(root, name)
            rel = os.path.relpath(full, CONTRACTS_DIR).replace(os.sep, "/")
            files[rel] = sha256_file(full)

    manifest = {
        "provenance": {
            "source_repository": args.source_repository,
            "source_branch": args.source_branch,
            "source_commit": args.source_commit,
            "contract_version": contract.get("contract_version"),
            "contract_digest": contract.get("digest"),
            "validator_schema_version": vc.VALIDATOR_SCHEMA_VERSION,
        },
        "files": dict(sorted(files.items())),
    }

    with open(MANIFEST_PATH, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)
        fh.write("\n")

    print(f"Wrote {MANIFEST_PATH} ({len(files)} file(s)).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
