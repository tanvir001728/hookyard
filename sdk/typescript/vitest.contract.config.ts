import { defineConfig } from "vitest/config";

// Contract tests run against a live Hookyard server. They are skipped unless
// HOOKYARD_URL and HOOKYARD_TOKEN are set. See test/contract.test.ts.
export default defineConfig({
  test: {
    include: ["test/contract.test.ts"],
    testTimeout: 60_000,
    hookTimeout: 60_000,
  },
});
