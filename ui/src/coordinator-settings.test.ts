import { describe, expect, it } from "vitest";
import { acceptPolicyResponse } from "./coordinator-settings";

describe("CoordinatorSettings workspace isolation", () => {
  it("rejects a deferred workspace A policy response after switching to workspace B", () => {
    const responseWorkspace = "workspace-a";
    const currentWorkspace = "workspace-b";
    expect(acceptPolicyResponse(currentWorkspace, responseWorkspace)).toBe(false);
  });

  it("accepts only the response for the currently loaded workspace", () => {
    expect(acceptPolicyResponse("workspace-b", "workspace-b")).toBe(true);
  });
});
