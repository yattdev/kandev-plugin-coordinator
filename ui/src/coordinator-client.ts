import type { AutomationBinding, AutomationPage, CoordinatorHost, EnsureResponse, ReportPage, StatusResponse } from "./contracts";

export class CoordinatorClient {
  constructor(private readonly host: CoordinatorHost, private readonly workspaceId: string) {}

  ensure(signal?: AbortSignal): Promise<EnsureResponse> {
    return this.host.api.invokeAction<EnsureResponse>("coordinator.ensure", { workspaceId: this.workspaceId }, { signal });
  }

  reports(cursor = "", signal?: AbortSignal): Promise<ReportPage> {
    return this.host.api.invokeAction<ReportPage>("coordinator.reports", {
      workspaceId: this.workspaceId,
      body: { cursor, limit: 20 },
    }, { signal });
  }

  status(signal?: AbortSignal): Promise<StatusResponse> {
    return this.host.api.invokeAction<StatusResponse>("coordinator.status", { workspaceId: this.workspaceId }, { signal });
  }

  automations(signal?: AbortSignal): Promise<AutomationPage> {
    return this.host.api.invokeAction<AutomationPage>("coordinator.automations", { workspaceId: this.workspaceId }, { signal });
  }

  bindAutomations(automationIds: string[], signal?: AbortSignal): Promise<{ bindings: AutomationBinding[] }> {
    return this.host.api.invokeAction("coordinator.automation-bind", { workspaceId: this.workspaceId, body: { automation_ids: automationIds } }, { signal });
  }

}
