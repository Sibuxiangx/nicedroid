import { useCallback, useEffect, useState } from "react"
import { apiClient, type ModelView } from "@/lib/api"
import { ProviderBenchmarkDialog } from "@/components/ProviderBenchmarkDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { Gauge } from "lucide-react"
import { toast } from "sonner"

const DEFAULT_VALUE = "__default__"

function tierBadge(tier: string) {
  return tier === "core" ? (
    <Badge className="text-[10px]">core</Badge>
  ) : (
    <Badge variant="secondary" className="text-[10px]">
      standard
    </Badge>
  )
}

function familyLabel(family: string): string {
  switch (family) {
    case "messages":
      return "Anthropic Messages"
    case "responses":
      return "OpenAI Responses"
    case "google":
      return "Google (透传)"
    default:
      return "Chat Completions"
  }
}

export function Models() {
  const [models, setModels] = useState<ModelView[]>([])
  const [benchModel, setBenchModel] = useState<ModelView | null>(null)

  const load = useCallback(() => {
    apiClient
      .models()
      .then((r) => setModels(r.models ?? []))
      .catch(() => toast.error("加载失败"))
  }, [])

  useEffect(() => {
    load()
  }, [load])

  async function pickProvider(m: ModelView, provider: string) {
    try {
      await apiClient.setModelProvider(m.id, provider === DEFAULT_VALUE ? "" : provider)
      toast.success(
        provider === DEFAULT_VALUE ? `${m.id} 已恢复默认供应商` : `${m.id} 已固定到 ${provider}`,
      )
      load()
    } catch (err) {
      toast.error((err as Error)?.message ?? "设置失败")
    }
  }

  const coreModels = models.filter((m) => m.tier === "core")
  const standardModels = models.filter((m) => m.tier === "standard")
  const multiProvider = models.filter((m) => m.provider_count > 1).length

  return (
    <>
      <div>
        <h1 className="text-xl font-semibold">模型目录</h1>
        <p className="text-sm text-muted-foreground">
          配额由 Factory 侧管理：standard 配额用尽后请求会直接报错，Droid Core 由用户显式指定模型使用。
          同一开源模型可由多家供应商承载，可手动固定或点「测速」自动选择最快的供应商。
        </p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">全部模型</CardTitle>
          <CardDescription>
            共 {models.length} 个 · standard {standardModels.length} · core {coreModels.length} · 多供应商{" "}
            {multiProvider} 个 · Gemini 为纯透传（/v1/generate）
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>模型 ID</TableHead>
                <TableHead>名称</TableHead>
                <TableHead>配额池</TableHead>
                <TableHead>上游协议</TableHead>
                <TableHead>供应商</TableHead>
                <TableHead>视觉</TableHead>
                <TableHead>倍率</TableHead>
                <TableHead>推理档位</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {models.map((m) => {
                const providers = m.providers ?? []
                const single = providers.length <= 1
                return (
                  <TableRow key={m.id}>
                    <TableCell>
                      <code className="text-xs">{m.id}</code>
                    </TableCell>
                    <TableCell className="font-medium">{m.display_name}</TableCell>
                    <TableCell>
                      <div className="flex gap-1">
                        {tierBadge(m.tier)}
                        {m.passthrough && (
                          <Badge variant="outline" className="text-[10px]">
                            透传
                          </Badge>
                        )}
                      </div>
                    </TableCell>
                    <TableCell className="text-xs text-muted-foreground">{familyLabel(m.family)}</TableCell>
                    <TableCell>
                      {single ? (
                        <div className="flex items-center gap-1.5">
                          <span className="text-xs text-muted-foreground">
                            {providers[0] || m.current_provider || "-"}
                          </span>
                          {m.pinned_provider && (
                            <Badge variant="secondary" className="text-[10px]">
                              已固定
                            </Badge>
                          )}
                        </div>
                      ) : (
                        <div className="flex items-center gap-1.5">
                          <Select
                            value={m.pinned_provider || DEFAULT_VALUE}
                            onValueChange={(v) => pickProvider(m, v)}
                          >
                            <SelectTrigger size="sm" className="w-[10.5rem]">
                              <SelectValue />
                            </SelectTrigger>
                            <SelectContent>
                              <SelectItem value={DEFAULT_VALUE}>默认（{m.provider}）</SelectItem>
                              {providers.map((p) => (
                                <SelectItem key={p} value={p}>
                                  {p}
                                </SelectItem>
                              ))}
                            </SelectContent>
                          </Select>
                          {m.pinned_provider && (
                            <Badge variant="secondary" className="text-[10px]">
                              已固定
                            </Badge>
                          )}
                        </div>
                      )}
                    </TableCell>
                    <TableCell className="text-xs">{m.vision ? "支持" : "-"}</TableCell>
                    <TableCell className="tabular-nums">{m.multiplier}×</TableCell>
                    <TableCell className="text-xs text-muted-foreground">
                      {(m.reasoning_efforts ?? []).join(" / ") || "-"}
                    </TableCell>
                    <TableCell className="text-right">
                      {!single && !m.passthrough && (
                        <Button size="sm" variant="outline" onClick={() => setBenchModel(m)}>
                          <Gauge className="mr-1 h-4 w-4" />
                          测速
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <ProviderBenchmarkDialog
        model={benchModel}
        onClose={() => setBenchModel(null)}
        onApplied={load}
      />
    </>
  )
}
