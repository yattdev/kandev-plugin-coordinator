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
    status(signal) {
      return this.host.api.invokeAction("coordinator.status", { workspaceId: this.workspaceId }, { signal });
    }
    automations(signal) {
      return this.host.api.invokeAction("coordinator.automations", { workspaceId: this.workspaceId }, { signal });
    }
    bindAutomations(automationIds, signal) {
      return this.host.api.invokeAction("coordinator.automation-bind", { workspaceId: this.workspaceId, body: { automation_ids: automationIds } }, { signal });
    }
  };

  // ui/src/coordinator-page.ts
  function createCoordinatorPage(host) {
    const React = host.React;
    const h = React.createElement;
    return function CoordinatorPage() {
      const { t } = host.i18n.useTranslation();
      const [workspaceId, setWorkspaceId] = React.useState(host.context.getActiveWorkspaceId() ?? "");
      const client = React.useMemo(() => new CoordinatorClient(host, workspaceId), [host, workspaceId]);
      const [tab, setTab] = React.useState("overview");
      const [state, setState] = React.useState({ automations: [], reports: [], loading: true });
      React.useEffect(() => host.context.subscribeActiveWorkspace((next) => setWorkspaceId(next ?? "")), [host]);
      React.useEffect(() => {
        const controller = new AbortController();
        if (!workspaceId) {
          setState({ automations: [], reports: [], loading: false, error: t("coordinator.noWorkspace") });
          return () => controller.abort();
        }
        setState((current) => ({ ...current, loading: true, error: void 0 }));
        Promise.all([client.ensure(controller.signal), client.status(controller.signal), client.automations(controller.signal), client.reports("", controller.signal)]).then(([ensure, status, automations, page]) => setState({ ensure, status, automations: automations.automations ?? [], reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false })).catch((error) => {
          if (!controller.signal.aborted) setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) }));
        });
        return () => controller.abort();
      }, [client, workspaceId, t]);
      const refresh = () => {
        setState((current) => ({ ...current, loading: true, error: void 0 }));
        void Promise.all([client.status(), client.automations(), client.reports()]).then(
          ([status, automations, page]) => setState((current) => ({ ...current, status, automations: automations.automations ?? [], reports: page.reports ?? [], nextCursor: page.next_cursor, loading: false })),
          (error) => setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) }))
        );
      };
      const loadMore = () => {
        if (!state.nextCursor) return;
        setState((current) => ({ ...current, loading: true, error: void 0 }));
        void client.reports(state.nextCursor).then(
          (page) => setState((current) => ({ ...current, reports: [...current.reports, ...page.reports ?? []], nextCursor: page.next_cursor, loading: false })),
          (error) => setState((current) => ({ ...current, loading: false, error: error instanceof Error ? error.message : String(error) }))
        );
      };
      const Button = host.ui.Button ?? "button";
      const switchTab = (value, label) => h(Button, { type: "button", className: "min-h-11 px-4", "aria-pressed": tab === value, onClick: () => setTab(value) }, label);
      const content = state.loading && !state.status ? h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading")) : tab === "overview" ? overviewView(h, state.status, state.automations, (id) => {
        void client.bindAutomations([id]).then(refresh);
      }, t) : tab === "chat" ? chatView(h, host, state.ensure, workspaceId, t) : reportsView(h, state.reports, t("coordinator.emptyReports"));
      return h(
        "div",
        { className: "flex h-full min-h-0 max-w-full flex-col overflow-hidden pb-[env(safe-area-inset-bottom)]" },
        h(
          "header",
          { className: "flex min-h-11 shrink-0 flex-wrap items-center justify-between gap-2 border-b px-4 py-2" },
          h("div", { role: "tablist", className: "flex max-w-full items-center gap-1 overflow-x-auto" }, switchTab("overview", t("coordinator.overview")), switchTab("chat", t("coordinator.chat")), switchTab("reports", t("coordinator.reports"))),
          h(Button, { type: "button", className: "min-h-11", onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings"))
        ),
        state.error ? h("p", { role: "alert", className: "shrink-0 px-4 py-2 text-destructive" }, state.error) : null,
        h("main", { className: "min-h-0 flex-1 overflow-hidden" }, content),
        tab === "overview" ? h(
          "footer",
          { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
          h(Button, { type: "button", className: "min-h-11", onClick: refresh }, t("coordinator.refresh")),
          h(Button, { type: "button", className: "min-h-11", onClick: () => host.navigate("/settings/automations") }, t("coordinator.setupAutomations"))
        ) : null,
        tab === "reports" ? h(
          "footer",
          { className: "flex shrink-0 gap-2 border-t p-2 pb-[max(0.5rem,env(safe-area-inset-bottom))]" },
          h(Button, { type: "button", className: "min-h-11", onClick: refresh }, t("coordinator.refresh")),
          state.nextCursor ? h(Button, { type: "button", className: "min-h-11", onClick: loadMore }, t("coordinator.loadMore")) : null
        ) : null
      );
    };
  }
  function overviewView(h, status, automations, bind, t) {
    const state = status?.state;
    const inbox = state?.inbox ?? [];
    const followUps = state?.follow_ups ?? [];
    return h(
      "section",
      { className: "h-full overflow-y-auto overflow-x-hidden p-4" },
      h("h1", { className: "text-lg font-semibold" }, t("coordinator.overview")),
      h("p", { className: "mt-1 text-sm text-muted-foreground" }, t("coordinator.overviewDescription")),
      h("dl", { className: "mt-4 grid max-w-full gap-3 sm:grid-cols-2" }, summaryCard(h, t("coordinator.openInbox"), String(inbox.filter((item) => item.status === "open").length)), summaryCard(h, t("coordinator.pendingFollowUps"), String(followUps.filter((item) => item.status === "pending").length))),
      h("h2", { className: "mt-6 font-medium" }, t("coordinator.inbox")),
      inbox.length === 0 ? h("p", { className: "mt-2 text-sm text-muted-foreground" }, t("coordinator.emptyInbox")) : h("ol", { className: "mt-2 space-y-2" }, ...inbox.map((item) => inboxRow(h, item))),
      h("h2", { className: "mt-6 font-medium" }, t("coordinator.hostCapabilities")),
      h("ul", { className: "mt-2 space-y-2" }, ...capabilityRows(h, state?.capabilities, t)),
      h("h2", { className: "mt-6 font-medium" }, t("coordinator.automationBindings")),
      automations.length === 0 ? h("p", { className: "mt-2 text-sm text-muted-foreground" }, t("coordinator.noAutomations")) : h("ul", { className: "mt-2 space-y-2" }, ...automations.map((automation) => automationRow(h, automation, bind, t)))
    );
  }
  function automationRow(h, automation, bind, t) {
    return h("li", { key: automation.id, className: "flex flex-wrap items-center justify-between gap-2 rounded-md border p-3" }, h("div", null, h("strong", null, automation.name), h("p", { className: "text-sm text-muted-foreground" }, automation.enabled ? t("coordinator.enabled") : t("coordinator.disabled"))), h("button", { type: "button", className: "min-h-11 rounded border px-3", disabled: !automation.enabled, onClick: () => bind(automation.id) }, t("coordinator.bindAutomation")));
  }
  function summaryCard(h, label, value) {
    return h("div", { className: "rounded-md border p-3" }, h("dt", { className: "text-sm text-muted-foreground" }, label), h("dd", { className: "mt-1 text-2xl font-semibold" }, value));
  }
  function inboxRow(h, item) {
    return h("li", { key: item.id, className: "rounded-md border p-3" }, h("div", { className: "flex flex-wrap justify-between gap-2" }, h("strong", null, item.title), h("span", { className: "text-xs text-muted-foreground" }, item.kind)), item.body ? h("p", { className: "mt-1 break-words text-sm" }, item.body) : null);
  }
  function capabilityRows(h, capabilities, t) {
    const entries = [[t("coordinator.principal"), capabilities?.principal], [t("coordinator.inboxCapability"), capabilities?.inbox], [t("coordinator.automations"), capabilities?.automations], [t("coordinator.relations"), capabilities?.relations]];
    return entries.map(([label, state]) => h("li", { key: label, className: "rounded-md border p-3 text-sm" }, h("strong", null, label), ": ", state?.status ?? t("coordinator.unknown"), state?.reason ? h("p", { className: "mt-1 text-muted-foreground" }, state.reason) : null));
  }
  function chatView(h, host, ensure, workspaceId, t) {
    const Button = host.ui.Button ?? "button";
    if (!host.ui.WorkspaceAgentChat || ensure?.status === "unavailable") return h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.unavailable"));
    if (ensure?.status === "configuration_required") return h("div", { className: "p-6" }, h("p", { role: "status" }, t("coordinator.configurationRequired")), h(Button, { type: "button", className: "mt-4 min-h-11", onClick: () => host.navigate("/settings/plugins/kandev-plugin-coordinator") }, t("coordinator.settings")));
    if (ensure?.status === "error") return h("p", { role: "alert", className: "p-6 text-destructive" }, ensure.error ?? t("coordinator.failed"));
    if (ensure?.conversation) return h(host.ui.WorkspaceAgentChat, { workspaceId, conversationKey: ensure.conversation.key, sessionId: ensure.conversation.session_id ?? "", placeholderOverride: t("coordinator.placeholder"), className: "min-h-0 flex-1 overflow-hidden" });
    return h("p", { role: "status", className: "p-6 text-muted-foreground" }, t("coordinator.loading"));
  }
  function reportsView(h, reports, emptyText) {
    if (reports.length === 0) return h("p", { className: "h-full overflow-y-auto p-6 text-muted-foreground" }, emptyText);
    return h("ol", { className: "h-full max-w-full space-y-3 overflow-y-auto overflow-x-hidden p-4" }, ...reports.map((report) => h("li", { key: report.id, className: "max-w-full rounded-md border p-4" }, h("div", { className: "flex flex-wrap items-baseline justify-between gap-2" }, h("h2", { className: "font-medium" }, report.title), h("time", { className: "text-xs text-muted-foreground", dateTime: report.created_at }, report.created_at)), h("pre", { className: "mt-3 max-w-full whitespace-pre-wrap break-words font-sans text-sm" }, report.body))));
  }

  // ui/locales/en.json
  var en_default = {
    "coordinator.label": "Coordinator",
    "coordinator.subtitle": "Board orchestration",
    "coordinator.chat": "Chat",
    "coordinator.overview": "Overview",
    "coordinator.overviewDescription": "Workspace health, human asks, and follow-up obligations.",
    "coordinator.inbox": "Inbox",
    "coordinator.emptyInbox": "No pending Coordinator items.",
    "coordinator.openInbox": "Open inbox items",
    "coordinator.pendingFollowUps": "Pending follow-ups",
    "coordinator.hostCapabilities": "Host capabilities",
    "coordinator.principal": "Coordinator authority",
    "coordinator.inboxCapability": "Host inbox",
    "coordinator.automations": "Automations",
    "coordinator.relations": "Task relations",
    "coordinator.unknown": "Unknown",
    "coordinator.setupAutomations": "Set up Automations",
    "coordinator.automationBindings": "Automation bindings",
    "coordinator.noAutomations": "No Automations are available in this workspace.",
    "coordinator.bindAutomation": "Bind",
    "coordinator.enabled": "Enabled",
    "coordinator.disabled": "Disabled",
    "coordinator.reports": "Reports",
    "coordinator.settings": "Coordinator settings",
    "coordinator.refresh": "Refresh reports",
    "coordinator.loadMore": "Load more reports",
    "coordinator.emptyReports": "No coordinator reports yet.",
    "coordinator.loading": "Loading coordinator\u2026",
    "coordinator.noWorkspace": "Select a workspace to use Coordinator.",
    "coordinator.placeholder": "Message the coordinator\u2026",
    "coordinator.unavailable": "Coordinator conversations require a compatible Kandev host.",
    "coordinator.configurationRequired": "Configure a usable coordinator or workspace default agent profile.",
    "coordinator.failed": "Coordinator request failed."
  };

  // ui/locales/fr.json
  var fr_default = {
    "coordinator.label": "Coordonnateur",
    "coordinator.subtitle": "Orchestration du tableau",
    "coordinator.chat": "Discussion",
    "coordinator.overview": "Aper\xE7u",
    "coordinator.overviewDescription": "\xC9tat de l\u2019espace, demandes humaines et suivis.",
    "coordinator.inbox": "Bo\xEEte de r\xE9ception",
    "coordinator.emptyInbox": "Aucun \xE9l\xE9ment Coordinator en attente.",
    "coordinator.openInbox": "\xC9l\xE9ments ouverts",
    "coordinator.pendingFollowUps": "Suivis en attente",
    "coordinator.hostCapabilities": "Fonctionnalit\xE9s de l\u2019h\xF4te",
    "coordinator.principal": "Autorit\xE9 Coordinator",
    "coordinator.inboxCapability": "Bo\xEEte de r\xE9ception h\xF4te",
    "coordinator.automations": "Automatisations",
    "coordinator.relations": "Relations de t\xE2ches",
    "coordinator.unknown": "Inconnu",
    "coordinator.setupAutomations": "Configurer les automatisations",
    "coordinator.automationBindings": "Liaisons d\u2019automatisation",
    "coordinator.noAutomations": "Aucune automatisation n\u2019est disponible dans cet espace.",
    "coordinator.bindAutomation": "Lier",
    "coordinator.enabled": "Activ\xE9e",
    "coordinator.disabled": "D\xE9sactiv\xE9e",
    "coordinator.reports": "Rapports",
    "coordinator.settings": "R\xE9glages du coordonnateur",
    "coordinator.refresh": "Actualiser les rapports",
    "coordinator.loadMore": "Charger plus de rapports",
    "coordinator.emptyReports": "Aucun rapport du coordonnateur.",
    "coordinator.loading": "Chargement du coordonnateur\u2026",
    "coordinator.noWorkspace": "S\xE9lectionnez un espace de travail pour utiliser le coordonnateur.",
    "coordinator.placeholder": "\xC9crire au coordonnateur\u2026",
    "coordinator.unavailable": "Les conversations du coordonnateur n\xE9cessitent une version compatible de Kandev.",
    "coordinator.configurationRequired": "Configurez un profil d\u2019agent utilisable pour le coordonnateur ou l\u2019espace de travail.",
    "coordinator.failed": "La requ\xEAte du coordonnateur a \xE9chou\xE9."
  };

  // ui/locales/qps-ploc.json
  var qps_ploc_default = {
    "coordinator.label": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159]",
    "coordinator.subtitle": "[\xDF\xF6\xF6\xE5\u0159\u0111 \xF6\u0159\xE7\u0125\xE9\u0161\u0163\u0159\xE5\u0163\xEE\xF6\xF6\xF1]",
    "coordinator.chat": "[\xC7\u0125\xE5\u0163]",
    "coordinator.overview": "[\xD6\xF6V\xE9\u0159V\xEE\xE9\u0175]",
    "coordinator.overviewDescription": "[\u0174\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9 \u0125\xE9\xE5\u013C\u0163\u0125, \u0125\xFB\u0271\xE5\xF1 \xE5\u0161\u0137\u0161, \xE5\xF1\u0111 \u0192\xF6\xF6\u013C\u013C\xF6\xF6\u0175-\xFB\xFE \xF6\u0180\u013C\xEE\u011D\xE5\u0163\xEE\xF6\xF6\xF1\u0161.]",
    "coordinator.inbox": "[\xCE\xF1\u0180\xF6\xF6x]",
    "coordinator.emptyInbox": "[\xD1\xF6\xF6 \xFE\xE9\xF1\u0111\xEE\xF1\u011D \xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xEE\u0163\xE9\u0271\u0161.]",
    "coordinator.openInbox": "[\xD6\xF6\xFE\xE9\xF1 \xEE\xF1\u0180\xF6\xF6x \xEE\u0163\xE9\u0271\u0161]",
    "coordinator.pendingFollowUps": "[\xDE\xE9\xF1\u0111\xEE\xF1\u011D \u0192\xF6\xF6\u013C\u013C\xF6\xF6\u0175-\xFB\xFE\u0161]",
    "coordinator.hostCapabilities": "[\u0124\xF6\xF6\u0161\u0163 \xE7\xE5\xFE\xE5\u0180\xEE\u013C\xEE\u0163\xEE\xE9\u0161]",
    "coordinator.principal": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xE5\xFB\u0163\u0125\xF6\xF6\u0159\xEE\u0163\xFD]",
    "coordinator.inboxCapability": "[\u0124\xF6\xF6\u0161\u0163 \xEE\xF1\u0180\xF6\xF6x]",
    "coordinator.automations": "[\xC5\xFB\u0163\xF6\xF6\u0271\xE5\u0163\xEE\xF6\xF6\xF1\u0161]",
    "coordinator.relations": "[\u0162\xE5\u0161\u0137 \u0159\xE9\u013C\xE5\u0163\xEE\xF6\xF6\xF1\u0161]",
    "coordinator.unknown": "[\xDB\xF1\u0137\xF1\xF6\xF6\u0175\xF1]",
    "coordinator.setupAutomations": "[\u0160\xE9\u0163 \xFB\xFE \xC5\xFB\u0163\xF6\xF6\u0271\xE5\u0163\xEE\xF6\xF6\xF1\u0161]",
    "coordinator.automationBindings": "[\xC5\xFB\u0163\xF6\xF6\u0271\xE5\u0163\xEE\xF6\xF6\xF1 \u0180\xEE\xF1\u0111\xEE\xF1\u011D\u0161]",
    "coordinator.noAutomations": "[\xD1\xF6\xF6 \xC5\xFB\u0163\xF6\xF6\u0271\xE5\u0163\xEE\xF6\xF6\xF1\u0161 \xE5\u0159\xE9 \xE5V\xE5\xEE\u013C\xE5\u0180\u013C\xE9 \xEE\xF1 \u0163\u0125\xEE\u0161 \u0175\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9.]",
    "coordinator.bindAutomation": "[\xDF\xEE\xF1\u0111]",
    "coordinator.enabled": "[\xC9\xF1\xE5\u0180\u013C\xE9\u0111]",
    "coordinator.disabled": "[\xD0\xEE\u0161\xE5\u0180\u013C\xE9\u0111]",
    "coordinator.reports": "[\u0158\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.settings": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0161\xE9\u0163\u0163\xEE\xF1\u011D\u0161]",
    "coordinator.refresh": "[\u0158\xE9\u0192\u0159\xE9\u0161\u0125 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.loadMore": "[\u013B\xF6\xF6\xE5\u0111 \u0271\xF6\xF6\u0159\xE9 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161]",
    "coordinator.emptyReports": "[\xD1\xF6\xF6 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \u0159\xE9\xFE\xF6\xF6\u0159\u0163\u0161 \xFD\xE9\u0163.]",
    "coordinator.loading": "[\u013B\xF6\xF6\xE5\u0111\xEE\xF1\u011D \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159\u2026]",
    "coordinator.noWorkspace": "[\u0160\xE9\u013C\xE9\xE7\u0163 \xE5 \u0175\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9 \u0163\xF6\xF6 \xFB\u0161\xE9 \xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159.]",
    "coordinator.placeholder": "[M\xE9\u0161\u0161\xE5\u011D\xE9 \u0163\u0125\xE9 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159\u2026]",
    "coordinator.unavailable": "[\xC7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xE7\xF6\xF6\xF1V\xE9\u0159\u0161\xE5\u0163\xEE\xF6\xF6\xF1\u0161 \u0159\xE9q\xFB\xEE\u0159\xE9 \xE5 \xE7\xF6\xF6\u0271\xFE\xE5\u0163\xEE\u0180\u013C\xE9 \u0136\xE5\xF1\u0111\xE9V \u0125\xF6\xF6\u0161\u0163.]",
    "coordinator.configurationRequired": "[\xC7\xF6\xF6\xF1\u0192\xEE\u011D\xFB\u0159\xE9 \xE5 \xFB\u0161\xE5\u0180\u013C\xE9 \xE7\xF6\xF6\u0159\u0111\xEE\xF1\xE5\u0163\xF6\xF6\u0159 \xF6\xF6\u0159 \u0175\xF6\xF6\u0159\u0137\u0161\xFE\xE5\xE7\xE9 \xFE\u0159\xF6\xF6\u0192\xEE\u013C\xE9.]",
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
