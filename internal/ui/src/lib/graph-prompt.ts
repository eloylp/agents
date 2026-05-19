import { catalogScope, catalogValue, defaultWorkspaceID, visibleCatalogItems, type CatalogItem } from '@/lib/workspace'

export interface GraphPromptItem extends CatalogItem {
  description?: string
  content?: string
}

export interface GraphPromptAgent {
  prompt_id?: string
  prompt_ref?: string
  prompt_scope?: string
  scope_type?: string
  scope_repo?: string
}

export function resolveGraphPrompt(agent: GraphPromptAgent, prompts: GraphPromptItem[], workspace: string): GraphPromptItem | null {
  const repo = agent.scope_type === 'repo' ? (agent.scope_repo ?? '') : ''
  const visiblePrompts = visibleCatalogItems(prompts, workspace, repo)
  const promptID = (agent.prompt_id ?? '').trim()
  if (promptID) {
    return visiblePrompts.find(prompt => catalogValue(prompt) === promptID) ?? null
  }

  const promptRef = (agent.prompt_ref ?? '').trim()
  if (!promptRef) return null

  const promptScope = (agent.prompt_scope ?? '').trim()
  if (promptScope) {
    return visiblePrompts.find(prompt => prompt.name === promptRef && catalogScope(prompt) === promptScope) ?? null
  }

  return visiblePrompts.find(prompt => prompt.name === promptRef && (!prompt.workspace_id || prompt.workspace_id === workspace || prompt.workspace_id === defaultWorkspaceID)) ?? null
}

export function graphPromptIdentifier(prompt: GraphPromptItem): string {
  return catalogValue(prompt)
}
