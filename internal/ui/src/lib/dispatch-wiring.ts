// Pure helpers for editing inter-agent dispatch wiring from the Graph page.
// Exported from src/lib/ so they can be unit-tested without mounting React Flow.

export interface StoreAgent {
  workspace_id?: string
  name: string
  backend: string
  model: string
  skills: string[]
  prompt?: string
  prompt_id?: string
  prompt_ref: string
  prompt_scope?: string
  scope_type: 'workspace' | 'repo' | string
  scope_repo: string
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

export interface DispatchRelationship {
  name: string
  description: string
  allow_dispatch: boolean
  can_dispatch: string[]
  status?: string
}

export function storeAgentFromResponse(data: Partial<StoreAgent>, fallbackName: string): StoreAgent {
  return {
    name: data.name ?? fallbackName,
    backend: data.backend ?? '',
    model: data.model ?? '',
    skills: data.skills ?? [],
    prompt: data.prompt ?? '',
    prompt_id: data.prompt_id ?? '',
    prompt_ref: data.prompt_ref ?? '',
    prompt_scope: data.prompt_scope ?? '',
    scope_type: data.scope_type ?? 'workspace',
    scope_repo: data.scope_repo ?? '',
    allow_prs: data.allow_prs ?? false,
    allow_dispatch: data.allow_dispatch ?? false,
    allow_memory: data.allow_memory ?? true,
    can_dispatch: data.can_dispatch ?? [],
    description: data.description ?? '',
  }
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

export function outgoingDispatchTargets(
  source: { can_dispatch?: string[] },
  agents: DispatchRelationship[],
): DispatchRelationship[] {
  const byName = new Map(agents.map(a => [a.name, a]))
  return (source.can_dispatch ?? [])
    .map(name => byName.get(name))
    .filter((a): a is DispatchRelationship => Boolean(a))
}

export function incomingDispatchSources(
  target: string,
  agents: DispatchRelationship[],
): DispatchRelationship[] {
  return agents.filter(a => (a.can_dispatch ?? []).includes(target))
}

export function availableDispatchTargets(
  source: string,
  existingCanDispatch: string[],
  agents: DispatchRelationship[],
): DispatchRelationship[] {
  return agents.filter(a => a.name !== source && !existingCanDispatch.includes(a.name))
}
