import { describe, expect, it } from 'vitest'
import { parseStreamLine } from './StreamCard'

describe('parseStreamLine', () => {
  it('renders a tool_use event with the server-prefixed tool name and the input as detail', () => {
    const line = JSON.stringify({
      kind: 'tool_use',
      tool: 'issue_read',
      server: 'github',
      input: '{"owner":"eloylp","repo":"agents"}',
    })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('tool_use')
    expect(entry.title).toBe('🔧 github.issue_read')
    expect(entry.detail).toBe('{"owner":"eloylp","repo":"agents"}')
  })

  it('renders a tool_result event using the server-prefixed name in the title', () => {
    const line = JSON.stringify({
      kind: 'tool_result',
      tool: 'issue_read',
      server: 'github',
      output: 'ok',
      duration_ms: 42,
    })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('tool_result')
    expect(entry.title).toBe('📤 github.issue_read')
    expect(entry.detail).toBe('ok')
  })

  it('shows the error string when a tool_result carries one, taking precedence over output', () => {
    const line = JSON.stringify({
      kind: 'tool_result',
      tool: 'issue_read',
      output: 'ok',
      error: 'permission denied',
    })
    const entry = parseStreamLine(line)
    expect(entry.detail).toBe('error: permission denied')
  })

  it('renders thinking events with the text body', () => {
    const line = JSON.stringify({ kind: 'thinking', text: 'plan: do X' })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('thinking')
    expect(entry.title).toBe('💬 thinking')
    expect(entry.detail).toBe('plan: do X')
  })

  it('renders usage events with the four-field token breakdown', () => {
    const line = JSON.stringify({
      kind: 'usage',
      usage: { input_tokens: 100, output_tokens: 200, cache_read_tokens: 5 },
    })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('usage')
    expect(entry.title).toBe('📊 usage')
    expect(entry.detail).toBe('in 100 · out 200 · cache 5')
  })

  it('falls back to raw on invalid JSON', () => {
    const entry = parseStreamLine('not json at all')
    expect(entry.kind).toBe('raw')
    expect(entry.raw).toBe('not json at all')
  })

  it('falls back to raw on unknown kinds, surfacing the kind in the title', () => {
    const line = JSON.stringify({ kind: 'mysterious-future-event' })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('raw')
    expect(entry.title).toBe('· mysterious-future-event')
  })

  it('passes raw events through without modification', () => {
    const line = JSON.stringify({ kind: 'raw', raw: 'unparsed line from a custom backend' })
    const entry = parseStreamLine(line)
    expect(entry.kind).toBe('raw')
    expect(entry.raw).toBe('unparsed line from a custom backend')
  })

  it('handles tool_use without a server prefix gracefully', () => {
    const line = JSON.stringify({ kind: 'tool_use', tool: 'Read', input: '{"file":"/x"}' })
    const entry = parseStreamLine(line)
    expect(entry.title).toBe('🔧 Read')
    expect(entry.detail).toBe('{"file":"/x"}')
  })
})
