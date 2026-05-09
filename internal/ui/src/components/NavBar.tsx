'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { useEffect, useState } from 'react'
import { useTheme } from '@/lib/theme'
import { useSelectedWorkspace } from '@/lib/workspace'

// `flow: true` marks the three pages on the event lifecycle path
// (Events → Runners → Traces). The navbar renders a small arrow
// between consecutive flow items so the natural traversal is visible
// at a glance: an event arrives, a runner picks it up, the trace
// records the execution detail.
const links = [
  { href: '/graph/',   label: 'Graph' },
  { href: '/',         label: 'Fleet' },
  { href: '/events/',  label: 'Events',  flow: true },
  { href: '/runners/', label: 'Runners', flow: true },
  { href: '/traces/',  label: 'Traces',  flow: true },
  { href: '/prompts/', label: 'Prompts' },
  { href: '/skills/',  label: 'Skills' },
  { href: '/memory/',  label: 'Memory' },
  { href: '/repos/',   label: 'Repos' },
  { href: '/config/',  label: 'Config' },
]

export default function NavBar() {
  const pathname = usePathname()
  const { theme, toggle } = useTheme()
  const { workspace, workspaces, setWorkspace } = useSelectedWorkspace()
  const [orphanCount, setOrphanCount] = useState(0)
  const [budgetAlertCount, setBudgetAlertCount] = useState(0)

  const signOut = async () => {
    await fetch('/auth/logout', { method: 'POST', credentials: 'same-origin' })
    window.location.replace('/')
  }

  useEffect(() => {
    let cancelled = false
    const load = async () => {
      try {
        const res = await fetch('/status', { cache: 'no-store' })
        if (!res.ok) return
        const data = await res.json() as { orphaned_agents?: { count?: number } }
        if (cancelled) return
        setOrphanCount(Number(data.orphaned_agents?.count ?? 0))
      } catch {
        // keep last known banner state on transient polling errors
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
        if (cancelled) return
        setBudgetAlertCount(Number(data.count ?? 0))
      } catch {
        // keep last known banner state on transient polling errors
      }
    }

    load()
    const id = window.setInterval(load, 30000)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])

  return (
    <>
      <nav style={{
        background: 'var(--bg-nav)',
        borderBottom: '2px solid var(--border-nav)',
        padding: '0 1.5rem',
        display: 'flex',
        alignItems: 'center',
        gap: '0',
        height: '48px',
      }}>
        <span style={{ fontWeight: 700, fontSize: '0.95rem', color: 'var(--accent)', marginRight: '2rem', letterSpacing: '0.05em' }}>
          AGENTS
        </span>
        {links.map(({ href, label, flow }, i) => {
          const active = pathname === href || (href !== '/' && pathname.startsWith(href))
          const next = links[i + 1]
          const showFlowArrow = flow && next?.flow
          return (
            <span key={href} style={{ display: 'flex', alignItems: 'center' }}>
              <Link
                href={href}
                style={{
                  padding: flow ? '0 0.65rem' : '0 1rem',
                  height: '48px',
                  display: 'flex',
                  alignItems: 'center',
                  fontSize: '0.875rem',
                  color: active ? 'var(--accent)' : 'var(--text-muted)',
                  borderBottom: active ? '2px solid var(--accent)' : '2px solid transparent',
                  fontWeight: active ? 600 : 400,
                }}
              >
                {label}
              </Link>
              {showFlowArrow && (
                <span aria-hidden style={{
                  color: 'var(--text-faint)',
                  fontSize: '0.85rem',
                  margin: '0 0.1rem',
                  userSelect: 'none',
                }}>→</span>
              )}
            </span>
          )
        })}
        <select
          value={workspace}
          onChange={e => setWorkspace(e.target.value)}
          title="Workspace"
          style={{
            marginLeft: 'auto',
            maxWidth: '180px',
            background: 'var(--bg-card)',
            border: '1px solid var(--border)',
            borderRadius: 4,
            padding: '4px 8px',
            fontSize: '0.78rem',
            color: 'var(--text-muted)',
          }}
        >
          {workspaces.length === 0 && <option value={workspace}>{workspace}</option>}
          {workspaces.map(w => <option key={w.id} value={w.id}>{w.name}</option>)}
        </select>
        <button
          onClick={toggle}
          style={{
            marginLeft: '0.5rem',
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: 0,
            padding: '4px 10px',
            cursor: 'pointer',
            fontSize: '0.78rem',
            color: 'var(--text-muted)',
          }}
        >
          {theme === 'light' ? 'Dark' : 'Light'}
        </button>
        <button
          onClick={signOut}
          style={{
            marginLeft: '0.5rem',
            background: 'var(--bg-card)',
            border: '1px solid var(--border)',
            borderRadius: 0,
            padding: '4px 10px',
            cursor: 'pointer',
            fontSize: '0.78rem',
            color: 'var(--text-muted)',
          }}
        >
          Sign out
        </button>
      </nav>
      {orphanCount > 0 && (
        <Link
          href="/config/?tab=backends&focus=orphans"
          style={{
            display: 'block',
            background: 'var(--bg-danger)',
            borderBottom: '1px solid var(--border-danger)',
            color: 'var(--text-danger)',
            padding: '0.5rem 1.5rem',
            fontSize: '0.82rem',
            fontWeight: 600,
            textDecoration: 'none',
          }}
        >
          {orphanCount} orphaned agent{orphanCount === 1 ? '' : 's'} detected (pinned model unavailable). Click to review and fix in Backends and tools.
        </Link>
      )}
      {budgetAlertCount > 0 && (
        <Link
          href="/config/?tab=tokens"
          style={{
            display: 'block',
            background: 'var(--bg-danger)',
            borderBottom: '1px solid var(--border-danger)',
            color: 'var(--text-danger)',
            padding: '0.5rem 1.5rem',
            fontSize: '0.82rem',
            fontWeight: 600,
            textDecoration: 'none',
          }}
        >
          {budgetAlertCount} token budget{budgetAlertCount === 1 ? '' : 's'} at or above alert threshold. Click to review in Token usage and limits.
        </Link>
      )}
    </>
  )
}
