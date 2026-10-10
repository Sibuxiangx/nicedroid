import { useCallback, useEffect, useMemo, useState } from "react"
import { apiClient, type ModelView } from "@/lib/api"
import { ProviderBenchmarkDialog } from "@/components/ProviderBenchmarkDialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
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
import { ChevronDown, ChevronRight, Gauge, Search } from "lucide-react"
import { toast } from "sonner"

const DEFAULT_VALUE = "__default__"

// Providers in the order they are shown: the open-weight pool this project
// exists for comes first, then the native APIs.
const PROVIDER_ORDER = ["factory", "anthropic", "openai", "google", "xai"]

const PROVIDER_LABEL: Record<string, { title: string; hint: string }> = {
  factory: { title: "Factory · Droid Core", hint: "开源模型，core 配额池，同一模型可由多家供应商承载" },
  anthropic: { title: "Anthropic", hint: "Claude 系列，走 Messages 协议" },
  openai: { title: "OpenAI", hint: "GPT 系列，走 Responses 协议" },
  google: { title: "Google", hint: "Gemini 系列，仅透传 /v1/generate，不参与协议转换" },
  xai: { title: "xAI", hint: "Grok 系列" },
}

function providerTitle(p: string) {
  return PROVIDER_LABEL[p]?.title ?? p
}

// legacyModel reports whether the CLI no longer offers a model. The registry
// states it two ways: entries hidden from the CLI's own picker, and entries
// carrying a retirement date.
function legacyModel(m: ModelView): boolean {
  return m.cli_visible === false || !!m.deprecated_on
}

function familyLabel(family: string): string {
  switch (family) {
    case "messages":
      return "Messages"
    case "responses":
      return "Responses"
    case "google":
      return "Google"
    default:
      return "Chat"
  }
}

interface RowProps {
  m: ModelView
  legacy?: boolean
  onPickProvider: (m: ModelView, provider: string) => void
  onBenchmark: (m: ModelView) => void
}

function ModelRow({ m, legacy, onPickProvider, onBenchmark }: RowProps) {
  const providers = m.providers ?? []
  const single = providers.length <= 1
  return (
    <TableRow className={legacy ? "text-muted-foreground" : undefined}>
      <TableCell>
        <code className="text-xs">{m.id}</code>
      </TableCell>
      <TableCell className={legacy ? undefined : "font-medium"}>{m.display_name}</TableCell>
      <TableCell>
        <div className="flex flex-wrap gap-1">
          <Badge variant={m.tier === "core" ? "default" : "secondary"} className="text-[10px]">
            {m.tier}
          </Badge>
          {m.passthrough && (
            <Badge variant="outline" className="text-[10px]">
              透传
            </Badge>
          )}
          {m.deprecated_on && (
            <Badge variant="outline" className="text-[10px]" title={`弃用于 ${m.deprecated_on}`}>
              已弃用 {m.deprecated_on}
            </Badge>
          )}
          {legacy && !m.deprecated_on && (
            <Badge variant="outline" className="text-[10px]">
              CLI 已隐藏
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
            <Select value={m.pinned_provider || DEFAULT_VALUE} onValueChange={(v) => onPickProvider(m, v)}>
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
          <Button size="sm" variant="outline" onClick={() => onBenchmark(m)}>
            <Gauge className="mr-1 h-4 w-4" />
            测速
          </Button>
        )}
      </TableCell>
    </TableRow>
  )
}

const HEAD = (
  <TableHeader>
    <TableRow>
      <TableHead>模型 ID</TableHead>
      <TableHead>名称</TableHead>
      <TableHead>配额池</TableHead>
      <TableHead>协议</TableHead>
      <TableHead>供应商</TableHead>
      <TableHead>视觉</TableHead>
      <TableHead>倍率</TableHead>
      <TableHead>推理档位</TableHead>
      <TableHead className="text-right">操作</TableHead>
    </TableRow>
  </TableHeader>
)

interface Section {
  provider: string
  current: ModelView[]
  legacy: ModelView[]
}

export function Models() {
  const [models, setModels] = useState<ModelView[]>([])
  const [benchModel, setBenchModel] = useState<ModelView | null>(null)
  const [query, setQuery] = useState("")
  const [openLegacy, setOpenLegacy] = useState<Record<string, boolean>>({})

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

  const sections = useMemo<Section[]>(() => {
    const q = query.trim().toLowerCase()
    const byProvider = new Map<string, Section>()
    for (const m of models) {
      if (q && !m.id.toLowerCase().includes(q) && !m.display_name.toLowerCase().includes(q)) continue
      let s = byProvider.get(m.provider)
      if (!s) {
        s = { provider: m.provider, current: [], legacy: [] }
        byProvider.set(m.provider, s)
      }
      ;(legacyModel(m) ? s.legacy : s.current).push(m)
    }
    for (const s of byProvider.values()) {
      const byName = (a: ModelView, b: ModelView) => a.display_name.localeCompare(b.display_name)
      s.current.sort(byName)
      s.legacy.sort(byName)
    }
    // Known providers in their fixed order, then anything unexpected.
    return [...byProvider.values()].sort((a, b) => {
      const ai = PROVIDER_ORDER.indexOf(a.provider)
      const bi = PROVIDER_ORDER.indexOf(b.provider)
      return (ai < 0 ? 99 : ai) - (bi < 0 ? 99 : bi)
    })
  }, [models, query])

  const totals = useMemo(() => {
    const current = models.filter((m) => !legacyModel(m)).length
    return {
      all: models.length,
      current,
      legacy: models.length - current,
      multiProvider: models.filter((m) => m.provider_count > 1).length,
      core: models.filter((m) => m.tier === "core").length,
    }
  }, [models])

  const shown = sections.reduce((n, s) => n + s.current.length + s.legacy.length, 0)

  return (
    <>
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div>
          <h1 className="text-xl font-semibold">模型目录</h1>
          <p className="text-sm text-muted-foreground">
            目录由 Droid CLI 的注册表生成，按供应商分区。同一开源模型可由多家供应商承载，
            可手动固定或点「测速」自动选择最快的供应商。CLI 已下线的模型折叠在「历史模型」里。
          </p>
        </div>
        <div className="relative">
          <Search className="absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            className="h-9 w-64 pl-8"
            placeholder="搜索模型 ID 或名称"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
          />
        </div>
      </div>

      <div className="flex flex-wrap gap-2 text-xs text-muted-foreground">
        <Badge variant="secondary" className="text-[10px]">
          共 {totals.all} 个
        </Badge>
        <Badge variant="secondary" className="text-[10px]">
          在用 {totals.current}
        </Badge>
        <Badge variant="outline" className="text-[10px]">
          历史 {totals.legacy}
        </Badge>
        <Badge variant="secondary" className="text-[10px]">
          core 池 {totals.core}
        </Badge>
        <Badge variant="secondary" className="text-[10px]">
          多供应商 {totals.multiProvider}
        </Badge>
        {query && (
          <Badge variant="outline" className="text-[10px]">
            匹配 {shown}
          </Badge>
        )}
      </div>

      {sections.map((s) => {
        const meta = PROVIDER_LABEL[s.provider]
        const expanded = !!openLegacy[s.provider]
        const multi = s.current.filter((m) => m.provider_count > 1).length
        return (
          <Card key={s.provider}>
            <CardHeader>
              <div className="flex flex-wrap items-center justify-between gap-2">
                <div>
                  <CardTitle className="text-base">{providerTitle(s.provider)}</CardTitle>
                  <CardDescription>
                    {s.current.length} 个在用
                    {s.legacy.length > 0 && ` · ${s.legacy.length} 个历史`}
                    {multi > 0 && ` · ${multi} 个多供应商`}
                    {meta?.hint && ` · ${meta.hint}`}
                  </CardDescription>
                </div>
                {s.legacy.length > 0 && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setOpenLegacy((prev) => ({ ...prev, [s.provider]: !expanded }))}
                  >
                    {expanded ? (
                      <ChevronDown className="mr-1 h-4 w-4" />
                    ) : (
                      <ChevronRight className="mr-1 h-4 w-4" />
                    )}
                    历史模型 {s.legacy.length}
                  </Button>
                )}
              </div>
            </CardHeader>
            <CardContent className="space-y-4">
              {s.current.length > 0 ? (
                <Table>
                  {HEAD}
                  <TableBody>
                    {s.current.map((m) => (
                      <ModelRow key={m.id} m={m} onPickProvider={pickProvider} onBenchmark={setBenchModel} />
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {query ? "没有匹配的在用模型" : "没有在用的模型"}
                </p>
              )}

              {expanded && s.legacy.length > 0 && (
                <div className="space-y-2 rounded-lg border border-dashed p-3">
                  <p className="text-xs text-muted-foreground">
                    以下模型已从 CLI 的模型列表中下线（标注了弃用日期，或已被 CLI 隐藏）。
                    它们仍然可以调用，但 Factory 可能随时停用，建议迁移到替代模型。
                  </p>
                  <Table>
                    {HEAD}
                    <TableBody>
                      {s.legacy.map((m) => (
                        <ModelRow
                          key={m.id}
                          m={m}
                          legacy
                          onPickProvider={pickProvider}
                          onBenchmark={setBenchModel}
                        />
                      ))}
                    </TableBody>
                  </Table>
                </div>
              )}
            </CardContent>
          </Card>
        )
      })}

      {!sections.length && (
        <Card>
          <CardContent className="pt-6">
            <p className="text-sm text-muted-foreground">没有匹配「{query}」的模型。</p>
          </CardContent>
        </Card>
      )}

      <ProviderBenchmarkDialog model={benchModel} onClose={() => setBenchModel(null)} onApplied={load} />
    </>
  )
}
