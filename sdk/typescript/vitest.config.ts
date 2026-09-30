import { defineConfig } from "vitest/config";

export default defineConfig({
  test: {
    include: ["test/**/*.test.ts"],
    // Contract tests need a live server: run them with `pnpm test:contract`.
    exclude: ["test/contract.test.ts", "**/node_modules/**"],
    restoreMocks: true,
    unstubEnvs: true,
  },
});
