import { useEffect, useState } from "react"
import { apiClient, type ModelView, type Stats } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { Textarea } from "@/components/ui/textarea"
import { Play, Save } from "lucide-react"
import { toast } from "sonner"

export function SettingsPage() {
  const [systemPrompt, setSystemPrompt] = useState("")
  const [upstream, setUpstream] = useState("")
  const [models, setModels] = useState<ModelView[]>([])
  const [savingPrompt, setSavingPrompt] = useState(false)

  // playground state
  const [pgModel, setPgModel] = useState("")
  const [pgMessage, setPgMessage] = useState("用一句话介绍你自己，并说明你是什么模型。")
  const [pgBusy, setPgBusy] = useState(false)
  const [pgResult, setPgResult] = useState<{ model: string; text: string } | null>(null)

  // stats snapshot for the reference line
  const [stats, setStats] = useState<Stats | null>(null)

  useEffect(() => {
    apiClient
      .settings()
      .then((s) => {
        setSystemPrompt(s.system_prompt)
        setUpstream(s.upstream)
      })
      .catch(() => toast.error("加载设置失败"))
    apiClient
      .models()
      .then((r) => {
        const usable = (r.models ?? []).filter((m) => !m.passthrough)
        setModels(r.models ?? [])
        if (usable.length) setPgModel(usable[0].id)
      })
      .catch(() => {})
    apiClient.stats(1).then(setStats).catch(() => {})
  }, [])

  async function savePrompt() {
    setSavingPrompt(true)
    try {
      await apiClient.putSettings(systemPrompt)
      toast.success("系统提示词已保存")
    } catch {
      toast.error("保存失败（不能为空）")
    } finally {
      setSavingPrompt(false)
    }
  }

  async function runPlayground() {
    if (!pgModel || !pgMessage.trim()) {
      toast.error("请选择模型并输入消息")
      return
    }
    setPgBusy(true)
    setPgResult(null)
    try {
      const r = await apiClient.playground(pgModel, pgMessage)
      setPgResult(r)
    } catch (e) {
      toast.error("测试失败")
      console.error(e)
    } finally {
      setPgBusy(false)
    }
  }

  const playgroundModels = models.filter((m) => !m.passthrough)

  return (
    <>
      <div>
        <h1 className="text-xl font-semibold">设置与测试</h1>
        <p className="text-sm text-muted-foreground">系统提示词 · 上游连接 · 快速验证</p>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">系统提示词</CardTitle>
          <CardDescription>
            网关要求每个请求的 system 消息以固定前缀开头，否则返回 403。请保留开头这句话。
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Textarea
            rows={5}
            value={systemPrompt}
            onChange={(e) => setSystemPrompt(e.target.value)}
          />
          <div className="flex items-center justify-between">
            <span className="text-xs text-muted-foreground">
              {systemPrompt.startsWith("You are Droid") ? (
                <span className="text-emerald-600">前缀正确</span>
              ) : (
                <span className="text-destructive">警告：缺少 "You are Droid..." 前缀会被上游拒绝</span>
              )}
            </span>
            <Button size="sm" onClick={savePrompt} disabled={savingPrompt}>
              <Save className="mr-1 h-4 w-4" />
              保存
            </Button>
          </div>
          <p className="text-xs text-muted-foreground">
            上游：<code>{upstream}</code>
          </p>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">Playground</CardTitle>
          <CardDescription>
            直接走完整链路验证模型可用性（非流式单轮）
            {stats && stats.avg_tps > 0 && (
              <span className="ml-2">
                近期平均 TPS {stats.avg_tps.toFixed(1)} · TTFT {Math.round(stats.avg_ttft_ms)}ms
              </span>
            )}
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex gap-4">
            <div className="w-72 space-y-2">
              <Label className="text-xs">模型</Label>
              <Select value={pgModel} onValueChange={setPgModel}>
                <SelectTrigger>
                  <SelectValue placeholder="选择模型" />
                </SelectTrigger>
                <SelectContent>
                  {playgroundModels.map((m) => (
                    <SelectItem key={m.id} value={m.id}>
                      {m.display_name} ({m.tier})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="flex-1 space-y-2">
              <Label className="text-xs">消息</Label>
              <Textarea rows={2} value={pgMessage} onChange={(e) => setPgMessage(e.target.value)} />
            </div>
          </div>
          <Button onClick={runPlayground} disabled={pgBusy}>
            <Play className="mr-1 h-4 w-4" />
            {pgBusy ? "请求中..." : "发送测试"}
          </Button>
          {pgResult && (
            <div className="rounded-md border bg-muted/40 p-4">
              <div className="mb-2 text-xs text-muted-foreground">实际使用：{pgResult.model}</div>
              <p className="whitespace-pre-wrap text-sm">{pgResult.text || "（空回复）"}</p>
            </div>
          )}
        </CardContent>
      </Card>
    </>
  )
}
