import { defineConfig } from "tsup";

export default defineConfig({
  entry: { index: "src/index.ts", testing: "src/testing.ts" },
  format: ["esm", "cjs"],
  // Both entry points share one copy of the SDK (errors, Job, ...) so that
  // `instanceof` checks work across "@hookyard/sdk" and "@hookyard/sdk/testing".
  splitting: true,
  dts: true,
  sourcemap: true,
  clean: true,
  target: "node18",
  platform: "neutral",
  treeshake: true,
});
