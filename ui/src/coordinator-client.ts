import type { CoordinatorHost, EnsureResponse, ReportPage, StatusResponse } from "./contracts";

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

}
