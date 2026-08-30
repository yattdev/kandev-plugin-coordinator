import type { CoordinatorHost, EnsureResponse, ReportPage, RunResponse } from "./contracts";

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

  run(trigger: "cycle" | "standup", idempotencyKey: string, signal?: AbortSignal): Promise<RunResponse> {
    const key = trigger === "cycle" ? "coordinator.run-cycle" : "coordinator.run-standup";
    return this.host.api.invokeAction<RunResponse>(key, {
      workspaceId: this.workspaceId,
      body: { idempotency_key: idempotencyKey },
    }, { signal });
  }
}

export function manualRunKey(trigger: string): string {
  const cryptoApi = globalThis.crypto;
  const value = cryptoApi && "randomUUID" in cryptoApi ? cryptoApi.randomUUID() : `${Date.now()}-${Math.random()}`;
  return `${trigger}-${value}`;
}
