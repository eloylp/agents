'use client'

import { useEffect, useState } from 'react'

const tokenStorageKey = 'agents_bearer_token'
const openAuthModalEvent = 'agents-auth-token-request'
const tokenChangedEvent = 'agents-auth-token-changed'

let fetchPatched = false

export function getStoredBearerToken(): string {
  if (typeof window === 'undefined') return ''
  return window.localStorage.getItem(tokenStorageKey) || ''
}

export function setStoredBearerToken(token: string) {
  if (typeof window === 'undefined') return
  const trimmed = token.trim()
  if (trimmed) {
    window.localStorage.setItem(tokenStorageKey, trimmed)
  } else {
    window.localStorage.removeItem(tokenStorageKey)
  }
  window.dispatchEvent(new Event(tokenChangedEvent))
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
    const token = sameOrigin ? getStoredBearerToken() : ''
    const headers = new Headers(init?.headers || (input instanceof Request ? input.headers : undefined))

    if (sameOrigin && token && !headers.has('Authorization')) {
      headers.set('Authorization', `Bearer ${token}`)
    }

    const response = await originalFetch(input, { ...init, headers })
    if (sameOrigin && response.status === 401) {
      requestBearerTokenModal()
    }
    return response
  }
}

export function AuthProvider({ children }: { children: React.ReactNode }) {
  const [open, setOpen] = useState(false)
  const [token, setToken] = useState('')

  useEffect(() => {
    patchFetch()
    setToken(getStoredBearerToken())

    const openModal = () => {
      setToken(getStoredBearerToken())
      setOpen(true)
    }
    const syncToken = () => setToken(getStoredBearerToken())

    window.addEventListener(openAuthModalEvent, openModal)
    window.addEventListener(tokenChangedEvent, syncToken)
    return () => {
      window.removeEventListener(openAuthModalEvent, openModal)
      window.removeEventListener(tokenChangedEvent, syncToken)
    }
  }, [])

  const save = () => {
    setStoredBearerToken(token)
    setOpen(false)
    window.location.reload()
  }

  const clear = () => {
    setStoredBearerToken('')
    setToken('')
  }

  return (
    <>
      {children}
      {open && (
        <div style={overlayStyle}>
          <div style={modalStyle}>
            <h2 style={{ color: 'var(--text-heading)', fontSize: '1rem', marginBottom: '0.5rem' }}>
              Agents bearer token
            </h2>
            <p style={{ color: 'var(--text-muted)', fontSize: '0.82rem', lineHeight: 1.5, marginBottom: '1rem' }}>
              This daemon requires a bearer token for API and MCP access. The UI stores it only in this browser.
            </p>
            <input
              autoFocus
              type="password"
              value={token}
              onChange={e => setToken(e.target.value)}
              placeholder="Paste bearer token"
              style={inputStyle}
            />
            <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end', marginTop: '1rem' }}>
              <button type="button" onClick={clear} style={secondaryButtonStyle}>Forget</button>
              <button type="button" onClick={() => setOpen(false)} style={secondaryButtonStyle}>Cancel</button>
              <button type="button" onClick={save} style={primaryButtonStyle}>Save token</button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}

export function AuthTokenSettings() {
  const [hasToken, setHasToken] = useState(false)

  useEffect(() => {
    const sync = () => setHasToken(getStoredBearerToken() !== '')
    sync()
    window.addEventListener(tokenChangedEvent, sync)
    return () => window.removeEventListener(tokenChangedEvent, sync)
  }, [])

  return (
    <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: '1rem', flexWrap: 'wrap' }}>
      <div>
        <h3 style={{ fontSize: '0.95rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }}>UI bearer token</h3>
        <p style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>
          Stored in this browser only. Server-side token hash is configured with AGENTS_AUTH_BEARER_TOKEN_HASH.
        </p>
      </div>
      <div style={{ display: 'flex', gap: '0.5rem' }}>
        <button type="button" onClick={requestBearerTokenModal} style={secondaryButtonStyle}>
          {hasToken ? 'Change token' : 'Set token'}
        </button>
        {hasToken && (
          <button type="button" onClick={() => setStoredBearerToken('')} style={secondaryButtonStyle}>
            Forget token
          </button>
        )}
      </div>
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
  borderRadius: '12px',
  boxShadow: '0 20px 60px rgba(0,0,0,0.22)',
  padding: '1.25rem',
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '0.65rem 0.75rem',
  border: '1px solid var(--border)',
  borderRadius: '8px',
  background: 'var(--bg-input)',
  color: 'var(--text)',
  fontFamily: 'inherit',
}

const secondaryButtonStyle: React.CSSProperties = {
  padding: '0.45rem 0.75rem',
  border: '1px solid var(--border)',
  borderRadius: '7px',
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
