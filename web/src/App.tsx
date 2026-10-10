import { useEffect, useState } from "react"
import { apiClient, ApiError } from "@/lib/api"
import { Layout, type PageKey } from "@/components/Layout"
import { Login } from "@/pages/Login"
import { Dashboard } from "@/pages/Dashboard"
import { Keys } from "@/pages/Keys"
import { Models } from "@/pages/Models"
import { Logs } from "@/pages/Logs"
import { SettingsPage } from "@/pages/Settings"
import { Skeleton } from "@/components/ui/skeleton"

export default function App() {
  const [username, setUsername] = useState<string | null>(null)
  const [checking, setChecking] = useState(true)
  const [page, setPage] = useState<PageKey>(() => {
    const h = window.location.hash.replace("#/", "")
    return (["dashboard", "keys", "models", "logs", "settings"].includes(h) ? h : "dashboard") as PageKey
  })

  useEffect(() => {
    apiClient
      .session()
      .then((s) => setUsername(s.username))
      .catch((err) => {
        if (!(err instanceof ApiError && err.status === 401)) console.error(err)
      })
      .finally(() => setChecking(false))
  }, [])

  useEffect(() => {
    window.location.hash = `#/${page}`
  }, [page])

  async function logout() {
    try {
      await apiClient.logout()
    } finally {
      setUsername(null)
    }
  }

  if (checking) {
    return (
      <div className="flex min-h-screen items-center justify-center">
        <Skeleton className="h-32 w-64" />
      </div>
    )
  }

  if (!username) {
    return <Login onSuccess={setUsername} />
  }

  return (
    <Layout page={page} onNavigate={setPage} username={username} onLogout={logout}>
      {page === "dashboard" && <Dashboard />}
      {page === "keys" && <Keys />}
      {page === "models" && <Models />}
      {page === "logs" && <Logs />}
      {page === "settings" && <SettingsPage />}
    </Layout>
  )
}
