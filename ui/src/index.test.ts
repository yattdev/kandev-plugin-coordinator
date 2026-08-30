import { describe, expect, it, vi } from "vitest";
import { registerCoordinator } from "./index";
import type { CoordinatorHost, CoordinatorRegistry } from "./contracts";
import { coordinatorCatalogs, localizedLabel } from "./locales";

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
			context: { getActiveWorkspaceId: () => "workspace-1", subscribeActiveWorkspace: vi.fn() },
      api: {},
			i18n: { locale: "en", t: (key: string) => key, useTranslation: vi.fn() },
      navigate: vi.fn(),
    } as unknown as CoordinatorHost;

    registerCoordinator(registry, host);

    expect(registry.registerTranslations).toHaveBeenCalledOnce();
    expect(registry.registerNavItem).toHaveBeenCalledWith(expect.objectContaining({
      id: "coordinator",
      label: "Coordinator",
      path: "/coordinator",
      section: "integrations",
    }));
    expect(registry.registerRoute).toHaveBeenCalledWith("/coordinator", expect.any(Function), expect.any(Object));
		expect(registry.registerRoute).toHaveBeenCalledWith("/coordinator", expect.any(Function), {
			topbar: { title: "Coordinator", subtitle: "coordinator_subtitle", icon: "bot" },
		});
  });

	it("registers only the host-supported English catalog", () => {
		expect(Object.keys(coordinatorCatalogs)).toEqual(["en"]);
		expect(localizedLabel()).toBe("Coordinator");
	});

  it("uses host-compatible translation keys", () => {
    for (const catalog of Object.values(coordinatorCatalogs)) {
      for (const [key, message] of Object.entries(catalog)) {
        expect(key).toMatch(/^[a-z][a-zA-Z0-9_-]*$/);
        expect(message).toEqual(expect.any(String));
      }
    }
  });
});
