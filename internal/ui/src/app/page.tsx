'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import StatusBadge from '@/components/StatusBadge'
import Link from 'next/link'

interface Binding {
  repo: string
  labels?: string[]
  events?: string[]
  cron?: string
  enabled: boolean
  schedule?: {
    last_run?: string
    next_run: string
    last_status?: string
  }
}

interface Agent {
  name: string
  backend: string
  skills?: string[]
  description?: string
  allow_dispatch: boolean
  can_dispatch?: string[]
  allow_prs: boolean
  current_status: string  // "running" | "idle" — live runtime state from /api/agents
  bindings?: Binding[]
}

function fmt(iso?: string) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

function AgentCard({ agent }: { agent: Agent }) {
  // Use live runtime status from the API; fall back to last cron outcome for error/success colouring.
  const scheduleStatuses = agent.bindings?.flatMap(b => b.schedule?.last_status ? [b.schedule.last_status] : []) ?? []
  const lastOutcome = scheduleStatuses.includes('error') ? 'error' : scheduleStatuses.includes('success') ? 'success' : 'idle'
  const currentStatus = agent.current_status === 'running' ? 'running' : lastOutcome

  return (
    <Card style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
        <div>
          <div style={{ fontWeight: 700, fontSize: '1rem', color: '#f1f5f9' }}>{agent.name}</div>
          {agent.description && <div style={{ fontSize: '0.8rem', color: '#64748b', marginTop: '2px' }}>{agent.description}</div>}
        </div>
        <StatusBadge status={currentStatus} />
      </div>

      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
        <span style={{ background: '#0f172a', border: '1px solid #334155', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: '#94a3b8' }}>
          {agent.backend}
        </span>
        {agent.skills?.map(s => (
          <span key={s} style={{ background: '#1e3a5f', border: '1px solid #1d4ed8', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: '#93c5fd' }}>
            {s}
          </span>
        ))}
      </div>

      {(agent.bindings ?? []).length > 0 && (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8rem' }}>
          <thead>
            <tr style={{ color: '#64748b' }}>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Repo</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Trigger</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Last run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Next run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Status</th>
            </tr>
          </thead>
          <tbody>
            {(agent.bindings ?? []).map((b, i) => (
              <tr key={i} style={{ borderTop: '1px solid #0f172a' }}>
                <td style={{ padding: '4px 0', color: '#94a3b8' }}>{b.repo}</td>
                <td style={{ padding: '4px 0', color: '#94a3b8' }}>
                  {b.cron ? `cron: ${b.cron}` : b.labels?.join(', ') ?? b.events?.join(', ') ?? '—'}
                </td>
                <td style={{ padding: '4px 0', color: '#64748b' }}>{fmt(b.schedule?.last_run)}</td>
                <td style={{ padding: '4px 0', color: '#64748b' }}>{b.schedule ? fmt(b.schedule.next_run) : '—'}</td>
                <td style={{ padding: '4px 0' }}>
                  {b.schedule?.last_status ? <StatusBadge status={b.schedule.last_status} /> : <span style={{ color: '#475569' }}>—</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div style={{ display: 'flex', gap: '1rem', fontSize: '0.75rem', color: '#475569', borderTop: '1px solid #334155', paddingTop: '0.5rem' }}>
        {agent.allow_prs && <span>✓ PRs</span>}
        {agent.allow_dispatch && <span>✓ dispatch</span>}
        {(agent.can_dispatch ?? []).length > 0 && <span>→ {agent.can_dispatch!.join(', ')}</span>}
      </div>
    </Card>
  )
}

export default function FleetPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/api/agents')
      .then(r => r.json())
      .then(data => { setAgents(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => { load() }, [])

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#f1f5f9' }}>Fleet Dashboard</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {agents.length} agent{agents.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          <Link href="/traces/" style={{ fontSize: '0.875rem', color: '#60a5fa' }}>View traces →</Link>
          <button
            onClick={load}
            style={{ background: '#1e293b', border: '1px solid #334155', color: '#94a3b8', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {error && <p style={{ color: '#f87171' }}>Error: {error}</p>}
      {!loading && !error && agents.length === 0 && (
        <p style={{ color: '#64748b' }}>No agents configured.</p>
      )}

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(420px, 1fr))', gap: '1rem' }}>
        {agents.map(a => <AgentCard key={a.name} agent={a} />)}
      </div>
    </div>
  )
}
