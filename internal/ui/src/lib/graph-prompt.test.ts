import { describe, expect, it } from 'vitest'
import { graphPromptIdentifier, resolveGraphPrompt, type GraphPromptItem } from './graph-prompt'

const prompts: GraphPromptItem[] = [
  { id: 'global_review', name: 'review', content: 'global' },
  { id: 'workspace_review', name: 'review', workspace_id: 'default', content: 'workspace' },
  { id: 'repo_review', name: 'review', workspace_id: 'default', repo: 'owner/repo', content: 'repo' },
]

describe('resolveGraphPrompt', () => {
  it('resolves a visible prompt by stable prompt id', () => {
    const prompt = resolveGraphPrompt({ prompt_id: 'repo_review', scope_type: 'repo', scope_repo: 'owner/repo' }, prompts, 'default')

    expect(prompt?.content).toBe('repo')
    expect(prompt ? graphPromptIdentifier(prompt) : '').toBe('repo_review')
  })

  it('rejects repo-scoped prompts outside the agent visible catalog scope', () => {
    const prompt = resolveGraphPrompt({ prompt_id: 'repo_review', scope_type: 'repo', scope_repo: 'owner/other' }, prompts, 'default')

    expect(prompt).toBeNull()
  })

  it('resolves legacy prompt ref and scope selectors', () => {
    const prompt = resolveGraphPrompt({ prompt_ref: 'review', prompt_scope: 'default' }, prompts, 'default')

    expect(prompt?.content).toBe('workspace')
  })
})
