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
  result?: BenchmarkResult
  summary?: ProviderSummary
  roundsDone: number
}

interface Props {
  model: ModelView | null
  onClose: () => void
  onApplied: () => void
}

// ProviderBenchmarkDialog streams the same generation through every backend of
// a model, shows time-to-first-token and tokens per second as each one lands,
// and can pin the fastest provider for that model.
export function ProviderBenchmarkDialog({ model, onClose, onApplied }: Props) {
  const [rows, setRows] = useState<Row[]>([])
  const [phase, setPhase] = useState<Phase>("idle")
  const [autoApply, setAutoApply] = useState(true)
  const [rounds, setRounds] = useState(2)
  const [best, setBest] = useState<{ provider: string; tps: number } | null>(null)
  const abortRef = useRef<AbortController | null>(null)

  const providers = useMemo(() => model?.providers ?? [], [model])

  function reset() {
    setRows(providers.map((p) => ({ provider: p, state: "pending", roundsDone: 0 })))
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
      providers.map((p) => ({ provider: p, state: "pending" as const, roundsDone: 0 })),
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
                const roundsDone = row.roundsDone + 1
                // A failed round must not erase a good one from an earlier
                // round, so only successful rounds replace the shown result.
                const keep = row.result?.ok && !r.ok
                return {
                  ...row,
                  roundsDone,
                  state: (keep ? row.state : r.ok ? "done" : "failed") as Row["state"],
                  result: keep ? row.result : r,
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
            setBest({ provider: ev.best, tps: ev.best_tps })
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

  const maxTPS = Math.max(0, ...rows.map((r) => r.summary?.tps ?? r.result?.tps ?? 0))
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
            对同一提示词做流式生成，各供应商轮流各测 {rounds} 轮，取各轮中位数。TPS 为端到端吞吐
            （输出 token ÷ 总耗时），不受上游缓冲影响。
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-3">
          {rows.map((row) => {
            const r = row.result
            const s = row.summary
            // Before the run finishes, show the latest round; afterwards show
            // the median across rounds, which is the number used to rank.
            const shownTPS = s?.tps ?? r?.tps ?? 0
            const shownTTFT = s?.ttft_ms || r?.ttft_ms || 0
            const pct = maxTPS > 0 && shownTPS ? Math.max(4, Math.round((shownTPS / maxTPS) * 100)) : 0
            const isBest = best?.provider === row.provider
            return (
              <div key={row.provider} className="space-y-1.5 rounded-lg border p-3">
                <div className="flex items-center justify-between gap-3">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{row.provider}</span>
                    {isBest && (
                      <Badge className="text-[10px]">
                        <Zap className="mr-0.5 h-3 w-3" />
                        最快
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
                    {s && s.rounds > 1 && (
                      <span className="text-xs text-muted-foreground">
                        {s.rounds_ok}/{s.rounds} 轮成功
                      </span>
                    )}
                  </div>
                  <div className="flex items-center gap-4 text-xs tabular-nums">
                    {(s?.ok || r?.ok) && (
                      <>
                        <span className="text-muted-foreground">
                          TTFT <span className="font-medium text-foreground">{shownTTFT}ms</span>
                        </span>
                        {r && (
                          <span className="text-muted-foreground">
                            {r.tokens} tok / {(r.elapsed_ms / 1000).toFixed(1)}s
                            {r.round ? ` · 第 ${r.round} 轮` : ""}
                          </span>
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
            <span className="text-sm">测试完成后自动固定最快供应商</span>
          </div>
          <div className="flex items-center gap-2">
            <span className="text-xs text-muted-foreground">轮数</span>
            <Select value={String(rounds)} onValueChange={(v) => setRounds(Number(v))}>
              <SelectTrigger size="sm" className="w-16" disabled={phase === "running"}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="1">1</SelectItem>
                <SelectItem value="2">2</SelectItem>
                <SelectItem value="3">3</SelectItem>
              </SelectContent>
            </Select>
          </div>
        </div>

        <DialogFooter className="sm:justify-between">
          <div className="text-xs text-muted-foreground">
            {best?.provider
              ? `最佳：${best.provider}（${best.tps.toFixed(1)} tok/s）`
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
