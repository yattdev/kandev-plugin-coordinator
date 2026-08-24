import { describe, expect, it, vi } from "vitest";
import { CoordinatorClient } from "./coordinator-client";
import type { CoordinatorHost } from "./contracts";

describe("CoordinatorClient", () => {
  it("uses declared authenticated actions without placing workspace authority in the body", async () => {
		const invokeAction = vi.fn(async () => ({ status: "ready", reports: [] }));
		const client = new CoordinatorClient({ api: { invokeAction } } as unknown as CoordinatorHost, "workspace-1");

    await client.ensure();
    await client.reports("cursor-1");
		await client.status();
		await client.automations();
		await client.bindAutomations(["automation-1"]);

		expect(invokeAction.mock.calls).toEqual([
			["coordinator.ensure", { workspaceId: "workspace-1" }, { signal: undefined }],
			["coordinator.reports", { workspaceId: "workspace-1", body: { cursor: "cursor-1", limit: 20 } }, { signal: undefined }],
			["coordinator.status", { workspaceId: "workspace-1" }, { signal: undefined }],
			["coordinator.automations", { workspaceId: "workspace-1" }, { signal: undefined }],
			["coordinator.automation-bind", { workspaceId: "workspace-1", body: { automation_ids: ["automation-1"] } }, { signal: undefined }],
		]);
  });
});
