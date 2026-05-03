'use client'

import { useState } from 'react'

// StreamCardEntry is the visual shape every transcript surface (live tail
// or persisted replay) maps onto. parseStreamLine builds entries from raw
// stdout JSONL; stepToCardEntries builds them from a persisted TraceStep.
export type StreamCardEntry = {
  at: number
  kind: 'thinking' | 'tool_use' | 'tool_result' | 'usage' | 'end' | 'raw'
  title: string
  detail?: string
  raw?: string
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

// parseStreamLine turns one CLI stdout JSONL line into a StreamCardEntry.
// Recognises Anthropic's stream-json shape (assistant / user / result
// events with content blocks) and OpenAI's chat.completion.chunk shape
// (choices[].delta.content). Anything else falls through as 'raw'.
export function parseStreamLine(line: string): StreamCardEntry {
  const at = Date.now()
  const raw = line
  let parsed: any
  try {
    parsed = JSON.parse(line)
  } catch {
    return { at, kind: 'raw', title: 'raw output', raw }
  }
  // Anthropic / claude stream-json
  if (parsed?.type === 'assistant' && parsed?.message?.content) {
    const blocks = parsed.message.content as Array<any>
    const tools = blocks.filter((b) => b?.type === 'tool_use')
    if (tools.length > 0) {
      const t = tools[0]
      return {
        at,
        kind: 'tool_use',
        title: `🔧 ${t.name || 'tool_use'}`,
        detail: typeof t.input === 'string' ? t.input : JSON.stringify(t.input ?? {}, null, 2),
        raw,
      }
    }
    const texts = blocks
      .filter((b) => b?.type === 'text')
      .map((b) => b.text)
      .filter(Boolean)
    if (texts.length > 0) {
      return { at, kind: 'thinking', title: '💬 thinking', detail: texts.join('\n\n'), raw }
    }
  }
  if (parsed?.type === 'user' && parsed?.message?.content) {
    const blocks = parsed.message.content as Array<any>
    const results = blocks.filter((b) => b?.type === 'tool_result')
    if (results.length > 0) {
      const r = results[0]
      const content = typeof r.content === 'string' ? r.content : JSON.stringify(r.content ?? '', null, 2)
      return { at, kind: 'tool_result', title: '📤 tool result', detail: content, raw }
    }
  }
  if (parsed?.type === 'result') {
    const usage = parsed.usage
    const usageStr = usage
      ? `in ${usage.input_tokens ?? usage.prompt_tokens ?? 0} · out ${usage.output_tokens ?? usage.completion_tokens ?? 0}` +
        (usage.cache_read_input_tokens ? ` · cache ${usage.cache_read_input_tokens}` : '')
      : ''
    return { at, kind: 'usage', title: '📊 result', detail: usageStr || JSON.stringify(parsed, null, 2), raw }
  }
  // Codex --json: events wrap the actual item under `item`.
  // - thread.started / turn.started: noise, render minimal raw entry
  // - item.started: a tool/command is starting → tool_use card with input
  // - item.completed:
  //     agent_message → thinking card with item.text
  //     command_execution → tool_result card with aggregated_output
  //     mcp_tool_call / function_call / etc. → tool_use card with output
  // - turn.completed: usage card
  if (parsed?.type === 'item.started' && parsed?.item?.type === 'command_execution') {
    const cmd = parsed.item.command || ''
    return { at, kind: 'tool_use', title: '🔧 bash', detail: cmd, raw }
  }
  if (parsed?.type === 'item.completed' && parsed?.item) {
    const it = parsed.item
    if (it.type === 'agent_message') {
      const text = (it.text || '').trim()
      if (text) return { at, kind: 'thinking', title: '💬 thinking', detail: text, raw }
    }
    if (it.type === 'command_execution') {
      return { at, kind: 'tool_result', title: '📤 tool result', detail: it.aggregated_output || '', raw }
    }
    // Generic tool fallback (mcp_tool_call, function_call, ...).
    if (it.name) {
      const tn = it.server ? `${it.server}.${it.name}` : it.name
      const input = typeof it.arguments === 'string' ? it.arguments : JSON.stringify(it.arguments ?? {}, null, 2)
      const output = typeof it.output === 'string' ? it.output : JSON.stringify(it.output ?? '', null, 2)
      const detail = output ? `${input}\n→\n${output}` : input
      return { at, kind: 'tool_use', title: `🔧 ${tn}`, detail, raw }
    }
  }
  if (parsed?.type === 'turn.completed') {
    const u = parsed.usage
    const usageStr = u
      ? `in ${u.input_tokens ?? 0} · out ${u.output_tokens ?? 0}` +
        (u.cached_input_tokens ? ` · cache ${u.cached_input_tokens}` : '')
      : ''
    return { at, kind: 'usage', title: '📊 turn completed', detail: usageStr || JSON.stringify(parsed, null, 2), raw }
  }
  // OpenAI / codex chat.completion.chunk
  if (parsed?.choices?.[0]?.delta) {
    const delta = parsed.choices[0].delta
    if (delta.content) {
      return { at, kind: 'thinking', title: '💬 thinking', detail: String(delta.content), raw }
    }
    if (delta.tool_calls?.[0]) {
      const tc = delta.tool_calls[0]
      const fnName = tc.function?.name || 'tool_call'
      const args = tc.function?.arguments || ''
      return { at, kind: 'tool_use', title: `🔧 ${fnName}`, detail: args, raw }
    }
  }
  return { at, kind: 'raw', title: parsed?.type ? `· ${parsed.type}` : 'raw output', raw }
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
  const accent =
    entry.kind === 'tool_use'
      ? '#fcd34d'
      : entry.kind === 'tool_result'
      ? '#5eead4'
      : entry.kind === 'thinking'
      ? '#60a5fa'
      : entry.kind === 'usage'
      ? '#a5b4fc'
      : entry.kind === 'end'
      ? 'var(--success)'
      : 'var(--text-faint)'
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
