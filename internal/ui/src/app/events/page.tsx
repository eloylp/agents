'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'

interface Event {
  at: string
  id: string
  repo: string
  kind: string
  number: number
  actor: string
  payload?: Record<string, unknown>
}

const kindColor: Record<string, string> = {
  'issues.labeled': '#1d4ed8',
  'issues.opened': '#1d4ed8',
  'pull_request.labeled': '#7c3aed',
  'pull_request.opened': '#7c3aed',
  'pull_request.synchronize': '#7c3aed',
  'issue_comment.created': '#0f766e',
  'pull_request_review.submitted': '#0f766e',
  'agent.dispatch': '#92400e',
  'push': '#166534',
}

function EventRow({ event, isNew }: { event: Event; isNew: boolean }) {
  const color = kindColor[event.kind] ?? '#1e293b'
  return (
    <div style={{
      display: 'grid',
      gridTemplateColumns: '150px 180px 120px 60px 100px 1fr',
      gap: '0.5rem',
      padding: '8px 0',
      borderBottom: '1px solid #1e293b',
      fontSize: '0.8rem',
      background: isNew ? 'rgba(96,165,250,0.05)' : 'transparent',
      transition: 'background 0.5s',
      alignItems: 'center',
    }}>
      <span style={{ color: '#64748b' }}>{new Date(event.at).toLocaleTimeString()}</span>
      <span style={{
        background: color,
        color: '#e2e8f0',
        padding: '2px 6px',
        borderRadius: '4px',
        fontSize: '0.72rem',
        display: 'inline-block',
      }}>
        {event.kind}
      </span>
      <span style={{ color: '#94a3b8', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{event.repo}</span>
      <span style={{ color: '#64748b' }}>{event.number > 0 ? `#${event.number}` : '—'}</span>
      <span style={{ color: '#475569' }}>{event.actor}</span>
      <span style={{ color: '#334155', fontFamily: 'monospace', fontSize: '0.7rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
        {JSON.stringify(event.payload ?? {})}
      </span>
    </div>
  )
}

export default function EventsPage() {
  const [events, setEvents] = useState<Event[]>([])
  const [newIds, setNewIds] = useState<Set<string>>(new Set())
  const [streaming, setStreaming] = useState(false)
  const [loading, setLoading] = useState(true)
  const [filter, setFilter] = useState('')
  const [timeRange, setTimeRange] = useState('1h')

  const timeRanges: Record<string, number> = { '15m': 15 * 60, '1h': 3600, '6h': 6 * 3600, '24h': 24 * 3600 }

  const load = () => {
    setLoading(true)
    const sinceMs = Date.now() - (timeRanges[timeRange] ?? 3600) * 1000
    const since = new Date(sinceMs).toISOString()
    fetch(`/api/events?since=${encodeURIComponent(since)}`)
      .then(r => r.json())
      .then(data => { setEvents((data ?? []).reverse()); setLoading(false) })
      .catch(() => setLoading(false))
  }

  useEffect(() => { load() }, [timeRange]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const es = new EventSource('/api/events/stream')
    setStreaming(true)
    es.onmessage = (e) => {
      try {
        const ev: Event = JSON.parse(e.data)
        setEvents(prev => [ev, ...prev.slice(0, 499)])
        setNewIds(prev => { const s = new Set(prev); s.add(ev.id); return s })
        setTimeout(() => setNewIds(prev => { const s = new Set(prev); s.delete(ev.id); return s }), 2000)
      } catch { /* ignore */ }
    }
    es.onerror = () => setStreaming(false)
    return () => es.close()
  }, [])

  const filtered = events.filter(e =>
    !filter || e.kind.includes(filter) || e.repo.includes(filter) || e.actor.includes(filter)
  )

  // Timeline: bucket events into 5-minute windows for the bar chart
  const buckets: Record<string, number> = {}
  const now = Date.now()
  for (const e of events) {
    const bucket = Math.floor((now - new Date(e.at).getTime()) / (5 * 60 * 1000))
    const label = `${bucket * 5}m`
    buckets[label] = (buckets[label] ?? 0) + 1
  }
  const bucketMax = Math.max(...Object.values(buckets), 1)
  const bucketKeys = Object.keys(buckets).sort((a, b) => parseInt(b) - parseInt(a)).slice(0, 24)

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#f1f5f9' }}>Events</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {filtered.length} event{filtered.length !== 1 ? 's' : ''} · {streaming ? '🟢 live' : '🔴 disconnected'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          {Object.keys(timeRanges).map(r => (
            <button key={r} onClick={() => setTimeRange(r)} style={{
              background: timeRange === r ? '#1d4ed8' : '#1e293b',
              border: '1px solid #334155',
              color: timeRange === r ? '#bfdbfe' : '#64748b',
              padding: '4px 8px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem',
            }}>{r}</button>
          ))}
          <input
            placeholder="Filter…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ background: '#1e293b', border: '1px solid #334155', color: '#e2e8f0', padding: '6px 10px', borderRadius: '6px', fontSize: '0.875rem', width: '180px' }}
          />
        </div>
      </div>

      {/* Timeline bar chart */}
      <Card title="Event Timeline (5-minute buckets)" style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: '3px', height: '60px' }}>
          {bucketKeys.map(k => (
            <div key={k} title={`${k} ago: ${buckets[k]} events`} style={{
              flex: 1, background: '#3b82f6', opacity: 0.7,
              height: `${(buckets[k] / bucketMax) * 100}%`,
              minHeight: '2px', borderRadius: '2px 2px 0 0',
            }} />
          ))}
          {bucketKeys.length === 0 && <p style={{ color: '#475569', fontSize: '0.8rem' }}>No events in window.</p>}
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.7rem', color: '#475569', marginTop: '4px' }}>
          <span>← {timeRange} ago</span>
          <span>now →</span>
        </div>
      </Card>

      {/* Live stream table */}
      <Card title="Event Stream">
        <div style={{
          display: 'grid',
          gridTemplateColumns: '150px 180px 120px 60px 100px 1fr',
          gap: '0.5rem',
          padding: '4px 0',
          borderBottom: '1px solid #334155',
          fontSize: '0.75rem',
          color: '#64748b',
          fontWeight: 600,
        }}>
          <span>Time</span><span>Kind</span><span>Repo</span><span>Num</span><span>Actor</span><span>Payload</span>
        </div>

        {loading && <p style={{ color: '#64748b', padding: '0.5rem 0' }}>Loading…</p>}
        {!loading && filtered.length === 0 && <p style={{ color: '#64748b', padding: '0.5rem 0' }}>No events.</p>}

        <div style={{ maxHeight: '500px', overflowY: 'auto' }}>
          {filtered.map(e => <EventRow key={e.id + e.at} event={e} isNew={newIds.has(e.id)} />)}
        </div>
      </Card>
    </div>
  )
}
