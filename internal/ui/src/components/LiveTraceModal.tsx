'use client'

import { useEffect, useRef, useState } from 'react'
import Link from 'next/link'
import { StreamCard, TranscriptFilter, allStreamCardKinds, stepToCardEntries, type PersistedStep, type StreamCardEntry, type StreamCardKind } from '@/components/StreamCard'
import { openAuthenticatedSSE } from '@/lib/sse'

export interface LiveTraceSpan {
  id: string
  agent: string
  repo: string
  kind: string
  rootEventId?: string
}

export default function LiveTraceModal({ span, onClose }: { span: LiveTraceSpan; onClose: () => void }) {
  const [entries, setEntries] = useState<StreamCardEntry[]>([])
  const [status, setStatus] = useState<'connecting' | 'live' | 'ended' | 'error'>('connecting')
  const [visibleKinds, setVisibleKinds] = useState<Set<StreamCardKind>>(allStreamCardKinds)
  const scrollRef = useRef<HTMLDivElement>(null)
  const stuckToBottom = useRef(true)
  const [hasNewWhileDetached, setHasNewWhileDetached] = useState(false)
  const visibleEntries = entries.filter(e => visibleKinds.has(e.kind))
  const traceHref = `/traces/?id=${encodeURIComponent(span.rootEventId || span.id)}`

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
              span <code>{span.id}</code> · {status === 'live' ? 'streaming' : status === 'ended' ? 'run completed' : status === 'error' ? 'disconnected' : 'connecting'} · {entries.length} event{entries.length !== 1 ? 's' : ''}
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
              Run completed.{' '}
              <Link href={traceHref} style={{ color: 'var(--accent)' }}>
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
