import { CoordinatorClient } from "./coordinator-client";
import type { CapabilityState, CoordinatorHost, EnsureResponse, InboxItem, ReportArtifact, StatusResponse } from "./contracts";

type Tab = "overview" | "chat" | "reports";
type PageState = { ensure?: EnsureResponse; status?: StatusResponse; reports: ReportArtifact[]; nextCursor?: string; loading: boolean; error?: string };

export function createCoordinatorPage(host: CoordinatorHost) {
  const React = host.React;
  const h = React.createElement;
  return function CoordinatorPage() {
    const { t } = host.i18n.useTranslation();
    const [workspaceId, setWorkspaceId] = React.useState(host.context.getActiveWorkspaceId() ?? "");
    const client = React.useMemo(() => new CoordinatorClient(host, workspaceId), [host, workspaceId]);
    const [tab, setTab] = React.useState<Tab>("overview");
    const [state, setState] = React.useState<PageState>({ reports: [], loading: true });

    React.useEffect(() => host.context.subscribeActiveWorkspace((next) => setWorkspaceId(next ?? "")), [host]);
    React.useEffect(() => {
      const controller = new AbortController();
      if (!workspaceId) {
        setState({ reports: [], loading: false, error: t("coordinator.noWorkspace") });
        return () => controller.abort();
      }
      setState((current) => ({ ...current, loading: true, error: undefined }));
      Promise.all([client.ensure(controller.signal), client.status(controller.signal), client.reports("", controller.signal)])
        .then(([ensure, status, page]) => setState({ ensure, status, reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false }))
        .catch((error: unknown) => {
          if (!controller.signal.aborted) setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) }));
        });
      return () => controller.abort();
    }, [client, workspaceId, t]);

    const refresh = () => {
      setState((current) => ({ ...current, loading: true, error: undefined }));
      void Promise.all([client.status(), client.reports()]).then(
        ([status, page]) => setState((current) => ({ ...current, status, reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false })),
        (error: unknown) => setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) })),
      );
    };
    const loadMore = () => {
      if (!state.nextCursor) return;
      setState((current) => ({ ...current, loading: true, error: undefined }));
      void client.reports(state.nextCursor).then(
        (page) => setState((current) => ({ ...current, reports: [...current.reports, ...(page.reports ?? [])], nextCursor: page.next_cursor, loading: false })),
        (error: unknown) => setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) })),
      );
    };

    const Button = host.ui.Button ?? "button";
    const switchTab = (value: Tab, label: string) => h(Button, { type: "button", className: "min-h-11 px-4", "aria-pressed": tab === value, onClick: () => setTab(value) }, label);
    const content = state.loading && !state.status ? h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading"))
      : tab === "overview" ? overviewView(h, state.status, t)
        : tab === "chat" ? chatView(h, host, state.ensure, workspaceId, t)
          : reportsView(h, state.reports, t("coordinator.emptyReports"));

    return h("div", { className: "flex h-full min-h-0 max-w-full flex-col overflow-hidden pb-[env(safe-area-inset-bottom)]" },
      h("header", { className: "flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2" },
        h("div", { role: "tablist", className: "flex max-w-full items-center gap-1 overflow-x-auto" }, switchTab("overview", t("coordinator.overview")), switchTab("chat", t("coordinator.chat")), switchTab("reports", t("coordinator.reports"))),
        h(Button, { type: "button", className: "min-h-11", onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings")),
      ),
      state.error ? h("p", { role: "alert", className: "shrink-0 px-4 py-2 text-destructive" }, state.error) : null,
      h("main", { className: "min-h-0 flex-1 overflow-hidden" }, content),
      tab === "overview" ? h("footer", { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
        h(Button, { type: "button", className: "min-h-11", onClick: refresh }, t("coordinator.refresh")), h(Button, { type: "button", className: "min-h-11", onClick: () => host.navigate("/settings/automations") }, t("coordinator.setupAutomations")),
      ) : null,
      tab === "reports" ? h("footer", { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
        h(Button, { type: "button", className: "min-h-11", onClick: refresh }, t("coordinator.refresh")), state.nextCursor ? h(Button, { type: "button", className: "min-h-11", onClick: loadMore }, t("coordinator.loadMore")) : null,
      ) : null,
    );
  };
}

function overviewView(h: CoordinatorHost["React"]["createElement"], status: StatusResponse | undefined, t: (key: string) => string): unknown {
  const state = status?.state;
  const inbox = state?.inbox ?? [];
  const followUps = state?.follow_ups ?? [];
  return h("section", { className: "h-full overflow-y-auto overflow-x-hidden p-4" },
    h("h1", { className: "text-lg font-semibold" }, t("coordinator.overview")), h("p", { className: "mt-1 text-sm text-muted-foreground" }, t("coordinator.overviewDescription")),
    h("dl", { className: "mt-4 grid max-w-full gap-3 sm:grid-cols-2" }, summaryCard(h, t("coordinator.openInbox"), String(inbox.filter((item) => item.status === "open").length)), summaryCard(h, t("coordinator.pendingFollowUps"), String(followUps.filter((item) => item.status === "pending").length))),
    h("h2", { className: "mt-6 font-medium" }, t("coordinator.inbox")),
    inbox.length === 0 ? h("p", { className: "mt-2 text-sm text-muted-foreground" }, t("coordinator.emptyInbox")) : h("ol", { className: "mt-2 space-y-2" }, ...inbox.map((item) => inboxRow(h, item))),
    h("h2", { className: "mt-6 font-medium" }, t("coordinator.hostCapabilities")), h("ul", { className: "mt-2 space-y-2" }, ...capabilityRows(h, state?.capabilities, t)),
  );
}

function summaryCard(h: CoordinatorHost["React"]["createElement"], label: string, value: string): unknown {
  return h("div", { className: "rounded-md border p-3" }, h("dt", { className: "text-sm text-muted-foreground" }, label), h("dd", { className: "mt-1 text-2xl font-semibold" }, value));
}
function inboxRow(h: CoordinatorHost["React"]["createElement"], item: InboxItem): unknown {
  return h("li", { key: item.id, className: "rounded-md border p-3" }, h("div", { className: "flex flex-wrap justify-between gap-2" }, h("strong", null, item.title), h("span", { className: "text-xs text-muted-foreground" }, item.kind)), item.body ? h("p", { className: "mt-1 break-words text-sm" }, item.body) : null);
}
function capabilityRows(h: CoordinatorHost["React"]["createElement"], capabilities: StatusResponse["capabilities"] | undefined, t: (key: string) => string): unknown[] {
  const entries: Array<[string, CapabilityState | undefined]> = [[t("coordinator.principal"), capabilities?.principal], [t("coordinator.inboxCapability"), capabilities?.inbox], [t("coordinator.automations"), capabilities?.automations], [t("coordinator.relations"), capabilities?.relations]];
  return entries.map(([label, state]) => h("li", { key: label, className: "rounded-md border p-3 text-sm" }, h("strong", null, label), ": ", state?.status ?? t("coordinator.unknown"), state?.reason ? h("p", { className: "mt-1 text-muted-foreground" }, state.reason) : null));
}
function chatView(h: CoordinatorHost["React"]["createElement"], host: CoordinatorHost, ensure: EnsureResponse | undefined, workspaceId: string, t: (key: string) => string): unknown {
  const Button = host.ui.Button ?? "button";
  if (!host.ui.WorkspaceAgentChat || ensure?.status === "unavailable") return h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.unavailable"));
  if (ensure?.status === "configuration_required") return h("div", { className: "p-6" }, h("p", { role: "status" }, t("coordinator.configurationRequired")), h(Button, { type: "button", className: "mt-4 min-h-11", onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings")));
  if (ensure?.status === "error") return h("p", { role: "alert", className: "p-6 text-destructive" }, ensure.error ?? t("coordinator.failed"));
  if (ensure?.conversation) return h(host.ui.WorkspaceAgentChat, { workspaceId, conversationKey: ensure.conversation.key, sessionId: ensure.conversation.session_id ?? "", placeholderOverride: t("coordinator.placeholder"), className: "min-h-0 flex-1 overflow-hidden" });
  return h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading"));
}
function reportsView(h: CoordinatorHost["React"]["createElement"], reports: ReportArtifact[], emptyText: string): unknown {
  if (reports.length === 0) return h("p", { className: "h-full overflow-y-auto p-6 text-muted-foreground" }, emptyText);
  return h("ol", { className: "h-full max-w-full space-y-3 overflow-y-auto overflow-x-hidden p-4" }, ...reports.map((report) => h("li", { key: report.id, className: "max-w-full rounded-md border p-4" }, h("div", { className: "flex flex-wrap items-baseline justify-between gap-2" }, h("h2", { className: "font-medium" }, report.title), h("time", { className: "text-xs text-muted-foreground", dateTime: report.created_at }, report.created_at)), h("pre", { className: "mt-3 max-w-full whitespace-pre-wrap break-words font-sans text-sm" }, report.body))));
}
