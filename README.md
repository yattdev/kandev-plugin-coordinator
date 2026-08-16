# kandev-plugin-coordinator

A native Kandev plugin for operating a long-lived Coordinator task. It adds a
localized `/coordinator` route in the **Integrations** navigation and packages
the Coordinator prompt as a versioned plugin asset.

## Runtime contract

The backend keeps the most recent cycle/report in Kandev plugin state, scoped
to the verified workspace. It exposes two authenticated workspace actions:
`coordinator.status` and `coordinator.report`; it also exposes equivalent
agent tools for coordinator conversations. The coordinator never accepts a
workspace identifier from untrusted action data.

Operator settings identify the coordinator task and configure the local
weekday standup schedule. The plugin computes the next wake time; the host
owns actual wake-job delivery.

The UI uses the host-provided `AgentConversations` and `WorkspaceAgentChat`
components. Until the matching host API is present, it displays an explicit
compatibility message rather than bundling an alternate chat UI.

## Development

The SDK remains a local sibling checkout dependency. With the standard layout,
run `make test-backend` and `make package-host`. The generated archive includes
`prompts/coordinator.md`; it has no runtime dependency on the source prompt.
