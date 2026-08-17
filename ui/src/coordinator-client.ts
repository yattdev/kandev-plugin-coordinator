import type { CoordinatorHost, EnsureResponse, ReportPage } from "./contracts";

export class CoordinatorClient {
  constructor(private readonly host: CoordinatorHost) {}

  ensure(signal?: AbortSignal): Promise<EnsureResponse> {
    return this.host.api.invokeAction<EnsureResponse>("coordinator.ensure", {}, { signal });
  }

  reports(cursor = "", signal?: AbortSignal): Promise<ReportPage> {
    return this.host.api.invokeAction<ReportPage>("coordinator.reports", { cursor, limit: 20 }, { signal });
  }

  run(trigger: "cycle" | "standup", idempotencyKey: string, signal?: AbortSignal): Promise<unknown> {
    const key = trigger === "cycle" ? "coordinator.run-cycle" : "coordinator.run-standup";
    return this.host.api.invokeAction(key, { idempotency_key: idempotencyKey }, { signal });
  }
}

export function manualRunKey(trigger: string): string {
  const cryptoApi = globalThis.crypto;
  const value = cryptoApi && "randomUUID" in cryptoApi ? cryptoApi.randomUUID() : `${Date.now()}-${Math.random()}`;
  return `${trigger}-${value}`;
}
