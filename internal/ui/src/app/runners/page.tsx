'use client'
import { Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { useSearchParams, useRouter } from 'next/navigation'
import Link from 'next/link'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import RepoFilter from '@/components/RepoFilter'
import WorkspaceSelect from '@/components/WorkspaceSelect'
import { StreamCard, TranscriptFilter, allStreamCardKinds, stepToCardEntries, type PersistedStep, type StreamCardEntry, type StreamCardKind } from '@/components/StreamCard'
import { fmtDuration } from '@/lib/format'
import { newRunnerTimeoutTracker, observeRunnerTimeouts } from '@/lib/runner-timeouts'
import { openAuthenticatedSSE } from '@/lib/sse'
import { useSelectedWorkspace, withWorkspace } from '@/lib/workspace'

interface RunnerRow {
  id: number
  event_id: string
  kind: string
  repo: string
  number: number
  actor?: string
  target_agent?: string
  status: 'enqueued' | 'running' | 'success' | 'error' | 'skipped'
  enqueued_at: string
  started_at?: string
  completed_at?: string
  payload?: Record<string, unknown>
  agent?: string
  span_id?: string
  run_duration_ms?: number
  summary?: string
  error?: string
  error_kind?: 'backend_auth' | 'backend_error' | 'runner_error' | 'timeout' | 'canceled' | 'parse_error' | 'unknown' | string
  error_detail?: string
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
  if (!n) return '-'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(2)} MB`
}

interface ListResponse {
  runners: RunnerRow[]
  total: number
  limit: number
  offset: number
}

const statusStyle: Record<string, { bg: string; text: string; border: string }> = {
  enqueued: { bg: 'rgba(59,130,246,0.15)',  text: '#60a5fa', border: '#1e3a5f' },
  running:  { bg: 'rgba(245,158,11,0.15)',  text: '#fcd34d', border: '#78350f' },
  success:  { bg: 'var(--success-bg)',       text: 'var(--success)', border: 'var(--success-border)' },
  error:    { bg: 'var(--bg-danger)',        text: 'var(--text-danger)', border: 'var(--border-danger)' },
  skipped:  { bg: 'rgba(100,116,139,0.15)',  text: 'var(--text-muted)', border: 'var(--border-subtle)' },
}

const POLL_MS = 2000
const HIGHLIGHT_MS = 4000

function isRunnerExecution(row: RunnerRow) {
  return row.status !== 'skipped'
}

function fmtTime(s?: string) {
  if (!s) return '-'
  return new Date(s).toLocaleTimeString()
}

function runnerCause(row: RunnerRow) {
  return row.error_detail || row.error || ''
}

function runnerKindLabel(kind?: string) {
  switch (kind) {
    case 'backend_auth': return 'Backend auth'
    case 'backend_error': return 'Backend error'
    case 'runner_error': return 'Runner error'
    case 'timeout': return 'Timeout'
    case 'canceled': return 'Canceled'
    case 'parse_error': return 'Parser'
    default: return kind || 'Failure'
  }
}

export default function RunnersPage() {
  return (
    <Suspense fallback={<p style={{ color: 'var(--text-muted)' }}>Loading runners...</p>}>
      <RunnersInner />
    </Suspense>
  )
}

function LiveStreamModal({ span, onClose }: { span: { id: string; agent: string; repo: string; kind: string }; onClose: () => void }) {
  const [entries, setEntries] = useState<StreamCardEntry[]>([])
  const [status, setStatus] = useState<'connecting' | 'live' | 'ended' | 'error'>('connecting')
  const [visibleKinds, setVisibleKinds] = useState<Set<StreamCardKind>>(allStreamCardKinds)
  const scrollRef = useRef<HTMLDivElement>(null)
  const stuckToBottom = useRef(true)
  const [hasNewWhileDetached, setHasNewWhileDetached] = useState(false)
  const visibleEntries = entries.filter(e => visibleKinds.has(e.kind))

  useEffect(() => {
    const stream = openAuthenticatedSSE(`/traces/${encodeURIComponent(span.id)}/stream`, {
      onOpen: () => setStatus('live'),
      onMessage: data => {
        try {
          const step = JSON.parse(data) as PersistedStep
          setEntries(prev => [...prev, ...stepToCardEntries(step, prev.length)])
        } catch {
          // Malformed stream rows are ignored; the durable /steps endpoint is
          // still the source of truth for transcript recovery.
        }
      },
      onEvent: event => {
        if (event === 'end') setStatus('ended')
      },
      onError: () => setStatus('error'),
    })
    return () => stream.close()
  }, [span.id])

  useEffect(() => {
    const el = scrollRef.current
    if (!el) return
    if (stuckToBottom.current) {
      el.scrollTop = el.scrollHeight
    } else {
      setHasNewWhileDetached(true)
    }
  }, [visibleEntries.length])

  const onScroll = () => {
    const el = scrollRef.current
    if (!el) return
    const distance = el.scrollHeight - (el.scrollTop + el.clientHeight)
    const atBottom = distance < 32
    stuckToBottom.current = atBottom
    if (atBottom && hasNewWhileDetached) setHasNewWhileDetached(false)
  }

  const jumpToLatest = () => {
    const el = scrollRef.current
    if (!el) return
    el.scrollTop = el.scrollHeight
    stuckToBottom.current = true
    setHasNewWhileDetached(false)
  }

  return (
    <div onClick={onClose} style={{
      position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.6)',
      display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 1000,
    }}>
      <div onClick={e => e.stopPropagation()} style={{
        width: 'min(900px, 92vw)', maxHeight: '90vh',
        background: 'var(--bg-card)', border: '1px solid var(--border)',
        borderRadius: '8px', display: 'flex', flexDirection: 'column',
        position: 'relative',
      }}>
        <div style={{
          padding: '0.75rem 1rem',
          borderBottom: '1px solid var(--border)',
          display: 'flex', justifyContent: 'space-between', alignItems: 'center',
        }}>
          <div>
            <div style={{ fontSize: '0.95rem', fontWeight: 700, color: 'var(--text-heading)' }}>
              Live: {span.agent} · {span.repo} · {span.kind}
            </div>
            <div style={{ fontSize: '0.75rem', color: 'var(--text-muted)', marginTop: '2px' }}>
              span <code>{span.id}</code> · {status === 'live' ? '🟢 streaming' : status === 'ended' ? '✓ run completed' : status === 'error' ? '🔴 disconnected' : '⏳ connecting'} · {entries.length} event{entries.length !== 1 ? 's' : ''}
            </div>
          </div>
          <button onClick={onClose} style={{
            background: 'var(--bg-input)', border: '1px solid var(--border)',
            color: 'var(--text)', padding: '4px 12px', borderRadius: '4px',
            cursor: 'pointer', fontSize: '0.85rem',
          }}>Close</button>
        </div>
        <div
          ref={scrollRef}
          onScroll={onScroll}
          style={{ flex: 1, overflowY: 'auto', padding: '0.75rem 1rem', minHeight: 0 }}
        >
          {entries.length === 0 && status === 'connecting' && (
            <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Waiting for output...</p>
          )}
          {entries.length === 0 && status === 'ended' && (
            <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>Run finished without emitting any output that the daemon captured.</p>
          )}
          {entries.length === 0 && status === 'error' && (
            <p style={{ color: 'var(--text-danger)', fontSize: '0.85rem' }}>Lost connection to the live stream. The run may still be in flight; close and reopen to retry.</p>
          )}
          {entries.length > 0 && (
            <TranscriptFilter entries={entries} visibleKinds={visibleKinds} onChange={setVisibleKinds} />
          )}
          {visibleEntries.map((e, i) => <StreamCard key={i} entry={e} />)}
          {entries.length > 0 && visibleEntries.length === 0 && (
            <p style={{ color: 'var(--text-faint)', fontSize: '0.78rem', fontStyle: 'italic' }}>All cards filtered out. Toggle a chip above to show them.</p>
          )}
          {status === 'ended' && entries.length > 0 && (
            <div style={{ marginTop: '1rem', padding: '0.5rem 0.75rem', background: 'var(--bg-input)', borderRadius: '4px', fontSize: '0.8rem' }}>
              ✓ Run completed.{' '}
              <Link href={`/traces/?id=${encodeURIComponent(span.id)}`} style={{ color: 'var(--accent)' }}>
                View full trace detail →
              </Link>
            </div>
          )}
        </div>
        {hasNewWhileDetached && (
          <button
            onClick={jumpToLatest}
            style={{
              position: 'absolute', bottom: 12, left: '50%', transform: 'translateX(-50%)',
              background: 'var(--accent)', color: 'var(--bg-card)',
              border: 'none', borderRadius: '999px',
              padding: '4px 14px', fontSize: '0.8rem', fontWeight: 600,
              cursor: 'pointer', boxShadow: '0 4px 12px rgba(0,0,0,0.25)',
            }}
          >↓ Latest</button>
        )}
      </div>
    </div>
  )
}

function RunnersInner() {
  const params = useSearchParams()
  const router = useRouter()
  const focusEvent = params.get('event') ?? ''
  const repoParam = params.get('repo') ?? ''
  const { workspace } = useSelectedWorkspace()

  const setRepoFilter = (repo: string) => {
    const p = new URLSearchParams(params.toString())
    if (repo) p.set('repo', repo)
    else p.delete('repo')
    router.push(`/runners?${p.toString()}`)
  }

  const [rows, setRows] = useState<RunnerRow[]>([])
  const [status, setStatus] = useState<'' | 'enqueued' | 'running' | 'completed'>('')
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [pendingId, setPendingId] = useState<number | null>(null)
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [highlightUntil, setHighlightUntil] = useState<number | null>(null)
  const [streamSpan, setStreamSpan] = useState<{ id: string; agent: string; repo: string; kind: string } | null>(null)
  const [timeoutToast, setTimeoutToast] = useState<RunnerRow | null>(null)
  const [failureToast, setFailureToast] = useState<RunnerRow | null>(null)
  const timeoutTracker = useRef(newRunnerTimeoutTracker())
  const notifiedFailures = useRef<Set<string>>(new Set())
  const failureToastReady = useRef(false)

  const load = async () => {
    try {
      const url = withWorkspace(`/runners?limit=200${status ? `&status=${status}` : ''}`, workspace)
      const res = await fetch(url, { cache: 'no-store' })
      if (!res.ok) throw new Error(`status ${res.status}`)
      const data: ListResponse = await res.json()
      const nextRows = data.runners ?? []
      const newTimeout = observeRunnerTimeouts(nextRows, timeoutTracker.current)
      if (newTimeout) setTimeoutToast(newTimeout)
      let newFailure: RunnerRow | null = null
      nextRows.forEach(r => {
        if (r.status !== 'error') return
        const key = `${r.id}:${r.span_id || ''}`
        if (notifiedFailures.current.has(key)) return
        notifiedFailures.current.add(key)
        if (failureToastReady.current && !newFailure) newFailure = r
      })
      if (newFailure) setFailureToast(newFailure)
      failureToastReady.current = true
      setRows(nextRows)
      setError(null)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    timeoutTracker.current = newRunnerTimeoutTracker()
    notifiedFailures.current = new Set()
    failureToastReady.current = false
    load()
    const id = window.setInterval(load, POLL_MS)
    return () => window.clearInterval(id)
  }, [status, workspace]) // eslint-disable-line react-hooks/exhaustive-deps

  // Trigger highlight pulse when arriving with ?event=X. Wait until
  // the first batch of rows lands, otherwise the animation runs while
  // the page still shows "Loading..." and the user never sees it. Once
  // we've kicked it off for a given focus, don't re-fire on every
  // subsequent poll (rows.length stays > 0).
  const [highlighted, setHighlighted] = useState(false)
  useEffect(() => {
    if (!focusEvent) {
      setHighlighted(false)
      setHighlightUntil(null)
      return
    }
    if (highlighted || rows.length === 0) return
    setHighlighted(true)
    setHighlightUntil(Date.now() + HIGHLIGHT_MS)
    const t = window.setTimeout(() => setHighlightUntil(null), HIGHLIGHT_MS)
    return () => window.clearTimeout(t)
  }, [focusEvent, rows.length, highlighted])

  const filtered = useMemo(() => {
    let result = rows.filter(isRunnerExecution)
    if (focusEvent) result = result.filter(r => r.event_id === focusEvent)
    if (repoParam) result = result.filter(r => r.repo === repoParam)
    return result
  }, [rows, focusEvent, repoParam])

  // Native browser confirm()/alert() are jarring against the dashboard
  // styling, plus they block the JS event loop. Drive every confirm /
  // failure surface through a single Modal-state machine instead.
  type DialogState =
    | null
    | { kind: 'confirm-delete'; id: number }
    | { kind: 'confirm-retry'; id: number }
    | { kind: 'error'; title: string; message: string }
  const [dialog, setDialog] = useState<DialogState>(null)

  const performDelete = async (id: number) => {
    setDialog(null)
    setPendingId(id)
    try {
      const res = await fetch(`/runners/${id}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 404) {
        const body = await res.text()
        setDialog({ kind: 'error', title: 'Delete failed', message: body || `HTTP ${res.status}` })
      }
      await load()
    } finally {
      setPendingId(null)
    }
  }

  const performRetry = async (id: number) => {
    setDialog(null)
    setPendingId(id)
    try {
      const res = await fetch(`/runners/${id}/retry`, { method: 'POST' })
      if (!res.ok) {
        const body = await res.text()
        setDialog({ kind: 'error', title: 'Retry failed', message: body || `HTTP ${res.status}` })
      }
      await load()
    } finally {
      setPendingId(null)
    }
  }

  const onDelete = (id: number) => setDialog({ kind: 'confirm-delete', id })
  const onRetry = (id: number) => setDialog({ kind: 'confirm-retry', id })

  const counts = filtered.reduce<Record<string, number>>((m, r) => {
    m[r.status] = (m[r.status] ?? 0) + 1
    return m
  }, {})

  return (
    <div>
      <style>{`
        @keyframes highlight-pulse {
          0%, 100% { background: transparent; box-shadow: inset 4px 0 0 transparent; }
          10%, 35%, 60% { background: rgba(56,189,248,0.32); box-shadow: inset 4px 0 0 var(--accent); }
          25%, 50%, 75% { background: rgba(56,189,248,0.10); box-shadow: inset 4px 0 0 var(--accent); }
        }
        .runner-row-highlight {
          animation: highlight-pulse ${HIGHLIGHT_MS}ms ease-out;
        }
      `}</style>

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Runners</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {focusEvent ? (
              <>Showing event <code style={{ color: 'var(--accent)' }}>{focusEvent}</code> · {filtered.length} row{filtered.length !== 1 ? 's' : ''}</>
            ) : (
              <>{filtered.length} runner execution{filtered.length !== 1 ? 's' : ''} · enqueued {counts.enqueued ?? 0} · running {counts.running ?? 0} · success {counts.success ?? 0} · error {counts.error ?? 0} · skipped/no-op events live in Events</>
            )}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <WorkspaceSelect compact />
          {focusEvent && (
            <Link
              href={repoParam ? `/runners?repo=${encodeURIComponent(repoParam)}` : '/runners/'}
              style={{
                background: 'var(--bg-card)', border: '1px solid var(--border)',
                color: 'var(--text-muted)', padding: '4px 10px', borderRadius: '4px',
                fontSize: '0.8rem', textDecoration: 'none',
              }}
            >Clear filter</Link>
          )}
          <RepoFilter selected={repoParam} onChange={setRepoFilter} workspace={workspace} />
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
          <span style={{ color: 'var(--text-danger)', fontSize: '0.85rem' }}>Error loading runners: {error}</span>
        </Card>
      )}
      {timeoutToast && (
        <Card style={{ marginBottom: '1rem', borderColor: 'var(--border-danger)' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center' }}>
            <span style={{ color: 'var(--text-danger)', fontSize: '0.85rem' }}>
              Runner timed out: {timeoutToast.agent || timeoutToast.target_agent || 'agent'} · {timeoutToast.repo || '-'} · {timeoutToast.error}
            </span>
            <button
              onClick={() => setTimeoutToast(null)}
              style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '4px 10px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem' }}
            >Dismiss</button>
          </div>
        </Card>
      )}
      {failureToast && (
        <Card style={{ marginBottom: '1rem', borderColor: 'var(--border-danger)' }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center' }}>
            <span style={{ color: 'var(--text-danger)', fontSize: '0.85rem' }}>
              Runner failed: {failureToast.agent || failureToast.target_agent || 'agent'} · {failureToast.repo || '-'} · {runnerKindLabel(failureToast.error_kind)}{runnerCause(failureToast) ? ` · ${runnerCause(failureToast)}` : ''}
            </span>
            <button
              onClick={() => setFailureToast(null)}
              style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '4px 10px', borderRadius: '4px', cursor: 'pointer', fontSize: '0.8rem' }}
            >Dismiss</button>
          </div>
        </Card>
      )}

      <Card title="Runner Rows">
        <div style={{
          display: 'grid',
          gridTemplateColumns: '60px 100px 130px 140px 60px 130px 90px 90px 160px',
          gap: '0.5rem',
          padding: '4px 0',
          borderBottom: '2px solid var(--border)',
          fontSize: '0.75rem',
          color: 'var(--accent)',
          fontWeight: 600,
        }}>
          <span>Event</span>
          <span>Status</span>
          <span>Agent</span>
          <span>Repo</span>
          <span>#</span>
          <span>Kind</span>
          <span>Started</span>
          <span>Duration</span>
          <span>Actions</span>
        </div>

        {loading && filtered.length === 0 && <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>Loading...</p>}
        {!loading && filtered.length === 0 && (
          <p style={{ color: 'var(--text-muted)', padding: '0.5rem 0' }}>
            {focusEvent ? 'No runner execution for this event. It may have been skipped; Events keeps the full audit log.' : 'No runner executions.'}
          </p>
        )}

        <div style={{ maxHeight: '600px', overflowY: 'auto' }}>
          {filtered.map((r, idx) => {
            const sStyle = statusStyle[r.status] ?? statusStyle.enqueued
            const busy = pendingId === r.id
            const rowKey = `${r.id}:${r.span_id || idx}`
            const isExpanded = expanded.has(rowKey)
            const startedAt = r.started_at
            const highlight = focusEvent && r.event_id === focusEvent && highlightUntil && Date.now() < highlightUntil

            const toggleExpanded = () => {
              setExpanded(prev => {
                const next = new Set(prev)
                if (next.has(rowKey)) next.delete(rowKey); else next.add(rowKey)
                return next
              })
            }

            return (
              <div key={rowKey} className={highlight ? 'runner-row-highlight' : ''}>
                <div onClick={toggleExpanded} style={{
                  display: 'grid',
                  gridTemplateColumns: '60px 100px 130px 140px 60px 130px 90px 90px 160px',
                  gap: '0.5rem',
                  padding: '8px 0',
                  fontSize: '0.8rem',
                  alignItems: 'center',
                  borderBottom: '1px solid var(--border-subtle)',
                  opacity: busy ? 0.5 : 1,
                  cursor: 'pointer',
                }}>
                  <span style={{ color: 'var(--text-faint)', fontFamily: 'monospace' }}>{r.id}</span>
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
                  }}>{r.status}</span>
                  <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {r.agent ? (
                      <Link href={`/?focus=${encodeURIComponent(r.agent)}`} onClick={e => e.stopPropagation()} style={{
                        background: 'rgba(56,189,248,0.12)',
                        color: 'var(--accent)',
                        border: '1px solid var(--accent)',
                        padding: '2px 8px',
                        borderRadius: '4px',
                        fontSize: '0.72rem',
                        textDecoration: 'none',
                      }}>{r.agent}</Link>
                    ) : r.target_agent ? (
                      <span style={{ color: 'var(--text-faint)', fontStyle: 'italic' }}>→ {r.target_agent}</span>
                    ) : (
                      <span style={{ color: 'var(--text-faint)' }}>-</span>
                    )}
                  </span>
                  <span style={{ color: 'var(--text-faint)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{r.repo || '-'}</span>
                  <span style={{ color: 'var(--text-faint)' }}>{r.number > 0 ? `#${r.number}` : '-'}</span>
                  <span style={{ color: 'var(--text)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: '0.75rem' }}>{r.kind || '-'}</span>
                  <span style={{ color: 'var(--text-faint)' }} title={startedAt}>{fmtTime(startedAt)}</span>
                  <span style={{ color: 'var(--text-faint)' }}>{fmtDuration(r.run_duration_ms)}</span>
                  <span style={{ display: 'flex', gap: '0.4rem' }} onClick={e => e.stopPropagation()}>
                    {r.status === 'running' && r.span_id && (
                      <button
                        onClick={() => setStreamSpan({ id: r.span_id!, agent: r.agent || '', repo: r.repo, kind: r.kind })}
                        title="Watch the agent's live thinking process"
                        style={{
                          background: 'var(--bg-card)',
                          border: '1px solid var(--accent)',
                          color: 'var(--accent)',
                          padding: '2px 8px',
                          borderRadius: '4px',
                          cursor: 'pointer',
                          fontSize: '0.72rem',
                        }}
                      >▶ Live</button>
                    )}
                    <button
                      disabled={busy || r.status === 'running' || r.status === 'enqueued'}
                      onClick={() => onRetry(r.id)}
                      title={r.status === 'running' || r.status === 'enqueued' ? 'Cannot retry an in-flight event' : 'Re-enqueue this event'}
                      style={{
                        background: 'var(--bg-card)',
                        border: '1px solid var(--border)',
                        color: r.status === 'running' || r.status === 'enqueued' ? 'var(--text-faint)' : 'var(--text-muted)',
                        padding: '2px 8px',
                        borderRadius: '4px',
                        cursor: busy || r.status === 'running' || r.status === 'enqueued' ? 'not-allowed' : 'pointer',
                        fontSize: '0.72rem',
                      }}
                    >Retry</button>
                    <button
                      disabled={busy}
                      onClick={() => onDelete(r.id)}
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
                {isExpanded && (
                  <div style={{
                    padding: '0.75rem 0.5rem 0.75rem 1rem',
                    background: 'var(--bg-input)',
                    borderBottom: '1px solid var(--border-subtle)',
                    fontSize: '0.78rem',
                  }}>
                    <div style={{ display: 'grid', gridTemplateColumns: '120px 1fr', gap: '4px 12px', marginBottom: '0.5rem' }}>
                      <span style={{ color: 'var(--text-faint)' }}>Event ID</span>
                      <span style={{ fontFamily: 'monospace', color: 'var(--text)' }}>{r.event_id || '-'}</span>
                      <span style={{ color: 'var(--text-faint)' }}>Actor</span>
                      <span style={{ color: 'var(--text)' }}>{r.actor || '-'}</span>
                      <span style={{ color: 'var(--text-faint)' }}>Enqueued</span>
                      <span style={{ color: 'var(--text-faint)' }}>{new Date(r.enqueued_at).toLocaleString()}</span>
                      {r.completed_at && (<>
                        <span style={{ color: 'var(--text-faint)' }}>Completed</span>
                        <span style={{ color: 'var(--text-faint)' }}>{new Date(r.completed_at).toLocaleString()}</span>
                      </>)}
                      {r.summary && (<>
                        <span style={{ color: 'var(--text-faint)' }}>Summary</span>
                        <span style={{ color: 'var(--text)' }}>{r.summary}</span>
                      </>)}
                      {r.error && (<>
                        <span style={{ color: 'var(--text-faint)' }}>Error</span>
                        <span style={{ color: 'var(--text-danger)' }}>{r.error}</span>
                      </>)}
                      {r.error_kind && (<>
                        <span style={{ color: 'var(--text-faint)' }}>Failure kind</span>
                        <span style={{ color: 'var(--text-danger)' }}>{runnerKindLabel(r.error_kind)}</span>
                      </>)}
                      {r.error_detail && (<>
                        <span style={{ color: 'var(--text-faint)' }}>Backend detail</span>
                        <span style={{ color: 'var(--text-danger)' }}>{r.error_detail}</span>
                      </>)}
                      {(r.input_tokens || r.output_tokens || r.cache_read_tokens || r.cache_write_tokens) ? (<>
                        <span style={{ color: 'var(--text-faint)' }}>Tokens</span>
                        <span style={{ color: 'var(--text)' }}>
                          in <strong>{fmtTokens(r.input_tokens)}</strong> · out <strong>{fmtTokens(r.output_tokens)}</strong>
                          {(r.cache_read_tokens ?? 0) > 0 && <> · cache hit <strong style={{ color: 'var(--success)' }}>{fmtTokens(r.cache_read_tokens)}</strong></>}
                          {(r.cache_write_tokens ?? 0) > 0 && <> · cache write <strong>{fmtTokens(r.cache_write_tokens)}</strong></>}
                        </span>
                      </>) : null}
                      {r.prompt_size ? (<>
                        <span style={{ color: 'var(--text-faint)' }}>Prompt</span>
                        <span style={{ color: 'var(--text)' }}>{fmtBytes(r.prompt_size)}{r.event_id ? <> · <Link href={`/traces/?id=${encodeURIComponent(r.event_id)}`} style={{ color: 'var(--accent)', textDecoration: 'none' }}>view in trace detail →</Link></> : null}</span>
                      </>) : null}
                    </div>
                    {r.event_id && r.completed_at && (
                      <div style={{ marginBottom: '0.5rem' }}>
                        <Link href={`/traces/?id=${encodeURIComponent(r.event_id)}`} style={{
                          color: 'var(--accent)', fontSize: '0.78rem', textDecoration: 'none',
                          border: '1px solid var(--accent)', padding: '3px 10px', borderRadius: '4px',
                        }}>View trace detail →</Link>
                      </div>
                    )}
                    {r.payload && (
                      <details>
                        <summary style={{ color: 'var(--text-faint)', cursor: 'pointer', fontSize: '0.75rem' }}>Raw payload</summary>
                        <pre style={{
                          marginTop: '0.5rem',
                          padding: '0.5rem',
                          background: 'var(--bg-card)',
                          border: '1px solid var(--border-subtle)',
                          borderRadius: '4px',
                          fontSize: '0.72rem',
                          color: 'var(--text-faint)',
                          overflow: 'auto',
                          maxHeight: '300px',
                        }}>{JSON.stringify(r.payload, null, 2)}</pre>
                      </details>
                    )}
                  </div>
                )}
              </div>
            )
          })}
        </div>
      </Card>
      {streamSpan && <LiveStreamModal span={streamSpan} onClose={() => setStreamSpan(null)} />}
      {dialog?.kind === 'confirm-delete' && (
        <Modal title={`Delete runner #${dialog.id}?`} onClose={() => setDialog(null)}>
          <p style={{ color: 'var(--text)', fontSize: '0.85rem', marginBottom: '1rem' }}>
            Removes the underlying <code>event_queue</code> row. If a worker has already received it from the in-memory channel buffer, it may still run; the row simply won&apos;t appear here afterwards. Affects every fanned-out agent for this event.
          </p>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
            <button
              onClick={() => setDialog(null)}
              style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.85rem' }}
            >Cancel</button>
            <button
              onClick={() => performDelete(dialog.id)}
              style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--error-bg)', color: 'var(--text-danger)', cursor: 'pointer', fontSize: '0.85rem', fontWeight: 600 }}
            >Delete</button>
          </div>
        </Modal>
      )}
      {dialog?.kind === 'confirm-retry' && (
        <Modal title={`Retry runner #${dialog.id}?`} onClose={() => setDialog(null)}>
          <p style={{ color: 'var(--text)', fontSize: '0.85rem', marginBottom: '1rem' }}>
            Re-enqueues the underlying event; every fanned-out agent will run again. The original row stays as audit history.
          </p>
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.5rem' }}>
            <button
              onClick={() => setDialog(null)}
              style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.85rem' }}
            >Cancel</button>
            <button
              onClick={() => performRetry(dialog.id)}
              style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--accent-bg)', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.85rem', fontWeight: 600 }}
            >Retry</button>
          </div>
        </Modal>
      )}
      {dialog?.kind === 'error' && (
        <Modal title={dialog.title} onClose={() => setDialog(null)}>
          <pre style={{ color: 'var(--text-danger)', fontSize: '0.82rem', whiteSpace: 'pre-wrap', wordBreak: 'break-word', margin: 0, marginBottom: '1rem' }}>
            {dialog.message}
          </pre>
          <div style={{ display: 'flex', justifyContent: 'flex-end' }}>
            <button
              onClick={() => setDialog(null)}
              style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.85rem' }}
            >Close</button>
          </div>
        </Modal>
      )}
    </div>
  )
}
