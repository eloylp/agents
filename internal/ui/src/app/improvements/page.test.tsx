import { fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ImprovementsPage from './page'

describe('<ImprovementsPage />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.clear()
  })

  it('renders proposal review metadata and diff for accepted catalog recommendations', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/workspaces') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'default', name: 'Default' }]) } as Response)
      }
      if (url === '/improvements/feedback?workspace=default') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations?workspace=default') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-1',
              feedback_event_id: 7,
              type: 'prompt_guidance',
              status: 'accepted',
              confidence: 'high',
              risk: 'low',
              finding: 'Tighten prompt',
              normalized_lesson: 'Use concrete language.',
              rationale: 'The linked review asked for sharper guidance.',
              attribution_confidence: 'exact',
              target_asset_type: 'prompt',
              target_base_version_id: 'promptver-1',
              proposed_new_body: 'new body',
              updated_at: '2026-06-01T18:00:00Z',
              feedback: {
                id: 7,
                workspace: 'default',
                repo_owner: 'acme',
                repo_name: 'repo',
                source_type: 'issue_comment',
                source_url: 'https://example.test/feedback',
                author_login: 'maintainer',
                author_authorized: true,
                raw_body: 'please improve this',
                link_confidence: 'exact',
                status: 'processed',
                ingested_at: '2026-06-01T17:00:00Z',
              },
            },
            {
              id: 'rec-2',
              feedback_event_id: 8,
              type: 'split_agent',
              status: 'accepted',
              confidence: 'medium',
              risk: 'medium',
              finding: 'Split broad agent',
              normalized_lesson: 'Separate concerns.',
              rationale: 'This is design work, not a catalog body edit.',
              attribution_confidence: 'inferred',
              updated_at: '2026-06-01T18:05:00Z',
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-1/proposal') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              recommendation_id: 'rec-1',
              target_asset_type: 'prompt',
              target_asset_id: 'prompt-a',
              base_version_id: 'promptver-1',
              base_version: { id: 'promptver-1', version: 1, state: 'published', description: 'desc', content: 'old body' },
              version: {
                id: 'promptver-2',
                version: 2,
                state: 'proposal',
                description: 'desc',
                content: 'new body',
                source_type: 'feedback_recommendation',
                source_ref: 'rec-1',
                author: 'agents-assistant',
                changelog: 'The linked review asked for sharper guidance.',
                base_version_id: 'promptver-1',
              },
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-2/proposal') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: 'recommendations' }))

    expect(await screen.findByText('Target prompt/prompt-a')).toBeInTheDocument()
    expect(screen.getByText('Base v1')).toBeInTheDocument()
    expect(screen.getByText('Proposal v2')).toBeInTheDocument()
    expect(screen.getByText('State proposal')).toBeInTheDocument()
    expect(screen.getByText('Source feedback_recommendation rec-1')).toBeInTheDocument()
    expect(screen.getByText('Design recommendation only in v1.')).toBeInTheDocument()
    const diff = screen.getByLabelText('Proposal diff for rec-1')
    expect(within(diff).getByText('-old body')).toBeInTheDocument()
    expect(within(diff).getByText('+new body')).toBeInTheDocument()
  })
})
