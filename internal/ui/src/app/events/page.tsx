'use client'
import { useState, useEffect } from 'react'
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

const kindStyle: Record<string, { bg: string; text: string; border: string }> = {
  'issues.labeled':     { bg: '#dbeafe', text: '#1e40af', border: '#93c5fd' },
  'issues.opened':      { bg: '#dbeafe', text: '#1e40af', border: '#93c5fd' },
  'issues.closed':      { bg: '#e0e7ff', text: '#3730a3', border: '#a5b4fc' },
  'pull_request.labeled':       { bg: '#ede9fe', text: '#5b21b6', border: '#c4b5fd' },
  'pull_request.opened':        { bg: '#ede9fe', text: '#5b21b6', border: '#c4b5fd' },
  'pull_request.synchronize':   { bg: '#ede9fe', text: '#5b21b6', border: '#c4b5fd' },
  'pull_request.closed':        { bg: '#fae8ff', text: '#86198f', border: '#e9d5ff' },
  'issue_comment.created':      { bg: '#ccfbf1', text: '#115e59', border: '#99f6e4' },
  'pull_request_review.submitted': { bg: '#ccfbf1', text: '#115e59', border: '#99f6e4' },
  'agent.dispatch':     { bg: '#fef3c7', text: '#92400e', border: '#fde68a' },
  'push':               { bg: '#dcfce7', text: '#166534', border: '#bbf7d0' },
}

const defaultKind = { bg: '#f1f5f9', text: '#475569', border: '#e2e8f0' }

function EventRow({ event, isNew }: { event: Event; isNew: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const style = kindStyle[event.kind] ?? defaultKind
  const payloadStr = JSON.stringify(event.payload ?? {}, null, expanded ? 2 : undefined)

  return (
    <div style={{
      borderBottom: '1px solid #e2e8f0',
      background: isNew ? 'rgba(37,99,235,0.04)' : 'transparent',
      transition: 'background 0.5s',
    }}>
      <div style={{
        display: 'grid',
        gridTemplateColumns: '140px 200px 140px 60px 100px 1fr',
        gap: '0.5rem',
        padding: '8px 0',
        fontSize: '0.8rem',
        alignItems: 'center',
      }}>
        <span style={{ color: '#475569' }}>{new Date(event.at).toLocaleTimeString()}</span>
        <span style={{
          background: style.bg,
          color: style.text,
          border: `1px solid ${style.border}`,
          padding: '2px 8px',
          borderRadius: '4px',
          fontSize: '0.72rem',
          fontWeight: 600,
          display: 'inline-block',
          width: 'fit-content',
        }}>
          {event.kind}
        </span>
        <span style={{ color: '#475569', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{event.repo}</span>
        <span style={{ color: '#475569' }}>{event.number > 0 ? `#${event.number}` : '—'}</span>
        <span style={{ color: '#64748b' }}>{event.actor}</span>
        <span
          onClick={() => setExpanded(!expanded)}
          style={{
            color: '#475569',
            fontFamily: 'monospace',
            fontSize: '0.72rem',
            cursor: 'pointer',
            overflow: expanded ? 'visible' : 'hidden',
            textOverflow: expanded ? 'unset' : 'ellipsis',
            whiteSpace: expanded ? 'pre-wrap' : 'nowrap',
          }}
          title="Click to expand/collapse"
        >
          {payloadStr}
        </span>
      </div>
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
    fetch(`/events?since=${encodeURIComponent(since)}`)
      .then(r => r.json())
      .then(data => { setEvents((data ?? []).reverse()); setLoading(false) })
      .catch(() => setLoading(false))
  }

  useEffect(() => { load() }, [timeRange]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const es = new EventSource('/events/stream')
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f' }}>Events</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {filtered.length} event{filtered.length !== 1 ? 's' : ''} · {streaming ? '🟢 live' : '🔴 disconnected'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          {Object.keys(timeRanges).map(r => (
            <button key={r} onClick={() => setTimeRange(r)} style={{
              background: timeRange === r ? '#2563eb' : '#ffffff',
              border: '1px solid #bfdbfe',
              color: timeRange === r ? '#ffffff' : '#64748b',
              padding: '4px 8px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem',
            }}>{r}</button>
          ))}
          <input
            placeholder="Filter..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#1e293b', padding: '6px 10px', borderRadius: '6px', fontSize: '0.875rem', width: '180px' }}
          />
        </div>
      </div>

      <Card title="Event Timeline (5-minute buckets)" style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: '3px', height: '60px' }}>
          {bucketKeys.map(k => (
            <div key={k} title={`${k} ago: ${buckets[k]} events`} style={{
              flex: 1, background: '#3b82f6', opacity: 0.7,
              height: `${(buckets[k] / bucketMax) * 100}%`,
              minHeight: '2px', borderRadius: '2px 2px 0 0',
            }} />
          ))}
          {bucketKeys.length === 0 && <p style={{ color: '#94a3b8', fontSize: '0.8rem' }}>No events in window.</p>}
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.7rem', color: '#94a3b8', marginTop: '4px' }}>
          <span>← {timeRange} ago</span>
          <span>now →</span>
        </div>
      </Card>

      <Card title="Event Stream">
        <div style={{
          display: 'grid',
          gridTemplateColumns: '140px 200px 140px 60px 100px 1fr',
          gap: '0.5rem',
          padding: '4px 0',
          borderBottom: '2px solid #bfdbfe',
          fontSize: '0.75rem',
          color: '#2563eb',
          fontWeight: 600,
        }}>
          <span>Time</span><span>Kind</span><span>Repo</span><span>#</span><span>Actor</span><span>Payload (click to expand)</span>
        </div>

        {loading && <p style={{ color: '#64748b', padding: '0.5rem 0' }}>Loading...</p>}
        {!loading && filtered.length === 0 && <p style={{ color: '#64748b', padding: '0.5rem 0' }}>No events.</p>}

        <div style={{ maxHeight: '500px', overflowY: 'auto' }}>
          {filtered.map(e => <EventRow key={e.id + e.at} event={e} isNew={newIds.has(e.id)} />)}
        </div>
      </Card>
    </div>
  )
}
