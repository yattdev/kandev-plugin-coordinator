(() => {
  // ui/src/coordinator-client.ts
  var CoordinatorClient = class {
    constructor(host, workspaceId) {
      this.host = host;
      this.workspaceId = workspaceId;
    }
    ensure(signal) {
      return this.host.api.invokeAction("coordinator.ensure", { workspaceId: this.workspaceId }, { signal });
    }
    reports(cursor = "", signal) {
      return this.host.api.invokeAction("coordinator.reports", {
        workspaceId: this.workspaceId,
        body: { cursor, limit: 20 }
      }, { signal });
    }
    run(trigger, idempotencyKey, signal) {
      const key = trigger === "cycle" ? "coordinator.run-cycle" : "coordinator.run-standup";
      return this.host.api.invokeAction(key, {
        workspaceId: this.workspaceId,
        body: { idempotency_key: idempotencyKey }
      }, { signal });
    }
  };
  function manualRunKey(trigger) {
    const cryptoApi = globalThis.crypto;
    const value = cryptoApi && "randomUUID" in cryptoApi ? cryptoApi.randomUUID() : `${Date.now()}-${Math.random()}`;
    return `${trigger}-${value}`;
  }

  // ui/src/coordinator-page.ts
  var coordinatorButtonClass = "min-h-11 px-4";
  function createCoordinatorPage(host) {
    const React = host.React;
    const h = React.createElement;
    return function CoordinatorPage() {
      const { t } = host.i18n.useTranslation();
      const [workspaceId, setWorkspaceId] = React.useState(host.context.getActiveWorkspaceId() ?? "");
      const client = React.useMemo(() => new CoordinatorClient(host, workspaceId), [host, workspaceId]);
      const [tab, setTab] = React.useState("chat");
      const [state, setState] = React.useState({ workspaceId, reports: [], loading: true });
      React.useEffect(() => host.context.subscribeActiveWorkspace((next) => setWorkspaceId(next ?? "")), [host]);
      React.useEffect(() => {
        const controller = new AbortController();
        if (!workspaceId) {
          setState({ workspaceId, reports: [], loading: false, error: t("coordinator.noWorkspace") });
          return () => controller.abort();
        }
        setState({ workspaceId, reports: [], loading: true });
        Promise.all([client.ensure(controller.signal), client.reports("", controller.signal)]).then(([ensure, page]) => {
          setState((current) => current.workspaceId === workspaceId ? { workspaceId, ensure, reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false } : current);
        }).catch((error) => {
          if (!controller.signal.aborted) {
            setState(updateCurrentWorkspace(workspaceId, (current) => ({
              ...current,
              loading: false,
              error: error instanceof Error ? error.message : String(error)
            })));
          }
        });
        return () => controller.abort();
      }, [client, workspaceId, t]);
      const refreshReports = () => {
        setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, loading: true, error: void 0 })));
        void client.reports().then(
          (page) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
            ...current,
            reports: page.reports ?? [],
            nextCursor: page.next_cursor,
            loading: false
          }))),
          (error) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
            ...current,
            loading: false,
            error: error instanceof Error ? error.message : String(error)
          })))
        );
      };
      const loadMoreReports = () => {
        if (!state.nextCursor) return;
        setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, loading: true, error: void 0 })));
        void client.reports(state.nextCursor).then(
          (page) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
            ...current,
            reports: [...current.reports, ...page.reports ?? []],
            nextCursor: page.next_cursor,
            loading: false
          }))),
          (error) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
            ...current,
            loading: false,
            error: error instanceof Error ? error.message : String(error)
          })))
        );
      };
      const run = (trigger) => {
        setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, notice: void 0, error: void 0 })));
        void client.run(trigger, manualRunKey(trigger)).then(
          (response) => {
            const status = response.dispatch.status;
            const notice = status === "skipped_busy" ? t("coordinator.runBusy") : status === "duplicate_occurrence" ? t("coordinator.runDuplicate") : t("coordinator.runQueued");
            setState(updateCurrentWorkspace(workspaceId, (current) => ({ ...current, notice })));
          },
          (error) => setState(updateCurrentWorkspace(workspaceId, (current) => ({
            ...current,
            error: error instanceof Error ? error.message : t("coordinator.failed")
          })))
        );
      };
      const Button = host.ui.Button ?? "button";
      const tabButton = (value, label) => h(Button, {
        type: "button",
        className: "min-h-11 px-4",
        "aria-pressed": tab === value,
        onClick: () => setTab(value)
      }, label);
      const isCurrentWorkspace = state.workspaceId === workspaceId;
      let content;
      if (!isCurrentWorkspace || state.loading && !state.ensure) {
        content = h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading"));
      } else if (!host.ui.WorkspaceAgentChat || state.ensure?.status === "unavailable") {
        content = h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.unavailable"));
      } else if (state.ensure?.status === "configuration_required") {
        content = h(
          "div",
          { className: "p-6" },
          h("p", { role: "status" }, t("coordinator.configurationRequired")),
          h(Button, { type: "button", className: `mt-4 ${coordinatorButtonClass}`, onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings"))
        );
      } else if (state.ensure?.status === "error") {
        content = h("p", { role: "alert", className: "p-6 text-destructive" }, state.ensure.error ?? t("coordinator.failed"));
      } else if (tab === "chat" && state.ensure?.conversation) {
        content = h(host.ui.WorkspaceAgentChat, {
          workspaceId,
          conversationKey: state.ensure.conversation.key,
          sessionId: state.ensure.conversation.session_id ?? "",
          placeholderOverride: t("coordinator.placeholder"),
          className: "min-h-0 flex-1 overflow-hidden"
        });
      } else {
        content = reportsView(h, state.reports, t("coordinator.emptyReports"));
      }
      return h(
        "div",
        { className: "flex h-full min-h-0 max-w-full flex-col overflow-hidden pb-[env(safe-area-inset-bottom)]" },
        h(
          "header",
          { className: "flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2" },
          h("div", { role: "tablist", className: "flex items-center gap-1" }, tabButton("chat", t("coordinator.chat")), tabButton("reports", t("coordinator.reports"))),
          h(
            "div",
            { className: "flex flex-wrap items-center gap-2" },
            h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => run("cycle") }, t("coordinator.runCycle")),
            h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => run("standup") }, t("coordinator.runStandup")),
            h(Button, { type: "button", className: coordinatorButtonClass, onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings"))
          )
        ),
        isCurrentWorkspace && state.error ? h("p", { role: "alert", className: "shrink-0 px-4 py-2 text-destructive" }, state.error) : null,
        isCurrentWorkspace && state.notice ? h("p", { role: "status", className: "shrink-0 px-4 py-2 text-muted-foreground" }, state.notice) : null,
        h("main", { className: "min-h-0 flex-1 overflow-hidden" }, content),
        tab === "reports" && isCurrentWorkspace ? h(
          "footer",
          { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
          h(Button, { type: "button", className: coordinatorButtonClass, onClick: refreshReports }, t("coordinator.refresh")),
          state.nextCursor ? h(Button, { type: "button", className: coordinatorButtonClass, onClick: loadMoreReports }, t("coordinator.loadMore")) : null
        ) : null
      );
    };
  }
  function updateCurrentWorkspace(workspaceId, update) {
    return (current) => current.workspaceId === workspaceId ? update(current) : current;
  }
  function reportsView(h, reports, emptyText) {
    if (reports.length === 0) {
      return h("p", { className: "h-full overflow-y-auto p-6 text-muted-foreground" }, emptyText);
    }
    return h(
      "ol",
      { className: "h-full max-w-full space-y-3 overflow-y-auto overflow-x-hidden p-4" },
      ...reports.map((report) => h(
        "li",
        { key: report.id, className: "max-w-full rounded-md border p-4" },
        h(
          "div",
          { className: "flex flex-wrap items-baseline justify-between gap-2" },
          h("h2", { className: "font-medium" }, report.title),
          h("time", { className: "text-xs text-muted-foreground", dateTime: report.created_at }, report.created_at)
        ),
        h("pre", { className: "mt-3 max-w-full whitespace-pre-wrap break-words font-sans text-sm" }, report.body)
      ))
    );
  }

  // ui/locales/en.json
  var en_default = {
    "coordinator.label": "Coordinator",
    "coordinator.subtitle": "Board orchestration",
    "coordinator.chat": "Chat",
    "coordinator.reports": "Reports",
    "coordinator.settings": "Coordinator settings",
    "coordinator.refresh": "Refresh reports",
    "coordinator.loadMore": "Load more reports",
    "coordinator.runCycle": "Run cycle now",
    "coordinator.runStandup": "Run standup now",
    "coordinator.emptyReports": "No coordinator reports yet.",
    "coordinator.loading": "Loading coordinator\u2026",
    "coordinator.noWorkspace": "Select a workspace to use Coordinator.",
    "coordinator.placeholder": "Message the coordinator\u2026",
    "coordinator.unavailable": "Coordinator conversations require a compatible Kandev host.",
    "coordinator.configurationRequired": "Configure a usable coordinator or workspace default agent profile.",
    "coordinator.runQueued": "Coordinator run queued.",
    "coordinator.runBusy": "Coordinator is busy. This run was not queued.",
    "coordinator.runDuplicate": "This coordinator run was already handled.",
    "coordinator.failed": "Coordinator request failed."
  };

  // ui/locales/fr.json
  var fr_default = {
    "coordinator.label": "Coordonnateur",
    "coordinator.subtitle": "Orchestration du tableau",
    "coordinator.chat": "Discussion",
    "coordinator.reports": "Rapports",
    "coordinator.settings": "R\xE9glages du coordonnateur",
    "coordinator.refresh": "Actualiser les rapports",
    "coordinator.loadMore": "Charger plus de rapports",
    "coordinator.runCycle": "Lancer un cycle",
    "coordinator.runStandup": "Lancer le rapport quotidien",
    "coordinator.emptyReports": "Aucun rapport du coordonnateur.",
    "coordinator.loading": "Chargement du coordonnateur\u2026",
    "coordinator.noWorkspace": "S\xE9lectionnez un espace de travail pour utiliser le coordonnateur.",
    "coordinator.placeholder": "\xC9crire au coordonnateur\u2026",
    "coordinator.unavailable": "Les conversations du coordonnateur n\xE9cessitent une version compatible de Kandev.",
    "coordinator.configurationRequired": "Configurez un profil d\u2019agent utilisable pour le coordonnateur ou l\u2019espace de travail.",
    "coordinator.runQueued": "Ex\xE9cution du coordonnateur mise en file.",
    "coordinator.runBusy": "Le coordonnateur est occup\xE9. Cette ex\xE9cution n\u2019a pas \xE9t\xE9 mise en file.",
    "coordinator.runDuplicate": "Cette ex\xE9cution du coordonnateur a d\xE9j\xE0 \xE9t\xE9 trait\xE9e.",
    "coordinator.failed": "La requ\xEAte du coordonnateur a \xE9chou\xE9."
  };

  // ui/locales/qps-ploc.json
  var qps_ploc_default = {
    "coordinator.label": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159]",
    "coordinator.subtitle": "[\xDF\xF6\xF6\xE5\u0159\u0111 \xF6\u0159\xE7\u0125\xE9\u0161\u0163\u0159\xE5\u0163\xEE\xF6\xF6\xF1]",
    "coordinator.chat": "[\xC7\u0125\xE5\u0163]",
    "coordinator.reports": "[\u0158\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.settings": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0161\xE9\u0163\u0163\xEE\xF1\u011D\u0161]",
    "coordinator.refresh": "[\u0158\xE9\u0192\u0159\xE9\u0161\u0125 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.loadMore": "[\u013B\xF6\xF6\xE5\u0111 \u0271\xF6\xF6\u0159\xE9 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.runCycle": "[\u0158\xFB\xF1 \xE7\xFD\xE7\u013C\xE9 \xF1\xF6\xF6\u0175]",
    "coordinator.runStandup": "[\u0158\xFB\xF1 \u0161\u0163\xE5\xF1\u0111\xFB\xFE \xF1\xF6\xF6\u0175]",
    "coordinator.emptyReports": "[\xD1\xF6\xF6 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161 \xFD\xE9\u0163.]",
    "coordinator.loading": "[\u013B\xF6\xF6\xE5\u0111\xEE\xF1\u011D \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159\u2026]",
    "coordinator.noWorkspace": "[\u0160\xE9\u013C\xE9\xE7\u0163 \xE5 \u0175\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9 \u0163\xF6\xF6 \xFB\u0161\xE9 \xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159.]",
    "coordinator.placeholder": "[M\xE9\u0161\u0161\xE5\u011D\xE9 \u0163\u0125\xE9 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159\u2026]",
    "coordinator.unavailable": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xE7\xF6\xF6\xF1V\xE9\u0159\u0161\xE5\u0163\xEE\xF6\xF6\xF1\u0161 \u0159\xE9q\xFB\xEE\u0159\xE9 \xE5 \xE7\xF6\xF6\u0271\xFE\xE5\u0163\xEE\u0180\u013C\xE9 \u0136\xE5\xF1\u0111\xE9V \u0125\xF6\xF6\u0161\u0163.]",
    "coordinator.configurationRequired": "[\xC7\xF6\xF6\xF1\u0192\xEE\u011D\xFB\u0159\xE9 \xE5 \xFB\u0161\xE5\u0180\u013C\xE9 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xF6\xF6\u0159 \u0175\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9 \xFE\u0159\xF6\xF6\u0192\xEE\u013C\xE9.]",
    "coordinator.runQueued": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0159\xFB\xF1 q\xFB\xE9\xFB\xE9\u0111.]",
    "coordinator.runBusy": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xEE\u0161 \u0180\xFB\u0161\xFD. \u0162\u0125\xEE\u0161 \u0159\xFB\xF1 \u0175\xE5\u0161 \xF1\xF6\u0163 q\xFB\xE9\xFB\xE9\u0111.]",
    "coordinator.runDuplicate": "[\u0162\u0125\xEE\u0161 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0159\xFB\xF1 \u0175\xE5\u0161 \xE5\u013C\u0159\xE9\xE5\u0111\xFD \u0125\xE5\xF1\u0111\u013C\xE9\u0111.]",
    "coordinator.failed": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0159\xE9q\xFB\xE9\u0161\u0163 \u0192\xE5\xEE\u013C\xE9\u0111.]"
  };

  // ui/src/locales.ts
  var coordinatorCatalogs = {
    en: en_default,
    fr: fr_default,
    "qps-ploc": qps_ploc_default
  };
  function localizedLabel(locale) {
    const normalized = locale.toLowerCase();
    const catalog = normalized.startsWith("fr") ? fr_default : normalized === "qps-ploc" ? qps_ploc_default : en_default;
    return catalog["coordinator.label"];
  }

  // ui/src/index.ts
  function registerCoordinator(registry, host) {
    registry.registerTranslations(coordinatorCatalogs);
    const label = localizedLabel(host.i18n.locale);
    registry.registerNavItem({
      id: "coordinator",
      label,
      path: "/coordinator",
      icon: "bot",
      section: "integrations"
    });
    registry.registerRoute("/coordinator", createCoordinatorPage(host), {
      topbar: { title: label, subtitle: host.i18n.t("coordinator.subtitle"), icon: "bot" }
    });
  }
  if (typeof window !== "undefined" && window.registerKandevPlugin) {
    window.registerKandevPlugin("kandev-plugin-coordinator", {
      initialize: registerCoordinator,
      destroy() {
      }
    });
  }
})();
