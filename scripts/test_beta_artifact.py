#!/usr/bin/env python3
import hashlib
import importlib.util
import io
import json
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location("beta_artifact", Path(__file__).with_name("beta_artifact.py"))
beta = importlib.util.module_from_spec(SPEC)
assert SPEC.loader
SPEC.loader.exec_module(beta)


MANIFEST = b'''id: "kandev-plugin-coordinator"\napi_version: 1\nversion: "0.1.0"\nruntime:\n  executables:\n    linux-amd64: "server/plugin-linux-amd64"\n    linux-arm64: "server/plugin-linux-arm64"\n    darwin-amd64: "server/plugin-darwin-amd64"\n    darwin-arm64: "server/plugin-darwin-arm64"\n    windows-amd64: "server/plugin-windows-amd64.exe"\n'''


class BetaArtifactTests(unittest.TestCase):
    def make_archive(self, directory: Path, mutate=None) -> Path:
        payloads = {"manifest.yaml": MANIFEST, "ui/bundle.js": b"bundle"}
        payloads.update({name: name.encode() for name in beta.REQUIRED_PACKAGE_ASSETS})
        for name in beta.read_plugin_identity_from_text(MANIFEST)[2]:
            payloads[name] = b"binary:" + name.encode()
        if mutate:
            mutate(payloads)
        checksums = "".join(f"{hashlib.sha256(value).hexdigest()}  {name}\n" for name, value in sorted(payloads.items()))
        payloads["checksums.txt"] = checksums.encode()
        archive = directory / "kandev-plugin-coordinator-0.1.0.tar.gz"
        with tarfile.open(archive, "w:gz") as out:
            for name, data in sorted(payloads.items()):
                info = tarfile.TarInfo(name); info.size = len(data); info.mtime = 0
                out.addfile(info, io.BytesIO(data))
        return archive

    def test_archive_validation_rejects_tampered_payload_and_bad_checksums(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            archive = self.make_archive(root)
            _, _, executables = beta.read_plugin_identity_from_text(MANIFEST)
            beta.validate_archive(archive, "kandev-plugin-coordinator", "0.1.0", executables, MANIFEST)
            bad = self.make_archive(root, lambda payloads: payloads.__setitem__("ui/bundle.js", b"tampered"))
            # Rewrite a valid archive then flip one compressed byte: either gzip or checksum validation must fail.
            content = bytearray(bad.read_bytes()); content[len(content) // 2] ^= 1; bad.write_bytes(content)
            with self.assertRaises(beta.ValidationError):
                beta.validate_archive(bad, "kandev-plugin-coordinator", "0.1.0", executables, MANIFEST)

    def test_archive_validation_rejects_checksum_omission(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            archive = self.make_archive(root)
            with tarfile.open(archive, "r:gz") as source:
                payloads = {member.name: source.extractfile(member).read() for member in source.getmembers()}
            payloads["checksums.txt"] = b""  # A valid tar with a forged checksum list.
            with tarfile.open(archive, "w:gz") as out:
                for name, data in sorted(payloads.items()):
                    info = tarfile.TarInfo(name); info.size = len(data); out.addfile(info, io.BytesIO(data))
            _, _, executables = beta.read_plugin_identity_from_text(MANIFEST)
            with self.assertRaises(beta.ValidationError):
                beta.validate_archive(archive, "kandev-plugin-coordinator", "0.1.0", executables, MANIFEST)

    def test_archive_validation_rejects_missing_required_assets(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            archive = self.make_archive(root, lambda payloads: payloads.pop("ui/locales/fr.json"))
            _, _, executables = beta.read_plugin_identity_from_text(MANIFEST)
            with self.assertRaises(beta.ValidationError):
                beta.validate_archive(archive, "kandev-plugin-coordinator", "0.1.0", executables, MANIFEST)

    def test_archive_validation_rejects_changed_source_manifest(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw)
            archive = self.make_archive(root, lambda payloads: payloads.__setitem__("manifest.yaml", MANIFEST + b"command: changed\n"))
            _, _, executables = beta.read_plugin_identity_from_text(MANIFEST)
            with self.assertRaises(beta.ValidationError):
                beta.validate_archive(archive, "kandev-plugin-coordinator", "0.1.0", executables, MANIFEST)

    def test_sidecar_rejects_false_pass_and_mismatched_identity(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw); (root / "manifest.yaml").write_bytes(MANIFEST)
            archive = self.make_archive(root)
            data = {"schema_version": 1, "plugin": {"id": "kandev-plugin-coordinator", "version": "0.1.0", "commit": "a" * 40}, "build": {"go_version": "go", "node_version": "node", "npm_version": "npm", "sdk_commit": beta.SDK_COMMIT, "verification_command": "make verify-beta-artifact"}, "artifact": {"filename": archive.name, "sha256": beta.sha256_file(archive)}, "host_candidate": {"repository": beta.HOST_REPOSITORY, "branch": beta.HOST_BRANCH, "commit": None, "status": "unavailable"}, "gates": beta.default_gates()}
            data["gates"][0] = {"name": "artifact_package", "status": "passed", "evidence": []}
            with self.assertRaises(beta.ValidationError): beta.validate_sidecar(data, root, False)
            data["gates"][0]["evidence"] = ["local command"]
            data["plugin"]["version"] = "9.9.9"
            with self.assertRaises(beta.ValidationError): beta.validate_sidecar(data, root, False)

    def test_create_requires_clean_source(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw); subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.email", "test@example.invalid"], cwd=root, check=True)
            subprocess.run(["git", "config", "user.name", "Test"], cwd=root, check=True)
            (root / "manifest.yaml").write_bytes(MANIFEST)
            subprocess.run(["git", "add", "manifest.yaml"], cwd=root, check=True)
            subprocess.run(["git", "commit", "-qm", "fixture"], cwd=root, check=True)
            (root / "dirty.txt").write_text("dirty", encoding="utf-8")
            with self.assertRaises(beta.ValidationError): beta.assert_clean_committed(root)

    def test_pinned_sdk_checkout_requires_exact_clean_sibling(self):
        with tempfile.TemporaryDirectory() as raw:
            root = Path(raw) / "plugin"; root.mkdir()
            with self.assertRaises(beta.ValidationError):
                beta.assert_pinned_sdk_checkout(root)
            sdk = root.parent / "kandev"; sdk.mkdir()
            subprocess.run(["git", "init", "-q"], cwd=sdk, check=True)
            subprocess.run(["git", "config", "user.email", "test@example.invalid"], cwd=sdk, check=True)
            subprocess.run(["git", "config", "user.name", "Test"], cwd=sdk, check=True)
            (sdk / "README").write_text("sdk", encoding="utf-8")
            subprocess.run(["git", "add", "README"], cwd=sdk, check=True)
            subprocess.run(["git", "commit", "-qm", "fixture"], cwd=sdk, check=True)
            with self.assertRaises(beta.ValidationError):
                beta.assert_pinned_sdk_checkout(root)

    def test_beta_make_targets_bind_the_pinned_sdk_over_a_caller_override(self):
        root = Path(__file__).parents[1]
        ordinary = subprocess.run(
            ["make", "-n", "KANDEV_SDK=/tmp/untrusted-sdk", "package"],
            cwd=root, text=True, capture_output=True,
        )
        self.assertEqual(ordinary.returncode, 0, ordinary.stderr)
        self.assertIn("cd /tmp/untrusted-sdk &&", ordinary.stdout)
        beta_target = subprocess.run(
            ["make", "-n", "KANDEV_SDK=/tmp/untrusted-sdk", "beta-package"],
            cwd=root, text=True, capture_output=True,
        )
        self.assertEqual(beta_target.returncode, 0, beta_target.stderr)
        self.assertIn("cd ../kandev/apps/backend &&", beta_target.stdout)
        self.assertNotIn("/tmp/untrusted-sdk", beta_target.stdout)

    def test_cli_accepts_root_after_subcommand(self):
        result = subprocess.run(
            ["python3", str(Path(__file__).with_name("beta_artifact.py")), "verify", "--root", ".", "--sidecar", "missing.json"],
            cwd=Path(__file__).parents[1], text=True, capture_output=True,
        )
        self.assertEqual(result.returncode, 1)
        self.assertIn("cannot read sidecar JSON", result.stderr)

    def test_cli_rejects_root_before_subcommand(self):
        result = subprocess.run(
            ["python3", str(Path(__file__).with_name("beta_artifact.py")), "--root", ".", "verify", "--sidecar", "missing.json"],
            cwd=Path(__file__).parents[1], text=True, capture_output=True,
        )
        self.assertEqual(result.returncode, 2)


if __name__ == "__main__":
    unittest.main(verbosity=2)
