# AGENTS.md

Guidance for AI coding agents working in this repository.

## What this project is

`nicedroid` is a self-hosted Go gateway that exposes a Factory (`fk-`) API key as
standard OpenAI, Anthropic, OpenAI Responses and Google endpoints, with an
embedded shadcn/ui admin console. Single binary, SQLite storage, no external
services.

It talks to Factory's **internal LLM gateway** (the same one the Droid CLI uses),
not to the public Agent SDK. Nothing here is affiliated with or endorsed by
Factory.

## Upstream gateway requirements

These are load-bearing. Getting one wrong makes every upstream request fail.

1. **Content gate.** Every LLM request must carry a leading system message
   containing exactly:

   ```
   You are Droid, an AI software engineering agent built by Factory.
   ```

   Without it the gateway answers `403 Forbidden`. The proxy injects it in
   `config.DefaultDroidSystemPrompt` and every request builder adds it; a
   user-supplied system prompt is appended *after* it, never replaces it.

2. **Client identification.** Requests need `user-agent: factory-cli/<version>`
   plus `x-client-version: <version>`. A missing or unparsable version is
   rejected with "Unable to determine client version".

3. **One endpoint per wire format** (`internal/upstream/client.go`):

   | Family constant          | Upstream path                          |
   | ------------------------ | -------------------------------------- |
   | `FamilyChatCompletions`  | `/api/llm/o/v1/chat/completions`       |
   | `FamilyMessages`         | `/api/llm/a/v1/messages`               |
   | `FamilyResponses`        | `/api/llm/o/v1/responses`              |
   | `FamilyGoogle`           | `/api/llm/g/v1/generate`               |
   | (quota polling)          | `/api/billing/limits`                  |

4. **Provider selection** is the `x-api-provider` header. The same open-weight
   model is served by several backends (fireworks, baseten, databricks, mistral)
   with very different speeds, so the proxy resolves a provider per request and
   retries the next one on 402 / 429 / 5xx.

## Product policies (do not change these without an explicit ask)

- **No model downgrade or swapping.** If a standard-pool request fails because
  the quota is exhausted, the upstream error is passed through to the client.
  There is deliberately no fallback from a standard model to a Droid Core model.
  Droid Core models are only used when the caller asks for them by name.
- **Gemini is passthrough-only.** `gemini-*` models are never translated between
  wire formats and never participate in interchange; they are reachable only
  through `/v1/generate`, which forwards the payload verbatim.

## Layout

```
cmd/server/          entry point (Echo, routes, background jobs)
internal/config/     env config, model catalog, provider capability table, cost model
internal/store/      SQLite: upstream keys, client keys, request logs, settings, migrations
internal/upstream/   gateway client + request translation into each wire format
internal/router/     quota polling and weighted key selection
internal/api/        proxy endpoints, SSE converters (all format pairs), usage capture
internal/admin/      admin JSON API, provider pinning, streaming benchmark, JWT auth
web/                 embedded admin UI (React + Vite + shadcn/ui); web/dist is committed
```

## Build and run

The Go toolchain is **not on `PATH`** in this environment; it lives at
`~/sdk/go/bin/go`.

```bash
./build.sh                       # frontend build + Go binary (embeds web/dist)
```

`web/dist` is committed, so `go build ./cmd/server` works on a clean clone. If you
change anything under `web/src`, rebuild the frontend before rebuilding Go — the
binary embeds the built assets, not the sources.

Run locally:

```bash
NICEDROID_LISTEN=:3000 \
NICEDROID_DB=$PWD/nicedroid.db \
NICEDROID_ADMIN_PASSWORD=admin123 \
./nicedroid
```

All configuration is environment-driven (see `internal/config/config.go`);
everything mutable at runtime lives in SQLite and is edited from the admin UI.

## Verifying changes

There is no test suite. Verify by running the server and exercising it:

```bash
curl -s localhost:3000/healthz
curl -s -c /tmp/n.jar -X POST localhost:3000/admin/api/login \
  -H 'content-type: application/json' \
  -d '{"username":"admin","password":"admin123"}'
curl -s -b /tmp/n.jar localhost:3000/admin/api/models
```

Use **cheap models** when exercising the LLM path. `kimi-k3` bills at a 1.2×
multiplier and burns a lot of quota; `glm-5.3-flash` (0.06×) is the right choice
for smoke tests. The provider benchmark costs one generation per provider per
round, so keep rounds low while iterating.

## Measuring TPS — two deliberately different numbers

- **Request logs** (`store.ComputeTPS`): for streaming requests, output tokens
  over `latency − TTFT`; for non-streaming, over the full latency. This answers
  "how fast did the model generate". A one-time repair migration
  (`tps_repair_v1`) fixed rows written before this was correct.
- **Provider benchmark** (`admin.measureTPS`): output tokens over the *entire*
  request. Providers that buffer a stream and flush it as one burst would make a
  `latency − TTFT` window collapse to a few milliseconds and report absurd
  figures (a real measurement came back as 299953 tok/s). Total elapsed time
  cannot be gamed that way, so it is what ranks providers.

Do not "unify" these two.

## Conventions

- Go code and comments in English; admin UI copy in Chinese.
- Run `gofmt -w` on touched Go files before finishing.
- Match the surrounding comment style: comments explain *why*, not *what*.
- Never log or commit secrets. `nicedroid.db` holds real `fk-` keys and is
  gitignored — keep it that way.
