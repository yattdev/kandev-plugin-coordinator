#!/usr/bin/env python3
"""Hermetic provenance check for the vendored Coordinator policy contract.

Verifies, without any network access, that:

1. The manifest's `provenance.source_repository`, `source_branch`, and
   `source_commit` exactly match the independently pinned constants in
   `scripts/contract_pins.py` -- NOT some value the manifest merely asserts
   about itself. A missing, malformed (e.g. a branch name or short SHA
   instead of a full commit SHA), mutable, or substituted (a different, even
   well-formed, repo/branch/SHA) value fails closed here, independent of
   anything else in the manifest.
2. Every file listed in `docs/contracts/upstream-manifest.json` is present
   on disk, as a real regular file (never a symlink or other non-regular
   file), with the exact sha256 checksum recorded at vendor time (proves no
   local edit, symlink substitution, or partial re-vendor has drifted the
   tree since the last deliberate refresh -- see docs/contracts/PROVENANCE.md).
3. No extra, unlisted file (or symlink, anywhere under the vendored tree,
   including symlinked directories) exists under `docs/contracts/upstream/`
   (proves nothing was added, or substituted via a symlink, outside the
   recorded manifest).
4. No manifest-recorded path attempts to escape `docs/contracts/` (rejects
   path traversal such as `../../etc/passwd` before ever opening a path).
5. The vendored contract's own `contract_version` and `digest` match the
   manifest's recorded values (catches a manifest that was never updated
   after a vendor refresh).
6. The vendored validator's `VALIDATOR_SCHEMA_VERSION` matches the
   manifest's recorded value (catches a validator refresh whose manifest
   entry silently kept the old schema version).

This is deliberately independent of validate_contract.py's own checks (which
validate the contract's *content*); this script only proves the vendored
*files* are exactly what the manifest says was vendored, from exactly the
pinned immutable source, and only what was vendored. Stdlib only. Exit 0 on
success, 1 on any mismatch.
"""
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


def is_safe_relative_path(rel):
    """Reject absolute paths, backslashes, and any ".." traversal component."""
    if not rel or os.path.isabs(rel) or "\\" in rel:
        return False
    parts = rel.split("/")
    return ".." not in parts and "" not in parts


def scan_upstream_tree(upstream_dir, contracts_dir):
    """Walk upstream_dir without following symlinked directories.

    Returns (regular_file_rel_paths, non_regular_rel_paths) where the second
    list holds every symlink (file or directory) or other non-regular entry
    found anywhere in the tree -- a symlink is never acceptable in a vendored
    tree, even one whose current target happens to have identical bytes,
    because its content is not pinned and can change later without any hash
    in this repository ever being touched.
    """
    regular_files = []
    non_regular = []
    for dirpath, dirnames, filenames in os.walk(upstream_dir, followlinks=False):
        keep_dirs = []
        for d in dirnames:
            if d == "__pycache__":
                continue
            full = os.path.join(dirpath, d)
            if os.path.islink(full):
                rel = os.path.relpath(full, contracts_dir).replace(os.sep, "/")
                non_regular.append(rel)
                # Do not descend into a symlinked directory.
                continue
            keep_dirs.append(d)
        dirnames[:] = keep_dirs

        for name in filenames:
            if name.endswith(".pyc"):
                continue
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, contracts_dir).replace(os.sep, "/")
            if os.path.islink(full) or not os.path.isfile(full):
                non_regular.append(rel)
            else:
                regular_files.append(rel)
    return sorted(regular_files), sorted(non_regular)


def check_pinned_provenance(provenance):
    """Independently verify provenance against scripts/contract_pins.py.

    Returns a list of (check_name, message) failures. Deliberately does not
    trust anything else in the manifest: these three values are compared
    only against the hardcoded pins, never against each other.
    """
    failures = []

    source_repository = provenance.get("source_repository")
    if source_repository != contract_pins.EXPECTED_SOURCE_REPOSITORY:
        failures.append((
            "source_repository_mismatch",
            f"manifest provenance.source_repository {source_repository!r} does not "
            f"match the independently pinned {contract_pins.EXPECTED_SOURCE_REPOSITORY!r} "
            "in scripts/contract_pins.py",
        ))

    source_branch = provenance.get("source_branch")
    if source_branch != contract_pins.EXPECTED_SOURCE_BRANCH:
        failures.append((
            "source_branch_mismatch",
            f"manifest provenance.source_branch {source_branch!r} does not match the "
            f"independently pinned {contract_pins.EXPECTED_SOURCE_BRANCH!r} in "
            "scripts/contract_pins.py",
        ))

    source_commit = provenance.get("source_commit")
    if not contract_pins.is_full_commit_sha(source_commit):
        failures.append((
            "source_commit_malformed",
            f"manifest provenance.source_commit {source_commit!r} is missing or is not "
            "a full 40-hex-character commit SHA (a branch name, tag, or abbreviated "
            "SHA is not acceptable provenance)",
        ))
    elif not contract_pins.matches_pinned_commit(source_commit):
        failures.append((
            "source_commit_mismatch",
            f"manifest provenance.source_commit {source_commit!r} does not match the "
            f"independently pinned {contract_pins.EXPECTED_SOURCE_COMMIT!r} in "
            "scripts/contract_pins.py",
        ))

    return failures


def run_provenance_check(contracts_dir=CONTRACTS_DIR):
    """Run every check and return (ok, failures) without printing or exiting.

    Kept side-effect free (besides reading files) so it can be exercised
    directly, against isolated fixture trees, by
    scripts/test_verify_contract_provenance.py.
    """
    upstream_dir = os.path.join(contracts_dir, "upstream")
    manifest_path = os.path.join(contracts_dir, "upstream-manifest.json")
    failures = []

    if os.path.islink(manifest_path) or not os.path.isfile(manifest_path):
        failures.append(("manifest_missing", f"{manifest_path} does not exist as a regular file"))
        return False, failures

    with open(manifest_path, encoding="utf-8") as fh:
        manifest = json.load(fh)

    provenance = manifest.get("provenance", {})
    files = manifest.get("files", {})

    failures.extend(check_pinned_provenance(provenance))

    if not files:
        failures.append(("manifest_empty", "manifest 'files' map is empty"))

    # Reject path traversal / absolute paths before ever touching the
    # filesystem with a manifest-supplied path.
    safe_manifest_paths = set()
    for rel in files:
        if not is_safe_relative_path(rel):
            failures.append((
                "path_traversal",
                f"manifest path {rel!r} is not a safe path relative to docs/contracts/ "
                "(absolute paths and \"..\" components are rejected)",
            ))
        else:
            safe_manifest_paths.add(rel)

    disk_files, non_regular = scan_upstream_tree(upstream_dir, contracts_dir)
    disk_paths = set(disk_files)

    for rel in non_regular:
        failures.append((
            "symlink_or_non_regular_file",
            f"{rel!r} is a symlink (or other non-regular file) -- the vendored tree "
            "must contain only real, regular files so its content is pinned by the "
            "checksum below, not by whatever a symlink currently happens to resolve to",
        ))

    missing_on_disk = sorted(safe_manifest_paths - disk_paths)
    for rel in missing_on_disk:
        failures.append(("file_missing", f"manifest lists {rel!r} but it is not on disk"))

    unlisted_on_disk = sorted(disk_paths - safe_manifest_paths)
    for rel in unlisted_on_disk:
        failures.append((
            "unlisted_file",
            f"{rel!r} exists under docs/contracts/upstream/ but is not in the manifest "
            "-- every vendored file must be recorded",
        ))

    for rel in sorted(safe_manifest_paths & disk_paths):
        expected = files[rel]
        actual = sha256_file(os.path.join(contracts_dir, rel))
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
    contract_path = os.path.join(upstream_dir, "coordinator-policy-contract.json")
    if "docs/contracts/upstream/coordinator-policy-contract.json" in safe_manifest_paths \
            and os.path.isfile(contract_path) and not os.path.islink(contract_path):
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

    validator_path = os.path.join(upstream_dir, "validate_contract.py")
    if "docs/contracts/upstream/validate_contract.py" in safe_manifest_paths \
            and os.path.isfile(validator_path) and not os.path.islink(validator_path):
        import importlib.util

        spec = importlib.util.spec_from_file_location("_vendored_validate_contract", validator_path)
        vc = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(vc)

        if vc.VALIDATOR_SCHEMA_VERSION != provenance.get("validator_schema_version"):
            failures.append((
                "provenance_mismatch",
                "manifest provenance.validator_schema_version "
                f"{provenance.get('validator_schema_version')!r} does not match vendored "
                f"VALIDATOR_SCHEMA_VERSION {vc.VALIDATOR_SCHEMA_VERSION!r}",
            ))

    return not failures, failures


def main():
    ok, failures = run_provenance_check(CONTRACTS_DIR)

    if not ok:
        for check, message in failures:
            print(f"FAIL [{check}] {message}", file=sys.stderr)
        print(f"{len(failures)} provenance check(s) failed.", file=sys.stderr)
        return 1

    with open(MANIFEST_PATH, encoding="utf-8") as fh:
        manifest = json.load(fh)
    print(f"OK: {len(manifest.get('files', {}))} vendored file(s) match recorded provenance.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
