import { expect, test } from "@playwright/test";

async function capture(page: import("@playwright/test").Page, testInfo: import("@playwright/test").TestInfo, name: string) {
  await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
}

const workspace = { workspaceId: "fixture-workspace" };
const ready = { status: "ready", conversation: { workspace_id: "fixture-workspace", key: "coordinator", status: "ready" } };
const firstReport = { id: "report-1", type: "cycle", title: "Workspace inventory", body: "Workspace inventory body", created_at: "2026-09-27T08:00:00Z" };
const secondReport = { id: "report-2", type: "cycle", title: "Recovered inventory", body: "Recovered inventory body", created_at: "2026-09-27T08:00:00Z" };
const ensure = (value: unknown) => ({ key: "coordinator.ensure", input: workspace, outcome: { status: "success", value } });
const reports = (cursor: string, value: unknown) => ({ key: "coordinator.reports", input: { ...workspace, body: { cursor, limit: 20 } }, outcome: { status: "success", value } });
const run = (outcome: unknown) => ({ key: "coordinator.run-cycle", input: { ...workspace, body: { idempotency_key: "cycle-00000000-0000-4000-8000-000000000001" } }, outcome });
const error = (message: string) => ({ status: "error", message });
const success = (value: unknown) => ({ status: "success", value });
const firstPage = { reports: [firstReport], next_cursor: "page-2" };
const secondPage = { reports: [secondReport] };

async function expectTrace(page: import("@playwright/test").Page, expected: unknown[]) {
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.actions)).toEqual(expected);
}

test("configuration fixture opens settings through the production page", async ({ page }, testInfo) => {
  await page.goto("/?scenario=configuration");
  await expect(page.getByRole("status")).toHaveText("Coordinator configuration is required.");
  await page.getByRole("main").getByRole("button", { name: "Settings" }).click();
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.navigations)).toEqual(["/settings/plugins/kandev-plugin-coordinator"]);
  await expectTrace(page, [ensure({ status: "configuration_required" }), reports("", firstPage)]);
  await capture(page, testInfo, "configuration-settings");
});

test("reports fixture renders inventory and paginates through client actions", async ({ page }, testInfo) => {
  await page.goto("/?scenario=reports");
  await page.getByRole("button", { name: "Reports" }).click();
  await expect(page.getByRole("heading", { name: "Workspace inventory" })).toBeVisible();
  await page.getByRole("button", { name: "Load more" }).click();
  await expect(page.getByRole("heading", { name: "Recovered inventory" })).toBeVisible();
  await expectTrace(page, [ensure(ready), reports("", firstPage), reports("page-2", secondPage)]);
  await capture(page, testInfo, "reports-pagination");
});

test("denied fixture presents an error and performs no simulated effect", async ({ page }, testInfo) => {
  await page.goto("/?scenario=denied");
  await page.getByRole("button", { name: "Run cycle" }).click();
  await expect(page.getByRole("alert")).toHaveText("Denied by fixture; zero simulated effects recorded.");
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.simulatedEffects)).toBe(0);
  await expectTrace(page, [ensure(ready), reports("", firstPage), run(error("Denied by fixture; zero simulated effects recorded."))]);
  await capture(page, testInfo, "denied-zero-effect");
});

test("recovery fixture refreshes after a transient error and queues a run", async ({ page }, testInfo) => {
  await page.goto("/?scenario=recovery");
  await expect(page.getByRole("alert")).toHaveText("Transient fixture report failure");
  await page.getByRole("button", { name: "Reports" }).click();
  await page.getByRole("button", { name: "Refresh" }).click();
  await expect(page.getByRole("heading", { name: "Workspace inventory" })).toBeVisible();
  await page.getByRole("button", { name: "Run cycle" }).click();
  await expect(page.getByRole("status")).toHaveText("Run queued.");
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.simulatedEffects)).toBe(1);
  await expectTrace(page, [
    ensure(ready),
    { key: "coordinator.reports", input: { ...workspace, body: { cursor: "", limit: 20 } }, outcome: error("Transient fixture report failure") },
    reports("", firstPage),
    run(success({ dispatch: { status: "queued", occurrence_key: "fixture-occurrence" } })),
  ]);
  await capture(page, testInfo, "recovery-run");
});
