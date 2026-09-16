import { CoordinatorClient } from "./coordinator-client";
import type { CoordinatorHost, WorkflowPolicy } from "./contracts";

// This belongs on the workspace integration page, so policy never leaks from
// one workspace into another. The server validates every selected ID against
// generic workflow readers before persisting it.
export function createCoordinatorSettings(host: CoordinatorHost) {
  const React = host.React;
  const h = React.createElement;
  return function CoordinatorSettings({ workspaceId }: { workspaceId?: string }) {
    const [value, setValue] = React.useState("[]");
    const [message, setMessage] = React.useState("");
    React.useEffect(() => {
      if (!workspaceId) {
        setMessage("Choose a workspace to configure monitoring.");
        return;
      }
      void new CoordinatorClient(host, workspaceId).policy().then(
        ({ selections }) => setValue(JSON.stringify(selections, null, 2)),
        (error: unknown) => setMessage(error instanceof Error ? error.message : String(error)),
      );
    }, [workspaceId]);
    const save = () => {
      if (!workspaceId) return;
      let selections: WorkflowPolicy[];
      try {
        selections = JSON.parse(value) as WorkflowPolicy[];
      } catch {
        setMessage("Selections must be valid JSON.");
        return;
      }
      if (!Array.isArray(selections)) {
        setMessage("Selections must be a JSON array.");
        return;
      }
      void new CoordinatorClient(host, workspaceId).savePolicy(selections).then(
        () => setMessage("Monitoring selections saved."),
        (error: unknown) => setMessage(error instanceof Error ? error.message : String(error)),
      );
    };
    const Button = host.ui.Button ?? "button";
    return h("div", { className: "space-y-3" },
      h("p", { className: "text-sm text-muted-foreground" }, "Use rows with workflow_id, workstep_id, and an optional prompt. Deleted steps remain saved but do not dispatch."),
      h("textarea", { className: "min-h-48 w-full rounded-md border p-3 font-mono text-sm", value, onChange: (event: { target: { value: string } }) => setValue(event.target.value), "aria-label": "Coordinator monitoring selections" }),
      h(Button, { type: "button", className: "min-h-11 px-4", onClick: save }, "Save monitoring selections"),
      message ? h("p", { role: "status" }, message) : null,
    );
  };
}
