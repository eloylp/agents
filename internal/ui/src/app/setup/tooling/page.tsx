'use client'

import Link from 'next/link'
import type { CSSProperties } from 'react'
import { useEffect, useMemo, useState } from 'react'
import Card from '@/components/Card'
import { buildSetupChecks, selectedBackends, setupComplete, type BackendsDiagnostics } from '@/lib/tooling-setup'

type BackendChoice = 'claude' | 'codex' | 'both'

export default function ToolingSetupPage() {
  const [choice, setChoice] = useState<BackendChoice>('both')
  const [diag, setDiag] = useState<BackendsDiagnostics | null>(null)
  const [loading, setLoading] = useState(true)
  const [discovering, setDiscovering] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    const stored = window.localStorage.getItem('agents_tooling_setup_backends') as BackendChoice | null
    if (stored === 'claude' || stored === 'codex' || stored === 'both') setChoice(stored)
    void loadDiagnostics()
  }, [])

  const selected = useMemo(() => selectedBackends(choice), [choice])
  const checks = useMemo(() => buildSetupChecks(diag, selected), [diag, selected])
  const complete = useMemo(() => setupComplete(diag, selected), [diag, selected])

  const updateChoice = (next: BackendChoice) => {
    setChoice(next)
    window.localStorage.setItem('agents_tooling_setup_backends', next)
  }

  async function loadDiagnostics() {
    setLoading(true)
    setError('')
    try {
      const res = await fetch('/backends/status', { cache: 'no-store' })
      if (!res.ok) throw new Error((await res.text()) || 'Could not load backend diagnostics.')
      setDiag(await res.json() as BackendsDiagnostics)
    } catch (e) {
      setDiag(null)
      setError(String(e))
    }
    setLoading(false)
  }

  async function refreshDiscovery() {
    setDiscovering(true)
    setError('')
    try {
      const res = await fetch('/backends/discover', { method: 'POST' })
      if (!res.ok) throw new Error((await res.text()) || 'Backend discovery failed.')
      await loadDiagnostics()
    } catch (e) {
      setError(String(e))
    }
    setDiscovering(false)
  }

  return (
    <div style={{ display: 'grid', gap: '1rem', maxWidth: '980px', margin: '0 auto' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem', flexWrap: 'wrap' }}>
        <div>
          <h1 style={{ fontSize: '1.35rem', color: 'var(--text-heading)', marginBottom: '0.35rem' }}>Tooling setup</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.86rem', maxWidth: '720px', lineHeight: 1.55 }}>
            Use the terminal companion for CLI-owned authentication, then re-check diagnostics here.
          </p>
        </div>
        <Link href="/graph/" style={buttonLinkStyle}>Enter graph</Link>
      </div>

      <Card style={{ display: 'grid', gap: '0.9rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: '1rem', flexWrap: 'wrap' }}>
          <div>
            <h2 style={sectionTitleStyle}>Backend tooling</h2>
            <p style={helpStyle}>Choose the CLIs this daemon should be ready to run.</p>
          </div>
          <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
            {(['claude', 'codex', 'both'] as BackendChoice[]).map(value => (
              <button
                key={value}
                type="button"
                onClick={() => updateChoice(value)}
                style={choice === value ? activeSegmentStyle : segmentStyle}
              >
                {value === 'both' ? 'Both' : value === 'claude' ? 'Claude' : 'Codex'}
              </button>
            ))}
          </div>
        </div>

        <code style={commandStyle}>docker compose exec -it agents agents-setup</code>

        <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
          <button type="button" onClick={loadDiagnostics} disabled={loading || discovering} style={primaryButtonStyle}>
            {loading ? 'Checking...' : 'Re-check'}
          </button>
          <button type="button" onClick={refreshDiscovery} disabled={loading || discovering} style={secondaryButtonStyle}>
            {discovering ? 'Refreshing...' : 'Refresh discovery'}
          </button>
          <Link href="/config/?tab=backends" style={subtleLinkStyle}>Backends config</Link>
        </div>
        {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      </Card>

      <Card style={{ display: 'grid', gap: '0.85rem' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: '1rem', flexWrap: 'wrap' }}>
          <div>
            <h2 style={sectionTitleStyle}>Checklist</h2>
            <p style={helpStyle}>{complete ? 'Selected tooling is healthy.' : 'Complete the missing items, then re-check diagnostics.'}</p>
          </div>
          <span style={complete ? successPillStyle : pendingPillStyle}>{complete ? 'Ready' : 'Setup needed'}</span>
        </div>

        <div style={{ display: 'grid', gap: '0.55rem' }}>
          {checks.map(check => (
            <div key={check.key} style={checkRowStyle}>
              <span aria-hidden style={check.ok ? okIconStyle : missingIconStyle}>{check.ok ? '✓' : '!'}</span>
              <div style={{ minWidth: 0 }}>
                <div style={{ fontWeight: 700, color: 'var(--text-heading)', fontSize: '0.86rem' }}>{check.label}</div>
                <div style={{ color: 'var(--text-muted)', fontSize: '0.78rem', lineHeight: 1.45 }}>{check.detail}</div>
              </div>
            </div>
          ))}
        </div>
      </Card>

      <Card style={{ display: 'grid', gap: '0.65rem' }}>
        <h2 style={sectionTitleStyle}>Next step</h2>
        <p style={helpStyle}>
          The wizard does not execute commands from the browser. Run the command in a terminal attached to the daemon container, then use discovery to refresh the persisted backend catalog.
        </p>
        <div>
          <Link href="/graph/" style={complete ? primaryLinkStyle : buttonLinkStyle}>
            {complete ? 'Open workflow designer' : 'Continue without blocking'}
          </Link>
        </div>
      </Card>
    </div>
  )
}

const sectionTitleStyle: CSSProperties = { fontSize: '0.98rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }
const helpStyle: CSSProperties = { color: 'var(--text-muted)', fontSize: '0.8rem', lineHeight: 1.5 }
const commandStyle: CSSProperties = { display: 'block', padding: '0.75rem', border: '1px solid var(--border)', borderRadius: '6px', background: 'var(--bg-input)', color: 'var(--text-heading)', overflowWrap: 'anywhere' }
const segmentStyle: CSSProperties = { border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text-muted)', borderRadius: '6px', padding: '0.45rem 0.7rem', cursor: 'pointer' }
const activeSegmentStyle: CSSProperties = { ...segmentStyle, background: 'var(--accent-bg)', color: 'var(--accent)', fontWeight: 700 }
const primaryButtonStyle: CSSProperties = { border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: 'white', borderRadius: '6px', padding: '0.48rem 0.75rem', cursor: 'pointer' }
const secondaryButtonStyle: CSSProperties = { border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--accent)', borderRadius: '6px', padding: '0.48rem 0.75rem', cursor: 'pointer' }
const buttonLinkStyle: CSSProperties = { ...secondaryButtonStyle, display: 'inline-flex', textDecoration: 'none' }
const primaryLinkStyle: CSSProperties = { ...primaryButtonStyle, display: 'inline-flex', textDecoration: 'none' }
const subtleLinkStyle: CSSProperties = { color: 'var(--accent)', alignSelf: 'center', fontSize: '0.82rem' }
const checkRowStyle: CSSProperties = { display: 'grid', gridTemplateColumns: '1.8rem minmax(0, 1fr)', gap: '0.65rem', alignItems: 'start', padding: '0.7rem', border: '1px solid var(--border-subtle)', borderRadius: '6px', background: 'var(--bg-input)' }
const okIconStyle: CSSProperties = { display: 'inline-flex', alignItems: 'center', justifyContent: 'center', width: '1.45rem', height: '1.45rem', borderRadius: '50%', background: 'var(--success-bg)', color: 'var(--success)', border: '1px solid var(--success-border)', fontWeight: 700 }
const missingIconStyle: CSSProperties = { ...okIconStyle, background: 'var(--bg-danger)', color: 'var(--text-danger)', border: '1px solid var(--border-danger)' }
const successPillStyle: CSSProperties = { border: '1px solid var(--success-border)', background: 'var(--success-bg)', color: 'var(--success)', borderRadius: '999px', padding: '0.3rem 0.65rem', fontSize: '0.76rem', fontWeight: 700 }
const pendingPillStyle: CSSProperties = { border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', borderRadius: '999px', padding: '0.3rem 0.65rem', fontSize: '0.76rem', fontWeight: 700 }
