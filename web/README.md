# nicedroid admin UI

React + TypeScript + Vite + shadcn/ui console, embedded into the Go binary with
`go:embed` (see `embed.go`).

```bash
npm install
npm run dev      # dev server; proxies /admin/api and /v1 to 127.0.0.1:3300
npm run build    # writes dist/, which the Go binary embeds
```

The committed `dist/` is what `go build ./cmd/server` embeds, so rerun
`npm run build` after changing anything under `src/` and before rebuilding Go.

Project overview, configuration and API documentation live in the
[repository README](../README.md).
