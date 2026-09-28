# Coordinator beta package validation handoff

This handoff creates a candidate for a disposable Kandev instance. It is not a
release and it does not make the Coordinator beta-ready.

## Build and verify

Use a clean, committed plugin checkout at an exact commit. The required SDK is
the sibling checkout described in the README, pinned to
`kdlbs/kandev@ff9b8b8ecfd32a7ca00708bbbbff330dc9ccc7a7`; its backend must be
available at `../kandev/apps/backend`. Install the locked Node dependencies
with `npm ci --ignore-scripts --include=dev` first.

Run:

```sh
make reproducible-beta
```

The command invokes the existing Host `plugin-pack`, leaving the installer
archive layout unchanged. It builds the five declared runtime executables,
creates `kandev-plugin-coordinator-0.1.0.tar.gz`, builds it again, compares the
full bytes, prints both SHA-256 values, writes
`kandev-plugin-coordinator-0.1.0.validation.json`, and verifies that pair.
`make verify-beta-artifact` rechecks an existing pair without rebuilding it.

The reproducibility claim is limited to two sequential builds on this machine,
from the same clean commit and local Go/Node/npm toolchain with the stated SDK
pin. It does not establish cross-machine reproducibility or compatibility with
an unpinned Host.

## Sidecar contract

The JSON sidecar is intentionally outside the tar.gz archive, so it can record
the archive SHA-256 without altering the installer-visible package or creating
a self-checksum cycle. It records schema version, the `manifest.yaml` plugin
id/version, exact plugin commit, Go/Node/npm versions, immutable SDK commit,
archive filename and digest, and the verification command.

`host_candidate` is deliberately separate from `build.sdk_commit`:

```json
{
  "repository": "yattdev/kandev",
  "branch": "feat-coordinator-plugin",
  "commit": null,
  "status": "unavailable"
}
```

Do not replace it with the upstream SDK pin. A later paired validation may
record an exact 40-hex fork head with `status: "recorded"`; it may use
`"validated"` only when the paired integration gate is passed with evidence.

The verifier rejects a dirty final source tree, malformed or mismatched plugin
identity/commit, archive digest changes, unsafe or unexpected package paths,
missing payload checksums, invalid checksums, malformed gate ledgers, and a
`passed` gate without evidence. It also rejects any claimed Coordinator
acceptance in this candidate sidecar.

## Validation ledger

The sidecar always enumerates these gates. Local gates begin as `not_run` and
external inputs begin as `unavailable`; neither state means passed or
beta-ready. Record commands, exact commit heads, CI URLs, and reviewer/QA
reports as evidence only when the owning gate has actually run.

| Gate | Initial state | Evidence to attach when run |
| --- | --- | --- |
| Artifact/package | not_run | archive SHA-256 and verifier command |
| Focused tests | not_run | command and result |
| Contract | not_run | `make verify-contract` result |
| Full suite/vet | not_run | `make test` and `make vet` results |
| Stage 1 adapter | unavailable | exact adapter head and validation |
| Stage 1a fixture Playwright | not_run | `npm run test:ui:browser` result |
| Paired exact-head Host/plugin integration | unavailable | both exact heads and integration result |
| Stage 2 disposable two-workspace E2E | unavailable | environment and result |
| Independent Review | not_run | reviewer report |
| Distinct QA | not_run | QA report from a separate session |
| Plugin PR/CI | unavailable | exact PR head and CI result |
| Coordinator acceptance | unavailable | Coordinator decision after all required evidence |

## Disposable-instance procedure

1. Copy both generated files to a disposable instance. In that instance's
   plugin settings, upload the `.tar.gz` through the normal plugin installer.
2. Confirm the installer accepts the archive and the Coordinator integration
   appears. Confirm its UI bundle, the three locales, prompt assets, and the
   runtime executable selected for that host platform are present. Do not use
   production credentials or a production board.
3. Record the exact plugin commit, archive SHA-256, exact Host commit (when
   available), installer observations, and test evidence in the paired gate
   ledger. A disposable test may be rolled back by uninstalling the plugin
   through that instance's plugin settings and deleting its disposable
   workspace; preserve the sidecar and test record first.

Coordinator acceptance remains the final beta-ready gate because it must
review the later paired Host/plugin evidence, adapter evidence, independent
review, distinct QA, PR CI tied to the exact head, and the Stage 2
two-workspace result. The requested
`docs/COORDINATOR_PLUGIN_TESTABLE_ROADMAP.md` was absent from the task base, so
this handoff relies on the explicit task contract and existing Stage 0/Stage 1a
documentation rather than claiming that missing roadmap's contents.
