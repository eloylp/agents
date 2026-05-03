'use client'
import React, { useState, useEffect, useRef, Suspense } from 'react'
import { useSearchParams, useRouter } from 'next/navigation'
import Card from '@/components/Card'
import StatusBadge from '@/components/StatusBadge'
import Link from 'next/link'
import RepoFilter, { useRepoFilter } from '@/components/RepoFilter'
import { StreamCard, stepToCardEntries, type PersistedStep } from '@/components/StreamCard'
import { fmtDuration } from '@/lib/format'

type TraceStep = PersistedStep

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
  prompt_size?: number
  input_tokens?: number
  output_tokens?: number
  cache_read_tokens?: number
  cache_write_tokens?: number
}

function fmtTokens(n?: number) {
  if (!n) return '0'
  if (n < 1000) return String(n)
  if (n < 1_000_000) return `${(n / 1000).toFixed(1)}K`
  return `${(n / 1_000_000).toFixed(2)}M`
}

function fmtBytes(n?: number) {
  if (!n) return '0'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(2)} MB`
}

// PromptPanel lazy-fetches /traces/{span_id}/prompt and shows the
// composed prompt the daemon sent to the AI CLI on this run. Hidden
// behind an accordion so the trace listing stays cheap; expanded only
// when the operator clicks. 404 → "no prompt recorded" (pre-009 spans
// or runs that didn't capture one).
function PromptPanel({ span }: { span: Span }) {
  const [open, setOpen] = useState(false)
  const [text, setText] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!span.prompt_size) {
    return null
  }

  const toggle = () => {
    if (!open && text === null) {
      setLoading(true)
      fetch(`/traces/${encodeURIComponent(span.span_id)}/prompt`)
        .then(async r => {
          if (r.status === 404) { setText(''); return }
          if (!r.ok) throw new Error(`status ${r.status}`)
          setText(await r.text())
        })
        .catch(e => setError((e as Error).message))
        .finally(() => setLoading(false))
    }
    setOpen(!open)
  }

  return (
    <div style={{ marginTop: '0.75rem', borderTop: '1px solid var(--border-subtle)', paddingTop: '0.5rem' }}>
      <button onClick={toggle} style={{
        background: 'none', border: 'none', color: 'var(--accent)',
        fontSize: '0.78rem', cursor: 'pointer', padding: 0,
        textAlign: 'left',
      }}>
        {open ? '▼' : '▶'} Prompt ({fmtBytes(span.prompt_size)})
      </button>
      {open && (
        <div style={{ marginTop: '0.5rem' }}>
          {loading && <span style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>Loading...</span>}
          {error && <span style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>Error: {error}</span>}
          {text === '' && <span style={{ color: 'var(--text-faint)', fontSize: '0.8rem' }}>No prompt recorded.</span>}
          {text && text !== '' && (
            <pre style={{
              background: 'var(--bg-input)',
              border: '1px solid var(--border-subtle)',
              borderRadius: '4px',
              padding: '0.75rem',
              fontSize: '0.72rem',
              fontFamily: 'monospace',
              color: 'var(--text)',
              whiteSpace: 'pre-wrap',
              wordBreak: 'break-word',
              maxHeight: '500px',
              overflowY: 'auto',
              margin: 0,
            }}>{text}</pre>
          )}
        </div>
      )}
    </div>
  )
}

// TokenUsagePanel renders the four-field token breakdown when present.
// Hidden when no tokens were recorded (pre-009 spans or non-CLI backends).
function TokenUsagePanel({ span }: { span: Span }) {
  const inT = span.input_tokens ?? 0
  const outT = span.output_tokens ?? 0
  const cacheR = span.cache_read_tokens ?? 0
  const cacheW = span.cache_write_tokens ?? 0
  if (inT + outT + cacheR + cacheW === 0) return null
  const total = inT + outT
  const cacheHitRatio = cacheR + cacheW > 0 ? cacheR / (cacheR + cacheW + inT) : 0
  return (
    <div style={{
      marginTop: '0.5rem',
      display: 'flex',
      gap: '0.75rem',
      fontSize: '0.78rem',
      color: 'var(--text-muted)',
      flexWrap: 'wrap',
    }}>
      <span><strong style={{ color: 'var(--text)' }}>Tokens:</strong></span>
      <span>in <strong style={{ color: 'var(--text)' }}>{fmtTokens(inT)}</strong></span>
      <span>out <strong style={{ color: 'var(--text)' }}>{fmtTokens(outT)}</strong></span>
      {cacheR > 0 && <span>cache hit <strong style={{ color: 'var(--success)' }}>{fmtTokens(cacheR)}</strong> ({(cacheHitRatio * 100).toFixed(0)}%)</span>}
      {cacheW > 0 && <span>cache write <strong style={{ color: 'var(--text)' }}>{fmtTokens(cacheW)}</strong></span>}
      <span style={{ color: 'var(--text-faint)' }}>· total {fmtTokens(total)}</span>
    </div>
  )
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
    <div style={{ display: 'flex', alignItems: 'center', padding: '4px 0', gap: '0.75rem', fontSize: '0.8rem', borderTop: '1px solid var(--border-subtle)' }}>
      <div style={{ width: '180px', flexShrink: 0, paddingLeft: `${span.dispatch_depth * 12}px`, color: 'var(--text)' }}>
        <div style={{ fontWeight: 600 }}>{span.agent}</div>
        <div style={{ fontSize: '0.7rem', color: 'var(--text-muted)' }}>
          {span.repo}{span.number > 0 ? ` #${span.number}` : ''} · {span.event_kind}
        </div>
        {span.invoked_by && <div style={{ fontSize: '0.7rem', color: 'var(--text-faint)' }}>← {span.invoked_by}</div>}
      </div>
      <div style={{ flex: 1, height: '18px', background: 'var(--bg)', borderRadius: '3px', position: 'relative' }}>
        <div style={{ position: 'absolute', left: `${leftPct}%`, width: `${widthPct}%`, height: '100%', background: color, borderRadius: '3px', opacity: 0.8 }} />
      </div>
      <div style={{ width: '70px', flexShrink: 0, textAlign: 'right', color: 'var(--text-muted)' }}>{fmtDuration(span.duration_ms)}</div>
      <div style={{ width: '70px', flexShrink: 0 }}><StatusBadge status={span.status} /></div>
    </div>
  )
}

// SpanTranscript renders the expandable tool-loop transcript for a single span.
// Steps are loaded lazily when the accordion is first opened.
function SpanTranscript({ spanId, stepCount }: { spanId: string; stepCount?: number }) {
  const [open, setOpen] = useState(false)
  const [steps, setSteps] = useState<TraceStep[] | null>(null)
  const [loading, setLoading] = useState(false)

  const toggle = () => {
    if (!open && steps === null) {
      setLoading(true)
      fetch(`/traces/${encodeURIComponent(spanId)}/steps`)
        .then(r => r.json())
        .then((data: TraceStep[]) => { setSteps(data ?? []); setLoading(false) })
        .catch(() => { setSteps([]); setLoading(false) })
    }
    setOpen(o => !o)
  }

  const label = stepCount != null && stepCount > 0
    ? `${stepCount} steps`
    : 'transcript'

  return (
    <div style={{ marginTop: '4px' }}>
      <button
        onClick={toggle}
        style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', fontSize: '0.75rem', padding: 0, display: 'flex', alignItems: 'center', gap: '4px' }}
      >
        <span style={{ fontFamily: 'monospace' }}>{open ? '▼' : '▶'}</span>
        <span>{label}</span>
      </button>
      {open && (
        <div style={{ marginTop: '6px', paddingLeft: '12px', borderLeft: '2px solid var(--border-subtle)' }}>
          {loading && <p style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>Loading…</p>}
          {!loading && steps !== null && steps.length === 0 && (
            <p style={{ color: 'var(--text-faint)', fontSize: '0.75rem', fontStyle: 'italic' }}>No transcript recorded for this span.</p>
          )}
          {!loading && steps !== null && steps.flatMap((step, i) =>
            stepToCardEntries(step, i * 2).map((entry, j) => (
              <StreamCard key={`${i}-${j}`} entry={entry} />
            ))
          )}
        </div>
      )}
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
        <button onClick={onBack} style={{ background: 'none', border: 'none', color: 'var(--text-muted)', cursor: 'pointer', fontSize: '0.875rem', padding: 0 }}>← All traces</button>
        <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)', marginTop: '0.5rem' }}>Trace detail</h1>
        <p style={{ fontFamily: 'monospace', color: 'var(--text-muted)', fontSize: '0.8rem', marginTop: '4px' }}>{rootId} · {sorted.length} span{sorted.length !== 1 ? 's' : ''} · {wallMs}ms total</p>
      </div>

      <Card title="Waterfall Timeline" style={{ marginBottom: '1rem' }}>
        <div style={{ display: 'flex', gap: '0.75rem', fontSize: '0.75rem', color: 'var(--text-muted)', marginBottom: '4px' }}>
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
            <tr style={{ color: 'var(--text-muted)', borderBottom: '1px solid var(--border)' }}>
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
                  <tr style={{ borderTop: '1px solid var(--border-subtle)' }}>
                    <td style={{ padding: '6px 0', color: 'var(--text)', paddingLeft: `${s.dispatch_depth * 12}px`, fontWeight: 600 }}>{s.agent}</td>
                    <td style={{ padding: '6px 0', color: 'var(--text-muted)' }}>{s.backend}</td>
                    <td style={{ padding: '6px 0', color: 'var(--text-muted)' }}>{s.repo}{s.number > 0 ? ` #${s.number}` : ''}</td>
                    <td style={{ padding: '6px 0', color: 'var(--text-muted)' }}>{s.event_kind}</td>
                    <td style={{ padding: '6px 0', color: 'var(--text-muted)' }}>{fmt(s.started_at)}</td>
                    <td style={{ padding: '6px 0', color: 'var(--text-muted)' }}>{fmtDuration(s.duration_ms)}</td>
                    <td style={{ padding: '6px 0' }}><StatusBadge status={s.status} /></td>
                  </tr>
                  <tr>
                    <td colSpan={7} style={{ padding: '2px 0 8px', paddingLeft: `${s.dispatch_depth * 12 + 12}px` }}>
                      {s.summary && <div style={{ fontSize: '0.78rem', color: 'var(--text-faint)', fontStyle: 'italic' }}>{s.summary}</div>}
                      {s.error && <div style={{ fontSize: '0.78rem', color: 'var(--text-danger)', marginTop: '2px' }}>{s.error}</div>}
                      <TokenUsagePanel span={s} />
                      <PromptPanel span={s} />
                      <SpanTranscript spanId={s.span_id} />
                    </td>
                  </tr>
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
  const startMsList = sorted.map(s => new Date(s.started_at).getTime()).filter(n => Number.isFinite(n) && n > 0)
  const finishMsList = sorted.map(s => new Date(s.finished_at).getTime()).filter(n => Number.isFinite(n) && n > 0)
  const minMs = startMsList.length ? Math.min(...startMsList) : 0
  const maxFinishMs = finishMsList.length ? Math.max(...finishMsList) : 0
  const maxMs = Math.max(maxFinishMs, ...startMsList)
  const totalMs = maxMs - minMs || 1
  const wallMs = maxMs - minMs
  const hasError = spans.some(s => s.status === 'error')

  const startedAt = sorted[0]?.started_at
  const finishedAt = maxFinishMs > 0 ? new Date(maxFinishMs).toISOString() : undefined

  return (
    <Card style={{ marginBottom: '1rem', cursor: 'pointer' }} >
      <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.35rem' }}>
        <div>
          <button
            onClick={() => onSelect(rootId)}
            style={{ background: 'none', border: 'none', fontFamily: 'monospace', fontSize: '0.8rem', color: 'var(--accent)', cursor: 'pointer', padding: 0 }}
          >
            {rootId}
          </button>
          <span style={{ color: 'var(--text-muted)', fontSize: '0.8rem', marginLeft: '1rem' }}>
            {sorted[0]?.repo ?? ''} · {sorted[0]?.event_kind ?? ''} · {spans.length} span{spans.length !== 1 ? 's' : ''}
          </span>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          {hasError && <StatusBadge status="error" />}
          <span style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>{wallMs}ms</span>
        </div>
      </div>
      {startedAt && (
        <div style={{ color: 'var(--text-faint)', fontSize: '0.75rem', marginBottom: '0.75rem', display: 'flex', gap: '1rem' }}>
          <span>Started: {fmt(startedAt)}</span>
          {finishedAt && <span>Finished: {fmt(finishedAt)}</span>}
        </div>
      )}
      {sorted.map(s => {
        const start = new Date(s.started_at).getTime()
        const leftPct = ((start - minMs) / totalMs) * 100
        const widthPct = Math.max(0.3, (s.duration_ms / totalMs) * 100)
        const colors = ['#3b82f6', '#10b981', '#f59e0b', '#8b5cf6', '#ec4899']
        const color = s.status === 'error' ? '#ef4444' : colors[s.dispatch_depth % colors.length]
        return (
          <div key={s.span_id} style={{ display: 'flex', alignItems: 'center', padding: '2px 0', gap: '0.75rem', fontSize: '0.75rem' }}>
            <div style={{ width: '120px', flexShrink: 0, paddingLeft: `${s.dispatch_depth * 10}px`, color: 'var(--text-muted)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{s.agent}</div>
            <div style={{ flex: 1, height: '12px', background: 'var(--bg)', borderRadius: '2px', position: 'relative' }}>
              <div style={{ position: 'absolute', left: `${leftPct}%`, width: `${widthPct}%`, height: '100%', background: color, borderRadius: '2px', opacity: 0.7 }} />
            </div>
            <div style={{ width: '60px', textAlign: 'right', color: 'var(--text-faint)' }}>{fmtDuration(s.duration_ms)}</div>
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
  const [repoFilter, setRepoFilter] = useRepoFilter()

  const load = () => {
    setLoading(true)
    fetch('/traces')
      .then(r => r.json())
      .then(data => { setSpans(data ?? []); setLoading(false) })
      .catch(() => setLoading(false))
  }

  useEffect(() => {
    load()
    const es = new EventSource('/traces/stream')
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
    if (repoFilter && s.repo !== repoFilter) continue
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Traces</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {rootIds.length} trace{rootIds.length !== 1 ? 's' : ''} · {streaming ? '🟢 live' : '🔴 disconnected'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem' }}>
          <RepoFilter selected={repoFilter} onChange={setRepoFilter} />
          <input
            placeholder="Filter by agent, repo, or ID…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '6px 10px', borderRadius: '6px', fontSize: '0.875rem', width: '240px' }}
          />
          <button onClick={load} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-muted)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
      {!loading && rootIds.length === 0 && <p style={{ color: 'var(--text-muted)' }}>No traces yet.</p>}
      {rootIds.map(id => (
        <TraceListItem key={id} rootId={id} spans={grouped[id]} onSelect={handleSelect} />
      ))}
    </div>
  )
}

export default function TracesPage() {
  return (
    <Suspense fallback={<p style={{ color: 'var(--text-muted)' }}>Loading…</p>}>
      <TracesContent />
    </Suspense>
  )
}
