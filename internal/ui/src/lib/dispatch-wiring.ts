// Pure helpers for editing inter-agent dispatch wiring from the Graph page.
// Exported from src/lib/ so they can be unit-tested without mounting React Flow.

export interface StoreAgent {
  name: string
  backend: string
  model: string
  skills: string[]
  prompt: string
  allow_prs: boolean
  allow_dispatch: boolean
  allow_memory: boolean
  can_dispatch: string[]
  description: string
}

export interface ConnectionCheck {
  ok: boolean
  reason?: string
}

export function validateConnection(
  source: string,
  target: string,
  existingCanDispatch: string[],
): ConnectionCheck {
  if (!source || !target) {
    return { ok: false, reason: 'both agents are required' }
  }
  if (source === target) {
    return { ok: false, reason: 'self-dispatch is not allowed' }
  }
  if (existingCanDispatch.includes(target)) {
    return { ok: false, reason: 'edge already exists' }
  }
  return { ok: true }
}

export function addCanDispatch(agent: StoreAgent, target: string): StoreAgent {
  if (agent.can_dispatch.includes(target)) return agent
  return { ...agent, can_dispatch: [...agent.can_dispatch, target] }
}

export function removeCanDispatch(agent: StoreAgent, target: string): StoreAgent {
  if (!agent.can_dispatch.includes(target)) return agent
  return { ...agent, can_dispatch: agent.can_dispatch.filter(t => t !== target) }
}

export function enableAllowDispatch(agent: StoreAgent): StoreAgent {
  if (agent.allow_dispatch) return agent
  return { ...agent, allow_dispatch: true }
}
