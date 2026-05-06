'use client'

import { useEffect, useState } from 'react'

const openAuthModalEvent = 'agents-auth-token-request'

type AuthStatus = {
  bootstrap_required: boolean
  authenticated: boolean
  user?: { username: string }
  legacy_enabled: boolean
}

type AuthToken = {
  id: number
  kind: string
  name: string
  prefix: string
  created_at: string
  expires_at?: string
  last_used_at?: string
  revoked_at?: string
}

let fetchPatched = false

export function getStoredBearerToken(): string {
  return ''
}

export function requestBearerTokenModal() {
  if (typeof window === 'undefined') return
  window.dispatchEvent(new Event(openAuthModalEvent))
}

function isSameOriginRequest(input: RequestInfo | URL): boolean {
  const raw = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
  try {
    return new URL(raw, window.location.href).origin === window.location.origin
  } catch {
    return false
  }
}

function patchFetch() {
  if (fetchPatched || typeof window === 'undefined') return
  fetchPatched = true
  const originalFetch = window.fetch.bind(window)

  window.fetch = async (input: RequestInfo | URL, init?: RequestInit) => {
    const sameOrigin = isSameOriginRequest(input)
    const headers = new Headers(init?.headers || (input instanceof Request ? input.headers : undefined))
    const response = await originalFetch(input, { ...init, headers, credentials: sameOrigin ? 'same-origin' : init?.credentials })
    if (sameOrigin && response.status === 401) {
      requestBearerTokenModal()
    }
    return response
  }
}

async function loadAuthStatus(): Promise<AuthStatus | null> {
  try {
    const res = await fetch('/auth/status', { cache: 'no-store' })
    if (!res.ok) return null
    return await res.json() as AuthStatus
  } catch {
    return null
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [username, setUsername] = useState('admin')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')

  useEffect(() => {
    patchFetch()
    let cancelled = false
    loadAuthStatus().then(next => {
      if (cancelled || !next) return
      setStatus(next)
      setOpen(next.bootstrap_required || !next.authenticated)
    })
    const openModal = () => setOpen(true)
    window.addEventListener(openAuthModalEvent, openModal)
    return () => {
      cancelled = true
      window.removeEventListener(openAuthModalEvent, openModal)
    }
  }, [])

  const submit = async () => {
    setError('')
    const path = status?.bootstrap_required ? '/auth/bootstrap' : '/auth/login'
    const res = await fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username, password }),
    })
    if (!res.ok) {
      setError(status?.bootstrap_required ? 'Bootstrap failed.' : 'Login failed.')
      return
    }
    const next = await loadAuthStatus()
    setStatus(next)
    setPassword('')
    setOpen(false)
  }

  return (
    <>
      {children}
      {open && (
        <div style={overlayStyle}>
          <div style={modalStyle}>
            <h2 style={{ color: 'var(--text-heading)', fontSize: '1rem', marginBottom: '0.75rem' }}>
              {status?.bootstrap_required ? 'Create first user' : 'Sign in'}
            </h2>
            <input
              autoFocus
              value={username}
              onChange={e => setUsername(e.target.value)}
              placeholder="Username"
              style={inputStyle}
            />
            <input
              type="password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              placeholder="Password"
              style={{ ...inputStyle, marginTop: '0.65rem' }}
              onKeyDown={e => {
                if (e.key === 'Enter') void submit()
              }}
            />
            {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.78rem', marginTop: '0.65rem' }}>{error}</p>}
            <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end', marginTop: '1rem' }}>
              {!status?.bootstrap_required && (
                <button type="button" onClick={() => setOpen(false)} style={secondaryButtonStyle}>Cancel</button>
              )}
              <button type="button" onClick={submit} style={primaryButtonStyle}>
                {status?.bootstrap_required ? 'Create user' : 'Sign in'}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

export function AuthTokenSettings() {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [tokens, setTokens] = useState<AuthToken[]>([])
  const [name, setName] = useState('Codex MCP')
  const [created, setCreated] = useState('')

  const refresh = async () => {
    const next = await loadAuthStatus()
    setStatus(next)
    if (!next?.authenticated) return
    const res = await fetch('/auth/tokens', { cache: 'no-store' })
    if (res.ok) setTokens(await res.json() as AuthToken[])
  }

  useEffect(() => {
    void refresh()
  }, [])

  const create = async () => {
    setCreated('')
    const res = await fetch('/auth/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    })
    if (!res.ok) return
    const data = await res.json() as AuthToken & { token: string }
    setCreated(data.token)
    await refresh()
  }

  const revoke = async (id: number) => {
    const res = await fetch(`/auth/tokens/${id}`, { method: 'DELETE' })
    if (res.ok) await refresh()
  }

  return (
    <div style={{ display: 'grid', gap: '1rem' }}>
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: '1rem', flexWrap: 'wrap' }}>
        <div>
          <h3 style={{ fontSize: '0.95rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }}>Authentication</h3>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>
            {status?.authenticated ? `Signed in as ${status.user?.username || 'user'}.` : 'No active browser session.'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          {!status?.authenticated && <button type="button" onClick={requestBearerTokenModal} style={secondaryButtonStyle}>Sign in</button>}
          {status?.authenticated && <button type="button" onClick={async () => { await fetch('/auth/logout', { method: 'POST' }); window.location.reload() }} style={secondaryButtonStyle}>Sign out</button>}
        </div>
      </div>
      {status?.authenticated && (
        <>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            <input value={name} onChange={e => setName(e.target.value)} style={{ ...inputStyle, maxWidth: '260px' }} />
            <button type="button" onClick={create} style={primaryButtonStyle}>Create API token</button>
          </div>
          {created && (
            <code style={{ display: 'block', padding: '0.75rem', border: '1px solid var(--border)', borderRadius: '6px', overflowWrap: 'anywhere', color: 'var(--text-heading)' }}>
              {created}
            </code>
          )}
          <div style={{ display: 'grid', gap: '0.5rem' }}>
            {tokens.map(token => (
              <div key={token.id} style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: '1rem', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '0.65rem' }}>
                <div>
                  <strong style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>{token.name}</strong>
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.74rem' }}>{token.kind} · {token.prefix} · {token.revoked_at ? 'revoked' : 'active'}</p>
                </div>
                {!token.revoked_at && <button type="button" onClick={() => revoke(token.id)} style={secondaryButtonStyle}>Revoke</button>}
              </div>
            ))}
          </div>
        </>
      )}
    </div>
  )
}

const overlayStyle: React.CSSProperties = {
  position: 'fixed',
  inset: 0,
  background: 'var(--bg-modal-overlay)',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  padding: '1rem',
  zIndex: 1000,
}

const modalStyle: React.CSSProperties = {
  width: 'min(460px, 100%)',
  background: 'var(--bg-card)',
  border: '1px solid var(--border)',
  borderRadius: '8px',
  boxShadow: '0 20px 60px rgba(0,0,0,0.22)',
  padding: '1.25rem',
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '0.65rem 0.75rem',
  border: '1px solid var(--border)',
  borderRadius: '6px',
  background: 'var(--bg-input)',
  color: 'var(--text)',
  fontFamily: 'inherit',
}

const secondaryButtonStyle: React.CSSProperties = {
  padding: '0.45rem 0.75rem',
  border: '1px solid var(--border)',
  borderRadius: '6px',
  background: 'var(--bg-card)',
  color: 'var(--text)',
  fontFamily: 'inherit',
  cursor: 'pointer',
}

const primaryButtonStyle: React.CSSProperties = {
  ...secondaryButtonStyle,
  borderColor: 'var(--btn-primary-border)',
  background: 'var(--btn-primary-bg)',
  color: '#fff',
}
