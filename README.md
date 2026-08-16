# kandev-plugin-coordinator

A native Kandev plugin for operating a long-lived Coordinator task. It adds a
localized `/coordinator` route in the **Integrations** navigation and packages
the Coordinator prompt as a versioned plugin asset.

## Runtime contract

The backend keeps reports and workflow policies in verified workspace state. Installation settings own cadence, agent profile, and the editable base prompt. Workflow settings select monitored worksteps and assign an optional prompt to each selected workstep. Each check composes base prompt, workstep prompt, and non-overridable safety invariants.

The UI uses the host-provided `AgentConversations` and `WorkspaceAgentChat` components. Until the matching host API is present, it displays an explicit compatibility message rather than bundling an alternate chat UI.

## Development

The SDK remains a local sibling checkout dependency. With the standard layout,
run `make test-backend` and `make package-host`. The generated archive includes
`prompts/coordinator.md`; it has no runtime dependency on the source prompt.
