'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { useEffect, useState } from 'react'
import { useTheme } from '@/lib/theme'
import { inferredBackends, setupComplete, type BackendsDiagnostics } from '@/lib/tooling-setup'

const links = [
  { href: '/graph/', label: 'Graph', group: 'Design' },
  { href: '/', label: 'Fleet', group: 'Design' },
  { href: '/events/', label: 'Events', group: 'Runtime' },
  { href: '/runners/', label: 'Runners', group: 'Runtime' },
  { href: '/traces/', label: 'Traces', group: 'Runtime' },
  { href: '/prompts/', label: 'Prompts', group: 'Knowledge' },
  { href: '/skills/', label: 'Skills', group: 'Knowledge' },
  { href: '/memory/', label: 'Memory', group: 'Knowledge' },
  { href: '/repos/', label: 'Repos', group: 'Config' },
  { href: '/config/', label: 'Config', group: 'Config' },
]

function activePath(pathname: string, href: string) {
  return pathname === href || (href !== '/' && pathname.startsWith(href))
}

export default function DashboardShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname()
  const { theme, toggle } = useTheme()
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [orphanCount, setOrphanCount] = useState(0)
  const [budgetAlertCount, setBudgetAlertCount] = useState(0)
  const [toolingSetupNeeded, setToolingSetupNeeded] = useState(false)

  const signOut = async () => {
    await fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' })
    window.location.replace('/')
  }

  useEffect(() => {
    setSidebarOpen(false)
  }, [pathname])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await fetch('/status', { cache: 'no-store' })
        if (!res.ok) return
        const data = await res.json() as { orphaned_agents?: { count?: number } }
        if (!cancelled) setOrphanCount(Number(data.orphaned_agents?.count ?? 0))
      } catch {
        // keep last known state on transient polling errors
      }
    }
    load()
    const id = window.setInterval(load, 30000)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await fetch('/token_budgets/alerts', { cache: 'no-store' })
        if (!res.ok) return
        const data = await res.json() as { count?: number }
        if (!cancelled) setBudgetAlertCount(Number(data.count ?? 0))
      } catch {
        // keep last known state on transient polling errors
      }
    }
    load()
    const id = window.setInterval(load, 30000)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await fetch('/backends/status', { cache: 'no-store' })
        if (!res.ok) return
        const data = await res.json() as BackendsDiagnostics
        const stored = window.localStorage.getItem('agents_tooling_setup_backends')
        const complete = setupComplete(data, inferredBackends(data, stored))
        if (!cancelled) setToolingSetupNeeded(!complete)
      } catch {
        // The setup wizard itself shows detailed diagnostic failures.
      }
    }
    load()
    const id = window.setInterval(load, 60000)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])

  const grouped = links.reduce<Record<string, typeof links>>((acc, link) => {
    acc[link.group] = acc[link.group] ?? []
    acc[link.group].push(link)
    return acc
  }, {})

  return (
    <div className="dashboard-shell">
      <style jsx global>{`
        .dashboard-shell { min-height: 100vh; }
        .shell-sidebar {
          position: fixed;
          top: 0;
          left: 0;
          bottom: 0;
          width: 244px;
          background: var(--bg-nav);
          border-right: 2px solid var(--border-nav);
          z-index: 900;
          display: flex;
          flex-direction: column;
          box-shadow: 10px 0 28px rgba(15,23,42,0.08);
        }
        .shell-main { margin-left: 244px; min-height: 100vh; }
        .shell-topbar {
          min-height: 50px;
          display: flex;
          align-items: center;
          gap: 0.75rem;
          padding: 0 1rem;
          border-bottom: 1px solid var(--border-subtle);
          background: color-mix(in srgb, var(--bg-card) 88%, transparent);
          backdrop-filter: blur(10px);
          position: sticky;
          top: 0;
          z-index: 500;
        }
        .shell-content { padding: 1.25rem; max-width: 1480px; margin: 0 auto; }
        .shell-menu-button { display: none; }
        .shell-overlay { display: none; }
        .shell-nav-link:hover { text-decoration: none; background: var(--accent-bg); color: var(--accent); }
        @media (max-width: 860px) {
          .shell-sidebar { transform: translateX(-102%); transition: transform 160ms ease; }
          .shell-sidebar.open { transform: translateX(0); }
          .shell-main { margin-left: 0; }
          .shell-menu-button { display: inline-flex; }
          .shell-overlay.open {
            display: block;
            position: fixed;
            inset: 0;
            background: rgba(0,0,0,0.36);
            z-index: 850;
          }
          .shell-content { padding: 1rem; }
        }
      `}</style>
      <div className={`shell-overlay ${sidebarOpen ? 'open' : ''}`} onClick={() => setSidebarOpen(false)} />
      <aside className={`shell-sidebar ${sidebarOpen ? 'open' : ''}`}>
        <div style={{ padding: '1.2rem 1rem 0.9rem', borderBottom: '1px solid var(--border-subtle)' }}>
          <div style={{ fontWeight: 800, color: 'var(--accent)', letterSpacing: '0.08em', fontSize: '1rem' }}>AGENTS</div>
          <div style={{ color: 'var(--text-faint)', fontSize: '0.72rem', marginTop: 4 }}>workflow studio</div>
        </div>
        <nav style={{ flex: 1, overflowY: 'auto', padding: '0.85rem' }}>
          {Object.entries(grouped).map(([group, items]) => (
            <div key={group} style={{ marginBottom: '1rem' }}>
              <div style={{ color: 'var(--text-faint)', fontSize: '0.68rem', fontWeight: 800, letterSpacing: '0.08em', textTransform: 'uppercase', margin: '0 0 0.35rem 0.35rem' }}>{group}</div>
              <div style={{ display: 'grid', gap: 3 }}>
                {items.map(link => {
                  const active = activePath(pathname, link.href)
                  return (
                    <Link
                      key={link.href}
                      href={link.href}
                      className="shell-nav-link"
                      style={{
                        display: 'flex',
                        alignItems: 'center',
                        minHeight: 34,
                        padding: '0 0.65rem',
                        border: `1px solid ${active ? 'var(--btn-primary-border)' : 'transparent'}`,
                        borderRadius: 0,
                        background: active ? 'var(--accent-bg)' : 'transparent',
                        color: active ? 'var(--accent)' : 'var(--text-muted)',
                        fontSize: '0.82rem',
                        fontWeight: active ? 700 : 500,
                      }}
                    >
                      {link.label}
                    </Link>
                  )
                })}
              </div>
            </div>
          ))}
        </nav>
        <div style={{ borderTop: '1px solid var(--border-subtle)', padding: '0.85rem', display: 'grid', gap: '0.5rem' }}>
          <button onClick={toggle} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 0, padding: '7px 10px', cursor: 'pointer', fontSize: '0.78rem', color: 'var(--text-muted)', textAlign: 'left' }}>
            {theme === 'light' ? 'Dark mode' : 'Light mode'}
          </button>
          <button onClick={signOut} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 0, padding: '7px 10px', cursor: 'pointer', fontSize: '0.78rem', color: 'var(--text-muted)', textAlign: 'left' }}>
            Sign out
          </button>
        </div>
      </aside>
      <div className="shell-main">
        <header className="shell-topbar">
          <button
            className="shell-menu-button"
            onClick={() => setSidebarOpen(true)}
            aria-label="Open navigation"
            style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 0, padding: '5px 9px', cursor: 'pointer' }}
          >
            Menu
          </button>
          <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', fontWeight: 600 }}>
            {links.find(link => activePath(pathname, link.href))?.label ?? 'Agents'}
          </div>
        </header>
        {orphanCount > 0 && (
          <Link href="/config/?tab=backends&focus=orphans" style={{ display: 'block', background: 'var(--bg-danger)', borderBottom: '1px solid var(--border-danger)', color: 'var(--text-danger)', padding: '0.5rem 1rem', fontSize: '0.82rem', fontWeight: 600, textDecoration: 'none' }}>
            {orphanCount} orphaned agent{orphanCount === 1 ? '' : 's'} detected. Click to review and fix in Backends and tools.
          </Link>
        )}
        {budgetAlertCount > 0 && (
          <Link href="/config/?tab=tokens" style={{ display: 'block', background: 'var(--bg-danger)', borderBottom: '1px solid var(--border-danger)', color: 'var(--text-danger)', padding: '0.5rem 1rem', fontSize: '0.82rem', fontWeight: 600, textDecoration: 'none' }}>
            {budgetAlertCount} token budget{budgetAlertCount === 1 ? '' : 's'} at or above alert threshold. Click to review in Token usage and limits.
          </Link>
        )}
        {toolingSetupNeeded && !pathname.startsWith('/setup/tooling') && (
          <Link href="/setup/tooling/" style={{ display: 'block', background: 'var(--accent-bg)', borderBottom: '1px solid var(--border)', color: 'var(--accent)', padding: '0.5rem 1rem', fontSize: '0.82rem', fontWeight: 600, textDecoration: 'none' }}>
            Tooling setup is incomplete. Open the guided setup checklist.
          </Link>
        )}
        <main className="shell-content">{children}</main>
      </div>
    </div>
  )
}
