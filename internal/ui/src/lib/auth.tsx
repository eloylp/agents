'use client'

import { useEffect, useState } from 'react'

const openAuthModalEvent = 'agents-auth-token-request'

type AuthStatus = {
  bootstrap_required: boolean
  authenticated: boolean
  user?: { username: string; is_admin: boolean }
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

type AuthUser = {
  id: number
  username: string
  created_at: string
  updated_at: string
  last_login_at?: string
  disabled_at?: string
  is_admin: boolean
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
  const [status, setStatus] = useState<AuthStatus | null>(null)

  useEffect(() => {
    patchFetch()
    let cancelled = false
    loadAuthStatus().then(next => {
      if (cancelled) return
      setStatus(next ?? { bootstrap_required: false, authenticated: false })
    })
    const redirectToLogin = () => window.location.replace('/')
    window.addEventListener(openAuthModalEvent, redirectToLogin)
    return () => {
      cancelled = true
      window.removeEventListener(openAuthModalEvent, redirectToLogin)
    }
  }, [])

  useEffect(() => {
    if (status && !status.authenticated) window.location.replace('/')
  }, [status])

  if (status?.authenticated === true) return <>{children}</>

  return <AuthRedirectScreen loading={status === null} />
}

export function AuthTokenSettings() {
  const [status, setStatus] = useState<AuthStatus | null>(null)
  const [users, setUsers] = useState<AuthUser[]>([])
  const [tokens, setTokens] = useState<AuthToken[]>([])
  const [name, setName] = useState('Codex MCP')
  const [created, setCreated] = useState('')
  const [newUsername, setNewUsername] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [userError, setUserError] = useState('')
  const [tokenError, setTokenError] = useState('')

  const refresh = async () => {
    const next = await loadAuthStatus()
    setStatus(next)
    if (!next?.authenticated) return
    const [usersRes, tokensRes] = await Promise.all([
      fetch('/auth/users', { cache: 'no-store' }),
      fetch('/auth/tokens', { cache: 'no-store' }),
    ])
    if (usersRes.ok) setUsers(await usersRes.json() as AuthUser[])
    if (tokensRes.ok) setTokens(await tokensRes.json() as AuthToken[])
  }

  useEffect(() => {
    void refresh()
  }, [])

  const create = async () => {
    setCreated('')
    setTokenError('')
    const res = await fetch('/auth/tokens', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ name }),
    })
    if (!res.ok) {
      setTokenError('Could not create API token.')
      return
    }
    const data = await res.json() as AuthToken & { token: string }
    setCreated(data.token)
    await refresh()
  }

  const createUser = async () => {
    setUserError('')
    const res = await fetch('/auth/users', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ username: newUsername, password: newPassword }),
    })
    if (!res.ok) {
      const text = await res.text()
      setUserError(text.trim() || 'Could not create user.')
      return
    }
    setNewUsername('')
    setNewPassword('')
    await refresh()
  }

  const deleteUser = async (id: number) => {
    setUserError('')
    const res = await fetch(`/auth/users/${id}`, { method: 'DELETE' })
    if (!res.ok) {
      const text = await res.text()
      setUserError(text.trim() || 'Could not remove user.')
      return
    }
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
          <section style={sectionStyle}>
            <div>
              <h4 style={sectionTitleStyle}>Users</h4>
              <p style={sectionHelpStyle}>Admin users can create or remove dashboard users. Every user can manage daemon configuration and create their own API tokens.</p>
            </div>
            <div style={{ display: 'grid', gap: '0.5rem' }}>
              {users.map(user => (
                <div key={user.id} style={rowStyle}>
                  <div>
                    <strong style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>{user.username}</strong>
                    <p style={{ color: 'var(--text-muted)', fontSize: '0.74rem' }}>
                      created {formatDate(user.created_at)}
                      {user.last_login_at ? ` · last login ${formatDate(user.last_login_at)}` : ''}
                      {user.disabled_at ? ' · disabled' : ''}
                    </p>
                  </div>
                  <div style={{ display: 'flex', gap: '0.45rem', alignItems: 'center', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    {user.is_admin && <span style={pillStyle}>admin</span>}
                    {status.user?.username === user.username && <span style={pillStyle}>current</span>}
                    {status.user?.is_admin && !user.is_admin && status.user?.username !== user.username && (
                      <button type="button" onClick={() => deleteUser(user.id)} style={secondaryButtonStyle}>Remove</button>
                    )}
                  </div>
                </div>
              ))}
            </div>
            {status.user?.is_admin ? (
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                <input value={newUsername} onChange={e => setNewUsername(e.target.value)} placeholder="Username" style={{ ...inputStyle, maxWidth: '220px' }} />
                <input type="password" value={newPassword} onChange={e => setNewPassword(e.target.value)} placeholder="Password" style={{ ...inputStyle, maxWidth: '260px' }} />
                <button type="button" onClick={createUser} style={primaryButtonStyle}>Create user</button>
              </div>
            ) : (
              <p style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>Only the admin user can create or remove dashboard users.</p>
            )}
            {userError && <p style={{ color: 'var(--text-danger)', fontSize: '0.78rem' }}>{userError}</p>}
          </section>

          <section style={sectionStyle}>
            <div>
              <h4 style={sectionTitleStyle}>API tokens</h4>
              <p style={sectionHelpStyle}>Create revocable bearer tokens for MCP and REST clients. Plaintext tokens are shown only once.</p>
            </div>
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
              <input value={name} onChange={e => setName(e.target.value)} style={{ ...inputStyle, maxWidth: '260px' }} />
              <button type="button" onClick={create} style={primaryButtonStyle}>Create API token</button>
            </div>
            {tokenError && <p style={{ color: 'var(--text-danger)', fontSize: '0.78rem' }}>{tokenError}</p>}
            {created && (
              <code style={{ display: 'block', padding: '0.75rem', border: '1px solid var(--border)', borderRadius: '6px', overflowWrap: 'anywhere', color: 'var(--text-heading)' }}>
                {created}
              </code>
            )}
            <div style={{ display: 'grid', gap: '0.5rem' }}>
              {tokens.map(token => (
                <div key={token.id} style={rowStyle}>
                  <div>
                    <strong style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>{token.name}</strong>
                    <p style={{ color: 'var(--text-muted)', fontSize: '0.74rem' }}>{token.kind} · {token.prefix} · {token.revoked_at ? 'revoked' : 'active'}</p>
                  </div>
                  {!token.revoked_at && <button type="button" onClick={() => revoke(token.id)} style={secondaryButtonStyle}>Revoke</button>}
                </div>
              ))}
            </div>
          </section>
        </>
      )}
    </div>
  )
}

function AuthRedirectScreen({ loading }: { loading: boolean }) {
  return (
    <div style={authPageStyle}>
      <div style={authBackdropStyle} />
      <div style={authCardStyle}>
        <p style={authEyebrowStyle}>Agents dashboard</p>
        <h1 style={authTitleStyle}>{loading ? 'Checking session' : 'Redirecting to sign in'}</h1>
        <p style={authCopyStyle}>
          {loading ? 'Checking your browser session.' : 'Opening the root login page.'}
        </p>
        <div style={authLoadingStyle}>{loading ? 'Checking session...' : 'Redirecting...'}</div>
      </div>
    </div>
  )
}

function formatDate(value: string) {
  if (!value) return 'unknown'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

const sectionStyle: React.CSSProperties = {
  display: 'grid',
  gap: '0.75rem',
  padding: '0.85rem',
  border: '1px solid var(--border-subtle)',
  borderRadius: '8px',
  background: 'var(--bg)',
}

const sectionTitleStyle: React.CSSProperties = {
  fontSize: '0.9rem',
  fontWeight: 700,
  color: 'var(--text-heading)',
  margin: 0,
}

const sectionHelpStyle: React.CSSProperties = {
  color: 'var(--text-muted)',
  fontSize: '0.76rem',
  marginTop: '0.25rem',
}

const rowStyle: React.CSSProperties = {
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'space-between',
  gap: '1rem',
  border: '1px solid var(--border-subtle)',
  borderRadius: '6px',
  padding: '0.65rem',
  background: 'var(--bg-card)',
}

const pillStyle: React.CSSProperties = {
  fontSize: '0.68rem',
  textTransform: 'uppercase',
  letterSpacing: '0.04em',
  border: '1px solid var(--border)',
  borderRadius: '999px',
  color: 'var(--text-muted)',
  padding: '0.15rem 0.45rem',
}

const authPageStyle: React.CSSProperties = {
  minHeight: '100vh',
  display: 'flex',
  alignItems: 'center',
  justifyContent: 'center',
  position: 'relative',
  overflow: 'hidden',
  padding: '1.25rem',
  background:
    'linear-gradient(135deg, rgba(7,17,31,0.82), rgba(13,34,55,0.58) 44%, rgba(238,246,255,0.18)), url("/ui/agents.jpg") center / cover no-repeat, linear-gradient(135deg, #07111f 0%, #0d2237 48%, #eef6ff 100%)',
}

const authBackdropStyle: React.CSSProperties = {
  position: 'absolute',
  inset: 0,
  opacity: 0.32,
  backgroundImage:
    'linear-gradient(rgba(255,255,255,0.16) 1px, transparent 1px), linear-gradient(90deg, rgba(255,255,255,0.16) 1px, transparent 1px)',
  backgroundSize: '44px 44px',
  maskImage: 'linear-gradient(120deg, transparent 0%, black 18%, black 82%, transparent 100%)',
}

const authCardStyle: React.CSSProperties = {
  position: 'relative',
  width: 'min(430px, 100%)',
  border: '1px solid rgba(255,255,255,0.55)',
  borderRadius: '20px',
  background: 'rgba(255,255,255,0.88)',
  backdropFilter: 'blur(18px)',
  padding: '1.35rem',
  boxShadow: '0 30px 90px rgba(2,6,23,0.34)',
}

const authEyebrowStyle: React.CSSProperties = {
  color: '#2563eb',
  fontSize: '0.72rem',
  fontWeight: 800,
  letterSpacing: '0.14em',
  textTransform: 'uppercase',
  marginBottom: '0.55rem',
}

const authTitleStyle: React.CSSProperties = {
  color: '#0f2742',
  fontSize: '1.65rem',
  letterSpacing: '-0.04em',
  lineHeight: 1.05,
  marginBottom: '0.65rem',
}

const authCopyStyle: React.CSSProperties = {
  color: '#475569',
  fontSize: '0.88rem',
  lineHeight: 1.55,
  marginBottom: '1.15rem',
}

const authLoadingStyle: React.CSSProperties = {
  border: '1px solid rgba(37,99,235,0.22)',
  borderRadius: '12px',
  background: 'rgba(239,246,255,0.72)',
  color: '#1e3a5f',
  padding: '0.8rem',
  fontSize: '0.82rem',
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
