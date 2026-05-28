import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import CatalogVersionsPanel from './CatalogVersionsPanel'

describe('<CatalogVersionsPanel />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows reference warnings and rolls exact pins forward', async () => {
    const onChanged = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/prompts/prompt-a/versions') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            { id: 'v2', version: 2, state: 'published', content: 'line 1\ninserted\nline 2' },
            { id: 'v1', version: 1, state: 'published', content: 'line 1\nline 2' },
          ]),
        } as Response)
      }
      if (url === '/prompts/prompt-a/versions/v2/references') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([{ kind: 'agent', workspace_id: 'default', name: 'tracker', tracking: true }]),
        } as Response)
      }
      if (url === '/prompts/prompt-a/versions/v1/references') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            { kind: 'agent', workspace_id: 'default', name: 'pinned-a', tracking: false },
            { kind: 'agent', workspace_id: 'team-a', name: 'pinned-b', tracking: false },
          ]),
        } as Response)
      }
      if (url === '/prompts/prompt-a/versions/v1/rollout') {
        expect(init?.method).toBe('POST')
        expect(init?.body).toBe(JSON.stringify({ to_version_id: 'v2' }))
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ updated: 2 }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<CatalogVersionsPanel type="prompt" assetID="prompt-a" currentVersionID="v2" onChanged={onChanged} />)

    await screen.findByText('v1')
    expect(screen.getByText('+inserted')).toBeInTheDocument()

    fireEvent.click(screen.getByText('v1'))

    expect(await screen.findByText('Publishing or rolling out changes can affect 2 live references.')).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Upgrade 2 exact pins to v2' }))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith(
        '/prompts/prompt-a/versions/v1/rollout',
        expect.objectContaining({
          body: JSON.stringify({ to_version_id: 'v2' }),
          method: 'POST',
        }),
      )
      expect(onChanged).toHaveBeenCalled()
    })
  })

  it('surfaces reference loading failures while keeping successful references', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/skills/architect/versions') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            { id: 'skillv2', version: 2, state: 'published', prompt: 'body v2' },
            { id: 'skillv1', version: 1, state: 'published', prompt: 'body v1' },
          ]),
        } as Response)
      }
      if (url === '/skills/architect/versions/skillv2/references') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([{ kind: 'agent', workspace_id: 'default', name: 'tracker', tracking: true }]),
        } as Response)
      }
      if (url === '/skills/architect/versions/skillv1/references') {
        return Promise.resolve({ ok: false, status: 500 } as Response)
      }
      return Promise.resolve({ ok: false, status: 404 } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<CatalogVersionsPanel type="skill" assetID="architect" currentVersionID="skillv1" />)

    expect(await screen.findByText('Error: load references: 500')).toBeInTheDocument()
    expect(await screen.findByText('default/tracker · tracking')).toBeInTheDocument()
  })
})
