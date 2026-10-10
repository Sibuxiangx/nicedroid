import { useEffect, useState } from "react"
import { apiClient, fmtTime, type ClientKey, type UpstreamKey } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "@/components/ui/dialog"
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
import { Copy, Plus, ShieldCheck, Trash2 } from "lucide-react"
import { toast } from "sonner"

function AddUpstreamDialog({ onAdded }: { onAdded: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [key, setKey] = useState("")
  const [weight, setWeight] = useState(1)
  const [busy, setBusy] = useState(false)

  async function submit() {
    if (!key.trim()) {
      toast.error("请填写 Factory API Key")
      return
    }
    setBusy(true)
    try {
      await apiClient.addUpstreamKey(name, key.trim(), weight)
      toast.success("已添加并自动刷新配额")
      setOpen(false)
      setName("")
      setKey("")
      setWeight(1)
      onAdded()
    } catch {
      toast.error("添加失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus className="mr-1 h-4 w-4" />
          添加上游 Key
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>添加上游 Key</DialogTitle>
          <DialogDescription>Factory API Key（fk- 开头），多个 Key 会按权重轮询</DialogDescription>
        </DialogHeader>
        <div className="space-y-4">
          <div className="space-y-2">
            <Label>名称</Label>
            <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 main / backup" />
          </div>
          <div className="space-y-2">
            <Label>API Key</Label>
            <Input value={key} onChange={(e) => setKey(e.target.value)} placeholder="fk-..." />
          </div>
          <div className="space-y-2">
            <Label>权重</Label>
            <Input
              type="number"
              min={1}
              value={weight}
              onChange={(e) => setWeight(Number(e.target.value) || 1)}
            />
          </div>
        </div>
        <DialogFooter>
          <Button onClick={submit} disabled={busy}>
            添加
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function AddClientDialog({ onAdded }: { onAdded: () => void }) {
  const [open, setOpen] = useState(false)
  const [name, setName] = useState("")
  const [rpm, setRpm] = useState(0)
  const [daily, setDaily] = useState(0)
  const [busy, setBusy] = useState(false)
  const [created, setCreated] = useState<ClientKey | null>(null)

  async function submit() {
    setBusy(true)
    try {
      const k = await apiClient.addClientKey(name, rpm, daily)
      setCreated(k)
      onAdded()
    } catch {
      toast.error("创建失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog
      open={open}
      onOpenChange={(v) => {
        setOpen(v)
        if (!v) setCreated(null)
      }}
    >
      <DialogTrigger asChild>
        <Button size="sm" variant="secondary">
          <Plus className="mr-1 h-4 w-4" />
          新建客户端 Key
        </Button>
      </DialogTrigger>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>新建客户端 Key</DialogTitle>
          <DialogDescription>客户端调用 /v1/* 时使用的密钥（sk-nicedroid- 开头）</DialogDescription>
        </DialogHeader>
        {created ? (
          <div className="space-y-3">
            <p className="text-sm text-muted-foreground">Key 创建成功，请立即复制保存：</p>
            <div className="flex items-center gap-2">
              <code className="flex-1 break-all rounded bg-muted p-2 text-xs">{created.key}</code>
              <Button
                size="icon"
                variant="outline"
                onClick={() => {
                  navigator.clipboard.writeText(created.key)
                  toast.success("已复制")
                }}
              >
                <Copy className="h-4 w-4" />
              </Button>
            </div>
          </div>
        ) : (
          <div className="space-y-4">
            <div className="space-y-2">
              <Label>名称</Label>
              <Input value={name} onChange={(e) => setName(e.target.value)} placeholder="例如 claude-code" />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <div className="space-y-2">
                <Label>RPM 限制（0=不限）</Label>
                <Input type="number" min={0} value={rpm} onChange={(e) => setRpm(Number(e.target.value) || 0)} />
              </div>
              <div className="space-y-2">
                <Label>每日 token（0=不限）</Label>
                <Input
                  type="number"
                  min={0}
                  value={daily}
                  onChange={(e) => setDaily(Number(e.target.value) || 0)}
                />
              </div>
            </div>
          </div>
        )}
        <DialogFooter>
          {!created && (
            <Button onClick={submit} disabled={busy}>
              创建
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

export function Keys() {
  const [upstream, setUpstream] = useState<UpstreamKey[]>([])
  const [clients, setClients] = useState<ClientKey[]>([])

  async function load() {
    try {
      const [u, c] = await Promise.all([apiClient.upstreamKeys(), apiClient.clientKeys()])
      setUpstream(u.keys ?? [])
      setClients(c.keys ?? [])
    } catch {
      toast.error("加载失败")
    }
  }

  useEffect(() => {
    void load()
  }, [])

  async function verify(id: number) {
    try {
      const r = await apiClient.verifyUpstreamKey(id)
      if (r.ok) toast.success("Key 有效")
      else toast.error(`Key 无效：${r.status}`)
      void load()
    } catch {
      toast.error("校验失败")
    }
  }

  return (
    <>
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Key 管理</h1>
          <p className="text-sm text-muted-foreground">上游 Factory Key 池 + 客户端访问密钥</p>
        </div>
        <div className="flex gap-2">
          <AddClientDialog onAdded={load} />
          <AddUpstreamDialog onAdded={load} />
        </div>
      </div>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">上游 Key 池</CardTitle>
          <CardDescription>按权重加权轮询；standard 配额耗尽的 key 会被自动跳过</CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>权重</TableHead>
                <TableHead>状态</TableHead>
                <TableHead>最近检查</TableHead>
                <TableHead>启用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {upstream.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name || `key #${k.id}`}</TableCell>
                  <TableCell>
                    <code className="text-xs">{k.key_masked}</code>
                  </TableCell>
                  <TableCell>{k.weight}</TableCell>
                  <TableCell>
                    {k.last_status ? (
                      <Badge variant={k.last_status === "ok" ? "default" : "destructive"} className="text-[10px]">
                        {k.last_status.slice(0, 30)}
                      </Badge>
                    ) : (
                      <span className="text-xs text-muted-foreground">未检查</span>
                    )}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{fmtTime(k.last_checked)}</TableCell>
                  <TableCell>
                    <Switch
                      checked={k.enabled}
                      onCheckedChange={async (v) => {
                        await apiClient.patchUpstreamKey(k.id, { enabled: v })
                        void load()
                      }}
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="outline" onClick={() => verify(k.id)}>
                        <ShieldCheck className="mr-1 h-3.5 w-3.5" />
                        校验
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        onClick={async () => {
                          if (!confirm(`删除 ${k.name || k.id}？`)) return
                          await apiClient.deleteUpstreamKey(k.id)
                          toast.success("已删除")
                          void load()
                        }}
                      >
                        <Trash2 className="h-3.5 w-3.5 text-destructive" />
                      </Button>
                    </div>
                  </TableCell>
                </TableRow>
              ))}
              {!upstream.length && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-sm text-muted-foreground">
                    还没有上游 Key，点右上角添加
                  </TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle className="text-base">客户端 Key</CardTitle>
          <CardDescription>
            存在启用的客户端 Key 时，/v1/* 需要携带 Authorization: Bearer 或 x-api-key；不配置则开放访问
          </CardDescription>
        </CardHeader>
        <CardContent>
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>名称</TableHead>
                <TableHead>Key</TableHead>
                <TableHead>RPM</TableHead>
                <TableHead>每日 token</TableHead>
                <TableHead>创建时间</TableHead>
                <TableHead>启用</TableHead>
                <TableHead className="text-right">操作</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {clients.map((k) => (
                <TableRow key={k.id}>
                  <TableCell className="font-medium">{k.name || `key #${k.id}`}</TableCell>
                  <TableCell>
                    <div className="flex items-center gap-1">
                      <code className="text-xs">{k.key.slice(0, 16)}...</code>
                      <Button
                        size="icon"
                        variant="ghost"
                        className="h-6 w-6"
                        onClick={() => {
                          navigator.clipboard.writeText(k.key)
                          toast.success("已复制")
                        }}
                      >
                        <Copy className="h-3 w-3" />
                      </Button>
                    </div>
                  </TableCell>
                  <TableCell>{k.rpm_limit || "∞"}</TableCell>
                  <TableCell>{k.daily_tokens || "∞"}</TableCell>
                  <TableCell className="text-xs text-muted-foreground">{fmtTime(k.created_at)}</TableCell>
                  <TableCell>
                    <Switch
                      checked={k.enabled}
                      onCheckedChange={async (v) => {
                        await apiClient.patchClientKey(k.id, { enabled: v })
                        void load()
                      }}
                    />
                  </TableCell>
                  <TableCell className="text-right">
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={async () => {
                        if (!confirm(`删除客户端 Key ${k.name || k.id}？`)) return
                        await apiClient.deleteClientKey(k.id)
                        toast.success("已删除")
                        void load()
                      }}
                    >
                      <Trash2 className="h-3.5 w-3.5 text-destructive" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
              {!clients.length && (
                <TableRow>
                  <TableCell colSpan={7} className="text-center text-sm text-muted-foreground">
                    未配置客户端 Key（开放模式）
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
