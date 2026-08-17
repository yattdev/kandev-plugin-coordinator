import { describe, expect, it, vi } from "vitest";
import { CoordinatorClient } from "./coordinator-client";
import type { CoordinatorHost } from "./contracts";

describe("CoordinatorClient", () => {
  it("uses declared authenticated actions without sending workspace authority", async () => {
    const invokeAction = vi.fn(async () => ({ status: "ready", reports: [] }));
    const client = new CoordinatorClient({ api: { invokeAction } } as unknown as CoordinatorHost);

    await client.ensure();
    await client.reports("cursor-1");
    await client.run("cycle", "manual-1");

    expect(invokeAction.mock.calls).toEqual([
      ["coordinator.ensure", {}, { signal: undefined }],
      ["coordinator.reports", { cursor: "cursor-1", limit: 20 }, { signal: undefined }],
      ["coordinator.run-cycle", { idempotency_key: "manual-1" }, { signal: undefined }],
    ]);
  });
});
