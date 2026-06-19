import { describe, expect, it } from 'vitest'
import { apiRoutes, withWorkspace, workspaceQuery } from './api-routes'

describe('apiRoutes', () => {
  it('keeps paths relative and encodes path parameters', () => {
    expect(apiRoutes.repos.binding('acme org', 'agent/repo', 'bind/1', { workspace: 'team a' }))
      .toBe('/repos/acme%20org/agent%2Frepo/bindings/bind%2F1?workspace=team+a')
    expect(apiRoutes.traces.prompt('span/1')).toBe('/traces/span%2F1/prompt')
  })

  it('builds query strings with workspace composition', () => {
    expect(apiRoutes.runners.list({ workspace: 'team-a', status: 'running', limit: 25, offset: 50 }))
      .toBe('/runners?status=running&limit=25&offset=50&workspace=team-a')
    expect(withWorkspace('/events?since=2026-01-01T00%3A00%3A00Z', 'demo'))
      .toBe('/events?since=2026-01-01T00%3A00%3A00Z&workspace=demo')
    expect(workspaceQuery('')).toBe('workspace=default')
  })

  it('skips empty optional filters', () => {
    expect(apiRoutes.improvements.feedback({ workspace: 'default', offset: 0, limit: undefined }))
      .toBe('/improvements/feedback?offset=0&workspace=default')
    expect(apiRoutes.catalog.prompts.list({ workspace: '' })).toBe('/prompts')
  })
})
