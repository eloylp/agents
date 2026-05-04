'use client'

import { useMemo, useState } from 'react'

// StreamCardEntry is the visual shape every transcript surface (live tail
// or persisted replay) maps onto. parseStreamLine builds entries from raw
// stdout JSONL; stepToCardEntries builds them from a persisted TraceStep.
export type StreamCardEntry = {
  at: number
  kind: StreamCardKind
  title: string
  detail?: string
  raw?: string
}

export type StreamCardKind = 'thinking' | 'tool_use' | 'tool_result' | 'usage' | 'end' | 'raw'

// kindMeta is the visual + label config for each card kind. Used by
// StreamCard for the accent colour and TranscriptFilter for the chip labels.
const kindMeta: Record<StreamCardKind, { label: string; emoji: string; accent: string }> = {
  tool_use:    { label: 'Tool calls',   emoji: '🔧', accent: '#fcd34d' },
  tool_result: { label: 'Tool results', emoji: '📤', accent: '#5eead4' },
  thinking:    { label: 'Thinking',     emoji: '💬', accent: '#60a5fa' },
  usage:       { label: 'Usage',        emoji: '📊', accent: '#a5b4fc' },
  end:         { label: 'End',          emoji: '✓',  accent: 'var(--success)' },
  raw:         { label: 'Other',        emoji: '·',  accent: 'var(--text-faint)' },
}

// TranscriptFilter renders a row of toggle pills, one per card kind that
// actually appears in the entries list. Clicking a pill toggles whether
// cards of that kind are visible. Returns null when only one kind is
// present (nothing to filter). Both Runners (live tail) and Traces
// (persisted replay) reuse it so the filtering UX is consistent.
export function TranscriptFilter({
  entries,
  visibleKinds,
  onChange,
}: {
  entries: StreamCardEntry[]
  visibleKinds: Set<StreamCardKind>
  onChange: (next: Set<StreamCardKind>) => void
}) {
  const counts = useMemo(() => {
    const c: Partial<Record<StreamCardKind, number>> = {}
    for (const e of entries) c[e.kind] = (c[e.kind] ?? 0) + 1
    return c
  }, [entries])
  const presentKinds = (Object.keys(kindMeta) as StreamCardKind[]).filter(k => (counts[k] ?? 0) > 0)
  if (presentKinds.length <= 1) return null

  const toggle = (k: StreamCardKind) => {
    const next = new Set(visibleKinds)
    if (next.has(k)) next.delete(k)
    else next.add(k)
    onChange(next)
  }
  const allOn = presentKinds.every(k => visibleKinds.has(k))
  const reset = () => onChange(new Set(presentKinds))

  return (
    <div style={{
      display: 'flex',
      flexWrap: 'wrap',
      gap: '4px',
      marginBottom: '0.5rem',
      alignItems: 'center',
      position: 'sticky',
      top: 0,
      zIndex: 1,
      background: 'var(--bg-card)',
      paddingBottom: '6px',
      borderBottom: '1px solid var(--border-subtle)',
    }}>
      {presentKinds.map(k => {
        const meta = kindMeta[k]
        const on = visibleKinds.has(k)
        return (
          <button
            key={k}
            onClick={() => toggle(k)}
            title={`${on ? 'Hide' : 'Show'} ${meta.label.toLowerCase()}`}
            style={{
              padding: '2px 10px',
              borderRadius: '999px',
              cursor: 'pointer',
              fontSize: '0.7rem',
              fontWeight: 500,
              border: `1px solid ${on ? meta.accent : 'var(--border)'}`,
              background: on ? 'var(--bg-input)' : 'transparent',
              color: on ? 'var(--text)' : 'var(--text-faint)',
              opacity: on ? 1 : 0.6,
            }}
          >
            {meta.emoji} {meta.label} ({counts[k]})
          </button>
        )
      })}
      {!allOn && (
        <button
          onClick={reset}
          title="Show all kinds"
          style={{
            padding: '2px 10px',
            borderRadius: '999px',
            cursor: 'pointer',
            fontSize: '0.7rem',
            border: '1px solid var(--border-subtle)',
            background: 'transparent',
            color: 'var(--text-faint)',
          }}
        >
          show all
        </button>
      )}
    </div>
  )
}

// allStreamCardKinds is the default visibleKinds set: every kind on.
export function allStreamCardKinds(): Set<StreamCardKind> {
  return new Set(Object.keys(kindMeta) as StreamCardKind[])
}

// PersistedStep mirrors the wire shape of one row from
// GET /traces/{span_id}/steps. The Go side is workflow.TraceStep.
export type PersistedStep = {
  kind?: 'tool' | 'thinking'
  tool_name?: string
  input_summary?: string
  output_summary?: string
  duration_ms?: number
}

// StreamEvent mirrors ai.StreamEvent on the Go side. The daemon parses the
// AI CLI's raw stdout into this canonical shape before publishing to the
// per-span SSE feed, so the frontend no longer carries format-specific
// (claude / codex / openai) parsers. Lifecycle for a tool call is two
// events: tool_use on start, tool_result on completion. Anything the
// daemon could not classify arrives as kind: 'raw'.
type StreamEvent = {
  kind: 'tool_use' | 'tool_result' | 'thinking' | 'usage' | 'raw'
  tool?: string
  server?: string
  input?: string
  output?: string
  text?: string
  error?: string
  duration_ms?: number
  usage?: {
    input_tokens?: number
    output_tokens?: number
    cache_read_tokens?: number
    cache_write_tokens?: number
  }
  raw?: string
}

// parseStreamLine turns one normalized SSE payload into a StreamCardEntry.
// Lines that cannot be parsed as JSON, or whose `kind` is unknown, render
// as 'raw' so nothing is silently dropped.
export function parseStreamLine(line: string): StreamCardEntry {
  const at = Date.now()
  let ev: StreamEvent
  try {
    ev = JSON.parse(line) as StreamEvent
  } catch {
    return { at, kind: 'raw', title: 'raw output', raw: line }
  }
  switch (ev.kind) {
    case 'tool_use': {
      const name = ev.server ? `${ev.server}.${ev.tool || 'tool'}` : (ev.tool || 'tool_use')
      return { at, kind: 'tool_use', title: `🔧 ${name}`, detail: ev.input || '', raw: line }
    }
    case 'tool_result': {
      const name = ev.server ? `${ev.server}.${ev.tool || 'tool'}` : (ev.tool || 'tool')
      const detail = ev.error ? `error: ${ev.error}` : (ev.output || '')
      return { at, kind: 'tool_result', title: `📤 ${name}`, detail, raw: line }
    }
    case 'thinking':
      return { at, kind: 'thinking', title: '💬 thinking', detail: ev.text || '', raw: line }
    case 'usage': {
      const u = ev.usage
      const detail = u
        ? `in ${u.input_tokens ?? 0} · out ${u.output_tokens ?? 0}` +
          (u.cache_read_tokens ? ` · cache ${u.cache_read_tokens}` : '')
        : ''
      return { at, kind: 'usage', title: '📊 usage', detail, raw: line }
    }
    case 'raw':
      return { at, kind: 'raw', title: 'raw output', raw: ev.raw || line }
    default:
      return { at, kind: 'raw', title: ev.kind ? `· ${ev.kind}` : 'raw output', raw: line }
  }
}

// stepToCardEntries maps a persisted TraceStep (one row from /steps) to one
// or more visual cards. A "tool" step expands into two cards (tool_use +
// tool_result) so the visual is identical to the live tail; a "thinking"
// step is one card.
export function stepToCardEntries(step: PersistedStep, indexAt = 0): StreamCardEntry[] {
  const at = indexAt
  if (step.kind === 'thinking') {
    return [
      {
        at,
        kind: 'thinking',
        title: '💬 thinking',
        detail: step.input_summary || '',
      },
    ]
  }
  // Default to tool. Older rows persisted before migration 011 have an
  // empty kind; treat them as tool, matching the migration default.
  const cards: StreamCardEntry[] = []
  cards.push({
    at,
    kind: 'tool_use',
    title: `🔧 ${step.tool_name || 'tool_use'}`,
    detail: step.input_summary || '',
  })
  if (step.output_summary || step.duration_ms) {
    cards.push({
      at: at + 1,
      kind: 'tool_result',
      title: '📤 tool result',
      detail: step.output_summary || '',
    })
  }
  return cards
}

// StreamCard renders one entry as a colour-coded, collapsible card. The
// preview shows the first 200 characters; clicking expands to the full
// detail (or the raw line, if the parser couldn't classify it).
export function StreamCard({ entry }: { entry: StreamCardEntry }) {
  const [open, setOpen] = useState(false)
  const accent = kindMeta[entry.kind]?.accent ?? 'var(--text-faint)'
  return (
    <div
      style={{
        borderLeft: `3px solid ${accent}`,
        padding: '0.5rem 0.75rem',
        marginBottom: '0.4rem',
        background: 'var(--bg-input)',
        borderRadius: '0 4px 4px 0',
        boxSizing: 'border-box',
        maxWidth: '100%',
        minWidth: 0,
        overflow: 'hidden',
      }}
    >
      <div
        onClick={() => setOpen(!open)}
        style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'flex-start',
          gap: '0.5rem',
          cursor: 'pointer',
          fontSize: '0.82rem',
          color: 'var(--text)',
          minWidth: 0,
        }}
      >
        <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0, flex: 1 }}>
          <strong>{entry.title}</strong>
        </span>
        <span style={{ color: 'var(--text-faint)', fontSize: '0.72rem', flexShrink: 0 }}>{open ? '▼' : '▶'}</span>
      </div>
      {entry.detail && !open && (
        <div
          style={{
            color: 'var(--text-muted)',
            fontSize: '0.78rem',
            marginTop: '4px',
            overflow: 'hidden',
            textOverflow: 'ellipsis',
            whiteSpace: 'nowrap',
            maxWidth: '100%',
          }}
        >
          {entry.detail.slice(0, 200)}
        </div>
      )}
      {open && (
        <pre
          style={{
            marginTop: '0.5rem',
            padding: '0.5rem',
            background: 'var(--bg-card)',
            border: '1px solid var(--border-subtle)',
            borderRadius: '4px',
            fontSize: '0.72rem',
            fontFamily: 'monospace',
            color: 'var(--text)',
            whiteSpace: 'pre-wrap',
            wordBreak: 'break-word',
            overflowWrap: 'anywhere',
            maxHeight: '300px',
            maxWidth: '100%',
            boxSizing: 'border-box',
            overflowY: 'auto',
            overflowX: 'hidden',
            margin: 0,
          }}
        >
          {entry.detail || entry.raw}
        </pre>
      )}
    </div>
  )
}
