import { useEffect, useState } from "react"
import { apiClient, fmtTime, type RequestLog } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ChevronLeft, ChevronRight, RefreshCw } from "lucide-react"
import { toast } from "sonner"

const PAGE_SIZE = 50

export function Logs() {
  const [logs, setLogs] = useState<RequestLog[]>([])
  const [total, setTotal] = useState(0)
  const [page, setPage] = useState(0)
  const [hours, setHours] = useState(24)
  const [model, setModel] = useState("")
  const [errorsOnly, setErrorsOnly] = useState(false)
  const [busy, setBusy] = useState(false)

  async function load() {
    setBusy(true)
    try {
      const params = new URLSearchParams({
        limit: String(PAGE_SIZE),
        offset: String(page * PAGE_SIZE),
        hours: String(hours),
      })
      if (model.trim()) params.set("model", model.trim())
      if (errorsOnly) params.set("errors_only", "true")
      const r = await apiClient.logs(params)
      setLogs(r.logs ?? [])
      setTotal(r.total ?? 0)
    } catch {
      toast.error("加载失败")
    } finally {
      setBusy(false)
    }
  }

  useEffect(() => {
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [page, hours, errorsOnly])

  const maxPage = Math.max(0, Math.ceil(total / PAGE_SIZE) - 1)

  return (
    <>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">请求日志</h1>
          <p className="text-sm text-muted-foreground">
            共 {total} 条 · 含延迟 / TTFT / 缓存命中 / TPS
          </p>
        </div>
        <Button variant="outline" size="sm" onClick={load} disabled={busy}>
          <RefreshCw className={`mr-1 h-4 w-4 ${busy ? "animate-spin" : ""}`} />
          刷新
        </Button>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">筛选</CardTitle>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap items-end gap-4">
            <div className="space-y-2">
              <Label className="text-xs">时间范围</Label>
              <select
                className="h-9 rounded-md border bg-transparent px-2 text-sm"
                value={hours}
                onChange={(e) => {
                  setPage(0)
                  setHours(Number(e.target.value))
                }}
              >
                <option value={1}>最近 1 小时</option>
                <option value={6}>最近 6 小时</option>
                <option value={24}>最近 24 小时</option>
                <option value={168}>最近 7 天</option>
                <option value={720}>最近 30 天</option>
              </select>
            </div>
            <div className="space-y-2">
              <Label className="text-xs">模型过滤</Label>
              <Input
                className="h-9 w-56"
                placeholder="模型 ID（模糊=精确匹配）"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === "Enter") {
                    setPage(0)
                    void load()
                  }
                }}
              />
            </div>
            <div className="flex items-center gap-2 pb-1.5">
              <Switch checked={errorsOnly} onCheckedChange={(v) => { setErrorsOnly(v); setPage(0) }} />
              <span className="text-sm">仅看错误</span>
            </div>
            <Button
              size="sm"
              onClick={() => {
                setPage(0)
                void load()
              }}
            >
              应用
            </Button>
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-6">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>时间</TableHead>
                <TableHead>模型</TableHead>
                <TableHead>供应商</TableHead>
                <TableHead>池</TableHead>
                <TableHead>模式</TableHead>
                <TableHead className="text-right">状态</TableHead>
                <TableHead className="text-right">延迟</TableHead>
                <TableHead className="text-right">TTFT</TableHead>
                <TableHead className="text-right">输入</TableHead>
                <TableHead className="text-right">输出</TableHead>
                <TableHead className="text-right">缓存读</TableHead>
                <TableHead className="text-right">TPS</TableHead>
                <TableHead className="text-right">积分</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {logs.map((l) => (
                <TableRow key={l.id}>
                  <TableCell className="whitespace-nowrap text-xs text-muted-foreground">
                    {fmtTime(l.ts)}
                  </TableCell>
                  <TableCell>
                    <div className="text-xs">
                      <div className="font-medium">{l.used_model || "-"}</div>
                      {l.requested_model !== l.used_model && (
                        <div className="text-muted-foreground">请求: {l.requested_model}</div>
                      )}
                    </div>
                  </TableCell>
                  <TableCell>
                    <span className="text-xs text-muted-foreground">{l.provider || "-"}</span>
                  </TableCell>
                  <TableCell>
                    <Badge variant={l.pool === "core" ? "default" : "secondary"} className="text-[10px]">
                      {l.pool}
                    </Badge>
                  </TableCell>
                  <TableCell>
                    <Badge variant="outline" className="text-[10px]">
                      {l.stream ? "流式" : "非流式"}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <Badge variant={l.status >= 400 ? "destructive" : "outline"} className="text-[10px]">
                      {l.status}
                    </Badge>
                  </TableCell>
                  <TableCell className="text-right tabular-nums text-xs">{l.latency_ms}ms</TableCell>
                  <TableCell className="text-right tabular-nums text-xs">{l.ttft_ms || "-"}</TableCell>
                  <TableCell className="text-right tabular-nums text-xs">{l.prompt_tokens}</TableCell>
                  <TableCell className="text-right tabular-nums text-xs">{l.completion_tokens}</TableCell>
                  <TableCell className="text-right tabular-nums text-xs">
                    {l.cache_read_tokens > 0 ? (
                      <span className="text-emerald-600">{l.cache_read_tokens}</span>
                    ) : (
                      "0"
                    )}
                  </TableCell>
                  <TableCell className="text-right tabular-nums text-xs">{l.tps ? l.tps.toFixed(1) : "-"}</TableCell>
                  <TableCell className="text-right tabular-nums text-xs">
                    {l.credits > 0 ? <span title="Factory Standard Credits（按 CLI 公式估算）">{l.credits}</span> : "-"}
                  </TableCell>
                </TableRow>
              ))}
              {!logs.length && (
                <TableRow>
                  <TableCell colSpan={13} className="text-center text-sm text-muted-foreground">
                    暂无记录
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
          <div className="mt-4 flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              第 {page + 1} / {maxPage + 1} 页
            </span>
            <div className="flex gap-2">
              <Button size="sm" variant="outline" disabled={page === 0} onClick={() => setPage(page - 1)}>
                <ChevronLeft className="h-4 w-4" />
              </Button>
              <Button size="sm" variant="outline" disabled={page >= maxPage} onClick={() => setPage(page + 1)}>
                <ChevronRight className="h-4 w-4" />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </>
  )
}
