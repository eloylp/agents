export interface BudgetScopeLike {
  scope_kind: string
  scope_name?: string
  workspace_id?: string
  repo?: string
  agent?: string
  backend?: string
}

export const budgetScopeOptions = [
  { value: 'global', label: 'Global', description: 'All workspaces, repos, agents, and backends.' },
  { value: 'workspace', label: 'Workspace', description: 'One workspace and all of its repos, agents, and backends.' },
  { value: 'repo', label: 'Repo (global)', description: 'One repo name across every workspace. Use workspace + repo for workspace isolation.' },
  { value: 'backend', label: 'Backend (global)', description: 'One backend across every workspace. Use workspace + backend for workspace isolation.' },
  { value: 'agent', label: 'Agent (global)', description: 'One agent name across every workspace. Use workspace + agent for workspace isolation.' },
  { value: 'workspace+repo', label: 'Workspace + repo', description: 'One repo inside one workspace.' },
  { value: 'workspace+agent', label: 'Workspace + agent', description: 'One agent inside one workspace.' },
  { value: 'workspace+backend', label: 'Workspace + backend', description: 'One backend inside one workspace.' },
  { value: 'workspace+repo+agent', label: 'Workspace + repo + agent', description: 'One agent/repo pair inside one workspace.' },
] as const

export function budgetScopeLabel(b: BudgetScopeLike) {
  switch (b.scope_kind) {
    case 'global':
      return 'Global: all workspaces'
    case 'workspace':
      return `Workspace: ${b.workspace_id || b.scope_name}`
    case 'repo':
      return `Repo: ${b.repo || b.scope_name} (global across workspaces)`
    case 'agent':
      return `Agent: ${b.agent || b.scope_name} (global across workspaces)`
    case 'backend':
      return `Backend: ${b.backend || b.scope_name} (global across workspaces)`
    case 'workspace+repo':
      return `${b.workspace_id} / ${b.repo}`
    case 'workspace+agent':
      return `${b.workspace_id} / ${b.agent}`
    case 'workspace+backend':
      return `${b.workspace_id} / ${b.backend}`
    case 'workspace+repo+agent':
      return `${b.workspace_id} / ${b.repo} / ${b.agent}`
    default:
      return b.scope_name ? `${b.scope_kind}: ${b.scope_name}` : b.scope_kind
  }
}

export function budgetScopeDescription(scopeKind: string) {
  return budgetScopeOptions.find(option => option.value === scopeKind)?.description ?? ''
}

export function isGlobalSimpleBudgetScope(scopeKind: string) {
  return scopeKind === 'repo' || scopeKind === 'agent' || scopeKind === 'backend'
}
