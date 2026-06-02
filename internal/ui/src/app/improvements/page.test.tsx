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

  it('renders editable proposal bundle items and posts item decisions', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
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
              id: 'rec-bundle',
              feedback_event_id: 9,
              type: 'catalog_patch_bundle',
              status: 'accepted',
              confidence: 'high',
              risk: 'medium',
              finding: 'Coordinate catalog updates',
              normalized_lesson: 'Use a bundle.',
              rationale: 'Multiple catalog assets need one review.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-01T18:10:00Z',
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-bundle/proposal') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations/rec-bundle/proposal-bundle') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            id: 'bundle-1',
            recommendation_id: 'rec-bundle',
            status: 'pending',
            items: [
              {
                id: 'item-guard',
                operation: 'update_existing',
                asset_type: 'guardrail',
                asset_id: 'guardrail-existing',
                base_version_id: 'guardver-1',
                proposed_body: 'guard v2',
                proposed_description: 'guard desc v2',
                proposed_enabled: false,
                proposed_position: 22,
                analyst_proposed_body: 'guard analyst body',
                decision: 'accepted',
                base_version: {
                  id: 'guardver-1',
                  version: 1,
                  state: 'published',
                  description: 'guard desc v1',
                  content: 'guard v1',
                  enabled: true,
                  position: 9,
                },
              },
              {
                id: 'item-skill',
                operation: 'create_new',
                asset_type: 'skill',
                proposed_ref: 'skill_new',
                proposed_name: 'New skill',
                proposed_scope: 'workspace',
                proposed_body: 'skill body',
                analyst_proposed_body: 'skill body',
                duplicate_risk: 'low',
                rationale: 'No existing skill covers it.',
                decision: 'accepted',
              },
            ],
          }),
        } as Response)
      }
      if (url.startsWith('/improvements/proposal-bundles/bundle-1/')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'bundle-1', status: 'pending', items: [] }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    vi.spyOn(window, 'prompt')
      .mockReturnValueOnce('Reject duplicate')
      .mockReturnValueOnce('existing-skill')
      .mockReturnValueOnce('Already covered')

    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: 'recommendations' }))

    expect(await screen.findByText('Bundle bundle-1 · pending')).toBeInTheDocument()
    const guardSection = screen.getByLabelText('Bundle item guardrail description for item-guard').closest('section')
    if (!guardSection) throw new Error('guardrail bundle section not found')
    const guard = within(guardSection)
    expect(guard.getByText('guardrail · update_existing')).toBeInTheDocument()
    expect(guard.getByText('base guardver-1')).toBeInTheDocument()
    expect(guard.getByLabelText('Bundle item guardrail description for item-guard')).toHaveValue('guard desc v2')
    expect(guard.getByLabelText('Bundle item guardrail position for item-guard')).toHaveValue(22)
    const guardDiff = guard.getByLabelText('Bundle item diff for item-guard')
    expect(within(guardDiff).getByText('-description: guard desc v1')).toBeInTheDocument()
    expect(within(guardDiff).getByText('+description: guard desc v2')).toBeInTheDocument()
    expect(within(guardDiff).getByText('-enabled: true')).toBeInTheDocument()
    expect(within(guardDiff).getByText('+enabled: false')).toBeInTheDocument()

    fireEvent.change(guard.getByLabelText('Bundle item guardrail description for item-guard'), { target: { value: 'edited desc' } })
    fireEvent.click(guard.getByLabelText('Enabled'))
    fireEvent.change(guard.getByLabelText('Bundle item guardrail position for item-guard'), { target: { value: '7' } })
    fireEvent.change(guard.getByDisplayValue('guard v2'), { target: { value: 'guard edited' } })
    fireEvent.click(guard.getByRole('button', { name: 'Save Item' }))

    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-guard',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({
          proposed_body: 'guard edited',
          proposed_ref: '',
          proposed_name: '',
          proposed_scope: '',
          proposed_description: 'edited desc',
          proposed_enabled: true,
          proposed_position: 7,
        }),
      }),
    )

    const skillSection = screen.getByLabelText('Bundle item ref for item-skill').closest('section')
    if (!skillSection) throw new Error('skill bundle section not found')
    const skill = within(skillSection)
    fireEvent.change(skill.getByLabelText('Bundle item ref for item-skill'), { target: { value: 'skill_new_edited' } })
    fireEvent.change(skill.getByLabelText('Bundle item name for item-skill'), { target: { value: 'Edited skill' } })
    fireEvent.change(skill.getByLabelText('Bundle item scope for item-skill'), { target: { value: 'global' } })
    fireEvent.change(skill.getByDisplayValue('skill body'), { target: { value: 'skill edited' } })
    fireEvent.click(skill.getByRole('button', { name: 'Save Item' }))
    fireEvent.click(skill.getByRole('button', { name: 'Reject' }))
    fireEvent.click(skill.getByRole('button', { name: 'Link Existing' }))

    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill',
      expect.objectContaining({
        method: 'PATCH',
        body: expect.stringContaining('"proposed_ref":"skill_new_edited"'),
      }),
    )
    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill/reject',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ reason: 'Reject duplicate' }) }),
    )
    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill/link-existing',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ asset_id: 'existing-skill', reason: 'Already covered' }) }),
    )
  })
})
