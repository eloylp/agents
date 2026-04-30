'use client'
import { useEffect, useState } from 'react'
import Card from '@/components/Card'

interface QueueEvent {
  id: number
  kind: string
  repo: string
  number: number
  status: 'enqueued' | 'running' | 'completed'
  enqueued_at: string
  started_at?: string
  completed_at?: string
}

interface ListResponse {
  events: QueueEvent[]
  total: number
  limit: number
  offset: number
}

const statusStyle: Record<string, { bg: string; text: string; border: string }> = {
  enqueued:  { bg: 'rgba(59,130,246,0.15)',  text: '#60a5fa', border: '#1e3a5f' },
  running:   { bg: 'rgba(245,158,11,0.15)',  text: '#fcd34d', border: '#78350f' },
  completed: { bg: 'var(--success-bg)',       text: 'var(--success)', border: 'var(--success-border)' },
}

const POLL_MS = 2000

function fmtTime(s?: string) {
  if (!s) return '—'
  return new Date(s).toLocaleTimeString()
}

function fmtDuration(start?: string, end?: string) {
  if (!start || !end) return '—'
  const ms = new Date(end).getTime() - new Date(start).getTime()
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60_000).toFixed(1)}m`
}

export default function QueuePage() {
  const [events, setEvents] = useState<QueueEvent[]>([])
  const [total, setTotal] = useState(0)
  const [status, setStatus] = useState<'' | 'enqueued' | 'running' | 'completed'>('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [pendingId, setPendingId] = useState<number | null>(null)

  const load = async () => {
    try {
      const url = `/queue?limit=200${status ? `&status=${status}` : ''}`
      const res = await fetch(url, { cache: 'no-store' })
      if (!res.ok) throw new Error(`status ${res.status}`)
      const data: ListResponse = await res.json()
      setEvents(data.events ?? [])
      setTotal(data.total)
      setError(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    load()
    const id = window.setInterval(load, POLL_MS)
    return () => window.clearInterval(id)
  }, [status]) // eslint-disable-line react-hooks/exhaustive-deps

  const onDelete = async (id: number) => {
    if (!confirm(`Delete event #${id}? The row will be removed from the table; a worker that already received it from the in-memory channel may still run it.`)) return
    setPendingId(id)
    try {
      const res = await fetch(`/queue/${id}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 404) {
        const body = await res.text()
        alert(`Delete failed: ${body || res.status}`)
      }
      await load()
    } finally {
      setPendingId(null)
    }
  }

  const onRetry = async (id: number) => {
    setPendingId(id)
    try {
      const res = await fetch(`/queue/${id}/retry`, { method: 'POST' })
      if (!res.ok) {
        const body = await res.text()
        alert(`Retry failed: ${body || res.status}`)
      }
      await load()
    } finally {
      setPendingId(null)
    }
  }

  const counts = events.reduce<Record<string, number>>((m, e) => {
    m[e.status] = (m[e.status] ?? 0) + 1
    return m
  }, {})

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Queue</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {total} row{total !== 1 ? 's' : ''} matching filter · enqueued {counts.enqueued ?? 0} · running {counts.running ?? 0} · completed {counts.completed ?? 0}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          {(['', 'enqueued', 'running', 'completed'] as const).map(s => (
            <button key={s || 'all'} onClick={() => setStatus(s)} style={{
              background: status === s ? 'var(--btn-primary-bg)' : 'var(--bg-card)',
              border: '1px solid var(--border)',
              color: status === s ? '#ffffff' : 'var(--text-muted)',
              padding: '4px 10px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem',
            }}>{s || 'All'}</button>
          ))}
        </div>
      </div>

      {error && (
        <Card style={{ marginBottom: '1rem', borderColor: 'var(--border-danger)' }}>
          <span style={{ color: 'var(--text-danger)', fontSize: '0.85rem' }}>Error loading queue: {error}</span>
        </Card>
      )}

      <Card title="Event Queue">
        <div style={{
          display: 'grid',
          gridTemplateColumns: '60px 100px 200px 160px 60px 110px 90px 90px 160px',
          gap: '0.5rem',
          padding: '4px 0',
          borderBottom: '2px solid var(--border)',
          fontSize: '0.75rem',
          color: 'var(--accent)',
          fontWeight: 600,
        }}>
          <span>ID</span>
          <span>Status</span>
          <span>Kind</span>
          <span>Repo</span>
          <span>#</span>
          <span>Enqueued</span>
          <span>Started</span>
          <span>Duration</span>
          <span>Actions</span>
        </div>

        {loading && events.length === 0 && <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>Loading...</p>}
        {!loading && events.length === 0 && <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>No events.</p>}

        <div style={{ maxHeight: '600px', overflowY: 'auto' }}>
          {events.map(ev => {
            const sStyle = statusStyle[ev.status]
            const busy = pendingId === ev.id
            return (
              <div key={ev.id} style={{
                display: 'grid',
                gridTemplateColumns: '60px 100px 200px 160px 60px 110px 90px 90px 160px',
                gap: '0.5rem',
                padding: '8px 0',
                fontSize: '0.8rem',
                alignItems: 'center',
                borderBottom: '1px solid var(--border-subtle)',
                opacity: busy ? 0.5 : 1,
              }}>
                <span style={{ color: 'var(--text-faint)', fontFamily: 'monospace' }}>{ev.id}</span>
                <span style={{
                  background: sStyle.bg,
                  color: sStyle.text,
                  border: `1px solid ${sStyle.border}`,
                  padding: '2px 8px',
                  borderRadius: '4px',
                  fontSize: '0.72rem',
                  fontWeight: 600,
                  display: 'inline-block',
                  width: 'fit-content',
                }}>{ev.status}</span>
                <span style={{ color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{ev.kind || '—'}</span>
                <span style={{ color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{ev.repo || '—'}</span>
                <span style={{ color: 'var(--text-faint)' }}>{ev.number > 0 ? `#${ev.number}` : '—'}</span>
                <span style={{ color: 'var(--text-faint)' }} title={ev.enqueued_at}>{fmtTime(ev.enqueued_at)}</span>
                <span style={{ color: 'var(--text-faint)' }} title={ev.started_at}>{fmtTime(ev.started_at)}</span>
                <span style={{ color: 'var(--text-faint)' }}>{fmtDuration(ev.started_at, ev.completed_at)}</span>
                <span style={{ display: 'flex', gap: '0.4rem' }}>
                  <button
                    disabled={busy || ev.status === 'running'}
                    onClick={() => onRetry(ev.id)}
                    title={ev.status === 'running' ? 'Cannot retry a running event' : 'Re-enqueue this event as a fresh row'}
                    style={{
                      background: 'var(--bg-card)',
                      border: '1px solid var(--border)',
                      color: ev.status === 'running' ? 'var(--text-faint)' : 'var(--text-muted)',
                      padding: '2px 8px',
                      borderRadius: '4px',
                      cursor: ev.status === 'running' || busy ? 'not-allowed' : 'pointer',
                      fontSize: '0.72rem',
                    }}
                  >Retry</button>
                  <button
                    disabled={busy}
                    onClick={() => onDelete(ev.id)}
                    style={{
                      background: 'var(--bg-card)',
                      border: '1px solid var(--border-danger)',
                      color: 'var(--text-danger)',
                      padding: '2px 8px',
                      borderRadius: '4px',
                      cursor: busy ? 'not-allowed' : 'pointer',
                      fontSize: '0.72rem',
                    }}
                  >Delete</button>
                </span>
              </div>
            )
          })}
        </div>
      </Card>
    </div>
  )
}
