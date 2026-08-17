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

### Local build recipe

When the SDK checkout is elsewhere, use the shared Go workspace explicitly:

```sh
GOWORK=/tmp/coordinator-go.work mise exec -- make test
GOWORK=/tmp/coordinator-go.work mise exec -- make verify-package-host KANDEV_SDK=/data/repos/workspaces/2e62401b-5ffe-4050-bc1b-d49ea5d5dbcd/github/kdlbs/kandev/apps/backend
```
