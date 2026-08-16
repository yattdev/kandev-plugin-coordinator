// Kandev native coordinator route. It relies only on the planned optional
// conversation contract and degrades visibly on older hosts.
window.registerKandevPlugin("kandev-plugin-coordinator", {
  initialize(registry, host) {
    const h = host.jsx;
    const french = (navigator.language || "").toLowerCase().startsWith("fr");
    const text = french
      ? { label: "Coordonnateur", subtitle: "Orchestration du tableau", unavailable: "Les conversations du coordonnateur requièrent une version de Kandev qui prend en charge cette intégration." }
      : { label: "Coordinator", subtitle: "Board orchestration", unavailable: "Coordinator conversations require a Kandev host that supports this integration." };
    const AgentConversations = host.ui.AgentConversations;
    const WorkspaceAgentChat = host.ui.WorkspaceAgentChat;
    function CoordinatorPage() {
      const workspaceId = host.context && host.context.workspaceId;
      if (!AgentConversations || !WorkspaceAgentChat) return h("p", { className: "p-6 text-muted-foreground", role: "status" }, text.unavailable);
      return h("div", { className: "flex h-full min-h-0 flex-col" },
        h(AgentConversations, { workspaceId, kind: "coordinator" }),
        h(WorkspaceAgentChat, { workspaceId, kind: "coordinator" }));
    }
    registry.registerNavItem({ id: "coordinator", label: text.label, path: "/coordinator", icon: "bot", section: "integrations" });
    registry.registerRoute("/coordinator", CoordinatorPage, { topbar: { subtitle: text.subtitle } });
  },
  destroy() {},
});
