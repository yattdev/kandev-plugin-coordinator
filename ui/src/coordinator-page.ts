import { CoordinatorClient, manualRunKey } from "./coordinator-client";
import type { CoordinatorHost, EnsureResponse, ReportArtifact } from "./contracts";

export const coordinatorButtonClass = "min-h-11 px-4";

type PageState = {
  workspaceId: string;
  ensure?: EnsureResponse;
  reports: ReportArtifact[];
  nextCursor?: string;
  loading: boolean;
  error?: string;
  notice?: string;
};

export function createCoordinatorPage(host: CoordinatorHost) {
  const React = host.React;
  const h = React.createElement;

  return function CoordinatorPage() {
    const { t } = host.i18n.useTranslation();
    const [workspaceId, setWorkspaceId] = React.useState(host.context.getActiveWorkspaceId() ?? "");
    const client = React.useMemo(() => new CoordinatorClient(host, workspaceId), [host, workspaceId]);
    const [tab, setTab] = React.useState<"chat" | "reports">("chat");
    const [state, setState] = React.useState<PageState>({ workspaceId, reports: [], loading: true });

    React.useEffect(() => host.context.subscribeActiveWorkspace((next) => setWorkspaceId(next ?? "")), [host]);

    React.useEffect(() => {
      const controller = new AbortController();
		if (!workspaceId) {
			setState({ workspaceId, reports: [], loading: false, error: t("coordinator.noWorkspace") });
			return () => controller.abort();
		}
      setState({ workspaceId, reports: [], loading: true });
      Promise.all([client.ensure(controller.signal), client.reports("", controller.signal)])
        .then(([ensure, page]) => {
          setState((current) => current.workspaceId === workspaceId
            ? { workspaceId, ensure, reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false }
            : current);
        })
        .catch((error: unknown) => {
          if (!controller.signal.aborted) {
            setState(updateCurrentWorkspace(workspaceId, (current) => ({
              ...current, loading: false, error: error instanceof Error ? error.message : String(error),
            })));
          }
        });
      return () => controller.abort();
    }, [client, workspaceId, t]);

    const refreshReports = () => {
      setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, loading: true, error: undefined })));
      void client.reports().then(
        (page) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
          ...current, reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false,
        }))),
        (error: unknown) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
          ...current, loading: false, error: error instanceof Error ? error.message : String(error),
        }))),
      );
    };

		const loadMoreReports = () => {
			if (!state.nextCursor) return;
			setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, loading: true, error: undefined })));
			void client.reports(state.nextCursor).then(
				(page) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
					...current,
					reports: [...current.reports, ...(page.reports ?? [])],
					nextCursor: page.next_cursor,
					loading: false,
				}))),
				(error: unknown) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
					...current, loading: false, error: error instanceof Error ? error.message : String(error),
				}))),
			);
		};

    const run = (trigger: "cycle" | "standup") => {
      setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, notice: undefined, error: undefined })));
      void client.run(trigger, manualRunKey(trigger)).then(
        (response) => {
          const status = response.dispatch.status;
          const notice = status === "skipped_busy"
            ? t("coordinator.runBusy")
            : status === "duplicate_occurrence"
              ? t("coordinator.runDuplicate")
              : t("coordinator.runQueued");
          setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, notice })));
        },
        (error: unknown) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
          ...current, error: error instanceof Error ? error.message : t("coordinator.failed"),
        }))),
      );
    };

    const Button = host.ui.Button ?? "button";
    const tabButton = (value: "chat" | "reports", label: string) =>
      h(Button, {
        type: "button",
        className: "min-h-11 px-4",
        "aria-pressed": tab === value,
        onClick: () => setTab(value),
      }, label);

    const isCurrentWorkspace = state.workspaceId === workspaceId;
    let content: unknown;
    if (!isCurrentWorkspace || (state.loading && !state.ensure)) {
      content = h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading"));
    } else if (!host.ui.WorkspaceAgentChat || state.ensure?.status === "unavailable") {
      content = h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.unavailable"));
    } else if (state.ensure?.status === "configuration_required") {
      content = h("div", { className: "p-6" },
        h("p", { role: "status" }, t("coordinator.configurationRequired")),
        h(Button, { type: "button", className: `mt-4 ${coordinatorButtonClass}`, onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings")),
      );
    } else if (state.ensure?.status === "error") {
		content = h("p", { role: "alert", className: "p-6 text-destructive" }, state.ensure.error ?? t("coordinator.failed"));
	} else if (tab === "chat" && state.ensure?.conversation) {
      content = h(host.ui.WorkspaceAgentChat, {
        workspaceId,
				conversationKey: state.ensure.conversation.key,
				sessionId: state.ensure.conversation.session_id ?? "",
				placeholderOverride: t("coordinator.placeholder"),
        className: "min-h-0 flex-1 overflow-hidden",
      });
    } else {
      content = reportsView(h, state.reports, t("coordinator.emptyReports"));
    }

    return h("div", { className: "flex h-full min-h-0 max-w-full flex-col overflow-hidden pb-[env(safe-area-inset-bottom)]" },
      h("header", { className: "flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2" },
        h("div", { role: "tablist", className: "flex items-center gap-1" }, tabButton("chat", t("coordinator.chat")), tabButton("reports", t("coordinator.reports"))),
        h("div", { className: "flex flex-wrap items-center gap-2" },
          h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => run("cycle") }, t("coordinator.runCycle")),
          h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => run("standup") }, t("coordinator.runStandup")),
          h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings")),
        ),
      ),
      isCurrentWorkspace && state.error ? h("p", { role: "alert", className: "shrink-0 px-4 py-2 text-destructive" }, state.error) : null,
      isCurrentWorkspace && state.notice ? h("p", { role: "status", className: "shrink-0 px-4 py-2 text-muted-foreground" }, state.notice) : null,
      h("main", { className: "min-h-0 flex-1 overflow-hidden" }, content),
			tab === "reports" && isCurrentWorkspace ? h("footer", { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
				h(Button, { type: "button", className: coordinatorButtonClass, onClick: refreshReports }, t("coordinator.refresh")),
				state.nextCursor ? h(Button, { type: "button", className: coordinatorButtonClass, onClick: loadMoreReports }, t("coordinator.loadMore")) : null,
			) : null,
    );
  };
}

export function updateCurrentWorkspace<T extends { workspaceId: string }>(
  workspaceId: string,
  update: (current: T) => T,
): (current: T) => T {
  return (current) => current.workspaceId === workspaceId ? update(current) : current;
}

function reportsView(
  h: CoordinatorHost["React"]["createElement"],
  reports: ReportArtifact[],
  emptyText: string,
): unknown {
  if (reports.length === 0) {
    return h("p", { className: "h-full overflow-y-auto p-6 text-muted-foreground" }, emptyText);
  }
  return h("ol", { className: "h-full max-w-full space-y-3 overflow-y-auto overflow-x-hidden p-4" },
    ...reports.map((report) => h("li", { key: report.id, className: "max-w-full rounded-md border p-4" },
      h("div", { className: "flex flex-wrap items-baseline justify-between gap-2" },
        h("h2", { className: "font-medium" }, report.title),
        h("time", { className: "text-xs text-muted-foreground", dateTime: report.created_at }, report.created_at),
      ),
      h("pre", { className: "mt-3 max-w-full whitespace-pre-wrap break-words font-sans text-sm" }, report.body),
    )),
  );
}
