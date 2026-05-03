'use client'
import { useState, useEffect } from 'react'
import Link from 'next/link'
import Card from '@/components/Card'
import RepoFilter, { useRepoFilter } from '@/components/RepoFilter'

interface Event {
  at: string
  id: string
  repo: string
  kind: string
  number: number
  actor: string
  payload?: Record<string, unknown>
  agents?: string[]
}

const kindStyle: Record<string, { bg: string; text: string; border: string }> = {
  'issues.labeled':     { bg: 'rgba(59,130,246,0.15)', text: '#60a5fa', border: '#1e3a5f' },
  'issues.opened':      { bg: 'rgba(59,130,246,0.15)', text: '#60a5fa', border: '#1e3a5f' },
  'issues.closed':      { bg: 'rgba(99,102,241,0.15)', text: '#a5b4fc', border: '#312e81' },
  'pull_request.labeled':       { bg: 'rgba(139,92,246,0.15)', text: '#c4b5fd', border: '#4c1d95' },
  'pull_request.opened':        { bg: 'rgba(139,92,246,0.15)', text: '#c4b5fd', border: '#4c1d95' },
  'pull_request.synchronize':   { bg: 'rgba(139,92,246,0.15)', text: '#c4b5fd', border: '#4c1d95' },
  'pull_request.closed':        { bg: 'rgba(217,70,239,0.15)', text: '#e9d5ff', border: '#701a75' },
  'issue_comment.created':      { bg: 'rgba(20,184,166,0.15)', text: '#5eead4', border: '#115e59' },
  'pull_request_review.submitted': { bg: 'rgba(20,184,166,0.15)', text: '#5eead4', border: '#115e59' },
  'agent.dispatch':     { bg: 'rgba(245,158,11,0.15)', text: '#fcd34d', border: '#78350f' },
  'push':               { bg: 'var(--success-bg)', text: 'var(--success)', border: 'var(--success-border)' },
}

const defaultKind = { bg: 'rgba(100,116,139,0.15)', text: 'var(--text-faint)', border: 'var(--border-subtle)' }

function EventRow({ event, isNew }: { event: Event; isNew: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const style = kindStyle[event.kind] ?? defaultKind
  const payloadStr = JSON.stringify(event.payload ?? {}, null, expanded ? 2 : undefined)

  return (
    <div style={{
      borderBottom: '1px solid var(--border-subtle)',
      background: isNew ? 'rgba(56,189,248,0.04)' : 'transparent',
      transition: 'background 0.5s',
    }}>
      <div style={{
        display: 'grid',
        gridTemplateColumns: '120px 180px 130px 50px 90px 160px 130px 1fr',
        gap: '0.5rem',
        padding: '8px 0',
        fontSize: '0.8rem',
        alignItems: 'center',
      }}>
        <span style={{ color: 'var(--text-faint)' }}>{new Date(event.at).toLocaleTimeString()}</span>
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
        <span style={{ color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{event.repo}</span>
        <span style={{ color: 'var(--text-faint)' }}>{event.number > 0 ? `#${event.number}` : '-'}</span>
        <span style={{ color: 'var(--text-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{event.actor}</span>
        <span style={{ display: 'flex', flexWrap: 'wrap', gap: '3px' }}>
          {(event.agents ?? []).map(a => (
            <Link key={a} href={`/?focus=${encodeURIComponent(a)}`} style={{
              background: 'rgba(56,189,248,0.12)',
              color: 'var(--accent)',
              border: '1px solid var(--accent)',
              padding: '1px 6px',
              borderRadius: '4px',
              fontSize: '0.68rem',
              textDecoration: 'none',
            }}>{a}</Link>
          ))}
          {(event.agents ?? []).length === 0 && <span style={{ color: 'var(--text-faint)' }}>, </span>}
        </span>
        <Link
          href={`/runners/?event=${encodeURIComponent(event.id)}`}
          style={{
            color: 'var(--accent)',
            border: '1px solid var(--accent)',
            padding: '2px 8px',
            borderRadius: '4px',
            fontSize: '0.72rem',
            textDecoration: 'none',
            width: 'fit-content',
          }}
          onClick={e => e.stopPropagation()}
          title="Open Runners filtered to this event"
        >View runners →</Link>
        <span
          onClick={() => setExpanded(!expanded)}
          style={{
            color: 'var(--text-faint)',
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
  const [repoFilter, setRepoFilter] = useRepoFilter()

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
    (!repoFilter || e.repo === repoFilter) &&
    (!filter || e.kind.includes(filter) || e.repo.includes(filter) || e.actor.includes(filter))
  )

  const buckets: Record<string, number> = {}
  const now = Date.now()
  for (const e of filtered) {
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Events</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {filtered.length} event{filtered.length !== 1 ? 's' : ''} · {streaming ? '🟢 live' : '🔴 disconnected'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          {Object.keys(timeRanges).map(r => (
            <button key={r} onClick={() => setTimeRange(r)} style={{
              background: timeRange === r ? 'var(--btn-primary-bg)' : 'var(--bg-card)',
              border: '1px solid var(--border)',
              color: timeRange === r ? '#ffffff' : 'var(--text-muted)',
              padding: '4px 8px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem',
            }}>{r}</button>
          ))}
          <RepoFilter selected={repoFilter} onChange={setRepoFilter} />
          <input
            placeholder="Filter..."
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '6px 10px', borderRadius: '6px', fontSize: '0.875rem', width: '180px' }}
          />
        </div>
      </div>

      <Card title="Event Timeline (5-minute buckets)" style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', alignItems: 'flex-end', gap: '3px', height: '60px' }}>
          {bucketKeys.map(k => (
            <div key={k} title={`${k} ago: ${buckets[k]} events`} style={{
              flex: 1, background: 'var(--accent)', opacity: 0.7,
              height: `${(buckets[k] / bucketMax) * 100}%`,
              minHeight: '2px', borderRadius: '2px 2px 0 0',
            }} />
          ))}
          {bucketKeys.length === 0 && <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem' }}>No events in window.</p>}
        </div>
        <div style={{ display: 'flex', justifyContent: 'space-between', fontSize: '0.7rem', color: 'var(--text-faint)', marginTop: '4px' }}>
          <span>← {timeRange} ago</span>
          <span>now →</span>
        </div>
      </Card>

      <Card title="Event Stream">
        <div style={{
          display: 'grid',
          gridTemplateColumns: '120px 180px 130px 50px 90px 160px 130px 1fr',
          gap: '0.5rem',
          padding: '4px 0',
          borderBottom: '2px solid var(--border)',
          fontSize: '0.75rem',
          color: 'var(--accent)',
          fontWeight: 600,
        }}>
          <span>Time</span><span>Kind</span><span>Repo</span><span>#</span><span>Actor</span><span>Agents</span><span>Runners</span><span>Payload</span>
        </div>

        {loading && <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>Loading...</p>}
        {!loading && filtered.length === 0 && <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>No events.</p>}

        <div style={{ maxHeight: '500px', overflowY: 'auto' }}>
          {filtered.map(e => <EventRow key={e.id + e.at} event={e} isNew={newIds.has(e.id)} />)}
        </div>
      </Card>
    </div>
  )
}
