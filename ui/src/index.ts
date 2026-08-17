import { createCoordinatorPage } from "./coordinator-page";
import type { CoordinatorHost, CoordinatorRegistry } from "./contracts";
import { coordinatorCatalogs, localizedLabel } from "./locales";

export function registerCoordinator(registry: CoordinatorRegistry, host: CoordinatorHost): void {
  registry.registerTranslations(coordinatorCatalogs);
  registry.registerNavItem({
    id: "coordinator",
    label: localizedLabel(host.i18n.locale),
    path: "/coordinator",
    icon: "bot",
    section: "integrations",
  });
  registry.registerRoute("/coordinator", createCoordinatorPage(host), {
    topbar: { subtitleKey: "coordinator.subtitle" },
  });
}

if (typeof window !== "undefined" && window.registerKandevPlugin) {
  window.registerKandevPlugin("kandev-plugin-coordinator", {
    initialize: registerCoordinator,
    destroy() {},
  });
}
