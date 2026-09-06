#!/usr/bin/env python3
"""Generate docs/contracts/upstream-manifest.json from the vendored tree.

Run this only as part of a deliberate vendor refresh (see
docs/contracts/PROVENANCE.md's refresh procedure), immediately after
replacing docs/contracts/upstream/ wholesale with a fresh copy from the
immutable source. It records the sha256 of every file currently under
docs/contracts/upstream/ plus the contract_version/digest/validator schema
read back from those same files, so scripts/verify_contract_provenance.py
can later prove nothing drifted since this snapshot was taken.

The --source-repository/--source-branch/--source-commit flags are NOT
free-form input: this script refuses to write a manifest unless they exactly
match the independently pinned constants in scripts/contract_pins.py. There
is exactly one immutable source pin at any time; an arbitrary fork, a
different branch, or "main"/a short SHA in place of the pinned full commit
SHA is rejected before anything is written. To adopt a new source SHA, first
update scripts/contract_pins.py as step one of the deliberate refresh
procedure -- see docs/contracts/PROVENANCE.md -- then re-run this script
(the flags then exist only as an explicit, fail-closed confirmation that the
caller intends to vendor that exact pin, not as a way to select an arbitrary
one).

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
SCRIPTS_DIR = os.path.dirname(os.path.abspath(__file__))
CONTRACTS_DIR = os.path.join(REPO_ROOT, "docs", "contracts")
UPSTREAM_DIR = os.path.join(CONTRACTS_DIR, "upstream")
MANIFEST_PATH = os.path.join(CONTRACTS_DIR, "upstream-manifest.json")

sys.path.insert(0, SCRIPTS_DIR)
import contract_pins  # noqa: E402


def sha256_file(path):
    h = hashlib.sha256()
    with open(path, "rb") as fh:
        for chunk in iter(lambda: fh.read(65536), b""):
            h.update(chunk)
    return h.hexdigest()


def _load_validator(upstream_dir):
    """Load validate_contract.py by explicit path (never via sys.path/import
    caching), so this never silently reuses a previously imported module."""
    import importlib.util

    validator_path = os.path.join(upstream_dir, "validate_contract.py")
    if os.path.islink(validator_path) or not os.path.isfile(validator_path):
        raise ValueError(f"{validator_path} is missing or not a regular file")
    spec = importlib.util.spec_from_file_location("_vendored_validate_contract_gen", validator_path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def build_manifest(contracts_dir, source_repository, source_branch, source_commit):
    """Build the manifest dict for contracts_dir/upstream.

    Raises ValueError if source_repository/source_branch/source_commit
    disagree with the independently pinned constants in contract_pins.py, or
    if any symlink or other non-regular file is found anywhere under
    contracts_dir/upstream (a vendored tree's content must be pinned by its
    checksum, never by whatever a symlink currently resolves to).
    """
    if source_repository != contract_pins.EXPECTED_SOURCE_REPOSITORY:
        raise ValueError(
            f"--source-repository {source_repository!r} does not match the "
            f"independently pinned {contract_pins.EXPECTED_SOURCE_REPOSITORY!r} in "
            "scripts/contract_pins.py -- update contract_pins.py first if this is a "
            "deliberate refresh"
        )
    if source_branch != contract_pins.EXPECTED_SOURCE_BRANCH:
        raise ValueError(
            f"--source-branch {source_branch!r} does not match the independently "
            f"pinned {contract_pins.EXPECTED_SOURCE_BRANCH!r} in scripts/contract_pins.py "
            "-- update contract_pins.py first if this is a deliberate refresh"
        )
    if not contract_pins.matches_pinned_commit(source_commit):
        raise ValueError(
            f"--source-commit {source_commit!r} is not a full 40-hex-character SHA "
            f"matching the independently pinned {contract_pins.EXPECTED_SOURCE_COMMIT!r} "
            "in scripts/contract_pins.py -- a branch name, tag, short SHA, or a "
            "different commit is rejected; update contract_pins.py first if this is a "
            "deliberate refresh"
        )

    upstream_dir = os.path.join(contracts_dir, "upstream")

    with open(os.path.join(upstream_dir, "coordinator-policy-contract.json"), encoding="utf-8") as fh:
        contract = json.load(fh)

    vc = _load_validator(upstream_dir)

    files = {}
    for root, dirs, names in os.walk(upstream_dir, followlinks=False):
        keep_dirs = []
        for d in dirs:
            if d == "__pycache__":
                continue
            full = os.path.join(root, d)
            if os.path.islink(full):
                raise ValueError(
                    f"refusing to vendor symlinked directory {full!r} -- the vendored "
                    "tree must contain only real, regular files and directories"
                )
            keep_dirs.append(d)
        dirs[:] = keep_dirs

        for name in sorted(names):
            if name.endswith(".pyc"):
                continue
            full = os.path.join(root, name)
            if os.path.islink(full) or not os.path.isfile(full):
                raise ValueError(
                    f"refusing to vendor symlink or non-regular file {full!r} -- the "
                    "vendored tree must contain only real, regular files"
                )
            rel = os.path.relpath(full, contracts_dir).replace(os.sep, "/")
            files[rel] = sha256_file(full)

    return {
        "provenance": {
            "source_repository": source_repository,
            "source_branch": source_branch,
            "source_commit": source_commit,
            "contract_version": contract.get("contract_version"),
            "contract_digest": contract.get("digest"),
            "validator_schema_version": vc.VALIDATOR_SCHEMA_VERSION,
        },
        "files": dict(sorted(files.items())),
    }


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source-repository", default=contract_pins.EXPECTED_SOURCE_REPOSITORY)
    parser.add_argument("--source-branch", default=contract_pins.EXPECTED_SOURCE_BRANCH)
    parser.add_argument("--source-commit", default=contract_pins.EXPECTED_SOURCE_COMMIT)
    args = parser.parse_args()

    try:
        manifest = build_manifest(CONTRACTS_DIR, args.source_repository, args.source_branch, args.source_commit)
    except ValueError as exc:
        print(f"FAIL [generator_input_rejected] {exc}", file=sys.stderr)
        return 1

    with open(MANIFEST_PATH, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)
        fh.write("\n")

    print(f"Wrote {MANIFEST_PATH} ({len(manifest['files'])} file(s)).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
