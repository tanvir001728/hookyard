import { defineConfig, devices } from "@playwright/test";

// Browser tests against a running Hookyard. Point HOOKYARD_E2E_URL at it and
// set HOOKYARD_E2E_TOKEN to one of its API tokens.
export default defineConfig({
  testDir: "./e2e",
  fullyParallel: false,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["list"]] : "list",
  use: {
    baseURL: process.env.HOOKYARD_E2E_URL ?? "http://localhost:8080",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"] } },
    { name: "mobile", use: { ...devices["Pixel 7"] } },
  ],
});
