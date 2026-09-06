#!/usr/bin/env python3
"""Isolated regression tests for the vendored-contract provenance guards.

These tests never touch the real repository's docs/contracts/ tree except by
reading it once (read-only) to build a fixture copy in a temp directory; all
mutation happens on that throwaway copy. They exist to prove the specific
gaps a distinct QA pass found in the provenance verifier/generator are
closed:

- missing/malformed/mutable/substituted source_commit (and source_repository
  / source_branch) values are rejected even though they were never
  previously cross-checked against anything independent of the manifest
  itself;
- a symlink standing in for a vendored file -- even one whose current target
  has byte-identical content to what the manifest expects -- is rejected,
  because a symlink's target is not pinned by the checksum;
- the manifest generator refuses arbitrary fork/repo/branch/"main"-as-commit
  inputs instead of writing whatever it is told;
- manifest-supplied paths cannot escape docs/contracts/ (path traversal);
- an emptied/incomplete provenance block fails every independent check
  rather than silently passing because nothing was present to compare.

Run directly: `python3 -m unittest scripts.test_verify_contract_provenance -v`
from the repository root, or as part of `make verify-contract`.
"""
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest

SCRIPTS_DIR = os.path.dirname(os.path.abspath(__file__))
REPO_ROOT = os.path.dirname(SCRIPTS_DIR)
REAL_CONTRACTS_DIR = os.path.join(REPO_ROOT, "docs", "contracts")

sys.path.insert(0, SCRIPTS_DIR)
import contract_pins  # noqa: E402
import generate_contract_manifest as gcm  # noqa: E402
import verify_contract_provenance as vcp  # noqa: E402


def _fresh_contracts_copy(dest_root):
    """Read-only copy of the real docs/contracts/ tree into an isolated tmp
    dir, so every test below mutates only its own throwaway fixture."""
    dest = os.path.join(dest_root, "contracts")
    shutil.copytree(REAL_CONTRACTS_DIR, dest, symlinks=False)
    return dest


def _load_manifest(contracts_dir):
    path = os.path.join(contracts_dir, "upstream-manifest.json")
    with open(path, encoding="utf-8") as fh:
        return json.load(fh)


def _write_manifest(contracts_dir, manifest):
    path = os.path.join(contracts_dir, "upstream-manifest.json")
    with open(path, "w", encoding="utf-8") as fh:
        json.dump(manifest, fh, indent=2, sort_keys=True)


def _failure_codes(failures):
    return {code for code, _ in failures}


class PristineFixtureTests(unittest.TestCase):
    """Sanity check that the fixture-copy mechanism itself is trustworthy."""

    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="contract-provenance-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.contracts_dir = _fresh_contracts_copy(self.tmp)

    def test_untouched_copy_passes(self):
        ok, failures = vcp.run_provenance_check(self.contracts_dir)
        self.assertTrue(ok, failures)


class PinnedProvenanceRegressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="contract-provenance-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.contracts_dir = _fresh_contracts_copy(self.tmp)

    def test_missing_source_commit_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        del manifest["provenance"]["source_commit"]
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_commit_malformed", _failure_codes(failures))

    def test_attacker_repository_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"]["source_repository"] = "attacker/tasks-coordinator"
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_repository_mismatch", _failure_codes(failures))

    def test_attacker_branch_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"]["source_branch"] = "main"
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_branch_mismatch", _failure_codes(failures))

    def test_mutable_commit_ref_rejected(self):
        """A branch name in place of a commit SHA must fail as malformed,
        not be silently accepted as "close enough"."""
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"]["source_commit"] = "main"
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_commit_malformed", _failure_codes(failures))

    def test_short_sha_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"]["source_commit"] = "2ca27d0"
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_commit_malformed", _failure_codes(failures))

    def test_substituted_valid_looking_commit_rejected(self):
        """A different, well-formed 40-hex-char SHA must still fail -- it is
        not the pinned commit, even though it passes the format check."""
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"]["source_commit"] = "a" * 40
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("source_commit_mismatch", _failure_codes(failures))

    def test_incomplete_provenance_block_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["provenance"] = {}
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        codes = _failure_codes(failures)
        self.assertIn("source_repository_mismatch", codes)
        self.assertIn("source_branch_mismatch", codes)
        self.assertIn("source_commit_malformed", codes)


class NonRegularFileRegressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="contract-provenance-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.contracts_dir = _fresh_contracts_copy(self.tmp)

    def test_symlink_to_identical_external_bytes_rejected(self):
        """A naive checksum-only check would pass this (the bytes match
        today); it must still be rejected because a symlink's target content
        is not pinned and can change later without any hash here moving."""
        vendored_path = os.path.join(self.contracts_dir, "upstream", "coordinator-policy-contract.json")
        external_path = os.path.join(self.tmp, "external-identical-bytes.json")
        shutil.copyfile(vendored_path, external_path)
        os.remove(vendored_path)
        os.symlink(external_path, vendored_path)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("symlink_or_non_regular_file", _failure_codes(failures))

    def test_symlinked_directory_rejected(self):
        real_fixtures_dir = os.path.join(self.contracts_dir, "upstream", "fixtures")
        external_dir = os.path.join(self.tmp, "external-fixtures-copy")
        shutil.copytree(real_fixtures_dir, external_dir)
        shutil.rmtree(real_fixtures_dir)
        os.symlink(external_dir, real_fixtures_dir)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        codes = _failure_codes(failures)
        self.assertIn("symlink_or_non_regular_file", codes)
        # The symlinked directory is never descended into, so its manifest
        # entries must also be reported missing on disk.
        self.assertIn("file_missing", codes)

    def test_checksum_drift_still_caught(self):
        target = os.path.join(self.contracts_dir, "upstream", "CONTRACT_MAPPING.md")
        with open(target, "a", encoding="utf-8") as fh:
            fh.write("\ntampered\n")

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("checksum_mismatch", _failure_codes(failures))

    def test_extra_unlisted_file_still_caught(self):
        extra_path = os.path.join(self.contracts_dir, "upstream", "sneaky-extra.json")
        with open(extra_path, "w", encoding="utf-8") as fh:
            fh.write("{}")

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("unlisted_file", _failure_codes(failures))


class PathTraversalRegressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="contract-provenance-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.contracts_dir = _fresh_contracts_copy(self.tmp)

    def test_dotdot_traversal_path_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["files"]["../../../etc/passwd"] = "0" * 64
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("path_traversal", _failure_codes(failures))

    def test_absolute_path_rejected(self):
        manifest = _load_manifest(self.contracts_dir)
        manifest["files"]["/etc/passwd"] = "0" * 64
        _write_manifest(self.contracts_dir, manifest)

        ok, failures = vcp.run_provenance_check(self.contracts_dir)

        self.assertFalse(ok)
        self.assertIn("path_traversal", _failure_codes(failures))


class GeneratorInputRegressionTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.mkdtemp(prefix="contract-generator-test-")
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)
        self.contracts_dir = _fresh_contracts_copy(self.tmp)

    def test_pinned_inputs_succeed_and_reverify(self):
        manifest = gcm.build_manifest(
            self.contracts_dir,
            contract_pins.EXPECTED_SOURCE_REPOSITORY,
            contract_pins.EXPECTED_SOURCE_BRANCH,
            contract_pins.EXPECTED_SOURCE_COMMIT,
        )
        self.assertEqual(manifest["provenance"]["source_commit"], contract_pins.EXPECTED_SOURCE_COMMIT)

        _write_manifest(self.contracts_dir, manifest)
        ok, failures = vcp.run_provenance_check(self.contracts_dir)
        self.assertTrue(ok, failures)

    def test_attacker_fork_rejected(self):
        with self.assertRaises(ValueError):
            gcm.build_manifest(
                self.contracts_dir,
                "attacker/tasks-coordinator",
                contract_pins.EXPECTED_SOURCE_BRANCH,
                contract_pins.EXPECTED_SOURCE_COMMIT,
            )

    def test_arbitrary_branch_rejected(self):
        with self.assertRaises(ValueError):
            gcm.build_manifest(
                self.contracts_dir,
                contract_pins.EXPECTED_SOURCE_REPOSITORY,
                "main",
                contract_pins.EXPECTED_SOURCE_COMMIT,
            )

    def test_main_as_commit_rejected(self):
        with self.assertRaises(ValueError):
            gcm.build_manifest(
                self.contracts_dir,
                contract_pins.EXPECTED_SOURCE_REPOSITORY,
                contract_pins.EXPECTED_SOURCE_BRANCH,
                "main",
            )

    def test_different_wellformed_sha_rejected(self):
        with self.assertRaises(ValueError):
            gcm.build_manifest(
                self.contracts_dir,
                contract_pins.EXPECTED_SOURCE_REPOSITORY,
                contract_pins.EXPECTED_SOURCE_BRANCH,
                "a" * 40,
            )

    def test_symlinked_vendored_file_rejected_at_generation_time(self):
        real_path = os.path.join(self.contracts_dir, "upstream", "adversarial_sweep.py")
        external = os.path.join(self.tmp, "external-copy.py")
        shutil.copyfile(real_path, external)
        os.remove(real_path)
        os.symlink(external, real_path)

        with self.assertRaises(ValueError):
            gcm.build_manifest(
                self.contracts_dir,
                contract_pins.EXPECTED_SOURCE_REPOSITORY,
                contract_pins.EXPECTED_SOURCE_BRANCH,
                contract_pins.EXPECTED_SOURCE_COMMIT,
            )

    def test_cli_rejects_malicious_commit_and_writes_nothing(self):
        """Exercise the real CLI entrypoint end-to-end: a malicious
        --source-commit must exit non-zero and must never write a
        manifest anywhere."""
        script = os.path.join(SCRIPTS_DIR, "generate_contract_manifest.py")
        real_manifest_path = os.path.join(REAL_CONTRACTS_DIR, "upstream-manifest.json")
        with open(real_manifest_path, encoding="utf-8") as fh:
            before = fh.read()

        result = subprocess.run(
            [sys.executable, script, "--source-commit", "main"],
            capture_output=True,
            text=True,
        )

        with open(real_manifest_path, encoding="utf-8") as fh:
            after = fh.read()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("generator_input_rejected", result.stderr)
        self.assertEqual(before, after, "rejected CLI input must never write the manifest")


class VerifierCliRegressionTest(unittest.TestCase):
    def test_cli_passes_against_real_vendored_tree(self):
        """End-to-end smoke test against the actual repository state (read
        only) proving the tightened checks still accept the real vendored
        tree."""
        script = os.path.join(SCRIPTS_DIR, "verify_contract_provenance.py")
        result = subprocess.run([sys.executable, script], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("OK:", result.stdout)


if __name__ == "__main__":
    unittest.main()
