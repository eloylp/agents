'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'

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
  return (
    <nav style={{
      background: '#0f1d32',
      borderBottom: '1px solid #1e3a5f',
      padding: '0 1.5rem',
      display: 'flex',
      alignItems: 'center',
      gap: '0',
      height: '48px',
    }}>
      <span style={{ fontWeight: 700, fontSize: '0.95rem', color: '#38bdf8', marginRight: '2rem', letterSpacing: '0.08em', textTransform: 'uppercase' }}>
        Agents
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
              color: active ? '#38bdf8' : '#64748b',
              borderBottom: active ? '2px solid #38bdf8' : '2px solid transparent',
              fontWeight: active ? 600 : 400,
            }}
          >
            {label}
          </Link>
        )
      })}
    </nav>
  )
}
