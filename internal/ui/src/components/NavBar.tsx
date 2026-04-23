'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
import { useEffect, useState } from 'react'
import { useTheme } from '@/lib/theme'

const links = [
  { href: '/', label: 'Fleet' },
  { href: '/skills/', label: 'Skills' },
  { href: '/repos/', label: 'Repos' },
  { href: '/traces/', label: 'Traces' },
  { href: '/graph/', label: 'Graph' },
  { href: '/events/', label: 'Events' },
  { href: '/memory/', label: 'Memory' },
  { href: '/config/', label: 'Config' },
]

export default function NavBar() {
  const pathname = usePathname()
  const { theme, toggle } = useTheme()
  const [orphanCount, setOrphanCount] = useState(0)

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
        {links.map(({ href, label }) => {
          const active = pathname === href || (href !== '/' && pathname.startsWith(href))
          return (
            <Link
              key={href}
              href={href}
              style={{
                padding: '0 1rem',
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
          )
        })}
        <button
          onClick={toggle}
          style={{
            marginLeft: 'auto',
            background: 'none',
            border: '1px solid var(--border)',
            borderRadius: '6px',
            padding: '4px 10px',
            cursor: 'pointer',
            fontSize: '0.78rem',
            color: 'var(--text-muted)',
          }}
        >
          {theme === 'light' ? 'Dark' : 'Light'}
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
    </>
  )
}
