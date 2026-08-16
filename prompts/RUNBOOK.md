# Coordinator operator runbook

Configure cadence and the default base prompt under **Settings > Plugins >
Coordinator**. Configure monitoring per workspace workflow under **Settings >
Workspace > Workflow**: select the worksteps the coordinator may monitor and,
if useful, add a prompt to each selected workstep. No selected worksteps means
that workflow is not scheduled for monitoring. An empty workstep prompt is
valid and adds no instruction.

For every check Kandev composes the editable base prompt, the selected
workstep prompt, and the coordinator safety invariants. The safety invariants
are always appended and cannot be changed by either setting.

The Coordinator route is read-only conversation chrome. Use the workflow
configuration to change monitoring policy; use the task conversation to direct
an individual coordinator run.
