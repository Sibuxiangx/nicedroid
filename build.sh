#!/usr/bin/env bash
# One-shot build: frontend (Vite) -> embedded into the Go binary.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"

echo "==> Building admin UI..."
if [ ! -d "$ROOT/web/node_modules" ]; then
  npm --prefix "$ROOT/web" install
fi
npm --prefix "$ROOT/web" run build

echo "==> Building Go server..."
if [ -x "$HOME/sdk/go/bin/go" ]; then
  GO_BIN="$HOME/sdk/go/bin/go"
elif command -v go >/dev/null 2>&1; then
  GO_BIN="go"
else
  echo "go toolchain not found" >&2
  exit 1
fi
(cd "$ROOT" && "$GO_BIN" build -o nicedroid ./cmd/server)

echo "==> Done: $ROOT/nicedroid"
echo "Run:"
echo "  export NICEDROID_ADMIN_PASSWORD=change-me   # optional; otherwise generated on first boot"
echo "  ./nicedroid                                 # listens on :3000 by default"
