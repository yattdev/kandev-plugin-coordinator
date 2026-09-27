import { defineConfig } from "@playwright/test";

export default defineConfig({
  testDir: "./ui/e2e",
  outputDir: "test-results/",
  use: { baseURL: "http://127.0.0.1:4173", screenshot: "off" },
  webServer: {
    command: "node scripts/fixture-ui-server.mjs",
    url: "http://127.0.0.1:4173",
    reuseExistingServer: false,
  },
});
