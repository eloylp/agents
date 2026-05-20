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
