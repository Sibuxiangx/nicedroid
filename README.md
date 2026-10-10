# nicedroid

**Turn a Factory `fk-` API key into standard OpenAI / Anthropic / Responses APIs.**

nicedroid is a single-binary Go gateway that sits in front of Factory's LLM
gateway — the same one the Droid CLI talks to. Point your existing OpenAI or
Anthropic SDK at it and you get Droid's models, including the cheap open-weight
**Droid Core** pool, in the wire format your client already speaks. It ships with
an embedded admin console for quota tracking, key pooling, provider routing and
per-request cost.

> Previously released as **droid2api**. If you came here searching for
> `droid2api`, this is the same project under its new name.

[中文文档 →](README.zh-CN.md)

---

## Features

- **Four wire formats, freely mixed.** OpenAI Chat Completions, Anthropic
  Messages, OpenAI Responses and Google generate. Any entry point can request any
  model: ask for GLM from an Anthropic client and the proxy translates the request
  and the streamed response for you.
- **Droid Core access.** The open-weight pool (`glm-5.3-flash`, `kimi-k3`,
  `deepseek-v4.1-flash`, …) is reachable through `/v1/responses` as well as the
  other formats.
- **Multi-provider routing.** One model, several backends (fireworks, baseten,
  databricks, mistral). Pin the fastest one by hand, or let the built-in
  streaming benchmark measure them and pick the winner. On 402 / 429 / 5xx the
  proxy fails over to the next provider transparently.
- **Quota awareness.** Polls Factory's billing limits and shows the standard and
  core pools across their 5-hour, weekly and monthly windows.
- **Honest accounting.** Per-request latency, time to first token, cache read /
  write tokens, tokens per second, and Factory Standard Credits estimated with
  the same formula the CLI uses.
- **Single binary.** The React admin UI is embedded with `go:embed`. State lives
  in one SQLite file. No external services.

## Endpoints

| Method | Path                         | Format                    |
| ------ | ---------------------------- | ------------------------- |
| `POST` | `/v1/chat/completions`       | OpenAI Chat Completions   |
| `POST` | `/v1/messages`               | Anthropic Messages        |
| `POST` | `/v1/messages/count_tokens`  | Anthropic token counting  |
| `POST` | `/v1/responses`              | OpenAI Responses          |
| `POST` | `/v1/generate`               | Google generate (passthrough) |
| `GET`  | `/v1/models`                 | Model list                |
| `GET`  | `/healthz`                   | Health check              |

## Quick start

Requires Go 1.27+ and Node 20+ (Node is only needed to rebuild the UI; a
prebuilt `web/dist` is committed, so `go build` works on a clean clone).

```bash
git clone https://github.com/Sibuxiangx/nicedroid
cd nicedroid
./build.sh
```

Then start it:

```bash
NICEDROID_ADMIN_PASSWORD=change-me ./nicedroid
```

Open <http://localhost:3000>, sign in, and add your Factory API key (`fk-…`) on
the **Keys** page. That key is what the gateway presents upstream.

Prefer to build it yourself:

```bash
cd web && npm install && npm run build && cd ..
go build -o nicedroid ./cmd/server
```

## Usage

Once an upstream key is configured, downstream clients authenticate with a
client key created in the admin console (`sk-nicedroid-…`). They are optional:
with no client keys defined, `/v1/*` is open.

**OpenAI-compatible:**

```bash
curl http://localhost:3000/v1/chat/completions \
  -H 'content-type: application/json' \
  -H 'authorization: Bearer sk-nicedroid-…' \
  -d '{
    "model": "glm-5.3-flash",
    "messages": [{"role": "user", "content": "Hello"}],
    "stream": true
  }'
```

**Anthropic-compatible** — point `ANTHROPIC_BASE_URL` at the gateway and Claude
Code or any Anthropic SDK works unchanged:

```bash
curl http://localhost:3000/v1/messages \
  -H 'content-type: application/json' \
  -d '{
    "model": "claude-sonnet-5-5",
    "max_tokens": 256,
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

**OpenAI Responses:**

```bash
curl http://localhost:3000/v1/responses \
  -H 'content-type: application/json' \
  -d '{"model": "deepseek-v4.1-flash", "input": "Hello"}'
```

## Models

**Standard pool** — Claude Opus 5 / Sonnet 5 / Haiku, GPT-6 and GPT-5.4 families,
Gemini 3 Pro and Flash (Gemini is passthrough-only, see below).

**Droid Core pool** — the cheap open-weight models, several of which are served
by multiple backends:

| Model                 | Providers                        |
| --------------------- | -------------------------------- |
| `glm-5.3`             | fireworks, baseten, databricks   |
| `glm-5.3-flash`       | fireworks, baseten, databricks   |
| `kimi-k3`             | fireworks, baseten, databricks   |
| `deepseek-v4.1-flash` | fireworks, baseten, databricks   |
| `nemotron-3-ultra`    | baseten, fireworks               |
| `qwen3.8-max`         | fireworks                        |
| `inkling`             | fireworks                        |
| `minimax-m3`          | fireworks                        |
| `mistral-large-4`     | mistral                          |

`GET /v1/models` and the admin console list the live catalog with vision support,
reasoning-effort levels and cost multipliers.

## Provider selection

Factory's gateway picks the serving backend from the `x-api-provider` header, and
backends differ a lot: measured on `glm-5.3-flash`, databricks finished roughly
20% faster end-to-end than fireworks and baseten. nicedroid therefore resolves a
provider per request:

- **Pin it manually** from the provider dropdown on the Models page. The choice
  is stored in SQLite; clearing it restores the catalog default.
- **Benchmark it** with the *Speed test* button. The server runs the same
  streaming generation through every provider of that model, round-robin over
  1–3 rounds, streams each result to the browser, and ranks providers by the
  median across rounds. Tick *auto-pin the fastest* to apply the winner.
- **Fail over automatically** at runtime: the pinned provider is tried first, then
  the rest of the catalog order, moving on at 402 / 429 / 5xx.

The benchmark reports **end-to-end throughput** (output tokens ÷ total request
time), not tokens ÷ generation window. Providers that buffer a stream and flush
it as one burst make the generation window collapse to a few milliseconds and
inflate that ratio into the hundreds of thousands; total elapsed time cannot be
faked that way. Time to first token is reported separately.

## Admin console

Served from the same binary at `/admin`:

- **Dashboard** — quota water levels for both pools, request volume, cache hit
  rate, latency and TPS trends.
- **Keys** — upstream key pool (weighted round-robin, skip-exhausted, one-click
  verification) and downstream client keys.
- **Models** — full catalog, reasoning-effort levels, provider pinning and the
  streaming speed test.
- **Logs** — every request with model, provider, pool, status, latency, TTFT,
  token counts, cache reads, TPS and estimated credits.
- **Settings** — system prompt override and a playground for smoke-testing a model.

The first boot generates an admin password and prints it to the log unless
`NICEDROID_ADMIN_PASSWORD` is set.

## Configuration

All configuration is environment-driven.

| Variable                             | Default                  | Purpose                                  |
| ------------------------------------ | ------------------------ | ---------------------------------------- |
| `NICEDROID_LISTEN`                   | `:3000`                  | Listen address                           |
| `NICEDROID_DB`                       | `nicedroid.db`           | SQLite path                              |
| `NICEDROID_UPSTREAM`                 | `https://api.factory.ai` | Upstream base URL                        |
| `NICEDROID_SYSTEM_PROMPT`            | Droid gate prompt        | Leading system message (see below)       |
| `NICEDROID_ADMIN_USER`               | `admin`                  | Admin username                           |
| `NICEDROID_ADMIN_PASSWORD`           | generated on first boot  | Admin password                           |
| `NICEDROID_JWT_SECRET`               | generated                | Session signing secret                   |
| `NICEDROID_QUOTA_POLL_SECONDS`       | `60`                     | Quota polling interval                   |
| `NICEDROID_REQUEST_TIMEOUT_SECONDS`  | `600`                    | Upstream request timeout                 |
| `NICEDROID_LOG_RETENTION_DAYS`       | `30`                     | Request log retention                    |

## How it works

Two constraints shape the whole proxy:

1. **The content gate.** Factory's gateway rejects every LLM request that does not
   begin with the system message
   `You are Droid, an AI software engineering agent built by Factory.` with a
   `403`. nicedroid injects it automatically and appends your own system prompt
   after it.
2. **Version identification.** Requests must carry a `factory-cli` user agent and
   client version, so the proxy identifies itself the same way the CLI does.

Given both, the proxy translates freely between the four wire formats in each
direction, including streaming (SSE) and tool calls.

## Caveats

- **Not affiliated with Factory.** This is an unofficial client that speaks to
  internal endpoints. Those endpoints are undocumented and can change without
  notice; a CLI update may break this project.
- Usage still counts against your own Factory quota and is billed by Factory. The
  credit figures shown in the logs are an estimate derived from the CLI's own
  formula, not an authoritative number.
- There is no model downgrade path by design: when a quota pool is exhausted, the
  upstream error is returned to the caller as-is.
- Gemini models are passthrough-only. They are reachable through `/v1/generate`
  and never participate in format conversion.
- Keep your database file private — it stores upstream API keys in plain text.

## Development

See [AGENTS.md](AGENTS.md) for the architecture, upstream requirements and the
reasoning behind the product policies. In short: `./build.sh` builds everything,
there is no test suite, and changes are verified by running the server and
exercising the endpoints with `curl`.

## License

No license has been chosen yet. Until one is added, the code is all rights
reserved.
