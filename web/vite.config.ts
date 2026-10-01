import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import type { Plugin } from "vite";
import { defineConfig } from "vitest/config";

const here = (path: string) => fileURLToPath(new URL(path, import.meta.url));

// Vite empties dist/ on every build; keep the placeholder that lets the Go
// server compile (go:embed) before the dashboard is built.
const keepGitkeep: Plugin = {
  name: "keep-gitkeep",
  closeBundle() {
    writeFileSync(here("./dist/.gitkeep"), "");
  },
};

export default defineConfig({
  plugins: [react(), tailwindcss(), keepGitkeep],
  resolve: {
    alias: { "@": here("./src") },
  },
  server: {
    // `make run` serves the API on :8080; the dev server proxies to it.
    proxy: {
      "/v1": "http://localhost:8080",
      "/ui": "http://localhost:8080",
    },
  },
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
  },
  test: {
    environment: "jsdom",
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    css: false,
  },
});
