import { CoordinatorClient } from "./coordinator-client";
import type { CoordinatorHost, WorkflowPolicy } from "./contracts";

// This belongs on the workspace integration page, so policy never leaks from
// one workspace into another. The server validates every selected ID against
// generic workflow readers before persisting it.
export function createCoordinatorSettings(host: CoordinatorHost) {
  const React = host.React;
  const h = React.createElement;
  return function CoordinatorSettings({ workspaceId }: { workspaceId?: string } = {}) {
    const [value, setValue] = React.useState("[]");
    const [message, setMessage] = React.useState("");
    const [loadedWorkspace, setLoadedWorkspace] = React.useState<string | undefined>(undefined);
    const [saving, setSaving] = React.useState(false);
    const currentWorkspace = React.useRef(workspaceId);
    currentWorkspace.current = workspaceId;
    React.useEffect(() => {
		const controller = new AbortController();
		setLoadedWorkspace(undefined);
		setSaving(false);
		setValue("[]");
      if (!workspaceId) {
        setMessage("Choose a workspace to configure monitoring.");
		return () => controller.abort();
      }
      setMessage("");
      const responseWorkspace = workspaceId;
      void new CoordinatorClient(host, responseWorkspace).policy(controller.signal).then(
        ({ selections }) => {
          if (!acceptPolicyResponse(currentWorkspace.current, responseWorkspace)) return;
          setValue(JSON.stringify(selections, null, 2));
          setLoadedWorkspace(responseWorkspace);
        },
        (error: unknown) => {
          if (controller.signal.aborted || !acceptPolicyResponse(currentWorkspace.current, responseWorkspace)) return;
          setMessage(error instanceof Error ? error.message : String(error));
        },
      );
		return () => controller.abort();
    }, [workspaceId]);
    const save = () => {
		if (!workspaceId || loadedWorkspace !== workspaceId || saving) return;
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
		const responseWorkspace = workspaceId;
		setSaving(true);
		void new CoordinatorClient(host, responseWorkspace).savePolicy(selections).then(
        () => {
          if (!acceptPolicyResponse(currentWorkspace.current, responseWorkspace)) return;
          setMessage("Monitoring selections saved.");
          setSaving(false);
        },
        (error: unknown) => {
          if (!acceptPolicyResponse(currentWorkspace.current, responseWorkspace)) return;
          setMessage(error instanceof Error ? error.message : String(error));
          setSaving(false);
        },
      );
    };
    const Button = host.ui.Button ?? "button";
    return h("div", { className: "space-y-3" },
      h("p", { className: "text-sm text-muted-foreground" }, "Use rows with workflow_id, workstep_id, and an optional prompt. Deleted steps remain saved but do not dispatch."),
      h("textarea", { className: "min-h-48 w-full rounded-md border p-3 font-mono text-sm", value, disabled: loadedWorkspace !== workspaceId, onChange: (event: { target: { value: string } }) => setValue(event.target.value), "aria-label": "Coordinator monitoring selections" }),
      h(Button, { type: "button", className: "min-h-11 px-4", disabled: loadedWorkspace !== workspaceId || saving, onClick: save }, "Save monitoring selections"),
      message ? h("p", { role: "status" }, message) : null,
    );
  };
}

// A late action response may affect only the workspace that initiated it.
export function acceptPolicyResponse(currentWorkspace: string | undefined, responseWorkspace: string): boolean {
  return currentWorkspace === responseWorkspace;
}
