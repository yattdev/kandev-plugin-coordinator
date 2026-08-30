import { createCoordinatorPage } from "./coordinator-page";
import type { CoordinatorHost, CoordinatorRegistry } from "./contracts";
import { coordinatorCatalogs, localizedLabel } from "./locales";

export function registerCoordinator(registry: CoordinatorRegistry, host: CoordinatorHost): void {
  registry.registerTranslations(coordinatorCatalogs);
	const label = localizedLabel(host.i18n.locale);
  registry.registerNavItem({
    id: "coordinator",
		label,
    path: "/coordinator",
    icon: "bot",
    section: "integrations",
  });
  registry.registerRoute("/coordinator", createCoordinatorPage(host), {
		topbar: { title: label, subtitle: host.i18n.t("coordinator_subtitle"), icon: "bot" },
  });
}

if (typeof window !== "undefined" && window.registerKandevPlugin) {
  window.registerKandevPlugin("kandev-plugin-coordinator", {
    initialize: registerCoordinator,
    destroy() {},
  });
}
