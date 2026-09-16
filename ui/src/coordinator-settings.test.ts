import { describe, expect, it, vi } from "vitest";
import { acceptPolicyResponse, createCoordinatorSettings } from "./coordinator-settings";
import type { CoordinatorHost } from "./contracts";

describe("CoordinatorSettings workspace isolation", () => {
  it("rejects a deferred workspace A policy response after switching to workspace B", () => {
    const responseWorkspace = "workspace-a";
    const currentWorkspace = "workspace-b";
    expect(acceptPolicyResponse(currentWorkspace, responseWorkspace)).toBe(false);
  });

  it("accepts only the response for the currently loaded workspace", () => {
    expect(acceptPolicyResponse("workspace-b", "workspace-b")).toBe(true);
  });

  it("keeps deferred A loads and saves from corrupting mounted workspace B", async () => {
    const pending = new Map<string, { resolve(value: { selections: unknown[] }): void }>();
    const invokeAction = vi.fn((_key: string, input?: { workspaceId?: string; body?: unknown }) => new Promise<{ selections: unknown[] }>((resolve) => {
      pending.set(`${input?.workspaceId}:${input?.body ? "save" : "load"}`, { resolve });
    }));
    const harness = mountedSettingsHarness();
    const host = { React: harness.react, ui: {}, api: { invokeAction }, context: {}, i18n: {}, navigate: vi.fn() } as unknown as CoordinatorHost;
    const Settings = createCoordinatorSettings(host);

    let tree = harness.render(Settings, { workspaceId: "workspace-a" });
    expect(control(tree, "textarea").props.disabled).toBe(true);
    tree = harness.render(Settings, { workspaceId: "workspace-b" });
    pending.get("workspace-b:load")?.resolve({ selections: [{ workflow_id: "b", workstep_id: "b" }] });
    await flush();
    tree = harness.render(Settings, { workspaceId: "workspace-b" });
    expect(control(tree, "textarea").props.value).toContain('"b"');
    expect(control(tree, "button").props.disabled).toBe(false);

    pending.get("workspace-a:load")?.resolve({ selections: [{ workflow_id: "a", workstep_id: "a" }] });
    await flush();
    tree = harness.render(Settings, { workspaceId: "workspace-b" });
    expect(control(tree, "textarea").props.value).toContain('"b"');
    expect(control(tree, "textarea").props.value).not.toContain('"a"');

    control(tree, "button").props.onClick();
    tree = harness.render(Settings, { workspaceId: "workspace-a" });
    pending.get("workspace-a:load")?.resolve({ selections: [] });
    await flush();
    pending.get("workspace-b:save")?.resolve({ selections: [] });
    await flush();
    tree = harness.render(Settings, { workspaceId: "workspace-a" });
    expect(control(tree, "textarea").props.value).toBe("[]");
    expect(JSON.stringify(tree)).not.toContain("Monitoring selections saved.");
  });
});

function flush() { return Promise.resolve().then(() => Promise.resolve()); }

function control(tree: unknown, type: string): { props: Record<string, any> } {
  if (tree && typeof tree === "object") {
    const node = tree as { type?: string; props?: Record<string, any>; children?: unknown[] };
    if (node.type === type) return { props: node.props ?? {} };
    for (const child of node.children ?? []) {
      try { return control(child, type); } catch { /* continue */ }
    }
  }
  throw new Error(`missing ${type}`);
}

function mountedSettingsHarness() {
  const state: unknown[] = [];
  const deps: unknown[][] = [];
  const cleanups: Array<(() => void) | undefined> = [];
  let cursor = 0;
  const react = {
    createElement: (type: unknown, props?: Record<string, unknown> | null, ...children: unknown[]) => ({ type, props: props ?? {}, children }),
    useState<T>(initial: T): [T, (value: T | ((current: T) => T)) => void] {
      const index = cursor++;
      if (!(index in state)) state[index] = initial;
      return [state[index] as T, (value) => { state[index] = typeof value === "function" ? (value as (current: T) => T)(state[index] as T) : value; }];
    },
    useRef<T>(initial: T): { current: T } {
      const index = cursor++;
      if (!(index in state)) state[index] = { current: initial };
      return state[index] as { current: T };
    },
    useMemo<T>(factory: () => T): T { cursor++; return factory(); },
    useEffect(effect: () => void | (() => void), nextDeps: unknown[]) {
      const index = cursor++;
      const changed = !deps[index] || deps[index].some((value, i) => value !== nextDeps[i]);
      if (changed) { cleanups[index]?.(); deps[index] = nextDeps; cleanups[index] = effect() || undefined; }
    },
  };
  return { react, render: (Component: (props: { workspaceId?: string }) => unknown, props: { workspaceId?: string }) => { cursor = 0; return Component(props); } };
}
