#!/usr/bin/env bash
# check-frontend.sh: the frontend checks, exactly as the
# frontend:typecheck-test CI job runs them (it calls this script;
# docs/ci.md). Locally: `make check-frontend`.
#
#   scripts/check-frontend.sh
#
# The CSS-token check, then `npm ci`, tsc, Vitest, the production build and
# the no-remote-resources check of the build (docs/privacy.md) in frontend/. npm's own settings apply: CI points npm_config_cache at a
# cached directory.
set -euo pipefail

cd "$(dirname "$0")/.."

node --version
npm --version

# No hard-coded colours outside tokens.css.
echo "==> scripts/check-css-tokens.sh"
scripts/check-css-tokens.sh

cd frontend
echo "==> npm ci"
npm ci --no-audit --no-fund
echo "==> npm run typecheck"
npm run typecheck
echo "==> npm test"
npm test
echo "==> npm run build"
npm run build
# No remote URLs in the bundle, CSP <meta> in index.html.
echo "==> npm run check:dist"
npm run check:dist

echo "check-frontend: ok"
