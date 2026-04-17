'use client'
import Link from 'next/link'
import { usePathname } from 'next/navigation'

const links = [
  { href: '/ui/', label: 'Fleet' },
  { href: '/ui/traces/', label: 'Traces' },
  { href: '/ui/graph/', label: 'Graph' },
  { href: '/ui/events/', label: 'Events' },
  { href: '/ui/memory/', label: 'Memory' },
  { href: '/ui/config/', label: 'Config' },
]

export default function NavBar() {
  const pathname = usePathname()
  return (
    <nav style={{
      background: '#1e293b',
      borderBottom: '1px solid #334155',
      padding: '0 1.5rem',
      display: 'flex',
      alignItems: 'center',
      gap: '0',
      height: '48px',
    }}>
      <span style={{ fontWeight: 700, fontSize: '0.95rem', color: '#f1f5f9', marginRight: '2rem' }}>
        agents
      </span>
      {links.map(({ href, label }) => {
        const active = pathname === href || (href !== '/ui/' && pathname.startsWith(href))
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
              color: active ? '#60a5fa' : '#94a3b8',
              borderBottom: active ? '2px solid #60a5fa' : '2px solid transparent',
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
