'use client'
import React, { useState, useEffect, useRef, Suspense } from 'react'
import { useSearchParams, useRouter } from 'next/navigation'
import Card from '@/components/Card'
import StatusBadge from '@/components/StatusBadge'
import Link from 'next/link'

interface Span {
  span_id: string
  root_event_id: string
  parent_span_id?: string
  agent: string
  backend: string
  repo: string
  number: number
  event_kind: string
  invoked_by?: string
  dispatch_depth: number
  queue_wait_ms: number
  artifacts_count: number
  summary?: string
  started_at: string
  finished_at: string
  duration_ms: number
  status: string
  error?: string
}

function fmt(iso: string) {
  return new Date(iso).toLocaleString()
}

function GanttRow({ span, minMs, totalMs }: { span: Span; minMs: number; totalMs: number }) {
  const start = new Date(span.started_at).getTime()
  const leftPct = totalMs > 0 ? ((start - minMs) / totalMs) * 100 : 0
  const widthPct = totalMs > 0 ? Math.max(0.3, (span.duration_ms / totalMs) * 100) : 1
  const colors = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899']
  const color = span.status === 'error' ? '#ef4444' : colors[span.dispatch_depth % colors.length]

  return (
    <div style={{ display: 'flex', alignItems: 'center', padding: '4px 0', gap: '0.75rem', fontSize: '0.8rem', borderTop: '1px solid #f8fafc' }}>
      <div style={{ width: '180px', flexShrink: 0, paddingLeft: `${span.dispatch_depth * 12}px`, color: '#1e293b' }}>
        <div style={{ fontWeight: 600 }}>{span.agent}</div>
        <div style={{ fontSize: '0.7rem', color: '#64748b' }}>
          {span.repo}{span.number > 0 ? ` #${span.number}` : ''} · {span.event_kind}
        </div>
        {span.invoked_by && <div style={{ fontSize: '0.7rem', color: '#94a3b8' }}>← {span.invoked_by}</div>}
      </div>
      <div style={{ flex: 1, height: '18px', background: '#f8fafc', borderRadius: '3px', position: 'relative' }}>
        <div style={{ position: 'absolute', left: `${leftPct}%`, width: `${widthPct}%`, height: '100%', background: color, borderRadius: '3px', opacity: 0.8 }} />
      </div>
      <div style={{ width: '70px', flexShrink: 0, textAlign: 'right', color: '#64748b' }}>{span.duration_ms}ms</div>
      <div style={{ width: '70px', flexShrink: 0 }}><StatusBadge status={span.status} /></div>
    </div>
  )
}

function TraceDetail({ rootId, allSpans, onBack }: { rootId: string; allSpans: Span[]; onBack: () => void }) {
  const spans = allSpans.filter(s => s.root_event_id === rootId)
  const sorted = [...spans].sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime())
  const times = sorted.flatMap(s => [new Date(s.started_at).getTime(), new Date(s.finished_at).getTime()])
  const minMs = times.length ? Math.min(...times) : 0
  const maxMs = times.length ? Math.max(...times) : 0
  const totalMs = maxMs - minMs || 1
  const wallMs = sorted.length > 0 ? maxMs - minMs : 0

  return (
    <div>
      <div style={{ marginBottom: '1.5rem' }}>
        <button onClick={onBack} style={{ background: 'none', border: 'none', color: '#64748b', cursor: 'pointer', fontSize: '0.875rem', padding: 0 }}>← All traces</button>
        <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f', marginTop: '0.5rem' }}>Trace detail</h1>
        <p style={{ fontFamily: 'monospace', color: '#64748b', fontSize: '0.8rem', marginTop: '4px' }}>{rootId} · {sorted.length} span{sorted.length !== 1 ? 's' : ''} · {wallMs}ms total</p>
      </div>

      <Card title="Waterfall Timeline" style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', gap: '0.75rem', fontSize: '0.75rem', color: '#64748b', marginBottom: '4px' }}>
          <span style={{ width: '140px' }}>Agent</span>
          <span style={{ flex: 1 }}>Timeline</span>
          <span style={{ width: '70px', textAlign: 'right' }}>Duration</span>
          <span style={{ width: '70px' }}>Status</span>
        </div>
        {sorted.map(s => <GanttRow key={s.span_id} span={s} minMs={minMs} totalMs={totalMs} />)}
      </Card>

      <Card title="Span Details">
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8rem' }}>
          <thead>
            <tr style={{ color: '#64748b', borderBottom: '1px solid #bfdbfe' }}>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Agent</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Backend</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Repo / #</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Kind</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Started</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Duration</th>
              <th style={{ textAlign: 'left', padding: '6px 0' }}>Status</th>
            </tr>
          </thead>
          <tbody>
            {sorted.map(s => {
              const detail = s.summary || s.error
              return (
                <React.Fragment key={s.span_id}>
                  <tr style={{ borderTop: '1px solid #e2e8f0' }}>
                    <td style={{ padding: '6px 0', color: '#1e293b', paddingLeft: `${s.dispatch_depth * 12}px`, fontWeight: 600 }}>{s.agent}</td>
                    <td style={{ padding: '6px 0', color: '#64748b' }}>{s.backend}</td>
                    <td style={{ padding: '6px 0', color: '#64748b' }}>{s.repo}{s.number > 0 ? ` #${s.number}` : ''}</td>
                    <td style={{ padding: '6px 0', color: '#64748b' }}>{s.event_kind}</td>
                    <td style={{ padding: '6px 0', color: '#64748b' }}>{fmt(s.started_at)}</td>
                    <td style={{ padding: '6px 0', color: '#64748b' }}>{s.duration_ms}ms</td>
                    <td style={{ padding: '6px 0' }}><StatusBadge status={s.status} /></td>
                  </tr>
                  {detail && (
                    <tr>
                      <td colSpan={7} style={{ padding: '4px 0 8px', paddingLeft: `${s.dispatch_depth * 12 + 12}px` }}>
                        {s.summary && <div style={{ fontSize: '0.78rem', color: '#475569', fontStyle: 'italic' }}>{s.summary}</div>}
                        {s.error && <div style={{ fontSize: '0.78rem', color: '#b91c1c', marginTop: '2px' }}>{s.error}</div>}
                      </td>
                    </tr>
                  )}
                </React.Fragment>
              )
            })}
          </tbody>
        </table>
      </Card>
    </div>
  )
}

function TraceListItem({ rootId, spans, onSelect }: { rootId: string; spans: Span[]; onSelect: (id: string) => void }) {
  const sorted = [...spans].sort((a, b) => new Date(a.started_at).getTime() - new Date(b.started_at).getTime())
  const times = sorted.flatMap(s => [new Date(s.started_at).getTime(), new Date(s.finished_at).getTime()])
  const minMs = times.length ? Math.min(...times) : 0
  const maxMs = times.length ? Math.max(...times) : 0
  const totalMs = maxMs - minMs || 1
  const wallMs = maxMs - minMs
  const hasError = spans.some(s => s.status === 'error')

  return (
    <Card style={{ marginBottom: '1rem', cursor: 'pointer' }} >
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.75rem' }}>
        <div>
          <button
            onClick={() => onSelect(rootId)}
            style={{ background: 'none', border: 'none', fontFamily: 'monospace', fontSize: '0.8rem', color: '#2563eb', cursor: 'pointer', padding: 0 }}
          >
            {rootId}
          </button>
          <span style={{ color: '#64748b', fontSize: '0.8rem', marginLeft: '1rem' }}>
            {sorted[0]?.repo ?? ''} · {sorted[0]?.event_kind ?? ''} · {spans.length} span{spans.length !== 1 ? 's' : ''}
          </span>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          {hasError && <StatusBadge status="error" />}
          <span style={{ color: '#64748b', fontSize: '0.8rem' }}>{wallMs}ms</span>
        </div>
      </div>
      {sorted.map(s => {
        const start = new Date(s.started_at).getTime()
        const leftPct = ((start - minMs) / totalMs) * 100
        const widthPct = Math.max(0.3, (s.duration_ms / totalMs) * 100)
        const colors = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899']
        const color = s.status === 'error' ? '#ef4444' : colors[s.dispatch_depth % colors.length]
        return (
          <div key={s.span_id} style={{ display: 'flex', alignItems: 'center', padding: '2px 0', gap: '0.75rem', fontSize: '0.75rem' }}>
            <div style={{ width: '120px', flexShrink: 0, paddingLeft: `${s.dispatch_depth * 10}px`, color: '#64748b', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.agent}</div>
            <div style={{ flex: 1, height: '12px', background: '#f8fafc', borderRadius: '2px', position: 'relative' }}>
              <div style={{ position: 'absolute', left: `${leftPct}%`, width: `${widthPct}%`, height: '100%', background: color, borderRadius: '2px', opacity: 0.7 }} />
            </div>
            <div style={{ width: '60px', textAlign: 'right', color: '#94a3b8' }}>{s.duration_ms}ms</div>
          </div>
        )
      })}
    </Card>
  )
}

function TracesContent() {
  const searchParams = useSearchParams()
  const router = useRouter()
  const selectedId = searchParams.get('id')

  const [spans, setSpans] = useState<Span[]>([])
  const [filter, setFilter] = useState('')
  const [loading, setLoading] = useState(true)
  const [streaming, setStreaming] = useState(false)

  const load = () => {
    setLoading(true)
    fetch('/api/traces')
      .then(r => r.json())
      .then(data => { setSpans(data ?? []); setLoading(false) })
      .catch(() => setLoading(false))
  }

  useEffect(() => {
    load()
    const es = new EventSource('/api/traces/stream')
    setStreaming(true)
    es.onmessage = (e) => {
      try {
        const sp: Span = JSON.parse(e.data)
        setSpans(prev => [...prev.slice(-199), sp])
      } catch { /* ignore */ }
    }
    es.onerror = () => setStreaming(false)
    return () => es.close()
  }, [])

  const handleSelect = (id: string) => {
    router.push(`/traces/?id=${encodeURIComponent(id)}`)
  }

  if (selectedId) {
    return <TraceDetail rootId={selectedId} allSpans={spans} onBack={() => router.push('/traces/')} />
  }

  const grouped: Record<string, Span[]> = {}
  for (const s of spans) {
    if (filter && !s.agent.includes(filter) && !s.repo.includes(filter) && !s.root_event_id.includes(filter)) continue
    if (!grouped[s.root_event_id]) grouped[s.root_event_id] = []
    grouped[s.root_event_id].push(s)
  }
  const rootIds = Object.keys(grouped).sort((a, b) => {
    const aMax = Math.max(...grouped[a].map(s => new Date(s.finished_at).getTime()))
    const bMax = Math.max(...grouped[b].map(s => new Date(s.finished_at).getTime()))
    return bMax - aMax
  })

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f' }}>Traces</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {rootIds.length} trace{rootIds.length !== 1 ? 's' : ''} · {streaming ? '🟢 live' : '🔴 disconnected'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <input
            placeholder="Filter by agent, repo, or ID…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#1e293b', padding: '6px 10px', borderRadius: '6px', fontSize: '0.875rem', width: '240px' }}
          />
          <button onClick={load} style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#64748b', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {!loading && rootIds.length === 0 && <p style={{ color: '#64748b' }}>No traces yet.</p>}
      {rootIds.map(id => (
        <TraceListItem key={id} rootId={id} spans={grouped[id]} onSelect={handleSelect} />
      ))}
    </div>
  )
}

export default function TracesPage() {
  return (
    <Suspense fallback={<p style={{ color: '#64748b' }}>Loading…</p>}>
      <TracesContent />
    </Suspense>
  )
}
