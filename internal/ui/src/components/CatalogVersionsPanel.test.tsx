import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import CatalogVersionsPanel from './CatalogVersionsPanel'

describe('<CatalogVersionsPanel />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('shows reference warnings for live tracking refs', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
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
	            { kind: 'agent', workspace_id: 'default', name: 'tracker-a', tracking: true },
	            { kind: 'agent', workspace_id: 'team-a', name: 'tracker-b', tracking: true },
	          ]),
	        } as Response)
	      }
	      return Promise.resolve({ ok: false, status: 404, text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<CatalogVersionsPanel type="prompt" assetID="prompt-a" currentVersionID="v2" />)

    await screen.findByText('v1')
    expect(screen.getByText('+inserted')).toBeInTheDocument()

    fireEvent.click(screen.getByText('v1'))

	    expect(await screen.findByText('Changes to this catalog item can affect 2 live references.')).toBeInTheDocument()
	    expect(screen.queryByRole('button', { name: /Upgrade/ })).not.toBeInTheDocument()
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

  it('renders first guardrail versions with settings in the diff', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/guardrails/default%2Fguardrails%2Fsecurity/versions') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'guardrailv1',
              version: 1,
              state: 'published',
              description: 'Security checks',
              content: 'Never expose secrets.',
              enabled: false,
              position: 7,
            },
          ]),
        } as Response)
      }
      if (url === '/guardrails/default%2Fguardrails%2Fsecurity/versions/guardrailv1/references') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404 } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<CatalogVersionsPanel type="guardrail" assetID="default/guardrails/security" currentVersionID="guardrailv1" />)

    expect(await screen.findByText('Initial content and settings')).toBeInTheDocument()
    expect(await screen.findByText('+description: Security checks')).toBeInTheDocument()
    expect(screen.getByText('+Never expose secrets.')).toBeInTheDocument()
    expect(screen.getByText('+enabled: false')).toBeInTheDocument()
    expect(screen.getByText('+position: 7')).toBeInTheDocument()
  })
})
