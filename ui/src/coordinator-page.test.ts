import { describe, expect, it } from "vitest";
import { updateCurrentWorkspace } from "./coordinator-page";

describe("CoordinatorPage workspace isolation", () => {
  it("ignores asynchronous updates from a previous workspace", () => {
    const current = { workspaceId: "workspace-b", reports: [] as string[], loading: true };
    const update = updateCurrentWorkspace("workspace-a", (state) => ({
      ...state, loading: false, reports: ["stale"],
    }));
    expect(update(current)).toBe(current);
  });

  it("applies asynchronous updates for the current workspace", () => {
    const current = { workspaceId: "workspace-a", reports: [] as string[], loading: true };
    const update = updateCurrentWorkspace("workspace-a", (state) => ({
      ...state, loading: false, reports: ["current"],
    }));
    expect(update(current)).toEqual({ workspaceId: "workspace-a", reports: ["current"], loading: false });
  });
});
