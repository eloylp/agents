'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'
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
  return (
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
  )
}
