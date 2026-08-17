import { describe, expect, it, vi } from "vitest";
import { registerCoordinator } from "./index";
import type { CoordinatorHost, CoordinatorRegistry } from "./contracts";

describe("registerCoordinator", () => {
  it("registers one localized Integrations destination and route", () => {
    const registry = {
      registerTranslations: vi.fn(),
      registerNavItem: vi.fn(),
      registerRoute: vi.fn(),
    } satisfies CoordinatorRegistry;
    const host = {
      React: {},
      ui: {},
      context: { workspaceId: "workspace-1" },
      api: {},
      i18n: { locale: "fr", useTranslation: vi.fn() },
      navigate: vi.fn(),
    } as unknown as CoordinatorHost;

    registerCoordinator(registry, host);

    expect(registry.registerTranslations).toHaveBeenCalledOnce();
    expect(registry.registerNavItem).toHaveBeenCalledWith(expect.objectContaining({
      id: "coordinator",
      label: "Coordonnateur",
      path: "/coordinator",
      section: "integrations",
    }));
    expect(registry.registerRoute).toHaveBeenCalledWith("/coordinator", expect.any(Function), expect.any(Object));
  });
});
