#!/usr/bin/env bash
# build-frontend.sh: install the frontend dependencies and build the Vite
# production bundle into frontend/dist, which the Go binary embeds, then
# check the bundle loads nothing remote (frontend/scripts/check-dist.mjs). The
# frontend:build CI job and the first step of scripts/linux.sh run it.
#
#   scripts/build-frontend.sh
#
# npm's own settings apply: CI and scripts/linux.sh point npm_config_cache
# at a cached directory.
set -euo pipefail

cd "$(dirname "$0")/../frontend"

node --version
npm --version
echo "==> npm ci"
npm ci --no-audit --no-fund
echo "==> npm run build"
npm run build
test -f dist/index.html
# The dist/ the binaries embed must load nothing from the network: no remote
# URLs, CSP <meta> present (docs/privacy.md).
echo "==> npm run check:dist"
npm run check:dist
echo "build-frontend: frontend/dist ready"
