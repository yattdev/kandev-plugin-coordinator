# kandev-plugin-coordinator

A native Kandev plugin for a permanent, workspace-scoped supervising agent.
It is a Stage-0 compatibility slice for a durable, workspace-level Coordinator.
The current hidden conversation is replaceable transport only, never Coordinator
identity or authority. The final product places Coordinator immediately after
Integrations and uses Kandev Automations for wake and event delivery.

## Compatibility

Development and CI currently pin Kandev commit
`ec6fd3632d8680b0e7c0a1b3c802588a26fc0b09`, which provides:

- `capabilities.agent_conversation` and `Host.AgentConversations` with
  Ensure/Dispatch/Delete;
- durable atomic occurrence idempotency and busy-session coalescing;
- effective workspace default agent-profile resolution before conversation
  creation;
- persisted workflow-step `coordinator_monitored` and `coordinator_prompt`;
- the host-owned `host.ui.WorkspaceAgentChat` component.
- `Host.TaskRelations().Get`, a compact, workspace-filtered relation graph with
  no descriptions, documents, metadata, or repository data.
- safe workspace Automation descriptors and server-stamped Automation delivery;
- opaque Workspace Coordinator principal Descriptor/Status/Audit projections.

No released Kandev tag contains that commit yet (`v0.89.0-76-gff9b8b8ec` as
of 2026-08-20), so the manifest intentionally does not claim an older minimum.
Set `min_kandev_version` to the first containing release before publishing this
plugin. Older hosts show an explicit compatibility state; there is no visible
task fallback.

## Configuration and Automations

Installation settings own the optional agent-profile override, editable base
prompt, and report template. Kandev Automations owns schedules and events; this
plugin starts no ticker, cron, or scheduler. Configure an Automation with the
Workspace Coordinator, an operator-selected agent/model, a safe workspace scope,
and a Coordinator prompt/template.

For each workspace, bind the operator-selected Automation IDs through the
authenticated `coordinator.automation-bind` action. On a server-stamped
`automation.triggered` delivery, the plugin re-reads that workspace descriptor,
uses its selected agent profile and prompt with plugin-owned policy, and
dispatches one idempotent managed-conversation occurrence. Unbound, foreign, or
disabled Automations do not dispatch work. The binding retains no schedule,
webhook secret, repository binding, or run history.

The packaged runbook includes templates for board reconciliation, PR/MR fixup,
and daily standup Automations. Each template documents trigger, Coordinator
identity, agent/model choice, safe scope, expected output, and human escalation.

Workflow settings are the only monitoring-policy source. The plugin reads the
host-owned monitored flag and optional multiline prompt on each workflow step;
it persists no shadow policy. Each occurrence batches selected steps in a
deterministic order and appends fixed safety invariants after all editable
content.

## State, lifecycle, and security

Coordinator memory and typed cycle/daily/status reports are workspace-scoped
Host state. Its durable identity is the logical `coordinator` key, not the
replaceable backing task or session. A bounded run ledger, reply follow-up
ledger, and Inbox projection survive execution replacement. Reports are
newest-first, cursor-paginated, and capped at 200.
Cycle logs are capped at 200 and entries older than seven days compact by ISO
week. Dispatch failures and `skipped_busy` results become status artifacts.

Disable, config restart, and upgrade preserve Coordinator policy and history;
Automation schedules remain host-owned. Re-enable may repair a replaceable
execution session without changing the durable Coordinator identity. Uninstall
cleanup is performed by Kandev using server-stamped provenance.

The initial page opens on Overview/Inbox, exposes the continuous logical Chat
through the native host component, and keeps Reports available for history.
When a durable principal, host inbox, or Automation setup API is not supplied,
Overview renders an explicit typed unavailable state. It never fabricates a
grant, consumes a backing task as identity, or installs a fallback schedule.

The manifest grants state, managed conversation, and read-only
workspace/workflow access. Browser actions and agent tools use host-verified
workspace context; body identifiers never select another workspace. The plugin
does not receive task/message write permission and never calls `Tasks.Create`
for a run.

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
make verify-package-host
```

When the host checkout lives elsewhere, create a temporary Go workspace that
replaces `github.com/kandev/kandev` with that checkout, then export `GOWORK` for
Go and Make commands. `make build-ui` reproducibly generates the checked-in
`ui/bundle.js` without bundling React. `make verify-package` cross-compiles all
declared platforms and produces `kandev-plugin-coordinator-0.1.0.tar.gz` with
checksums, UI/locales, and self-contained prompt assets; recipes and development
sources are excluded.
