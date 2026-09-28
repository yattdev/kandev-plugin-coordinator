#!/usr/bin/env python3
"""Create and verify Coordinator beta package provenance sidecars.

The sidecar deliberately stays outside the installer archive so it can record
the archive digest without changing the Host plugin-pack format.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import re
import subprocess
import sys
import tarfile
from pathlib import Path, PurePosixPath

SCHEMA_VERSION = 1
SDK_COMMIT = "ff9b8b8ecfd32a7ca00708bbbbff330dc9ccc7a7"
HOST_REPOSITORY = "yattdev/kandev"
HOST_BRANCH = "feat-coordinator-plugin"
GATE_NAMES = (
    "artifact_package", "focused_tests", "contract", "full_suite_vet",
    "stage_1_adapter", "stage_1a_fixture_playwright",
    "paired_exact_head_host_plugin_integration", "stage_2_disposable_two_workspace_e2e",
    "independent_review", "distinct_qa", "plugin_pr_ci", "coordinator_acceptance",
)
VALID_GATE_STATES = {"not_run", "unavailable", "passed", "failed"}
HEX40 = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
REQUIRED_PACKAGE_ASSETS = frozenset({
    "ui/locales/en.json",
    "ui/locales/fr.json",
    "ui/locales/qps-ploc.json",
    "prompts/coordinator.md",
    "prompts/monitoring-cycle.md",
    "prompts/default-report-template.md",
    "prompts/RUNBOOK.md",
})


class ValidationError(Exception):
    pass


def fail(message: str) -> None:
    raise ValidationError(message)


def command(*args: str, cwd: Path) -> str:
    try:
        return subprocess.check_output(args, cwd=cwd, text=True, stderr=subprocess.PIPE).strip()
    except (OSError, subprocess.CalledProcessError) as error:
        fail(f"cannot run {' '.join(args)}: {error}")


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_plugin_identity(manifest: Path) -> tuple[str, str, set[str]]:
    """Parse only the small manifest subset needed for package validation."""
    text = manifest.read_text(encoding="utf-8")
    identifier = re.search(r'^id:\s*"?([^"\n#]+?)"?\s*$', text, re.M)
    version = re.search(r'^version:\s*"?([^"\n#]+?)"?\s*$', text, re.M)
    if not identifier or not version:
        fail("manifest.yaml must contain scalar id and version")
    executables = set(re.findall(r'^\s+[\w-]+:\s*"(server/plugin-[^"]+)"\s*$', text, re.M))
    if len(executables) != 5:
        fail("manifest.yaml must declare exactly five runtime executables")
    return identifier.group(1).strip(), version.group(1).strip(), executables


def safe_name(name: str) -> str:
    path = PurePosixPath(name)
    if not name or name.startswith("/") or "\\" in name or ".." in path.parts or path.is_absolute():
        fail(f"unsafe archive member: {name!r}")
    return path.as_posix()


def permitted_member(name: str, executables: set[str]) -> bool:
    return name in ({"manifest.yaml", "ui/bundle.js", "checksums.txt"} | executables) or (
        name.startswith("ui/locales/") and name.endswith(".json") and name.count("/") == 2
    ) or (name.startswith("prompts/") and name.endswith(".md") and name.count("/") == 1)


def parse_checksums(data: bytes) -> dict[str, str]:
    result: dict[str, str] = {}
    for raw_line in data.decode("utf-8").splitlines():
        match = re.fullmatch(r"([0-9a-f]{64})  (.+)", raw_line)
        if not match:
            fail("checksums.txt has an invalid line")
        digest, name = match.groups()
        name = safe_name(name)
        if name in result or name == "checksums.txt":
            fail("checksums.txt has duplicate or self-referential entry")
        result[name] = digest
    return result


def validate_archive(
    archive: Path,
    expected_id: str,
    expected_version: str,
    executables: set[str],
    expected_manifest: bytes,
) -> None:
    if not archive.is_file():
        fail(f"archive does not exist: {archive}")
    try:
        with tarfile.open(archive, "r:gz") as package:
            members = package.getmembers()
            names: list[str] = []
            payloads: dict[str, bytes] = {}
            for member in members:
                name = safe_name(member.name)
                if name in names:
                    fail(f"duplicate archive member: {name}")
                if not member.isfile():
                    fail(f"archive member is not a regular file: {name}")
                if not permitted_member(name, executables):
                    fail(f"unexpected archive member: {name}")
                source = package.extractfile(member)
                if source is None:
                    fail(f"cannot read archive member: {name}")
                names.append(name)
                payloads[name] = source.read()
    except (tarfile.TarError, OSError) as error:
        fail(f"invalid gzip tar archive: {error}")
    required = {"manifest.yaml", "ui/bundle.js", "checksums.txt", *executables, *REQUIRED_PACKAGE_ASSETS}
    if not required.issubset(payloads):
        fail("archive is missing required package members")
    if payloads["manifest.yaml"] != expected_manifest:
        fail("packaged manifest.yaml differs from source manifest.yaml")
    try:
        archive_id, archive_version, archive_executables = read_plugin_identity_from_text(payloads["manifest.yaml"])
    except UnicodeDecodeError:
        fail("packaged manifest.yaml is not UTF-8")
    if (archive_id, archive_version, archive_executables) != (expected_id, expected_version, executables):
        fail("packaged manifest identity or executable map differs from source manifest")
    checksums = parse_checksums(payloads["checksums.txt"])
    signed = set(payloads) - {"checksums.txt"}
    if set(checksums) != signed:
        fail("checksums.txt does not cover exactly the package payload")
    for name, digest in checksums.items():
        if hashlib.sha256(payloads[name]).hexdigest() != digest:
            fail(f"checksum mismatch for {name}")


def read_plugin_identity_from_text(data: bytes) -> tuple[str, str, set[str]]:
    text = data.decode("utf-8")
    identifier = re.search(r'^id:\s*"?([^"\n#]+?)"?\s*$', text, re.M)
    version = re.search(r'^version:\s*"?([^"\n#]+?)"?\s*$', text, re.M)
    executables = set(re.findall(r'^\s+[\w-]+:\s*"(server/plugin-[^"]+)"\s*$', text, re.M))
    if not identifier or not version or len(executables) != 5:
        fail("packaged manifest.yaml is malformed")
    return identifier.group(1).strip(), version.group(1).strip(), executables


def assert_clean_committed(root: Path) -> str:
    if command("git", "status", "--porcelain", cwd=root):
        fail("final artifact requires a clean source tree")
    commit = command("git", "rev-parse", "HEAD", cwd=root)
    if not HEX40.fullmatch(commit):
        fail("HEAD is not an exact 40-hex commit")
    return commit


def assert_pinned_sdk_checkout(root: Path) -> str:
    """Require the sibling SDK source used by plugin-pack to be the pinned commit."""
    sdk_root = root.parent / "kandev"
    if not sdk_root.is_dir():
        fail(f"pinned SDK checkout is missing: {sdk_root}")
    commit = command("git", "rev-parse", "HEAD", cwd=sdk_root)
    if commit != SDK_COMMIT:
        fail("pinned SDK checkout is not at the recorded commit")
    if command("git", "status", "--porcelain", cwd=sdk_root):
        fail("pinned SDK checkout must be clean")
    return commit


def default_gates() -> list[dict[str, object]]:
    external = {"stage_1_adapter", "paired_exact_head_host_plugin_integration", "stage_2_disposable_two_workspace_e2e", "plugin_pr_ci", "coordinator_acceptance"}
    return [{"name": name, "status": "unavailable" if name in external else "not_run", "evidence": []} for name in GATE_NAMES]


def validate_sidecar(data: object, root: Path, require_clean: bool) -> dict[str, object]:
    if not isinstance(data, dict) or set(data) != {"schema_version", "plugin", "build", "artifact", "host_candidate", "gates"}:
        fail("sidecar must contain exactly the documented top-level fields")
    if data["schema_version"] != SCHEMA_VERSION:
        fail("unsupported sidecar schema version")
    plugin = data["plugin"]
    build = data["build"]
    artifact = data["artifact"]
    host = data["host_candidate"]
    gates = data["gates"]
    if not all(isinstance(value, dict) for value in (plugin, build, artifact, host)) or not isinstance(gates, list):
        fail("sidecar sections have invalid types")
    manifest = root / "manifest.yaml"
    expected_manifest = manifest.read_bytes()
    expected_id, expected_version, executables = read_plugin_identity(manifest)
    if plugin != {"id": expected_id, "version": expected_version, "commit": plugin.get("commit")} or not HEX40.fullmatch(str(plugin.get("commit", ""))):
        fail("plugin identity or commit is malformed or differs from manifest.yaml")
    if set(build) != {"go_version", "node_version", "npm_version", "sdk_commit", "verification_command"} or build["sdk_commit"] != SDK_COMMIT:
        fail("build metadata is malformed or SDK pin differs")
    if not all(isinstance(build[key], str) and build[key] for key in build):
        fail("build metadata values must be non-empty strings")
    if set(artifact) != {"filename", "sha256"} or not isinstance(artifact["filename"], str) or "/" in artifact["filename"] or not SHA256.fullmatch(str(artifact["sha256"])):
        fail("artifact metadata is malformed")
    if set(host) != {"repository", "branch", "commit", "status"} or host.get("repository") != HOST_REPOSITORY or host.get("branch") != HOST_BRANCH:
        fail("future Host candidate identifies the wrong repository or branch")
    if host.get("commit") is None:
        if host.get("status") != "unavailable":
            fail("a null future Host candidate must be unavailable")
    elif not HEX40.fullmatch(str(host.get("commit"))) or host.get("status") not in {"recorded", "validated"}:
        fail("future Host candidate commit or status is malformed")
    if len(gates) != len(GATE_NAMES) or {gate.get("name") for gate in gates if isinstance(gate, dict)} != set(GATE_NAMES):
        fail("gate ledger must enumerate every required gate exactly once")
    for gate in gates:
        if set(gate) != {"name", "status", "evidence"} or gate["status"] not in VALID_GATE_STATES or not isinstance(gate["evidence"], list) or not all(isinstance(item, str) and item for item in gate["evidence"]):
            fail("gate is malformed")
        if gate["status"] == "passed" and not gate["evidence"]:
            fail(f"passed gate {gate['name']} has no evidence")
        if gate["name"] == "coordinator_acceptance" and gate["status"] == "passed":
            fail("sidecar cannot declare Coordinator acceptance")
    paired_gate = next(gate for gate in gates if gate["name"] == "paired_exact_head_host_plugin_integration")
    if host.get("status") == "validated" and not (paired_gate["status"] == "passed" and paired_gate["evidence"]):
        fail("a validated Host candidate requires passed paired integration evidence")
    archive = root / artifact["filename"]
    if not archive.is_file():
        fail(f"archive does not exist: {archive}")
    if sha256_file(archive) != artifact["sha256"]:
        fail("artifact SHA-256 differs from sidecar")
    validate_archive(archive, expected_id, expected_version, executables, expected_manifest)
    if require_clean and assert_clean_committed(root) != plugin["commit"]:
        fail("sidecar plugin commit is not the clean checked-out HEAD")
    return data


def create(args: argparse.Namespace) -> None:
    root = Path(args.root).resolve()
    archive = Path(args.archive).resolve()
    output = Path(args.output).resolve()
    commit = assert_clean_committed(root)
    assert_pinned_sdk_checkout(root)
    manifest = root / "manifest.yaml"
    expected_manifest = manifest.read_bytes()
    identifier, version, executables = read_plugin_identity(manifest)
    validate_archive(archive, identifier, version, executables, expected_manifest)
    tool_versions = {
        "go_version": command("go", "version", cwd=root),
        "node_version": command("node", "--version", cwd=root),
        "npm_version": command("npm", "--version", cwd=root),
        "sdk_commit": SDK_COMMIT,
        "verification_command": args.verification_command,
    }
    data = {"schema_version": SCHEMA_VERSION, "plugin": {"id": identifier, "version": version, "commit": commit}, "build": tool_versions, "artifact": {"filename": archive.name, "sha256": sha256_file(archive)}, "host_candidate": {"repository": HOST_REPOSITORY, "branch": HOST_BRANCH, "commit": None, "status": "unavailable"}, "gates": default_gates()}
    output.write_text(json.dumps(data, indent=2, sort_keys=True) + "\n", encoding="utf-8")


def verify(args: argparse.Namespace) -> None:
    root = Path(args.root).resolve()
    try:
        data = json.loads(Path(args.sidecar).read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        fail(f"cannot read sidecar JSON: {error}")
    validate_sidecar(data, root, args.require_clean)


def main() -> int:
    parser = argparse.ArgumentParser()
    subcommands = parser.add_subparsers(dest="action", required=True)
    create_parser = subcommands.add_parser("create")
    create_parser.add_argument("--root", default=".")
    create_parser.add_argument("--archive", required=True)
    create_parser.add_argument("--output", required=True)
    create_parser.add_argument("--verification-command", required=True)
    verify_parser = subcommands.add_parser("verify")
    verify_parser.add_argument("--root", default=".")
    verify_parser.add_argument("--sidecar", required=True)
    verify_parser.add_argument("--require-clean", action="store_true")
    args = parser.parse_args()
    try:
        (create if args.action == "create" else verify)(args)
    except ValidationError as error:
        print(f"beta artifact validation failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
