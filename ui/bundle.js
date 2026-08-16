// Native coordinator route. The parent host supplies these conversation
// primitives; no second React runtime is bundled.
window.registerKandevPlugin("kandev-plugin-coordinator", {
  initialize(registry, host) {
    const h = host.jsx;
    const AgentConversations = host.ui.AgentConversations;
    const WorkspaceAgentChat = host.ui.WorkspaceAgentChat;
    function CoordinatorPage() {
      if (!AgentConversations || !WorkspaceAgentChat) return h("p", { className: "p-6 text-muted-foreground" }, "Coordinator conversations require a host with the coordinator UI contract.");
      return h("div", { className: "flex h-full min-h-0 flex-col" }, h(AgentConversations, { workspaceId: host.context.workspaceId, kind: "coordinator" }), h(WorkspaceAgentChat, { workspaceId: host.context.workspaceId, kind: "coordinator" }));
    }
    registry.registerNavItem({ id: "coordinator", label: "Coordinator", path: "/coordinator", icon: "bot", section: "integrations" });
    registry.registerRoute("/coordinator", CoordinatorPage, { topbar: { subtitle: "Board orchestration" } });
  },
  destroy() {},
});
