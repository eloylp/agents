import { describe, expect, it } from 'vitest'
import { budgetScopeDescription, budgetScopeLabel, isGlobalSimpleBudgetScope } from './budget-copy'

describe('budget scope copy', () => {
  it('labels simple repo agent and backend scopes as global across workspaces', () => {
    expect(budgetScopeLabel({ scope_kind: 'repo', repo: 'owner/repo' })).toBe('Repo: owner/repo (global across workspaces)')
    expect(budgetScopeLabel({ scope_kind: 'agent', agent: 'coder' })).toBe('Agent: coder (global across workspaces)')
    expect(budgetScopeLabel({ scope_kind: 'backend', backend: 'codex' })).toBe('Backend: codex (global across workspaces)')
  })

  it('describes workspace-qualified scopes as isolated to one workspace', () => {
    expect(budgetScopeDescription('workspace+repo')).toContain('inside one workspace')
    expect(budgetScopeDescription('workspace+agent')).toContain('inside one workspace')
    expect(budgetScopeDescription('workspace+backend')).toContain('inside one workspace')
  })

  it('distinguishes global simple scopes from workspace scopes', () => {
    expect(isGlobalSimpleBudgetScope('repo')).toBe(true)
    expect(isGlobalSimpleBudgetScope('agent')).toBe(true)
    expect(isGlobalSimpleBudgetScope('backend')).toBe(true)
    expect(isGlobalSimpleBudgetScope('workspace+repo')).toBe(false)
    expect(isGlobalSimpleBudgetScope('workspace')).toBe(false)
  })
})
