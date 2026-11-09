import { readFileSync, writeFileSync } from "node:fs";
import { resolve } from "node:path";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import wails from "@wailsio/runtime/plugins/vite";

// Vite empties dist/ before each build, which deletes the committed
// dist/.gitkeep that lets `go build` embed dist/ on a fresh checkout
// (see frontend/assets.go). Recreate it so a build leaves git clean.
function keepDistPlaceholder(): Plugin {
  let outDir = "dist";
  return {
    name: "postsonde:keep-dist-placeholder",
    apply: "build",
    configResolved(config) {
      outDir = resolve(config.root, config.build.outDir);
    },
    closeBundle() {
      writeFileSync(resolve(outDir, ".gitkeep"), "");
    },
  };
}

// Writes the Content Security Policy from csp.json into the built
// index.html as a <meta> tag, first in <head> so it covers everything after
// it (docs/privacy.md). Build only: the Vite dev server injects an
// inline script and an HMR WebSocket the strict policy would block; in
// `wails3 dev` the Go asset server sends a relaxed policy header instead.
// The Go side reads the same file (frontend/assets.go), and
// scripts/check-dist.mjs checks the tag landed in dist/index.html.
function contentSecurityPolicyMeta(): Plugin {
  const csp = JSON.parse(
    readFileSync(resolve(import.meta.dirname, "csp.json"), "utf8"),
  ) as { directives: string[] };
  const policy = csp.directives.join("; ");
  if (/["<>&]/.test(policy)) {
    throw new Error(`csp.json: the policy must not contain " < > or &`);
  }
  return {
    name: "postsonde:csp-meta",
    apply: "build",
    // A plain string edit rather than Vite's tag descriptors, which would
    // entity-encode every ' in the policy and make dist/index.html unreadable.
    transformIndexHtml(html) {
      const head = /<head>/i;
      if (!head.test(html)) {
        throw new Error("index.html: no <head> to put the CSP <meta> in");
      }
      return html.replace(
        head,
        `<head>\n    <meta http-equiv="Content-Security-Policy" content="${policy}" />`,
      );
    },
  };
}

// https://vitejs.dev/config/
export default defineConfig({
  server: {
    host: "127.0.0.1",
    port: Number(process.env.WAILS_VITE_PORT) || 9245,
    strictPort: true,
  },
  plugins: [
    react(),
    wails("./bindings"),
    contentSecurityPolicyMeta(),
    keepDistPlaceholder(),
  ],
});
