import { describe, expect, it } from 'vitest'
import { stepToCardEntries } from './StreamCard'

describe('stepToCardEntries', () => {
  it('renders thinking steps with the text body', () => {
    const entries = stepToCardEntries({ kind: 'thinking', input_summary: 'plan: do X' })
    expect(entries).toHaveLength(1)
    expect(entries[0].kind).toBe('thinking')
    expect(entries[0].title).toBe('💬 thinking')
    expect(entries[0].detail).toBe('plan: do X')
  })

  it('expands tool steps into call and result cards', () => {
    const entries = stepToCardEntries({
      kind: 'tool',
      tool_name: 'github.issue_read',
      input_summary: '{"owner":"eloylp","repo":"agents"}',
      output_summary: 'ok',
      duration_ms: 42,
    })

    expect(entries).toHaveLength(2)
    expect(entries[0].kind).toBe('tool_use')
    expect(entries[0].title).toBe('🔧 github.issue_read')
    expect(entries[0].detail).toBe('{"owner":"eloylp","repo":"agents"}')
    expect(entries[1].kind).toBe('tool_result')
    expect(entries[1].title).toBe('📤 tool result')
    expect(entries[1].detail).toBe('ok')
  })

  it('treats missing kind as a legacy tool step', () => {
    const entries = stepToCardEntries({ tool_name: 'Read', input_summary: '/x' })
    expect(entries).toHaveLength(1)
    expect(entries[0].kind).toBe('tool_use')
    expect(entries[0].title).toBe('🔧 Read')
  })
})
