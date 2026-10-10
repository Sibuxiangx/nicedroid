import { useState } from "react"
import { apiClient, ApiError } from "@/lib/api"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

export function Login({ onSuccess }: { onSuccess: (username: string) => void }) {
  const [username, setUsername] = useState("admin")
  const [password, setPassword] = useState("")
  const [error, setError] = useState("")
  const [busy, setBusy] = useState(false)

  async function submit(e: React.FormEvent) {
    e.preventDefault()
    setBusy(true)
    setError("")
    try {
      const res = await apiClient.login(username, password)
      onSuccess(res.username)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "登录失败")
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-screen items-center justify-center bg-muted/40">
      <Card className="w-full max-w-sm">
        {/* CardHeader is a grid, so centring needs justify-items (cross axis);
            items-center alone would leave the img at the start because a
            replaced element does not stretch to fill its cell. */}
        <CardHeader className="justify-items-center gap-2 text-center">
          <img src="/logo.webp" alt="" width={64} height={64} className="h-16 w-16 rounded-xl" />
          <CardTitle className="text-2xl">nicedroid</CardTitle>
          <CardDescription>管理控制台登录</CardDescription>
        </CardHeader>
        <CardContent>
          <form onSubmit={submit} className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="username">用户名</Label>
              <Input
                id="username"
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                autoComplete="username"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="password">密码</Label>
              <Input
                id="password"
                type="password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                autoComplete="current-password"
              />
            </div>
            {error && <p className="text-sm text-destructive">{error}</p>}
            <Button type="submit" className="w-full" disabled={busy}>
              {busy ? "登录中..." : "登录"}
            </Button>
            <p className="text-xs text-muted-foreground">
              首次启动的密码会打印在服务端日志里，可在设置中修改环境变量后重新初始化。
            </p>
          </form>
        </CardContent>
      </Card>
    </div>
  )
}
