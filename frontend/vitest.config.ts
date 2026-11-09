import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

// Test config.
// Kept apart from vite.config.ts so tests don't load the Wails dev plugin
// (it expects generated bindings) or the dist/.gitkeep build hook.
// Test files are never imported from main.tsx, so they stay out of the
// production bundle; tsconfig's "include": ["src"] still type-checks them.
export default defineConfig({
  plugins: [react()],
  server: {
    fs: {
      // The error tests replay Go golden files (`?raw` imports) so both
      // sides agree on the JSON shape (docs/errors.md). Allow only those
      // directories outside frontend/, and the auto Content-Type cases
      // shared with internal/core/request (lib/contentType.test.ts), and
      // the filter pipe cases shared with internal/core/template
      // (lib/templateFilters.test.ts), and the custom theme tokens of
      // internal/core/themes (styles/customTheme.test.ts).
      allow: [
        ".",
        "../internal/app/testdata",
        "../internal/app/apperr/testdata",
        "../internal/core/request/testdata",
        "../internal/core/template/testdata",
        "../internal/core/themes/testdata",
      ],
    },
  },
  test: {
    environment: "jsdom",
    // scripts/: tests for the build tooling (check-dist.mjs).
    include: ["src/**/*.test.{ts,tsx}", "scripts/**/*.test.mjs"],
    setupFiles: ["src/test/setup.ts"],
    // Vitest empties CSS imports, `?raw` ones too. The token contrast check
    // (styles/contrast.test.ts, styles/themeContrast.ts) reads tokens.css,
    // the focus style check (styles/focusStyles.test.ts) every stylesheet as text.
    css: { include: [/\.css\?raw/] },
    // VITEST_MAX_WORKERS caps the worker count (default: one per core), so
    // several checkouts can run their tests at once without every jsdom
    // test timing out.
    maxWorkers: Number(process.env.VITEST_MAX_WORKERS) || undefined,
  },
});
