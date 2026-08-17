import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/MarkdownEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea aria-label="Prompt content editor" value={value} onChange={e => onChange(e.target.value)} />
  ),
}))

vi.mock('@/components/CatalogVersionsPanel', () => ({
  default: ({ assetID }: { assetID: string }) => (
    <div data-testid="catalog-versions">versions for {assetID}</div>
  ),
}))

import PromptsPage from './page'

describe('<PromptsPage />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('uses selector-sized workspace and repo option lookups', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/prompts?limit=50&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            items: [{
              id: 'repo-prompt',
              workspace_id: 'workspace-a',
              repo: 'repo-a',
              name: 'repo prompt',
              description: 'Repo prompt',
              content: 'Use repo context.',
            }],
            total: 1,
            limit: 50,
            offset: 0,
          }),
        } as Response)
      }
      if (url === '/workspaces?limit=500&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            items: [{ id: 'workspace-a', name: 'Workspace A' }],
            total: 1,
            limit: 500,
            offset: 0,
          }),
        } as Response)
      }
      if (url === '/catalog/delegation') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ enabled: false }) } as Response)
      }
      if (url === '/repos?workspace=workspace-a&limit=500&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            items: [{ name: 'repo-a' }, { name: 'repo-b' }],
            total: 2,
            limit: 500,
            offset: 0,
          }),
        } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<PromptsPage />)

    expect(await screen.findByText('repo prompt')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/workspaces?limit=500&offset=0', { cache: 'no-store' })

    fireEvent.change(screen.getByDisplayValue('All scopes'), { target: { value: 'repo' } })
    fireEvent.change(await screen.findByDisplayValue('All workspaces'), { target: { value: 'workspace-a' } })

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/repos?workspace=workspace-a&limit=500&offset=0', { cache: 'no-store' }))

    fireEvent.click(screen.getByRole('button', { name: '+ New prompt' }))
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Global'), { target: { value: 'repo' } })
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Select workspace...'), { target: { value: 'workspace-a' } })

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/repos?workspace=workspace-a&limit=500&offset=0', { cache: 'no-store' }))
  })

  it('links delegated users to the configured catalog file', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/prompts?limit=50&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            items: [{ id: 'delegated-prompt', name: 'delegated prompt', description: '', content: 'body' }],
            total: 1,
            limit: 50,
            offset: 0,
          }),
        } as Response)
      }
      if (url === '/workspaces?limit=500&offset=0') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [{ id: 'team-a', name: 'Team A' }], total: 1, limit: 500, offset: 0 }) } as Response)
      }
      if (url === '/catalog/delegation') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ enabled: true, repo: 'acme/catalog', branch: 'main', catalog_path: 'catalog.yml' }),
        } as Response)
      }
      if (url === '/prompts/delegated-prompt/scope') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'delegated-prompt', workspace_id: 'team-a', name: 'delegated prompt', description: '', content: 'body' }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<PromptsPage />)

    const link = await screen.findByRole('link', { name: 'catalog.yml' })
    expect(link).toHaveAttribute('href', 'https://github.com/acme/catalog/blob/main/catalog.yml')
    expect(screen.getByRole('button', { name: '+ New prompt' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Scope' }))
    expect(screen.queryByTestId('catalog-versions')).not.toBeInTheDocument()
    expect(screen.getByText('Content versions are managed in the delegated GitHub catalog while delegation is enabled.')).toBeInTheDocument()
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Global'), { target: { value: 'workspace' } })
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Select workspace...'), { target: { value: 'team-a' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/prompts/delegated-prompt/scope', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ scope: 'workspace', workspace_id: 'team-a' }),
    })))
  })
})
