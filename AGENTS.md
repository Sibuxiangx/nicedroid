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
- **Proxy auth is default-deny.** `/v1/*` refuses every request until an enabled
  client key exists, because those endpoints spend the operator's upstream
  quota. An earlier revision served them openly when no keys were configured,
  which is harmless on localhost and an open relay on a public host. Do not
  restore that: an unconfigured deployment must fail closed. The admin console
  keeps its own JWT auth and is deliberately not behind this middleware, so the
  first key can always be created there.

## The model catalog is generated, not written

`manifest.json` is the source of truth for every model: ids, providers, quota
pools, vision support, reasoning efforts, cost multipliers and promotions. The Go
binary embeds it (`manifest_embed.go`) and `internal/config/manifest.go`
translates it into `Model` values at startup. `internal/config/models.go` holds
only the `Model` type and the lookup helpers.

Do not add a model by editing Go. Extract it:

```bash
go run ./tools/extract-manifest ~/.local/bin/droid \
  --version "$(droid --version)" --out manifest.json
```

`tools/extract-manifest` parses the model registry out of the minified bundle
compiled into the CLI binary, because no API serves it. Two consequences worth
knowing before you touch it:

- **Field order is not stable** between registry entries, so the parser anchors on
  the map key (`"model-id":{id:"model-id",`) and brace-matches the value. Do not
  add a regex that assumes a field order.
- **Some values are JS identifiers, not literals** (`apiProviders:N`,
  `cost:{promotions:[Mt]}`). These are resolved only when exactly one
  unambiguous definition exists, and otherwise reported as missing. Never guess.

The extractor validates its own output and refuses to write a manifest that is
missing models the CLI lists in `droid exec --help` (`--cross-check`), and it
checks that it agrees with the CLI about which models are retired. It also skips
`auto` / `auto-fast` on purpose: those are server-side router entries, not models,
and the gateway cannot describe, route or benchmark them.

Two distinctions in the data are easy to get wrong:

- **Platform vs backend.** `provider` is the platform serving the model
  (`factory`, `anthropic`, `openai`, `google`, `xai`); `providers` is the list of
  backends the gateway can dispatch to with `x-api-provider`. For open-weight
  models these differ: `glm-5.3-flash` is `provider: "factory"` with
  `providers: ["fireworks","baseten","databricks"]`. Collapsing them puts
  Factory models under a "fireworks" heading in the UI and breaks the
  per-platform cost defaults in `cost.go`.
- **Current vs retired.** A model is retired when the registry gives it a
  `deprecation.date` or hides it with `availableInCLI:!1`. The admin console
  collapses retired models; `GET /v1/models` lists them with a `deprecated` flag.
  The two signals do not overlap — most hidden models carry no date.

The output must stay **deterministic**: the same binary has to produce
byte-identical JSON. There is deliberately no extraction timestamp, and model
ordering is sorted. This is what lets the refresh job decide by comparing
`cli.version` alone — a run-dependent field would make every run differ and
produce an empty commit every day.

`.github/workflows/update-models.yml` runs this daily: it compares the manifest's
`cli.version` against the latest `@factory/cli` on npm and does nothing when they
match. When they differ it downloads that build, verifies its SHA-256, checks the
build reports the version it was published under, re-extracts, runs the tests,
and commits. The CLI version in the manifest is also the version the proxy
identifies itself as upstream (`config.ManifestVersion()`), so refreshing the
catalog is what moves the gateway onto a newer CLI release.

## Layout

```
manifest.json        generated model catalog — the source of truth for every model
tools/               extract-manifest: builds manifest.json from a Droid CLI binary
cmd/server/          entry point (Echo, routes, background jobs)
internal/config/     env config, manifest decoding, provider capability table, cost model
internal/store/      SQLite: upstream keys, client keys, request logs, settings, migrations
internal/upstream/   gateway client + request translation into each wire format
internal/router/     quota polling and weighted key selection
internal/api/        proxy endpoints, SSE converters (all format pairs), usage capture
internal/admin/      admin JSON API, provider pinning, streaming benchmark, JWT auth
web/                 embedded admin UI (React + Vite + shadcn/ui); web/dist is committed
web/assets-source/   source artwork the icons are cropped from
deploy/              Podman image + systemd unit for running this on a server
.github/workflows/   daily refresh of the model catalog
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

The catalog has tests; run them first, since they guard the embedded manifest:

```bash
go test ./...
```

Beyond that there is no test suite. Verify by running the server and exercising it:

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
  over `latency − TTFT` — but only when the window clears both an absolute
  floor (100 ms) and a relative one (10% of the request). Many providers
  buffer a stream and flush it as one burst, which puts TTFT within a few tens
  of milliseconds of the total latency; dividing by that sliver reported
  hundreds of tokens per second for a two-second request, and whether a row
  cleared the old absolute 50 ms floor was a millisecond coin flip that made
  the same request swing between 5 and 500 tok/s. When TTFT dominates there is
  no way to recover the true generation speed, so the figure falls back to
  end-to-end. `tps_repair_v1` and `tps_repair_v2` are one-time repairs for
  rows written before each of these corrections.
- **Provider benchmark** (`admin.measureTPS`): output tokens over the *entire*
  request. Providers that buffer a stream and flush it as one burst would make a
  `latency − TTFT` window collapse to a few milliseconds and report absurd
  figures (a real measurement came back as 299953 tok/s). Total elapsed time
  cannot be gamed that way, so it is what ranks providers.

Do not "unify" these two.

## Icons

The admin icons are a 1:1 crop of the artwork in `web/assets-source/`, generated
by `tools/make-icons.sh` (ImageMagick 7 + cwebp). Do not hand-edit the files in
`web/public/` — adjust the `CROP` at the top of the script and rerun it. The
script is deterministic, so a rerun on an unchanged crop produces identical
bytes and leaves no diff.

`favicon.ico` carries 16/32/48 for browsers; `logo.webp` is the rounded 128px
mark used in the app bar and on the login card, with its corners baked in so the
`<img>` needs no CSS mask. `apple-touch-icon.png` is deliberately opaque: iOS
composites transparency onto black.

## Conventions

- Go code and comments in English; admin UI copy in Chinese.
- Run `gofmt -w` on touched Go files before finishing.
- Match the surrounding comment style: comments explain *why*, not *what*.
- Never log or commit secrets. `nicedroid.db` holds real `fk-` keys and is
  gitignored — keep it that way.
