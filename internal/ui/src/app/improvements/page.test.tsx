import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import ImprovementsPage from './page'

vi.mock('@/lib/workspace', () => ({
  useSelectedWorkspace: () => ({
    workspace: 'team-a',
    workspaces: [],
    workspaceNotice: '',
    setWorkspace: vi.fn(),
  }),
  withWorkspace: (path: string, workspace: string) => `${path}?workspace=${encodeURIComponent(workspace)}`,
}))

describe('<ImprovementsPage /> clarification flow', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('loads recommendation detail before opening the clarification modal', async () => {
    const listRecommendation = {
      id: 'rec_1',
      feedback_event_id: 7,
      type: 'patch_prompt',
      status: 'needs_user_input',
      confidence: 'medium',
      risk: 'low',
      finding: 'Clarify the rollout target.',
      normalized_lesson: 'Clarify rollout scope.',
      rationale: 'The recommendation needs maintainer context.',
      attribution_confidence: 'exact',
      target_asset_type: 'prompt',
      target_base_version_id: 'promptver_1',
      updated_at: '2026-06-01T19:00:00Z',
    }
    const detailRecommendation = {
      ...listRecommendation,
      feedback: {
        id: 7,
        workspace: 'team-a',
        repo_owner: 'owner',
        repo_name: 'repo',
        source_type: 'issue_comment',
        source_url: 'https://github.com/owner/repo/issues/7#issuecomment-1',
        author_login: 'maintainer',
        author_authorized: true,
        raw_body: 'Apply this only to refactorer prompts.',
        link_confidence: 'exact',
        linked_agent_name: 'coder',
        linked_prompt_version_id: 'promptver_1',
        status: 'analyzed',
        ingested_at: '2026-06-01T18:00:00Z',
      },
    }
    const fetchMock = vi.fn((url: string) => {
      if (url === '/improvements/feedback?workspace=team-a') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations?workspace=team-a') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([listRecommendation]) } as Response)
      }
      if (url === '/improvements/recommendations/rec_1') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(detailRecommendation) } as Response)
      }
      return Promise.resolve({ ok: false, json: () => Promise.resolve({}) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: 'clarify' }))

    await waitFor(() => {
      expect(fetchMock).toHaveBeenCalledWith('/improvements/recommendations/rec_1', { cache: 'no-store' })
    })
    expect(await screen.findByText('Apply this only to refactorer prompts.')).toBeInTheDocument()
    expect(screen.queryByText('No feedback body available.')).not.toBeInTheDocument()
  })
})
