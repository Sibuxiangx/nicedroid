import { useEffect, useState } from "react"
import { apiClient } from "@/lib/api"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Separator } from "@/components/ui/separator"
import { cn } from "@/lib/utils"
import {
  LayoutDashboard,
  KeyRound,
  Boxes,
  ScrollText,
  Settings,
  Sun,
  Moon,
  LogOut,
} from "lucide-react"

export type PageKey = "dashboard" | "keys" | "models" | "logs" | "settings"

const NAV: { key: PageKey; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { key: "dashboard", label: "Dashboard", icon: LayoutDashboard },
  { key: "keys", label: "Key 管理", icon: KeyRound },
  { key: "models", label: "模型目录", icon: Boxes },
  { key: "logs", label: "请求日志", icon: ScrollText },
  { key: "settings", label: "设置与测试", icon: Settings },
]

type Theme = "light" | "dark"

function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => {
    const saved = localStorage.getItem("nicedroid_theme")
    if (saved === "light" || saved === "dark") return saved
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light"
  })
  useEffect(() => {
    document.documentElement.classList.toggle("dark", theme === "dark")
    localStorage.setItem("nicedroid_theme", theme)
  }, [theme])
  return { theme, toggle: () => setTheme((t) => (t === "dark" ? "light" : "dark")) }
}

export function Layout({
  page,
  onNavigate,
  username,
  onLogout,
  children,
}: {
  page: PageKey
  onNavigate: (p: PageKey) => void
  username: string
  onLogout: () => void
  children: React.ReactNode
}) {
  const { theme, toggle } = useTheme()
  const [standardMax, setStandardMax] = useState<number | null>(null)

  useEffect(() => {
    let alive = true
    const load = () => {
      apiClient
        .quotas()
        .then((q) => {
          if (!alive) return
          const max = q.keys.reduce((m, k) => Math.max(m, k.standard_max), 0)
          setStandardMax(q.keys.length ? max : null)
        })
        .catch(() => {})
    }
    load()
    const t = setInterval(load, 60_000)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [])

  return (
    <div className="min-h-screen">
      {/* Appbar */}
      <header className="sticky top-0 z-30 flex h-14 items-center gap-3 border-b bg-background px-4">
        <span className="text-base font-semibold tracking-tight">nicedroid</span>
        {standardMax !== null && (
          <Badge
            variant={standardMax >= 100 ? "destructive" : "secondary"}
            className="text-[10px]"
            title="Standard 配额已用百分比"
          >
            STD {standardMax}%
          </Badge>
        )}
        <div className="ml-auto flex items-center gap-2">
          <span className="hidden text-xs text-muted-foreground sm:inline">{username}</span>
          <Button
            variant="ghost"
            size="icon"
            onClick={toggle}
            title={theme === "dark" ? "切换到浅色" : "切换到深色"}
          >
            {theme === "dark" ? <Sun className="h-4 w-4" /> : <Moon className="h-4 w-4" />}
          </Button>
          <Button variant="ghost" size="sm" onClick={onLogout}>
            <LogOut className="mr-1 h-4 w-4" />
            退出
          </Button>
        </div>
      </header>

      <div className="flex">
        {/* Sidebar */}
        <aside className="sticky top-14 h-[calc(100vh-3.5rem)] w-52 shrink-0 border-r bg-sidebar">
          <nav className="space-y-1 p-2">
            {NAV.map((item) => {
              const Icon = item.icon
              return (
                <button
                  key={item.key}
                  onClick={() => onNavigate(item.key)}
                  className={cn(
                    "flex w-full items-center gap-3 rounded-md px-3 py-2 text-sm transition-colors",
                    page === item.key
                      ? "bg-accent text-accent-foreground font-medium"
                      : "text-muted-foreground hover:bg-accent/50 hover:text-foreground"
                  )}
                >
                  <Icon className="h-4 w-4" />
                  {item.label}
                </button>
              )
            })}
          </nav>
          <Separator className="mx-2 my-2" />
        </aside>

        {/* Content: full width */}
        <main className="min-w-0 flex-1 p-4 lg:p-6">
          <div className="space-y-6">{children}</div>
        </main>
      </div>
    </div>
  )
}
