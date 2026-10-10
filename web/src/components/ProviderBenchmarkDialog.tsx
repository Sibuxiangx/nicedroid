import { useEffect, useMemo, useRef, useState } from "react"
import {
  apiClient,
  benchmarkProviders,
  type BenchmarkResult,
  type ModelView,
  type ProviderSummary,
} from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Progress } from "@/components/ui/progress"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"
import { Loader2, Play, Square, Zap } from "lucide-react"
import { toast } from "sonner"

type Phase = "idle" | "running" | "done" | "stopped"

interface Row {
  provider: string
  state: "pending" | "running" | "done" | "failed"
  summary?: ProviderSummary
  /** Latest result per phase, so the two groups report independently. */
  tpsResult?: BenchmarkResult
  cacheResult?: BenchmarkResult
  tpsDone: number
  cacheDone: number
  /** Running cache totals across the cache group, for a live hit rate. */
  cachePrompt: number
  cacheCached: number
  /** Which group the most recent round belonged to. */
  lastPhase?: "tps" | "cache"
}

interface Props {
  model: ModelView | null
  onClose: () => void
  onApplied: () => void
}

// ProviderBenchmarkDialog streams the same generation through every backend of
// a model, shows time-to-first-token, tokens per second and the prompt cache
// hit rate as each one lands, and can pin the best provider for that model.
// Cache dominates the ranking: a backend that cannot cache re-bills the whole
// prompt every turn, which costs more than a slower one that can.
export function ProviderBenchmarkDialog({ model, onClose, onApplied }: Props) {
  const [rows, setRows] = useState<Row[]>([])
  const [phase, setPhase] = useState<Phase>("idle")
  const [autoApply, setAutoApply] = useState(true)
  const [rounds, setRounds] = useState(3)
  const [best, setBest] = useState<{
    provider: string
    tps: number
    cacheHit: number
    bar: number
  } | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const providers = useMemo(() => model?.providers ?? [], [model])

  function reset() {
    setRows(
      providers.map((p) => ({
        provider: p,
        state: "pending" as const,
        tpsDone: 0,
        cacheDone: 0,
        cachePrompt: 0,
        cacheCached: 0,
      })),
    )
    setPhase("idle")
    setBest(null)
  }

  useEffect(() => {
    reset()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model?.id])

  useEffect(() => {
    return () => abortRef.current?.abort()
  }, [])

  async function run(roundCount: number) {
    if (!model) return
    abortRef.current?.abort()
    const ctrl = new AbortController()
    abortRef.current = ctrl
    setBest(null)
    setRounds(roundCount)
    setRows(
      providers.map((p) => ({
        provider: p,
        state: "pending" as const,
        tpsDone: 0,
        cacheDone: 0,
        cachePrompt: 0,
        cacheCached: 0,
      })),
    )
    setPhase("running")
    try {
      await benchmarkProviders(
        model.id,
        providers,
        roundCount,
        (ev) => {
          if (ev.type === "probe") {
            setRows((prev) =>
              prev.map((row) =>
                row.provider === ev.provider ? { ...row, state: "running" as const } : row,
              ),
            )
          } else if (ev.type === "result") {
            const r = ev.result
            setRows((prev) =>
              prev.map((row) => {
                if (row.provider !== r.provider) return row
                const isTps = r.phase === "tps"
                // A failed round must not erase a good one from an earlier
                // round, so only successful rounds replace the shown result.
                const prevResult = isTps ? row.tpsResult : row.cacheResult
                const keep = prevResult?.ok && !r.ok
                return {
                  ...row,
                  lastPhase: r.phase,
                  tpsDone: isTps ? row.tpsDone + 1 : row.tpsDone,
                  cacheDone: isTps ? row.cacheDone : row.cacheDone + 1,
                  // Running cache totals, so the hit rate can be shown while
                  // the cache group is still in flight.
                  cachePrompt: isTps || keep ? row.cachePrompt : row.cachePrompt + r.prompt_tokens,
                  cacheCached: isTps || keep ? row.cacheCached : row.cacheCached + r.cached_tokens,
                  state: (keep ? row.state : r.ok ? "done" : "failed") as Row["state"],
                  tpsResult: isTps ? (keep ? row.tpsResult : r) : row.tpsResult,
                  cacheResult: isTps ? row.cacheResult : keep ? row.cacheResult : r,
                }
              }),
            )
          } else if (ev.type === "summary") {
            setRows((prev) =>
              prev.map((row) => {
                const s = ev.summaries.find((x) => x.provider === row.provider)
                if (!s) return row
                return {
                  ...row,
                  summary: s,
                  state: (s.ok ? "done" : "failed") as Row["state"],
                }
              }),
            )
            setBest({
              provider: ev.best,
              tps: ev.best_tps,
              cacheHit: ev.best_cache_hit,
              bar: ev.cache_hit_bar,
            })
            setPhase("done")
          }
        },
        ctrl.signal,
      )
    } catch (err) {
      if ((err as Error)?.name === "AbortError") {
        setPhase("stopped")
        return
      }
      toast.error((err as Error)?.message ?? "测试失败")
      setPhase("stopped")
    }
  }

  async function apply(provider: string) {
    if (!model || !provider) return
    try {
      await apiClient.setModelProvider(model.id, provider)
      toast.success(`已固定 ${model.id} → ${provider}`)
      onApplied()
    } catch (err) {
      toast.error((err as Error)?.message ?? "应用失败")
    }
  }

  const maxTPS = Math.max(0, ...rows.map((r) => r.summary?.tps ?? r.tpsResult?.tps ?? 0))
  const finished = phase === "done" || phase === "stopped"

  // Auto-pin the winner once the run completes, so a single click on a model
  // is enough to pick its fastest provider.
  const appliedRef = useRef(false)
  useEffect(() => {
    if (phase !== "done") {
      appliedRef.current = false
      return
    }
    if (!autoApply || appliedRef.current || !best?.provider) return
    appliedRef.current = true
    void apply(best.provider)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [phase, autoApply, best?.provider])

  return (
    <Dialog open={!!model} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>供应商测速 · {model?.display_name}</DialogTitle>
          <DialogDescription>
            分两组探测：速度组 2 轮，用长生成任务测端到端吞吐（输出 token ÷
            总耗时）；缓存组 {rounds} 轮重复同一请求，测提示词命中缓存的占比。命中率 ≥
            {best?.bar ?? 90}% 才认为该供应商可用于 agent 场景，达标者中再选最快的。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {rows.map((row) => {
            const s = row.summary
            // Show whichever group is in flight, falling back to the speed
            // group once it is done; the summary carries the final figures.
            const r = (row.lastPhase === "cache" ? row.cacheResult : row.tpsResult) ?? row.tpsResult
            const shownTPS = s?.tps ?? r?.tps ?? 0
            const shownTTFT = s?.ttft_ms || r?.ttft_ms || 0
            const pct = maxTPS > 0 && shownTPS ? Math.max(4, Math.round((shownTPS / maxTPS) * 100)) : 0
            const isBest = best?.provider === row.provider
            // Live cache hit rate while the cache group runs; the summary
            // replaces it with the settled average.
            const liveHit = row.cachePrompt > 0 ? (row.cacheCached / row.cachePrompt) * 100 : null
            const cacheHit = s?.cache_rounds ? s.cache_hit_pct : liveHit
            const cacheRounds = s?.cache_rounds ?? row.cacheDone
            const cacheGood = (cacheHit ?? 0) >= (best?.bar ?? 90)
            return (
              <div key={row.provider} className="space-y-1.5 rounded-lg border p-3">
                <div className="flex items-center justify-between gap-3">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{row.provider}</span>
                    {isBest && (
                      <Badge className="text-[10px]">
                        <Zap className="mr-0.5 h-3 w-3" />
                        {cacheGood ? "缓存最优" : "最快"}
                      </Badge>
                    )}
                    {model?.pinned_provider === row.provider && (
                      <Badge variant="secondary" className="text-[10px]">
                        已固定
                      </Badge>
                    )}
                    {row.state === "pending" && <span className="text-xs text-muted-foreground">等待中</span>}
                    {row.state === "running" && (
                      <Loader2 className="h-3.5 w-3.5 animate-spin text-muted-foreground" />
                    )}
                    {!s && (row.tpsDone > 0 || row.cacheDone > 0) && (
                      <span className="text-xs text-muted-foreground">
                        速度 {row.tpsDone}/2
                        {cacheRounds > 0 ? ` · 缓存 ${row.cacheDone}/${rounds}` : ""}
                      </span>
                    )}
                    {s && (
                      <span className="text-xs text-muted-foreground">
                        速度 {s.tps_rounds_ok}/{s.tps_rounds} 轮
                        {s.cache_rounds > 0 ? ` · 缓存 ${s.cache_rounds} 轮` : ""}
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-4 text-xs tabular-nums">
                    {(s?.ok || r?.ok) && (
                      <>
                        {cacheHit !== null && cacheRounds > 0 && (
                          <span
                            className={
                              cacheGood
                                ? "text-emerald-600 dark:text-emerald-400"
                                : "text-amber-600 dark:text-amber-400"
                            }
                            title={
                              s?.cache_rounds
                                ? `${s.cache_rounds} 轮缓存组平均值（速度组已预热缓存）`
                                : "缓存组进行中的累计命中率"
                            }
                          >
                            缓存{" "}
                            <span className="font-semibold">
                              {cacheHit.toFixed(1)}%
                            </span>
                            {!s && row.cacheDone < rounds && (
                              <span className="text-muted-foreground">（{row.cacheDone}/{rounds}）</span>
                            )}
                          </span>
                        )}
                        <span className="text-muted-foreground">
                          TTFT <span className="font-medium text-foreground">{shownTTFT}ms</span>
                        </span>
                        {r && (
                          <span className="text-muted-foreground">
                            {r.tokens} tok / {(r.elapsed_ms / 1000).toFixed(1)}s
                            {r.round ? ` · ${r.phase === "tps" ? "速度" : "缓存"}第 ${r.round} 轮` : ""}
                          </span>
                        )}
                        {row.lastPhase === "cache" && !r?.tokens && (
                          <span className="text-muted-foreground">缓存组进行中…</span>
                        )}
                        <span className="text-base font-semibold">{shownTPS.toFixed(1)}</span>
                        <span className="text-muted-foreground">tok/s</span>
                      </>
                    )}
                    {row.state === "failed" && (
                      <span className="max-w-[22rem] truncate text-destructive" title={s?.error ?? r?.error}>
                        {s?.error || r?.error || "失败"}
                      </span>
                    )}
                  </div>
                </div>
                <Progress value={pct} className="h-1.5" />
              </div>
            )
          })}
          {!rows.length && (
            <p className="text-sm text-muted-foreground">该模型只有一个上游供应商，无需测速。</p>
          )}
        </div>

        <div className="flex items-center justify-between gap-4">
          <div className="flex items-center gap-2">
            <Switch checked={autoApply} onCheckedChange={setAutoApply} />
            <span className="text-sm">测试完成后自动固定最优供应商（缓存优先）</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">缓存轮数</span>
            <Select value={String(rounds)} onValueChange={(v) => setRounds(Number(v))}>
              <SelectTrigger size="sm" className="w-16" disabled={phase === "running"}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="2">2</SelectItem>
                <SelectItem value="3">3</SelectItem>
                <SelectItem value="4">4</SelectItem>
                <SelectItem value="5">5</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <DialogFooter className="sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {best?.provider
              ? `最佳：${best.provider}（缓存 ${best.cacheHit.toFixed(1)}% · ${best.tps.toFixed(1)} tok/s）`
              : phase === "running"
                ? "测试进行中…"
                : "点击开始测试"}
          </div>
          <div className="flex gap-2">
            {phase === "running" ? (
              <Button variant="outline" size="sm" onClick={() => abortRef.current?.abort()}>
                <Square className="mr-1 h-4 w-4" />
                停止
              </Button>
            ) : (
              <Button size="sm" onClick={() => run(rounds)} disabled={!rows.length}>
                <Play className="mr-1 h-4 w-4" />
                {finished ? "重新测试" : "开始测试"}
              </Button>
            )}
            <Button
              size="sm"
              variant="secondary"
              disabled={!best?.provider}
              onClick={() => best?.provider && apply(best.provider)}
            >
              应用最佳
            </Button>
          </div>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
