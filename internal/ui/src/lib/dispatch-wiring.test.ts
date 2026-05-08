import { describe, expect, it } from 'vitest'
import {
  addCanDispatch,
  availableDispatchTargets,
  enableAllowDispatch,
  incomingDispatchSources,
  outgoingDispatchTargets,
  removeCanDispatch,
  storeAgentFromResponse,
  validateConnection,
  type DispatchRelationship,
  type StoreAgent,
} from './dispatch-wiring'

function makeAgent(overrides: Partial<StoreAgent> = {}): StoreAgent {
  return {
    name: 'a1',
    backend: 'claude',
    model: '',
    skills: [],
    prompt: 'do the thing',
    allow_prs: false,
    allow_dispatch: false,
    allow_memory: true,
    can_dispatch: [],
    description: '',
    ...overrides,
  }
}

describe('validateConnection', () => {
  it('normalizes store agent responses with the backend canonical name', () => {
    const agent = storeAgentFromResponse({
      name: 'canonical-agent',
      skills: ['go-testing'],
      allow_dispatch: true,
      can_dispatch: ['reviewer'],
    }, ' user supplied ')

    expect(agent).toEqual({
      name: 'canonical-agent',
      backend: '',
      model: '',
      skills: ['go-testing'],
      prompt: '',
      allow_prs: false,
      allow_dispatch: true,
      allow_memory: true,
      can_dispatch: ['reviewer'],
      description: '',
    })
  })

  it('falls back to the requested name when a store agent response omits it', () => {
    const agent = storeAgentFromResponse({}, 'fallback-agent')

    expect(agent.name).toBe('fallback-agent')
    expect(agent.allow_memory).toBe(true)
    expect(agent.skills).toEqual([])
    expect(agent.can_dispatch).toEqual([])
  })

  it('accepts a connection between two distinct agents', () => {
    expect(validateConnection('a1', 'a2', [])).toEqual({ ok: true })
  })

  it('rejects self-dispatch', () => {
    const res = validateConnection('a1', 'a1', [])
    expect(res.ok).toBe(false)
    expect(res.reason).toMatch(/self-dispatch/)
  })

  it('rejects missing source or target', () => {
    expect(validateConnection('', 'a2', []).ok).toBe(false)
    expect(validateConnection('a1', '', []).ok).toBe(false)
  })

  it('rejects duplicate edges', () => {
    const res = validateConnection('a1', 'a2', ['a2'])
    expect(res.ok).toBe(false)
    expect(res.reason).toMatch(/already exists/)
  })
})

describe('addCanDispatch', () => {
  it('appends the target to can_dispatch', () => {
    const a = makeAgent({ can_dispatch: ['existing'] })
    const out = addCanDispatch(a, 'new')
    expect(out.can_dispatch).toEqual(['existing', 'new'])
  })

  it('is a no-op when target is already present', () => {
    const a = makeAgent({ can_dispatch: ['existing'] })
    const out = addCanDispatch(a, 'existing')
    // Identity preserved so React doesn't re-render unnecessarily.
    expect(out).toBe(a)
  })

  it('does not mutate the original agent', () => {
    const a = makeAgent({ can_dispatch: ['existing'] })
    addCanDispatch(a, 'new')
    expect(a.can_dispatch).toEqual(['existing'])
  })
})

describe('removeCanDispatch', () => {
  it('removes the target from can_dispatch', () => {
    const a = makeAgent({ can_dispatch: ['a', 'b', 'c'] })
    const out = removeCanDispatch(a, 'b')
    expect(out.can_dispatch).toEqual(['a', 'c'])
  })

  it('is a no-op when target is not present', () => {
    const a = makeAgent({ can_dispatch: ['a', 'b'] })
    const out = removeCanDispatch(a, 'c')
    expect(out).toBe(a)
  })

  it('does not mutate the original agent', () => {
    const a = makeAgent({ can_dispatch: ['a', 'b'] })
    removeCanDispatch(a, 'a')
    expect(a.can_dispatch).toEqual(['a', 'b'])
  })
})

describe('enableAllowDispatch', () => {
  it('sets allow_dispatch to true when currently false', () => {
    const a = makeAgent({ allow_dispatch: false })
    const out = enableAllowDispatch(a)
    expect(out.allow_dispatch).toBe(true)
  })

  it('is a no-op when already true', () => {
    const a = makeAgent({ allow_dispatch: true })
    const out = enableAllowDispatch(a)
    expect(out).toBe(a)
  })
})

function makeRelationship(overrides: Partial<DispatchRelationship>): DispatchRelationship {
  return {
    name: 'agent',
    description: 'does work',
    allow_dispatch: true,
    can_dispatch: [],
    status: 'idle',
    ...overrides,
  }
}

describe('dispatch relationship derivation', () => {
  const agents = [
    makeRelationship({ name: 'coder', can_dispatch: ['reviewer', 'missing'] }),
    makeRelationship({ name: 'reviewer', can_dispatch: ['docs'] }),
    makeRelationship({ name: 'docs', allow_dispatch: false }),
  ]

  it('lists outgoing targets that exist', () => {
    expect(outgoingDispatchTargets({ can_dispatch: ['reviewer', 'missing'] }, agents).map(a => a.name)).toEqual(['reviewer'])
  })

  it('lists incoming sources for a target', () => {
    expect(incomingDispatchSources('docs', agents).map(a => a.name)).toEqual(['reviewer'])
  })

  it('lists available targets excluding self and duplicates', () => {
    expect(availableDispatchTargets('coder', ['reviewer'], agents).map(a => a.name)).toEqual(['docs'])
  })
})
