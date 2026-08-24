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
  status: "ready" | "unavailable" | "configuration_required" | "authorization_required" | "error";
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

export type CapabilityState = { status: "available" | "unavailable" | "degraded"; reason?: string };

export type CoordinatorState = {
  identity: { logical_key: string };
  inbox: InboxItem[];
  follow_ups: FollowUp[];
  runs: CoordinatorRun[];
  capabilities: {
    principal: CapabilityState;
    inbox: CapabilityState;
    automations: CapabilityState;
    relations: CapabilityState;
  };
  automation_bindings: AutomationBinding[];
};

export type InboxItem = {
  id: string;
  kind: "human_decision" | "pending_reply" | "blocker" | "human_qa";
  task_id?: string;
  title: string;
  body?: string;
  created_at: string;
  status: "open" | "acknowledged" | "resolved";
};

export type FollowUp = {
  id: string;
  request: string;
  expected_evidence: string;
  due_at?: string;
  status: "pending" | "acknowledged" | "completed" | "stalled" | "blocked";
};

export type CoordinatorRun = {
  id: string;
  status: "started" | "running" | "completed" | "blocked" | "failed" | "coalesced";
  started_at: string;
};

export type StatusResponse = {
  status: "ready" | "unavailable" | "configuration_required" | "authorization_required" | "error";
  message?: string;
  state: CoordinatorState;
  capabilities: CoordinatorState["capabilities"];
};

export type AutomationDescriptor = {
  id: string;
  workspace_id: string;
  name: string;
  description: string;
  agent_profile_id?: string;
  enabled: boolean;
};

export type AutomationPage = { automations: AutomationDescriptor[] };
export type AutomationBinding = { automation_id: string; name: string; bound_at: string };

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
  context: {
    getActiveWorkspaceId(): string | undefined;
    subscribeActiveWorkspace(listener: (workspaceId: string | undefined) => void): () => void;
  };
  api: {
    invokeAction<T>(
      key: string,
      input?: { workspaceId?: string; body?: unknown },
      options?: { signal?: AbortSignal },
    ): Promise<T>;
  };
  i18n: {
    locale: string;
		t(key: string, options?: { defaultValue?: string }): string;
    useTranslation(): { locale: string; t(key: string, options?: { defaultValue?: string }): string };
  };
  navigate(href: string, options?: { replace?: boolean }): void;
};

export type CoordinatorRegistry = {
  registerTranslations(catalogs: Record<string, Record<string, string>>): void;
  registerNavItem(item: { id: string; label: string; path: string; icon?: string; section?: string }): void;
  registerRoute(
    path: string,
    component: Component,
    options?: { topbar?: boolean | { title?: string; subtitle?: string; icon?: string } },
  ): void;
};

declare global {
  interface Window {
    registerKandevPlugin?: (
      id: string,
      lifecycle: { initialize(registry: CoordinatorRegistry, host: CoordinatorHost): void; destroy(): void },
    ) => void;
  }
}
