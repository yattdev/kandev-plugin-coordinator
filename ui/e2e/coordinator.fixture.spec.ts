import { expect, test } from "@playwright/test";

async function capture(page: import("@playwright/test").Page, testInfo: import("@playwright/test").TestInfo, name: string) {
  await page.screenshot({ path: testInfo.outputPath(`${name}.png`), fullPage: true });
}

test("configuration fixture opens settings through the production page", async ({ page }, testInfo) => {
  await page.goto("/?scenario=configuration");
  await expect(page.getByRole("status")).toHaveText("Coordinator configuration is required.");
  await page.getByRole("main").getByRole("button", { name: "Settings" }).click();
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.navigations)).toEqual(["/settings/plugins/kandev-plugin-coordinator"]);
  await capture(page, testInfo, "configuration-settings");
});

test("reports fixture renders inventory and paginates through client actions", async ({ page }, testInfo) => {
  await page.goto("/?scenario=reports");
  await page.getByRole("button", { name: "Reports" }).click();
  await expect(page.getByRole("heading", { name: "Workspace inventory" })).toBeVisible();
  await page.getByRole("button", { name: "Load more" }).click();
  await expect(page.getByRole("heading", { name: "Recovered inventory" })).toBeVisible();
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.actions.map((action) => action.key))).toContain("coordinator.reports");
  await capture(page, testInfo, "reports-pagination");
});

test("denied fixture presents an error and performs no simulated effect", async ({ page }, testInfo) => {
  await page.goto("/?scenario=denied");
  await page.getByRole("button", { name: "Run cycle" }).click();
  await expect(page.getByRole("alert")).toHaveText("Denied by fixture; zero simulated effects recorded.");
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.simulatedEffects)).toBe(0);
  await expect.poll(() => page.evaluate(() => window.__coordinatorFixture?.actions.at(-1)?.key)).toBe("coordinator.run-cycle");
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
  await capture(page, testInfo, "recovery-run");
});
