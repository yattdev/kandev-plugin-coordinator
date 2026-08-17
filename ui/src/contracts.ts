export type ElementNode = unknown;
export type Component = (props?: Record<string, unknown>) => ElementNode;

export type ConversationDescriptor = {
  workspace_id: string;
  key: string;
  task_id?: string;
  session_id?: string;
  status: string;
};

export type EnsureResponse = {
  status: "ready" | "unavailable" | "configuration_required" | "error";
  error?: string;
  conversation?: ConversationDescriptor;
};

export type ReportArtifact = {
  id: string;
  type: "cycle" | "daily" | "status";
  title: string;
  body: string;
  created_at: string;
};

export type ReportPage = { reports: ReportArtifact[]; next_cursor?: string };

export type CoordinatorHost = {
  React: {
    createElement(type: unknown, props?: Record<string, unknown> | null, ...children: unknown[]): ElementNode;
    useEffect(effect: () => void | (() => void), dependencies: unknown[]): void;
    useMemo<T>(factory: () => T, dependencies: unknown[]): T;
    useState<T>(initial: T): [T, (value: T | ((current: T) => T)) => void];
  };
  jsx(type: unknown, props?: Record<string, unknown> | null, ...children: unknown[]): ElementNode;
  ui: {
    WorkspaceAgentChat?: Component;
    Button?: unknown;
    Spinner?: unknown;
  };
  context?: { workspaceId?: string };
  api: {
    invokeAction<T>(key: string, input?: Record<string, unknown>, options?: { signal?: AbortSignal }): Promise<T>;
  };
  i18n: {
    locale: string;
    useTranslation(): { locale: string; t(key: string, options?: { defaultValue?: string }): string };
  };
  navigate(href: string, options?: { replace?: boolean }): void;
};

export type CoordinatorRegistry = {
  registerTranslations(catalogs: Record<string, Record<string, string>>): void;
  registerNavItem(item: { id: string; label: string; path: string; icon?: string; section?: string }): void;
  registerRoute(path: string, component: Component, options?: Record<string, unknown>): void;
};

declare global {
  interface Window {
    registerKandevPlugin?: (
      id: string,
      lifecycle: { initialize(registry: CoordinatorRegistry, host: CoordinatorHost): void; destroy(): void },
    ) => void;
  }
}
