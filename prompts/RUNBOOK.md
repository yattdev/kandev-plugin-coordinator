# Coordinator operator runbook

Configure schedule, optional agent-profile override, base prompt, and report
template under **Settings > Plugins > Coordinator**. An empty profile override
uses each workspace's effective default profile. A missing, disabled, or
incompatible effective profile produces `configuration_required` without a
partial conversation.

Configure monitoring in each workspace's workflow settings. Select only the
steps the Coordinator may inspect and optionally add a multiline prompt. The
plugin reads `coordinator_monitored` and `coordinator_prompt` directly from the
host; it stores no shadow policy. No selected steps means no scheduled or
manual dispatch for that workspace.

The daily wake defaults to 07:55 America/Montreal on weekdays. A successful
daily dispatch arms 45-minute monitoring cycles inside the 08:00-18:00 window.
Use **Run cycle now** or **Run standup now** for an explicit run before arming
or after a failed cadence. Stable occurrence keys make scheduler retries
idempotent. A busy conversation is recorded as `skipped_busy` and is tried
again only at the next eligible cadence or by a new manual invocation.

For every run, Kandev composes the editable base prompt, the ordered selected
step instructions, the trigger/report instruction, and fixed safety
invariants. Configured text can never follow or remove the invariant block.

Coordinator state and report history are workspace-scoped. Report history is
newest-first, cursor-paginated, and capped at 200 artifacts. Recent cycle logs
are capped at 200; entries older than seven days are compacted by ISO week.
Disabling, upgrading, or restarting preserves the host-managed conversation
and state. Uninstall cleanup is host-owned and provenance-scoped to this
plugin.

This release requires a Kandev build containing the `agent_conversation`
capability, `Host.AgentConversations`, host-owned workflow-step coordinator
settings, and `host.ui.WorkspaceAgentChat`. Older hosts show an explicit
compatibility state rather than creating a visible task fallback.
