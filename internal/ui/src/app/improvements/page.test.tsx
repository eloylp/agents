import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
      if (url === '/improvements/feedback') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations') {
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
      if (url === '/improvements/feedback') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations') {
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
    const rejectDialog = screen.getByRole('dialog')
    fireEvent.change(within(rejectDialog).getByLabelText('Bundle item decision reason'), { target: { value: 'Reject duplicate' } })
    fireEvent.click(within(rejectDialog).getByRole('button', { name: 'Reject Item' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill/reject',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ reason: 'Reject duplicate' }) }),
    ))

    fireEvent.click(skill.getByRole('button', { name: 'Link Existing' }))
    const linkDialog = screen.getByRole('dialog')
    fireEvent.change(within(linkDialog).getByLabelText('Existing asset id/ref'), { target: { value: 'existing-skill' } })
    fireEvent.change(within(linkDialog).getByLabelText('Bundle item decision reason'), { target: { value: 'Already covered' } })
    fireEvent.click(within(linkDialog).getByRole('button', { name: 'Link Existing' }))

    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill',
      expect.objectContaining({
        method: 'PATCH',
        body: expect.stringContaining('"proposed_ref":"skill_new_edited"'),
      }),
    )
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill/link-existing',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ asset_id: 'existing-skill', reason: 'Already covered' }) }),
    ))
  })

  it('links a published create-new skill to the attributed agent', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/improvements/feedback') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-skill',
              workspace: 'team-a',
              feedback_event_id: 10,
              type: 'catalog_patch_bundle',
              status: 'accepted',
              confidence: 'high',
              risk: 'low',
              finding: 'Create Go API skill',
              normalized_lesson: 'Use per-method handlers.',
              rationale: 'The maintainer asked for a reusable skill.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-01T18:20:00Z',
              feedback: {
                id: 10,
                workspace: 'team-a',
                repo_owner: 'acme',
                repo_name: 'repo',
                source_type: 'pull_request_review_comment',
                source_url: 'https://example.test/review',
                author_login: 'maintainer',
                author_authorized: true,
                raw_body: '/agents improve add go-api',
                link_confidence: 'exact',
                linked_agent_name: 'coder',
                status: 'analyzed',
                ingested_at: '2026-06-01T18:15:00Z',
              },
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/memory') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations/rec-skill/proposal') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations/rec-skill/proposal-bundle') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            id: 'bundle-skill',
            recommendation_id: 'rec-skill',
            status: 'published',
            items: [
              {
                id: 'item-go-api',
                operation: 'create_new',
                asset_type: 'skill',
                proposed_ref: 'go-api',
                proposed_name: 'Go API',
                proposed_scope: 'workspace',
                proposed_body: 'Use one handler per HTTP method.',
                analyst_proposed_body: 'Use one handler per HTTP method.',
                decision: 'published',
                published_version_id: 'skillver-go-api-1',
              },
            ],
          }),
        } as Response)
      }
      if (url === '/agents/coder?workspace=team-a' && (!init || init.method === undefined)) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ name: 'coder', skills: ['testing'] }) } as Response)
      }
      if (url === '/agents/coder?workspace=team-a' && init?.method === 'PATCH') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ name: 'coder', skills: ['testing', 'go-api'] }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: 'recommendations' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Add go-api to coder' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/agents/coder?workspace=team-a',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ skills: ['testing', 'go-api'] }),
      }),
    ))
    expect(await screen.findByText('Linked go-api to coder.')).toBeInTheDocument()
  })

  it('renders and edits assistant preference memory', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/improvements/feedback') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url === '/improvements/recommendations') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-memory',
              feedback_event_id: 11,
              type: 'skill_guidance',
              status: 'recommended',
              confidence: 'medium',
              risk: 'low',
              finding: 'Extract reusable guidance',
              normalized_lesson: 'Prefer skills.',
              rationale: 'Shared guidance should be reusable.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-02T12:00:00Z',
              memory_influences: [{ id: 'mem-1', key: 'prefer_skills', value: 'Prefer reusable skills.' }],
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/memory') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'mem-1',
              workspace: 'default',
              key: 'prefer_skills',
              value: 'Prefer reusable skills.',
              status: 'active',
              evidence_type: 'manual_user_entry',
              confidence: 'medium',
              updated_at: '2026-06-02T12:00:00Z',
            },
            {
              id: 'mem-2',
              workspace: 'default',
              key: 'broad_rollouts',
              value: 'Require stronger evidence before broad rollout.',
              status: 'proposed',
              evidence_type: 'rejected_recommendation',
              evidence_id: 'rec-9',
              evidence_url: 'https://example.test/evidence',
              confidence: 'low',
              updated_at: '2026-06-02T12:05:00Z',
            },
          ]),
        } as Response)
      }
      if (url.includes('/proposal')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (url.startsWith('/improvements/memory/')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    expect(await screen.findByText('Memory prefer_skills: Prefer reusable skills.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'memory' }))

    expect(await screen.findByDisplayValue('Prefer reusable skills.')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Memory key'), { target: { value: 'prefer_short_rules' } })
    fireEvent.change(screen.getByLabelText('Memory value'), { target: { value: 'Prefer short prompt rules.' } })
    fireEvent.click(screen.getByRole('button', { name: 'Add Memory' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/memory',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({
          key: 'prefer_short_rules',
          value: 'Prefer short prompt rules.',
          confidence: 'medium',
          status: 'active',
          evidence_type: 'manual_user_entry',
        }),
      }),
    ))

    fireEvent.change(screen.getByLabelText('Memory confidence for mem-1'), { target: { value: 'high' } })
    expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/memory/mem-1',
      expect.objectContaining({ method: 'PATCH', body: JSON.stringify({ confidence: 'high' }) }),
    )
    fireEvent.click(screen.getByRole('button', { name: 'Approve' }))
    expect(fetchMock).toHaveBeenCalledWith('/improvements/memory/mem-2/approve', expect.objectContaining({ method: 'POST' }))
    fireEvent.click(screen.getAllByRole('button', { name: 'Archive' })[0])
    expect(fetchMock).toHaveBeenCalledWith('/improvements/memory/mem-1/archive', expect.objectContaining({ method: 'POST' }))
  })
})
