import { describe, expect, it } from 'vitest'
import { Binding, bindingsEqual, groupByAgent } from './bindings'

describe('bindingsEqual', () => {
  it('ignores id differences — the diff drives PATCH decisions on edits', () => {
    const a: Binding = { id: 1, agent: 'coder', labels: ['ai:x'] }
    const b: Binding = { id: 2, agent: 'coder', labels: ['ai:x'] }
    expect(bindingsEqual(a, b)).toBe(true)
  })

  it('treats undefined and true enabled as equivalent', () => {
    const a: Binding = { agent: 'coder', cron: '* * * * *' }
    const b: Binding = { agent: 'coder', cron: '* * * * *', enabled: true }
    expect(bindingsEqual(a, b)).toBe(true)
  })

  it('distinguishes explicit false from default true', () => {
    const a: Binding = { agent: 'coder', cron: '* * * * *' }
    const b: Binding = { agent: 'coder', cron: '* * * * *', enabled: false }
    expect(bindingsEqual(a, b)).toBe(false)
  })

  it('compares labels element-wise', () => {
    const a: Binding = { agent: 'coder', labels: ['a', 'b'] }
    const b: Binding = { agent: 'coder', labels: ['a', 'c'] }
    expect(bindingsEqual(a, b)).toBe(false)
  })

  it('returns false when agents differ', () => {
    expect(bindingsEqual(
      { agent: 'a', labels: ['x'] },
      { agent: 'b', labels: ['x'] },
    )).toBe(false)
  })
})

describe('groupByAgent', () => {
  it('preserves first-seen order and groups by agent', () => {
    const got = groupByAgent([
      { agent: 'b', labels: ['x'] },
      { agent: 'a', labels: ['y'] },
      { agent: 'b', cron: '* * * * *' },
    ])
    expect(got.map(([a]) => a)).toEqual(['b', 'a'])
    expect(got[0][1]).toHaveLength(2)
  })
})
