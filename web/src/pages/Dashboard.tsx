import { useEffect, useState } from "react"
import {
  apiClient,
  fmtDuration,
  fmtNum,
  fmtPct,
  type QuotaSnapshot,
  type Stats,
} from "@/lib/api"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { Separator } from "@/components/ui/separator"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import {
  Bar,
  BarChart,
  CartesianGrid,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts"
import { RefreshCw } from "lucide-react"
import { toast } from "sonner"

function pctColor(v: number): string {
  if (v >= 90) return "text-destructive"
  if (v >= 70) return "text-amber-500"
  return "text-emerald-500"
}

// tooltipStyle keeps recharts tooltips readable in both light and dark themes.
const tooltipStyle: React.CSSProperties = {
  backgroundColor: "var(--popover)",
  color: "var(--popover-foreground)",
  border: "1px solid var(--border)",
  borderRadius: 8,
  fontSize: 12,
}

function QuotaPool({
  title,
  pool,
  tone,
}: {
  title: string
  pool: { fiveHour: { usedPercent: number; windowEnd: string | null; secondsRemaining: number | null }; weekly: { usedPercent: number; windowEnd: string | null; secondsRemaining: number | null }; monthly: { usedPercent: number; windowEnd: string | null; secondsRemaining: number | null } }
  tone: "standard" | "core"
}) {
  const rows = [
    { label: "5 小时", w: pool.fiveHour },
    { label: "每周", w: pool.weekly },
    { label: "每月", w: pool.monthly },
  ]
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-2">
        <span className="text-sm font-medium">{title}</span>
        <Badge variant={tone === "core" ? "default" : "secondary"} className="text-[10px]">
          {tone === "core" ? "Droid Core" : "Standard"}
        </Badge>
      </div>
      {rows.map((r) => (
        <div key={r.label} className="grid grid-cols-[56px_1fr_120px] items-center gap-2">
          <span className="text-xs text-muted-foreground">{r.label}</span>
          <div className="flex items-center gap-2">
            <Progress value={r.w.usedPercent} className="h-2" />
            <span className={`w-10 text-right text-xs tabular-nums ${pctColor(r.w.usedPercent)}`}>
              {r.w.usedPercent}%
            </span>
          </div>
          <span className="text-xs text-muted-foreground">
            {r.w.secondsRemaining ? `重置 ${fmtDuration(r.w.secondsRemaining)}后` : "未激活"}
          </span>
        </div>
      ))}
    </div>
  )
}

export function Dashboard() {
  const [stats, setStats] = useState<Stats | null>(null)
  const [quotas, setQuotas] = useState<QuotaSnapshot | null>(null)
  const [hours, setHours] = useState(24)
  const [busy, setBusy] = useState(false)

  async function load() {
    try {
      const [s, q] = await Promise.all([apiClient.stats(hours), apiClient.quotas()])
      setStats(s)
      setQuotas(q)
    } catch {
      toast.error("加载失败")
    }
  }

  useEffect(() => {
    void load()
    const t = setInterval(load, 30_000)
    return () => clearInterval(t)
  }, [hours])

  async function refresh() {
    setBusy(true)
    try {
      const q = await apiClient.refreshQuotas()
      setQuotas(q)
      toast.success("配额已刷新")
    } catch {
      toast.error("刷新失败")
    } finally {
      setBusy(false)
    }
  }

  const hourly = (stats?.hourly ?? []).map((h) => ({
    hour: h.hour.slice(11, 16),
    requests: h.requests,
    errors: h.errors,
  }))
  const tpsSeries = (stats?.tps_series ?? []).map((p) => ({
    hour: p.hour.slice(11, 16),
    tps: Number(p.avg_tps.toFixed(1)),
    cache: Number((p.cache_hit_rate * 100).toFixed(1)),
  }))

  return (
    <>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Dashboard</h1>
          <p className="text-sm text-muted-foreground">配额水位 · 请求量 · 缓存率 · TPS</p>
        </div>
        <div className="flex items-center gap-2">
          <select
            className="h-9 rounded-md border bg-transparent px-2 text-sm"
            value={hours}
            onChange={(e) => setHours(Number(e.target.value))}
          >
            <option value={6}>最近 6 小时</option>
            <option value={24}>最近 24 小时</option>
            <option value={168}>最近 7 天</option>
          </select>
          <Button variant="outline" size="sm" onClick={refresh} disabled={busy}>
            <RefreshCw className={`mr-1 h-4 w-4 ${busy ? "animate-spin" : ""}`} />
            刷新配额
          </Button>
        </div>
      </div>

      <div className="grid gap-4 md:grid-cols-3 lg:grid-cols-5">
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>请求总数</CardDescription>
            <CardTitle className="text-2xl tabular-nums">{fmtNum(stats?.total_requests ?? 0)}</CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            错误 {stats?.error_requests ?? 0} · 平均延迟 {Math.round(stats?.avg_latency_ms ?? 0)}ms
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>预估积分 (FSC)</CardDescription>
            <CardTitle className="text-2xl tabular-nums">{fmtNum(stats?.credits ?? 0)}</CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            Factory Standard Credits · 按 CLI 公式估算
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>缓存命中率</CardDescription>
            <CardTitle className="text-2xl tabular-nums">{fmtPct(stats?.cache_hit_rate ?? 0)}</CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            读 {fmtNum(stats?.cache_read_tokens ?? 0)} / 写 {fmtNum(stats?.cache_write_tokens ?? 0)} tokens
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>平均 TPS</CardDescription>
            <CardTitle className="text-2xl tabular-nums">{(stats?.avg_tps ?? 0).toFixed(1)}</CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            生成段速度 · TTFT {Math.round(stats?.avg_ttft_ms ?? 0)}ms
          </CardContent>
        </Card>
        <Card>
          <CardHeader className="pb-2">
            <CardDescription>Token 用量</CardDescription>
            <CardTitle className="text-2xl tabular-nums">
              {fmtNum((stats?.prompt_tokens ?? 0) + (stats?.completion_tokens ?? 0))}
            </CardTitle>
          </CardHeader>
          <CardContent className="text-xs text-muted-foreground">
            输入 {fmtNum(stats?.prompt_tokens ?? 0)} · 输出 {fmtNum(stats?.completion_tokens ?? 0)}
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">配额水位</CardTitle>
          <CardDescription>Standard 与 Droid Core 双池实时用量；配额用尽后请求会直接报错</CardDescription>
        </CardHeader>
        <CardContent className="space-y-6">
          {quotas?.keys.length ? (
            quotas.keys.map((k) => (
              <div key={k.key_id} className="space-y-4">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{k.key_name || `key #${k.key_id}`}</span>
                  <span className="text-xs text-muted-foreground">{k.key_masked}</span>
                  {k.overage_preference && (
                    <Badge variant="outline" className="text-[10px]">
                      overage: {k.overage_preference}
                    </Badge>
                  )}
                  {k.last_error && (
                    <Badge variant="destructive" className="text-[10px]">
                      {k.last_error.slice(0, 40)}
                    </Badge>
                  )}
                </div>
                <div className="grid gap-6 md:grid-cols-2">
                  <QuotaPool title="Standard 配额" pool={k.standard} tone="standard" />
                  <QuotaPool title="Core 配额" pool={k.core} tone="core" />
                </div>
                <Separator />
              </div>
            ))
          ) : (
            <p className="text-sm text-muted-foreground">
              还没有配置上游 API Key，去「Key 管理」添加。
            </p>
          )}
        </CardContent>
      </Card>

      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">每小时请求量</CardTitle>
          </CardHeader>
          <CardContent className="h-56">
            <ResponsiveContainer width="100%" height="100%">
              <BarChart data={hourly}>
                <CartesianGrid strokeDasharray="3 3" className="stroke-border" />
                <XAxis dataKey="hour" fontSize={11} />
                <YAxis fontSize={11} allowDecimals={false} />
                <Tooltip contentStyle={tooltipStyle} />
                <Bar dataKey="requests" name="请求" fill="var(--color-chart-1, #6366f1)" radius={[3, 3, 0, 0]} />
                <Bar dataKey="errors" name="错误" fill="var(--color-chart-4, #ef4444)" radius={[3, 3, 0, 0]} />
              </BarChart>
            </ResponsiveContainer>
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">TPS / 缓存命中率</CardTitle>
          </CardHeader>
          <CardContent className="h-56">
            <ResponsiveContainer width="100%" height="100%">
              <LineChart data={tpsSeries}>
                <CartesianGrid strokeDasharray="3 3" className="stroke-border" />
                <XAxis dataKey="hour" fontSize={11} />
                <YAxis yAxisId="tps" fontSize={11} />
                <YAxis yAxisId="cache" orientation="right" fontSize={11} unit="%" domain={[0, 100]} />
                <Tooltip contentStyle={tooltipStyle} />
                <Line yAxisId="tps" type="monotone" dataKey="tps" name="TPS" stroke="#6366f1" dot={false} strokeWidth={2} />
                <Line yAxisId="cache" type="monotone" dataKey="cache" name="缓存命中率%" stroke="#10b981" dot={false} strokeWidth={2} />
              </LineChart>
            </ResponsiveContainer>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">模型明细</CardTitle>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>模型</TableHead>
                <TableHead className="text-right">请求</TableHead>
                <TableHead className="text-right">输入 tokens</TableHead>
                <TableHead className="text-right">输出 tokens</TableHead>
                <TableHead className="text-right">缓存命中</TableHead>
                <TableHead className="text-right">平均 TPS</TableHead>
                <TableHead className="text-right">预估积分</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {(stats?.by_model ?? []).map((m) => (
                <TableRow key={m.model}>
                  <TableCell className="font-medium">{m.model || "-"}</TableCell>
                  <TableCell className="text-right tabular-nums">{m.requests}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtNum(m.prompt_tokens)}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtNum(m.completion_tokens)}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtPct(m.cache_hit_rate)}</TableCell>
                  <TableCell className="text-right tabular-nums">{m.avg_tps.toFixed(1)}</TableCell>
                  <TableCell className="text-right tabular-nums">{fmtNum(m.credits)}</TableCell>
                </TableRow>
              ))}
              {!(stats?.by_model ?? []).length && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-sm text-muted-foreground">
                    暂无数据
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
    </>
  )
}
