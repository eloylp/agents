import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ConfigPage from './page'

vi.mock('@/lib/workspace', () => ({
  defaultWorkspaceID: 'default',
  useSelectedWorkspace: () => ({
    workspace: 'team-a',
    workspaces: [],
    workspaceNotice: '',
    setWorkspace: vi.fn(),
  }),
  withWorkspace: (path: string, workspace: string) => `${path}?workspace=${encodeURIComponent(workspace)}`,
}))

describe('<ConfigPage /> runtime workspace overrides', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState(null, '', '/')
  })

  it('switches workspace override inputs and saves to the selected workspace endpoint', async () => {
    window.history.replaceState(null, '', '/ui/config?tab=runtime')

    const fetchMock = vi.fn((url: string, init?: RequestInit) => {
      if (url === '/config') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            workspaces: [
              { id: 'team-a', name: 'Team A', runner_image: 'ghcr.io/example/team-a:v1' },
              { id: 'team-b', name: 'Team B', runner_image: 'ghcr.io/example/team-b:v1' },
            ],
          }),
        } as Response)
      }
      if (url === '/runtime') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ runner_image: 'ghcr.io/example/global:v1', constraints: {} }),
        } as Response)
      }
      if (url === '/workspaces/team-b/runtime' && init?.method === 'PUT') {
        expect(JSON.parse(String(init.body))).toEqual({ runner_image: 'ghcr.io/example/team-b:v2' })
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({ id: 'team-b', name: 'Team B', runner_image: 'ghcr.io/example/team-b:v2' }),
        } as Response)
      }
      return Promise.resolve({
        ok: false,
        text: () => Promise.resolve(`unexpected request: ${url}`),
        json: () => Promise.resolve({}),
      } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ConfigPage />)

    const workspaceSelect = await screen.findByLabelText('Workspace')
    await waitFor(() => {
      expect(screen.getByLabelText('Runner image for Team A')).toHaveValue('ghcr.io/example/team-a:v1')
    })

    fireEvent.change(workspaceSelect, { target: { value: 'team-b' } })

    const teamBInput = await screen.findByLabelText('Runner image for Team B')
    expect(teamBInput).toHaveValue('ghcr.io/example/team-b:v1')

    fireEvent.change(teamBInput, { target: { value: 'ghcr.io/example/team-b:v2' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save override for Team B' }))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith('/workspaces/team-b/runtime', expect.objectContaining({ method: 'PUT' }))
    })
    expect(await screen.findByText('Workspace runner override saved for Team B.')).toBeInTheDocument()
  })
})

describe('<ConfigPage /> backend pagination', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.history.replaceState(null, '', '/')
  })

  it('paginates the backend admin list while loading selector options separately', async () => {
    window.history.replaceState(null, '', '/ui/config?tab=backends')

    const backendPage = (offset: number) => ({
      items: [{
        name: offset === 0 ? 'backend-001' : 'backend-051',
        command: 'codex',
        healthy: true,
        timeout_seconds: 600,
        max_prompt_chars: 12000,
      }],
      total: 75,
      limit: 50,
      offset,
    })
    const fetchMock = vi.fn((url: string) => {
      if (url === '/config') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      if (url === '/backends?limit=50&offset=0') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(backendPage(0)) } as Response)
      }
      if (url === '/backends?limit=50&offset=50') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(backendPage(50)) } as Response)
      }
      if (url === '/backends?limit=500&offset=0') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            items: [
              backendPage(0).items[0],
              backendPage(50).items[0],
              { name: 'selector-only', command: 'claude', healthy: true, timeout_seconds: 600, max_prompt_chars: 12000 },
            ],
            total: 75,
            limit: 500,
            offset: 0,
          }),
        } as Response)
      }
      if (url === '/backends/status') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ backends: [], tools: [] }) } as Response)
      }
      if (url === '/agents/orphans/status') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ count: 0, agents: [] }) } as Response)
      }
      return Promise.resolve({
        ok: false,
        text: () => Promise.resolve(`unexpected request: ${url}`),
        json: () => Promise.resolve({}),
      } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ConfigPage />)

    expect(await screen.findByText('backend-001')).toBeInTheDocument()
    expect(screen.getAllByText('1-50 of 75')).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledWith('/backends?limit=500&offset=0')

    fireEvent.click(screen.getAllByRole('button', { name: 'Next' })[0])

    expect(await screen.findByText('backend-051')).toBeInTheDocument()
    expect(screen.getAllByText('51-75 of 75')).toHaveLength(2)
    expect(fetchMock).toHaveBeenCalledWith('/backends?limit=50&offset=50')
  })
})
