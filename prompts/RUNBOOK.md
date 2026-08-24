# Coordinator operator runbook

Configure the optional agent-profile override, base prompt, and report template
under **Settings > Plugins > Coordinator**. Kandev Automations owns schedules
and event triggers; this plugin starts no ticker, cron job, or scheduler. An
empty profile override uses each workspace's effective default profile. A missing, disabled, or
incompatible effective profile produces `configuration_required` without a
partial conversation.

Configure monitoring in each workspace's workflow settings. Select only the
steps the Coordinator may inspect and optionally add a multiline prompt. The
plugin reads `coordinator_monitored` and `coordinator_prompt` directly from the
host; it stores no shadow policy.

Create an Automation with the Workspace Coordinator, an operator-selected
agent/model, a safe workspace scope, and one of these templates. Expected output
is a bounded artifact or visible human escalation.

### wakeup:cycle / board reconciliation

Recommended trigger: periodic or board-change Automation. Scope it to monitored
workflow steps. Prompt: `WAKE:CYCLE Reconcile monitored board health, follow-up
ledger, Done integrity, Coordinator Todo handoffs, and pending human/Human-QA
obligations.`

### PR/MR fixup cycle

Recommended trigger: PR/MR check or review event. Scope it to linked workspace
and PR/MR metadata. Prompt: `WAKE:PR_FIXUP Reconcile exact-head checks, review
threads, blockers, and the next human ask.` Never merge, archive, delete, or
perform terminal cleanup.

### daily standup cycle

Recommended trigger: an operator-selected daily Automation. Scope it to the
workspace. Prompt: `WAKE:STANDUP Produce Needs your decision, Awaiting your
testing, Watch, FYI, and Board pulse.`

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
