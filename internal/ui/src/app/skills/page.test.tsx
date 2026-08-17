import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/MarkdownEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea aria-label="Skill prompt editor" value={value} onChange={e => onChange(e.target.value)} />
  ),
}))

vi.mock('@/components/CatalogVersionsPanel', () => ({
  default: ({ assetID }: { assetID: string }) => (
    <div data-testid="catalog-versions">versions for {assetID}</div>
  ),
}))

import SkillsPage from './page'

describe('<SkillsPage />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('keeps long stable skill ids in tooltips instead of row labels', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/skills?limit=50&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'go-api',
              name: 'go api boundaries',
              prompt: 'Keep handlers thin.',
              version_id: 'skillver-go-api',
              version: 3,
            },
            {
              id: 'testing',
              name: 'testing',
              prompt: 'Run focused tests.',
              version_id: 'skillver-testing',
              version: 1,
            },
          ]),
        } as Response)
      }
      if (url === '/workspaces?limit=500&offset=0') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'default', name: 'Default' }]) } as Response)
      }
      if (url === '/catalog/delegation') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ enabled: false }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<SkillsPage />)

    expect(await screen.findByText('go api boundaries')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/workspaces?limit=500&offset=0', { cache: 'no-store' })
    expect(screen.getByText('testing')).toBeInTheDocument()
    expect(screen.queryByText('go-api · go api boundaries')).not.toBeInTheDocument()
    expect(screen.getByText('go api boundaries')).toHaveAttribute('title', 'go api boundaries · go-api')

    const goAPI = screen.getByText('go api boundaries').closest('div')
    if (!goAPI) throw new Error('go-api skill row not found')
    fireEvent.click(within(goAPI.parentElement?.parentElement ?? goAPI).getByRole('button', { name: 'Edit' }))

    expect(await screen.findByText('Edit, go api boundaries')).toBeInTheDocument()
    expect(screen.getByText('Stable id')).toBeInTheDocument()
    expect(screen.getByText('go-api')).toBeInTheDocument()
    expect(screen.getByTestId('catalog-versions')).toHaveTextContent('versions for go-api')

    fireEvent.change(screen.getByLabelText('Skill prompt editor'), { target: { value: 'Updated guidance.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/skills/go-api',
      expect.objectContaining({ method: 'PATCH' }),
    ))
  }, 10000)

  it('links delegated users to the configured catalog file', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/skills?limit=50&offset=0') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ items: [{ id: 'delegated-skill', name: 'delegated skill', prompt: 'body' }], total: 1, limit: 50, offset: 0 }) } as Response)
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
      if (url === '/skills/delegated-skill/scope') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'delegated-skill', workspace_id: 'team-a', name: 'delegated skill', prompt: 'body' }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<SkillsPage />)

    const link = await screen.findByRole('link', { name: 'catalog.yml' })
    expect(link).toHaveAttribute('href', 'https://github.com/acme/catalog/blob/main/catalog.yml')
    expect(screen.getByRole('button', { name: '+ Create skill' })).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Scope' }))
    expect(screen.queryByTestId('catalog-versions')).not.toBeInTheDocument()
    expect(screen.getByText('Content versions are managed in the delegated GitHub catalog while delegation is enabled.')).toBeInTheDocument()
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Global'), { target: { value: 'workspace' } })
    fireEvent.change(within(screen.getByRole('dialog')).getByDisplayValue('Select workspace...'), { target: { value: 'team-a' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/skills/delegated-skill/scope', expect.objectContaining({
      method: 'PATCH',
      body: JSON.stringify({ scope: 'workspace', workspace_id: 'team-a' }),
    })))
  })
})
