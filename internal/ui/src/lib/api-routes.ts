import { defaultWorkspaceID } from './workspace-constants'

type QueryValue = string | number | boolean | null | undefined
type Query = Record<string, QueryValue>

function cleanQuery(query?: Query): URLSearchParams {
  const params = new URLSearchParams()
  if (!query) return params
  Object.entries(query).forEach(([key, value]) => {
    if (value === undefined || value === null || value === '') return
    params.set(key, String(value))
  })
  return params
}

function path(base: string, query?: Query): string {
  const params = cleanQuery(query)
  const suffix = params.toString()
  return suffix ? `${base}?${suffix}` : base
}

function appendQuery(base: string, query?: Query): string {
  const params = cleanQuery(query)
  const suffix = params.toString()
  if (!suffix) return base
  return `${base}${base.includes('?') ? '&' : '?'}${suffix}`
}

function enc(value: string | number): string {
  return encodeURIComponent(String(value))
}

export function workspaceQuery(workspace: string): string {
  const id = workspace.trim() || defaultWorkspaceID
  return cleanQuery({ workspace: id }).toString()
}

export function withWorkspace(base: string, workspace: string): string {
  const id = workspace.trim() || defaultWorkspaceID
  return appendQuery(base, { workspace: id })
}

function scopedList(base: string, query?: Query & { workspace?: string }): string {
  const { workspace, ...rest } = query ?? {}
  return workspace ? withWorkspace(path(base, rest), workspace) : path(base, rest)
}

function catalogRoutes(assetPath: 'prompts' | 'skills' | 'guardrails') {
  return {
    list: (query?: Query) => path(`/${assetPath}`, query),
    one: (id: string) => `/${assetPath}/${enc(id)}`,
    versions: (id: string) => `/${assetPath}/${enc(id)}/versions`,
    versionReferences: (id: string, versionID: string) => `/${assetPath}/${enc(id)}/versions/${enc(versionID)}/references`,
  }
}

export const apiRoutes = {
  status: () => '/status',
  run: () => '/run',
  config: () => '/config',
  runtime: () => '/runtime',
  exportConfig: () => '/export',
  importConfig: (query?: Query) => path('/import', query),
  runners: {
    list: (query?: Query & { workspace?: string; status?: string; limit?: number; offset?: number }) => scopedList('/runners', query),
    one: (id: string | number) => `/runners/${enc(id)}`,
    retry: (id: string | number) => `/runners/${enc(id)}/retry`,
  },
  agents: {
    list: (query?: Query & { workspace?: string; limit?: number; offset?: number }) => scopedList('/agents', query),
    one: (name: string, query?: Query & { workspace?: string }) => scopedList(`/agents/${enc(name)}`, query),
    orphansStatus: () => '/agents/orphans/status',
  },
  repos: {
    list: (query?: Query & { workspace?: string; limit?: number; offset?: number }) => scopedList('/repos', query),
    one: (owner: string, repo: string, query?: { workspace?: string }) => scopedList(`/repos/${enc(owner)}/${enc(repo)}`, query),
    bindings: (owner: string, repo: string, query?: { workspace?: string }) => scopedList(`/repos/${enc(owner)}/${enc(repo)}/bindings`, query),
    binding: (owner: string, repo: string, bindingID: string | number, query?: { workspace?: string }) => scopedList(`/repos/${enc(owner)}/${enc(repo)}/bindings/${enc(bindingID)}`, query),
  },
  workspaces: {
    list: (query?: Query) => path('/workspaces', query),
    one: (id: string) => `/workspaces/${enc(id)}`,
    runtime: (id: string) => `/workspaces/${enc(id)}/runtime`,
    guardrails: (id: string) => `/workspaces/${enc(id)}/guardrails`,
  },
  catalog: {
    prompts: catalogRoutes('prompts'),
    skills: catalogRoutes('skills'),
    guardrails: {
      ...catalogRoutes('guardrails'),
      reset: (id: string) => `/guardrails/${enc(id)}/reset`,
    },
  },
  graph: {
    view: (query?: { workspace?: string }) => scopedList('/graph', query),
    layout: (query?: { workspace?: string }) => scopedList('/graph/layout', query),
  },
  improvements: {
    feedback: (query?: Query & { workspace?: string }) => scopedList('/improvements/feedback', query),
    recommendations: (query?: Query & { workspace?: string }) => scopedList('/improvements/recommendations', query),
    recommendation: (id: string) => `/improvements/recommendations/${enc(id)}`,
    recommendationStatus: (id: string) => `/improvements/recommendations/${enc(id)}/status`,
    clarification: (id: string) => `/improvements/recommendations/${enc(id)}/clarification`,
    analyzeFeedback: (feedbackID: string | number) => `/improvements/feedback/${enc(feedbackID)}/analyze`,
    bundleItem: (bundleID: string, itemID: string) => `/improvements/proposal-bundles/${enc(bundleID)}/items/${enc(itemID)}`,
    rejectBundleItem: (bundleID: string, itemID: string) => `/improvements/proposal-bundles/${enc(bundleID)}/items/${enc(itemID)}/reject`,
    linkExistingBundleItem: (bundleID: string, itemID: string) => `/improvements/proposal-bundles/${enc(bundleID)}/items/${enc(itemID)}/link-existing`,
    publishBundle: (bundleID: string) => `/improvements/proposal-bundles/${enc(bundleID)}/publish`,
    discardBundle: (bundleID: string) => `/improvements/proposal-bundles/${enc(bundleID)}/discard`,
  },
  traces: {
    list: (query?: Query & { workspace?: string }) => scopedList('/traces', query),
    prompt: (spanID: string) => `/traces/${enc(spanID)}/prompt`,
    steps: (spanID: string) => `/traces/${enc(spanID)}/steps`,
    stream: (query?: { workspace?: string }) => scopedList('/traces/stream', query),
    spanStream: (spanID: string) => `/traces/${enc(spanID)}/stream`,
  },
  events: {
    list: (query?: Query & { workspace?: string }) => scopedList('/events', query),
    stream: (query?: { workspace?: string }) => scopedList('/events/stream', query),
  },
  auth: {
    status: () => '/auth/status',
    users: (query?: Query) => path('/auth/users', query),
    user: (id: string | number) => `/auth/users/${enc(id)}`,
    tokens: (query?: Query) => path('/auth/tokens', query),
    token: (id: string | number) => `/auth/tokens/${enc(id)}`,
    logout: () => '/auth/logout',
    password: () => '/auth/me/password',
  },
  backends: {
    list: (query?: Query) => path('/backends', query),
    one: (name: string) => `/backends/${enc(name)}`,
    status: () => '/backends/status',
    discover: () => '/backends/discover',
    local: () => '/backends/local',
  },
  tokenBudgets: {
    list: (query?: Query) => path('/token_budgets', query),
    one: (id: string | number) => `/token_budgets/${enc(id)}`,
    leaderboard: (query?: Query & { workspace?: string }) => scopedList('/token_leaderboard', query),
    alerts: () => '/token_budgets/alerts',
  },
  memory: {
    stream: (query?: { workspace?: string }) => scopedList('/memory/stream', query),
    one: (agent: string, repoKey: string, query?: { workspace?: string }) => scopedList(`/memory/${enc(agent)}/${enc(repoKey)}`, query),
  },
}
