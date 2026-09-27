import React from "react";
import { createRoot } from "react-dom/client";
import { createCoordinatorPage } from "./coordinator-page";
import type { CoordinatorHost, EnsureResponse, ReportPage, RunResponse } from "./contracts";

type Scenario = "configuration" | "reports" | "denied" | "recovery";
type Action = { key: string; input?: { workspaceId?: string; body?: unknown } };

declare global {
  interface Window {
    __coordinatorFixture?: { actions: Action[]; simulatedEffects: number; navigations: string[] };
  }
}

const report = (id: string, title: string) => ({
  id,
  type: "cycle" as const,
  title,
  body: `${title} body`,
  created_at: "2026-09-27T08:00:00Z",
});

function scenarioFromLocation(): Scenario {
  const value = new URLSearchParams(window.location.search).get("scenario");
  return value === "configuration" || value === "denied" || value === "recovery" ? value : "reports";
}

function fixtureHost(scenario: Scenario): CoordinatorHost {
  const actions: Action[] = [];
  const navigations: string[] = [];
  let simulatedEffects = 0;
  let recoveryFailed = false;
  window.__coordinatorFixture = { actions, simulatedEffects, navigations };
  const translations: Record<string, string> = {
    coordinator_loading: "Loading Coordinator…",
    coordinator_noWorkspace: "Choose a workspace first.",
    coordinator_unavailable: "Coordinator is unavailable.",
    coordinator_configurationRequired: "Coordinator configuration is required.",
    coordinator_settings: "Settings",
    coordinator_failed: "Coordinator request failed.",
    coordinator_placeholder: "Message the Coordinator",
    coordinator_emptyReports: "No reports yet.",
    coordinator_chat: "Chat",
    coordinator_reports: "Reports",
    coordinator_runCycle: "Run cycle",
    coordinator_runStandup: "Run standup",
    coordinator_runBusy: "A run is already active.",
    coordinator_runDuplicate: "This run was already requested.",
    coordinator_runQueued: "Run queued.",
    coordinator_refresh: "Refresh",
    coordinator_loadMore: "Load more",
  };
  const t = (key: string) => translations[key] ?? key;
  const invokeAction = async <T,>(key: string, input?: { workspaceId?: string; body?: unknown }): Promise<T> => {
    actions.push({ key, input });
    if (key === "coordinator.ensure") {
      const response: EnsureResponse = scenario === "configuration"
        ? { status: "configuration_required" }
        : { status: "ready", conversation: { workspace_id: "fixture-workspace", key: "coordinator", status: "ready" } };
      return response as T;
    }
    if (key === "coordinator.reports") {
      const cursor = (input?.body as { cursor?: string } | undefined)?.cursor ?? "";
      if (scenario === "recovery" && !recoveryFailed) {
        recoveryFailed = true;
        throw new Error("Transient fixture report failure");
      }
      const response: ReportPage = cursor === "page-2"
        ? { reports: [report("report-2", "Recovered inventory") ] }
        : { reports: [report("report-1", "Workspace inventory")], next_cursor: "page-2" };
      return response as T;
    }
    if (key === "coordinator.run-cycle" || key === "coordinator.run-standup") {
      if (scenario === "denied") throw new Error("Denied by fixture; zero simulated effects recorded.");
      simulatedEffects += 1;
      window.__coordinatorFixture!.simulatedEffects = simulatedEffects;
      const response: RunResponse = { dispatch: { status: "queued", occurrence_key: "fixture-occurrence" } };
      return response as T;
    }
    throw new Error(`Unexpected fixture action: ${key}`);
  };
  return {
    React,
    jsx: React.createElement,
    ui: {
      Button: "button",
      WorkspaceAgentChat: () => React.createElement("p", { "data-testid": "fixture-chat" }, "Fixture native chat"),
    },
    context: {
      getActiveWorkspaceId: () => "fixture-workspace",
      subscribeActiveWorkspace: () => () => undefined,
    },
    api: { invokeAction },
    i18n: {
      locale: "en",
      t,
      useTranslation: () => ({ locale: "en", t }),
    },
    navigate: (href) => { navigations.push(href); },
  };
}

const Page = createCoordinatorPage(fixtureHost(scenarioFromLocation()));
createRoot(document.getElementById("root")!).render(React.createElement(Page as React.ComponentType));
