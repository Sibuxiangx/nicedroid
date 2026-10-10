// Typed client for the nicedroid admin API. Auth rides on the HttpOnly cookie
// set by /admin/api/login, so every request uses same-origin credentials.

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function api<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`/admin/api${path}`, {
    credentials: "same-origin",
    headers: { "content-type": "application/json" },
    ...init,
  })
  if (!res.ok) {
    let msg = res.statusText
    try {
      const body = await res.json()
      msg = body.error ?? msg
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg)
  }
  return (await res.json()) as T
}

// ---------- types ----------

export interface Window {
  usedPercent: number
  windowEnd: string | null
  secondsRemaining: number | null
}

export interface PoolLimits {
  fiveHour: Window
  weekly: Window
  monthly: Window
}

export interface KeyState {
  key_id: number
  key_name: string
  key_masked: string
  standard_max: number
  core_max: number
  standard: PoolLimits
  core: PoolLimits
  overage_preference: string
  extra_usage_cents: number
  last_error?: string
  checked_at: string
}

export interface QuotaSnapshot {
  keys: KeyState[]
  updated_at: string
}

export interface ModelStat {
  model: string
  requests: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits: number
  cache_hit_rate: number
  avg_tps: number
}

export interface HourlyPoint {
  hour: string
  requests: number
  errors: number
}

export interface TPSPoint {
  hour: string
  avg_tps: number
  avg_ttft_ms: number
  cache_hit_rate: number
}

export interface Stats {
  total_requests: number
  error_requests: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits: number
  cache_hit_rate: number
  avg_tps: number
  avg_ttft_ms: number
  avg_latency_ms: number
  by_model: ModelStat[] | null
  hourly: HourlyPoint[] | null
  tps_series: TPSPoint[] | null
}

export interface UpstreamKey {
  id: number
  name: string
  key_masked: string
  enabled: boolean
  weight: number
  last_status: string
  last_checked: string | null
  created_at: string
}

export interface ClientKey {
  id: number
  name: string
  key: string
  enabled: boolean
  rpm_limit: number
  daily_tokens: number
  models: string
  created_at: string
}

export interface ModelView {
  id: string
  display_name: string
  family: string
  provider: string
  providers?: string[]
  tier: string
  vision: boolean
  multiplier: number
  passthrough: boolean
  reasoning_efforts?: string[]
  current_provider: string
  pinned_provider: string
  provider_count: number
  // Copied from the CLI registry. `cli_visible` is false for entries the CLI no
  // longer offers; `deprecated_on` is the retirement date the registry records.
  service_tier?: string
  cli_visible: boolean
  deprecated_on?: string
  replacement?: string
  promotions?: { discount: number; starts_at: string; expires_at: string; label: string }[]
}

export interface RequestLog {
  id: number
  ts: string
  client_key: string
  requested_model: string
  used_model: string
  provider: string
  pool: string
  status: number
  latency_ms: number
  ttft_ms: number
  prompt_tokens: number
  completion_tokens: number
  cache_read_tokens: number
  cache_write_tokens: number
  credits: number
  tps: number
  stream: boolean
  error: string
}

export interface BenchmarkResult {
  provider: string
  ok: boolean
  round: number
  ttft_ms: number
  elapsed_ms: number
  tokens: number
  chars: number
  tps: number
  error?: string
}

export interface ProviderSummary {
  provider: string
  ok: boolean
  tps: number
  ttft_ms: number
  rounds_ok: number
  rounds: number
  error?: string
}

export type BenchmarkEvent =
  | { type: "start"; providers: string[]; rounds: number }
  | { type: "probe"; provider: string; round: number }
  | { type: "result"; result: BenchmarkResult }
  | { type: "summary"; summaries: ProviderSummary[]; best: string; best_tps: number }

// benchmarkProviders runs the streaming provider probe and reports each
// provider result as it arrives. The server pushes SSE events; fetch is used
// instead of EventSource so the call rides on the admin cookie cleanly.
export async function benchmarkProviders(
  modelID: string,
  providers: string[],
  rounds: number,
  onEvent: (ev: BenchmarkEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await fetch(`/admin/api/models/${encodeURIComponent(modelID)}/benchmark`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ providers, rounds }),
    signal,
  })
  if (!res.ok || !res.body) {
    let msg = res.statusText
    try {
      const body = await res.json()
      msg = body.error ?? msg
    } catch {
      /* ignore */
    }
    throw new ApiError(res.status, msg)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ""
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let idx = buf.indexOf("\n\n")
    while (idx >= 0) {
      const block = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      let event = "message"
      let data = ""
      for (const line of block.split("\n")) {
        if (line.startsWith("event:")) event = line.slice(6).trim()
        else if (line.startsWith("data:")) data += line.slice(5).trim()
      }
      if (data) {
        try {
          const parsed = JSON.parse(data)
          if (event === "start")
            onEvent({ type: "start", providers: parsed.providers ?? [], rounds: parsed.rounds ?? 1 })
          else if (event === "probe")
            onEvent({ type: "probe", provider: parsed.provider ?? "", round: parsed.round ?? 1 })
          else if (event === "result") onEvent({ type: "result", result: parsed })
          else if (event === "summary")
            onEvent({
              type: "summary",
              summaries: parsed.providers ?? [],
              best: parsed.best ?? "",
              best_tps: parsed.best_tps ?? 0,
            })
        } catch {
          /* ignore malformed frame */
        }
      }
      idx = buf.indexOf("\n\n")
    }
  }
}

// ---------- calls ----------

export const apiClient = {
  login: (username: string, password: string) =>
    api<{ token: string; username: string }>("/login", {
      method: "POST",
      body: JSON.stringify({ username, password }),
    }),
  logout: () => api<{ ok: boolean }>("/logout", { method: "POST" }),
  session: () => api<{ username: string }>("/session"),
  stats: (hours: number) => api<Stats>(`/stats?hours=${hours}`),
  quotas: () => api<QuotaSnapshot>("/quotas"),
  refreshQuotas: () => api<QuotaSnapshot>("/quotas/refresh", { method: "POST" }),
  upstreamKeys: () => api<{ keys: UpstreamKey[] }>("/upstream-keys"),
  addUpstreamKey: (name: string, key: string, weight: number) =>
    api<{ id: number }>("/upstream-keys", {
      method: "POST",
      body: JSON.stringify({ name, key, weight }),
    }),
  patchUpstreamKey: (id: number, patch: Partial<Pick<UpstreamKey, "name" | "enabled" | "weight">>) =>
    api<{ ok: boolean }>(`/upstream-keys/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),
  deleteUpstreamKey: (id: number) =>
    api<{ ok: boolean }>(`/upstream-keys/${id}`, { method: "DELETE" }),
  verifyUpstreamKey: (id: number) =>
    api<{ ok: boolean; status: string }>(`/upstream-keys/${id}/verify`, { method: "POST" }),
  clientKeys: () => api<{ keys: ClientKey[] }>("/client-keys"),
  addClientKey: (name: string, rpm: number, dailyTokens: number) =>
    api<ClientKey>("/client-keys", {
      method: "POST",
      body: JSON.stringify({ name, rpm_limit: rpm, daily_tokens: dailyTokens }),
    }),
  patchClientKey: (id: number, patch: Partial<Pick<ClientKey, "name" | "enabled">>) =>
    api<{ ok: boolean }>(`/client-keys/${id}`, { method: "PATCH", body: JSON.stringify(patch) }),
  deleteClientKey: (id: number) => api<{ ok: boolean }>(`/client-keys/${id}`, { method: "DELETE" }),
  models: () => api<{ models: ModelView[] }>("/models"),
  setModelProvider: (id: string, provider: string) =>
    api<{ ok: boolean; model: string; current_provider: string; pinned_provider: string }>(
      `/models/${encodeURIComponent(id)}/provider`,
      { method: "PUT", body: JSON.stringify({ provider }) },
    ),
  logs: (params: URLSearchParams) => api<{ logs: RequestLog[]; total: number }>(`/logs?${params}`),
  settings: () =>
    api<{ system_prompt: string; upstream: string; prompt_cache: boolean }>("/settings"),
  putSettings: (systemPrompt: string, promptCache?: boolean) =>
    api<{ ok: boolean; prompt_cache: boolean }>("/settings", {
      method: "PUT",
      body: JSON.stringify({ system_prompt: systemPrompt, prompt_cache: promptCache }),
    }),
  playground: (model: string, message: string) =>
    api<{ model: string; text: string; raw_usage: unknown }>("/playground", {
      method: "POST",
      body: JSON.stringify({ model, message }),
    }),
}

// ---------- helpers ----------

export function fmtPct(v: number | null | undefined, digits = 1): string {
  if (v === null || v === undefined || Number.isNaN(v)) return "-"
  return `${(v * 100).toFixed(digits)}%`
}

export function fmtNum(v: number | null | undefined): string {
  if (v === null || v === undefined) return "-"
  if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`
  if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`
  return String(v)
}

export function fmtTime(iso: string | null | undefined): string {
  if (!iso) return "-"
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return iso
  return d.toLocaleString("zh-CN", { hour12: false })
}

export function fmtDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return "-"
  const h = Math.floor(seconds / 3600)
  const m = Math.floor((seconds % 3600) / 60)
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}
