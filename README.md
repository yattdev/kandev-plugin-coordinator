# kandev-plugin-coordinator

A native Kandev plugin for a permanent, workspace-scoped supervising agent.
It adds one localized **Coordinator** destination under **Integrations** and a
full-height `/coordinator` route with native chat, typed reports, manual run
controls, and a direct settings link. The backing task/session is host-managed,
workflowless, ephemeral, and hidden from Kanban and task lists.

## Compatibility

Development and CI currently pin Kandev commit
`ff9b8b8ecfd32a7ca00708bbbbff330dc9ccc7a7`, which provides:

- `capabilities.agent_conversation` and `Host.AgentConversations` with
  Ensure/Dispatch/Delete;
- durable atomic occurrence idempotency and busy-session coalescing;
- effective workspace default agent-profile resolution before conversation
  creation;
- persisted workflow-step `coordinator_monitored` and `coordinator_prompt`;
- the host-owned `host.ui.WorkspaceAgentChat` component.

No released Kandev tag contains that commit yet (`v0.89.0-76-gff9b8b8ec` as
of 2026-08-20), so the manifest intentionally does not claim an older minimum.
Set `min_kandev_version` to the first containing release before publishing this
plugin. Older hosts show an explicit compatibility state; there is no visible
task fallback.

## Configuration and scheduling

Installation settings own the optional agent-profile override, editable base
prompt, report template, timezone, day mode, cadence, and window. A blank
profile override uses each workspace's effective default. Invalid, disabled,
or missing effective profiles return `configuration_required` without creating
a partial conversation.

Defaults are weekdays, America/Montreal, daily standup at 07:55, and monitoring
every 45 minutes from 08:00 through 18:00. Monitoring cycles arm only after the
first successful daily dispatch. Manual cycle and standup actions work before
arming and use caller-specific idempotency keys.

Workflow settings are the only monitoring-policy source. The plugin reads the
host-owned monitored flag and optional multiline prompt on each workflow step;
it persists no shadow policy. Each occurrence batches selected steps in a
deterministic order and appends fixed safety invariants after all editable
content.

## State, lifecycle, and security

Coordinator memory and typed cycle/daily/status reports are workspace-scoped
Host state. Reports are newest-first, cursor-paginated, and capped at 200.
Cycle logs are capped at 200 and entries older than seven days compact by ISO
week. Dispatch failures and `skipped_busy` results become status artifacts.

Disable, config restart, and upgrade stop the cancellable scheduler while
preserving conversation and state. Re-enable repairs/reuses the same stable
`coordinator` conversation. Uninstall cleanup is performed by Kandev using
server-stamped plugin provenance.

The manifest grants state, managed conversation, and read-only
workspace/workflow access. Browser actions and agent tools use host-verified
workspace context; body identifiers never select another workspace. The plugin
does not receive task/message write permission and never calls `Tasks.Create`
for a run.

## Durable state and policy contract

`server/durablestate` provides the plugin-owned SQLite materialized-state,
append-only mutation log, snapshots, hash-anchored compaction, deterministic
restore/replay, and fencing primitives defined by the pinned Coordinator state
contract. It remains a library boundary; scheduler and host transport policy
stay in `server/coordinator`.

The exact upstream Coordinator policy contract and standalone validator are
vendored under `docs/contracts/upstream`. The repository validates both those
bytes and this plugin's defaults snapshot in CI, failing closed on authority,
gate, queue/receipt, or Done-integrity drift. See
[`docs/contracts/README.md`](docs/contracts/README.md) for the checks and
[`docs/contracts/PROVENANCE.md`](docs/contracts/PROVENANCE.md) for the immutable
source pin and refresh procedure.

## Development

The SDK is currently a sibling checkout because `pkg/pluginsdk` is not a
standalone published module:

```text
parent/
├── kandev/apps/backend
├── kandev/apps/packages/plugin-sdk
└── kandev-plugin-coordinator
```

Install development dependencies and run the full verification:

```sh
npm ci --ignore-scripts --include=dev
make test
make vet
make verify-contract
make verify-package-host
```

When the host checkout lives elsewhere, create a temporary Go workspace that
replaces `github.com/kandev/kandev` with that checkout, then export `GOWORK` for
Go and Make commands. `make build-ui` reproducibly generates the checked-in
`ui/bundle.js` without bundling React. `make verify-package` cross-compiles all
declared platforms and produces `kandev-plugin-coordinator-0.1.0.tar.gz` with
checksums, UI/locales, and self-contained prompt assets; recipes and development
sources are excluded.
