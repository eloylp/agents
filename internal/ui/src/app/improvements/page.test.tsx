import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

vi.mock('@/components/MarkdownEditor', () => ({
  default: ({ value, onChange }: { value: string; onChange: (v: string) => void }) => (
    <textarea value={value} onChange={e => onChange(e.target.value)} />
  ),
}))

import ImprovementsPage from './page'

const catalogEndpointRows: Record<string, unknown[]> = {
  '/prompts': [],
  '/skills': [
    { id: 'go-api', name: 'go api boundaries' },
    { id: 'existing-skill', name: 'Existing skill', workspace_id: 'default' },
    { id: 'other-skill', name: 'Other skill' },
    { id: 'repo-hidden-skill', name: 'Repo hidden skill', workspace_id: 'default', repo: 'acme/other-repo' },
  ],
  '/guardrails': [
    { id: 'guardrail-existing', name: 'Existing guardrail' },
  ],
}

function catalogEndpointResponse(url: string) {
  if (!(url in catalogEndpointRows)) return null
  return Promise.resolve({ ok: true, json: () => Promise.resolve(catalogEndpointRows[url]) } as Response)
}

function matchesEndpoint(url: string, path: string) {
  return url === path || url.startsWith(`${path}?`)
}

describe('<ImprovementsPage />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
    window.localStorage.clear()
  })

  it('renders proposal bundle metadata and diff for ready catalog proposals', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/workspaces') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'default', name: 'Default' }]) } as Response)
      }
      const catalogResponse = catalogEndpointResponse(url)
      if (catalogResponse) return catalogResponse
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-1',
              feedback_event_id: 7,
              type: 'prompt_guidance',
              status: 'recommended',
              confidence: 'high',
              risk: 'low',
              finding: 'Tighten prompt',
              normalized_lesson: 'Use concrete language.',
              rationale: 'The linked review asked for sharper guidance.',
              attribution_confidence: 'exact',
              target_asset_type: 'prompt',
              target_asset_id: 'prompt-a',
              target_base_version_id: 'promptver-1',
              proposed_new_body: 'new body',
              updated_at: '2026-06-01T18:00:00Z',
              proposal_bundle: {
                id: 'bundle-rec-1',
                recommendation_id: 'rec-1',
                status: 'pending',
                items: [
                  {
                    id: 'item-prompt',
                    operation: 'update_existing',
                    asset_type: 'prompt',
                    asset_id: 'prompt-a',
                    base_version_id: 'promptver-1',
                    proposed_body: 'new body',
                    analyst_proposed_body: 'new body',
                    decision: 'accepted',
                    base_version: { id: 'promptver-1', version: 1, state: 'published', description: 'desc', content: 'old body' },
                  },
                ],
              },
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
              status: 'recommended',
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
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    expect(screen.queryByRole('button', { name: 'recommendations' })).not.toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))

    expect(await screen.findByText('Bundle bundle-rec-1 · pending · 1 items')).toBeInTheDocument()
    expect(screen.getByText('#7')).toBeInTheDocument()
    expect(screen.getByText('bundle-rec-1')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Bundle bundle-rec-1 · pending')).toBeInTheDocument()
    expect(within(dialog).getByText('prompt · update_existing')).toBeInTheDocument()
    expect(within(dialog).getByText('Base')).toBeInTheDocument()
    expect(within(dialog).getByText('promptver-1')).toBeInTheDocument()
    const diff = screen.getByLabelText('Bundle item diff for item-prompt')
    expect(within(diff).getByText('-old body')).toBeInTheDocument()
    expect(within(diff).getByText('+new body')).toBeInTheDocument()
  })

  it('blocks finalizing a bundle when another pending staged change targets the same catalog item', async () => {
    const recommendations = ['rec-a', 'rec-b'].map((id, index) => ({
      id,
      feedback_event_id: 20 + index,
      type: 'catalog_patch_bundle',
      status: 'recommended',
      confidence: 'high',
      risk: 'low',
      finding: `Update go-api ${index}`,
      normalized_lesson: 'Keep one staged change.',
      rationale: 'The same skill should not have two pending staged changes.',
      attribution_confidence: 'exact',
      target_asset_type: 'skill',
      target_asset_id: 'go-api',
      target_base_version_id: 'skillver-base',
      updated_at: `2026-06-01T18:0${index}:00Z`,
      proposal_bundle: {
        id: `bundle-${id}`,
        recommendation_id: id,
        status: 'pending',
        items: [
          {
            id: `item-${id}`,
            operation: 'update_existing',
            asset_type: 'skill',
            asset_id: 'go-api',
            base_version_id: 'skillver-base',
            proposed_body: `skill body ${index}`,
            analyst_proposed_body: `skill body ${index}`,
            decision: 'accepted',
            base_version: { id: 'skillver-base', version: 1, state: 'published', prompt: 'old skill body' },
          },
        ],
      },
    }))
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url === '/workspaces') return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'default', name: 'Default' }]) } as Response)
      const catalogResponse = catalogEndpointResponse(url)
      if (catalogResponse) return catalogResponse
      if (matchesEndpoint(url, '/improvements/feedback')) return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      if (matchesEndpoint(url, '/improvements/recommendations')) return Promise.resolve({ ok: true, json: () => Promise.resolve(recommendations) } as Response)
      if (url === '/skills/go-api') return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'go-api', version_id: 'skillver-base' }) } as Response)
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    expect(await screen.findByText('Bundle bundle-rec-b · pending · 1 items')).toBeInTheDocument()
    expect(await screen.findAllByText('skill/go-api · go api boundaries')).toHaveLength(2)
    fireEvent.click(screen.getAllByRole('button', { name: 'Inspect' })[0])
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('go-api · go api boundaries')).toBeInTheDocument()
    expect(within(dialog).getByText(/another pending bundle already has a staged change for this catalog item: bundle-rec-a/i)).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Finalize Bundle' })).not.toBeInTheDocument()
  })

  it('renders editable proposal bundle items and posts item decisions', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/workspaces') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'default', name: 'Default' }]) } as Response)
      }
      const catalogResponse = catalogEndpointResponse(url)
      if (catalogResponse) return catalogResponse
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-bundle',
              feedback_event_id: 9,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'high',
              risk: 'medium',
              finding: 'Coordinate catalog updates',
              normalized_lesson: 'Use a bundle.',
              rationale: 'Multiple catalog assets need one review.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-01T18:10:00Z',
              proposal_bundle: {
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
                    proposed_ref: 'existing-skill',
                    proposed_name: 'New skill',
                    proposed_scope: 'workspace',
                    proposed_body: 'skill body',
                    analyst_proposed_body: 'skill body',
                    duplicate_risk: 'low',
                    rationale: 'No existing skill covers it.',
                    decision: 'accepted',
                  },
                  {
                    id: 'item-guard-new',
                    operation: 'create_new',
                    asset_type: 'guardrail',
                    proposed_ref: 'new-guardrail',
                    proposed_name: 'New guardrail',
                    proposed_scope: 'global',
                    proposed_description: 'New guardrail description',
                    proposed_enabled: true,
                    proposed_position: 40,
                    proposed_body: 'new guardrail body',
                    analyst_proposed_body: 'new guardrail body',
                    duplicate_risk: 'low',
                    rationale: 'Guardrails are linked by scope.',
                    decision: 'accepted',
                  },
                ],
              },
            },
          ]),
        } as Response)
      }
      if (url.startsWith('/improvements/proposal-bundles/bundle-1/')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'bundle-1', status: 'pending', items: [] }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))

    expect(await screen.findByText('Bundle bundle-1 · pending · 3 items')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    const guardSection = screen.getByLabelText('Bundle item guardrail description for item-guard').closest('section')
    if (!guardSection) throw new Error('guardrail bundle section not found')
    const guard = within(guardSection)
    expect(guard.getByText('guardrail · update_existing')).toBeInTheDocument()
    expect(guard.getByText('Base')).toBeInTheDocument()
    expect(guard.getByText('guardver-1')).toBeInTheDocument()
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
    fireEvent.click(guard.getByRole('button', { name: 'Save Changes' }))

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
    expect(skill.getByLabelText('Bundle item scope for item-skill')).toHaveDisplayValue('Workspace: default')
    fireEvent.change(skill.getByLabelText('Bundle item scope for item-skill'), { target: { value: 'global' } })
    expect(skill.getByLabelText('Bundle item scope for item-skill')).toHaveDisplayValue('Global')
    fireEvent.change(skill.getByDisplayValue('skill body'), { target: { value: 'skill edited' } })
    fireEvent.click(skill.getByRole('button', { name: 'Save Changes' }))

    fireEvent.click(skill.getByRole('button', { name: 'Reject' }))
    const rejectDialog = screen.getAllByRole('dialog').at(-1)
    if (!rejectDialog) throw new Error('reject dialog not found')
    fireEvent.change(within(rejectDialog).getByLabelText('Bundle item decision reason'), { target: { value: 'Reject duplicate' } })
    fireEvent.click(within(rejectDialog).getByRole('button', { name: 'Reject Item' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/proposal-bundles/bundle-1/items/item-skill/reject',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ reason: 'Reject duplicate' }) }),
    ))

    const newGuardSection = screen.getByLabelText('Bundle item ref for item-guard-new').closest('section')
    if (!newGuardSection) throw new Error('new guardrail bundle section not found')
    const newGuard = within(newGuardSection)
    expect(newGuard.getByText('guardrail · create_new')).toBeInTheDocument()
    expect(newGuard.queryByRole('button', { name: 'Use Existing Asset' })).not.toBeInTheDocument()

    const useExistingButton = skill.getByRole('button', { name: 'Use Existing Asset' })
    expect(useExistingButton).toHaveAttribute('title', expect.stringContaining('does not attach the asset to any agent'))
    fireEvent.click(useExistingButton)
    await waitFor(() => expect(screen.getByRole('heading', { name: 'Use existing asset' })).toBeInTheDocument())
    const linkDialog = screen.getByRole('heading', { name: 'Use existing asset' }).closest('[role="dialog"]')
    if (!linkDialog) throw new Error('link dialog not found')
    expect(within(linkDialog).getByText(/does not attach that asset to any agent/i)).toBeInTheDocument()
    await waitFor(() => expect(within(linkDialog).getByRole('option', { name: /Existing skill/ })).toBeInTheDocument())
    expect(within(linkDialog).queryByRole('option', { name: /Repo hidden skill/ })).not.toBeInTheDocument()
    await waitFor(() => expect(within(linkDialog).getByLabelText('Existing asset id/ref')).toHaveValue('existing-skill'))
    fireEvent.change(within(linkDialog).getByLabelText('Bundle item decision reason'), { target: { value: 'Already covered' } })
    fireEvent.click(within(linkDialog).getByRole('button', { name: 'Use Existing Asset' }))

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
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-skill',
              workspace: 'team-a',
              feedback_event_id: 10,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'high',
              risk: 'low',
              finding: 'Create Go API skill',
              normalized_lesson: 'Use per-method handlers.',
              rationale: 'The maintainer asked for a reusable skill.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-01T18:20:00Z',
              proposal_bundle: {
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
              },
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

    fireEvent.click(await screen.findByRole('button', { name: /History/ }))
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }))
    const attachButton = await screen.findByRole('button', { name: 'Add go-api to coder' })
    expect(attachButton).toHaveAttribute('title', expect.stringContaining('Attach this published skill to the attributed agent'))
    fireEvent.click(attachButton)

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/agents/coder?workspace=team-a',
      expect.objectContaining({
        method: 'PATCH',
        body: JSON.stringify({ skills: ['testing', 'go-api'] }),
      }),
    ))
    expect(await screen.findByText('Linked go-api to coder.')).toBeInTheDocument()
  })

  it('does not mark published history bundles as stale or actionable', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-published-stale',
              feedback_event_id: 20,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'high',
              risk: 'low',
              finding: 'Published catalog bundle',
              normalized_lesson: 'Already finalized.',
              rationale: 'The bundle was published and should remain immutable history.',
              attribution_confidence: 'unresolved',
              structured_output: {
                changes: [
                  { operation: 'update_existing', asset_type: 'prompt', asset_id: 'lab-coder', base_version_id: 'promptver-old' },
                ],
              },
              updated_at: '2026-06-07T13:19:18Z',
              proposal_bundle: {
                id: 'bundle-published-stale',
                recommendation_id: 'rec-published-stale',
                status: 'published',
                items: [
                  {
                    id: 'item-prompt',
                    operation: 'update_existing',
                    asset_type: 'prompt',
                    asset_id: 'lab-coder',
                    base_version_id: 'promptver-old',
                    proposed_body: 'published prompt body',
                    analyst_proposed_body: 'published prompt body',
                    decision: 'published',
                    published_version_id: 'promptver-new',
                    stale: true,
                    current_version_id: 'promptver-new',
                    base_version: { id: 'promptver-old', version: 1, state: 'published', description: 'old', content: 'old prompt body' },
                  },
                ],
              },
            },
          ]),
        } as Response)
      }
      if (url === '/prompts/lab-coder') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'lab-coder', version_id: 'promptver-new' }) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]), text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /History/ }))

    expect(await screen.findByText('Bundle bundle-published-stale · published · 1 items')).toBeInTheDocument()
    expect(screen.getByText('published')).toBeInTheDocument()
    expect(screen.queryByText('ready')).not.toBeInTheDocument()
    expect(screen.queryByText('stale target')).not.toBeInTheDocument()
    expect(screen.queryByText(/Target changed since analysis/)).not.toBeInTheDocument()
    expect(screen.queryByText('Proposal blocked: target changed')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Re-analyze' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Inspect' })).toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalledWith('/prompts/lab-coder', expect.anything())
  })

  it('shows resolved bundles as terminal history', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-resolved',
              feedback_event_id: 21,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'medium',
              risk: 'low',
              finding: 'Reuse an existing skill',
              normalized_lesson: 'Avoid duplicate skills.',
              rationale: 'The proposed new skill was already covered by an existing asset.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-07T13:22:18Z',
              proposal_bundle: {
                id: 'bundle-resolved',
                recommendation_id: 'rec-resolved',
                status: 'resolved',
                items: [
                  {
                    id: 'item-skill',
                    operation: 'create_new',
                    asset_type: 'skill',
                    asset_id: 'existing-skill',
                    proposed_ref: 'duplicate-skill',
                    proposed_name: 'Duplicate skill',
                    proposed_scope: 'workspace',
                    proposed_body: 'duplicate body',
                    analyst_proposed_body: 'duplicate body',
                    decision: 'linked_existing',
                    decision_reason: 'Already covered',
                  },
                ],
              },
            },
          ]),
        } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]), text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /History/ }))

    expect(await screen.findByText('Bundle bundle-resolved · resolved · 1 items')).toBeInTheDocument()
    expect(screen.getByText('resolved')).toBeInTheDocument()
    expect(screen.queryByText('ready')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reject' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Inspect' }))
    expect(await screen.findByText('Uses existing asset')).toBeInTheDocument()
    expect(screen.getByText('Already covered')).toBeInTheDocument()
  })

  it('keeps failed proposals active and offers retry', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url === '/workspaces') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([{ id: 'lab', name: 'Lab' }]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-failed',
              feedback_event_id: 27,
              type: 'catalog_patch_bundle',
              status: 'failed',
              confidence: 'low',
              risk: 'medium',
              finding: 'Analyst run failed',
              normalized_lesson: 'Retry the same feedback.',
              rationale: 'The backend failed before producing a proposal.',
              attribution_confidence: 'unresolved',
              error: 'runner container exited with status 1',
              updated_at: '2026-06-02T16:00:00Z',
              clarification: {
                recommendation_id: 'rec-failed',
                author: 'dashboard',
                body: 'Please retry with the clarification context.',
                created_at: '2026-06-02T15:59:00Z',
                updated_at: '2026-06-02T15:59:00Z',
              },
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-failed/clarification' && init?.method === 'POST') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]), text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))

    expect(await screen.findByText('Analyst run failed')).toBeInTheDocument()
    expect(screen.getByText('failed')).toBeInTheDocument()
    expect(screen.getByText('Error: runner container exited with status 1')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /History/ }))
    expect(screen.queryByText('Analyst run failed')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Proposals/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/recommendations/rec-failed/clarification',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ body: 'Please retry with the clarification context.' }),
      }),
    ))
    expect(await screen.findByText('Clarification retry queued.')).toBeInTheDocument()
  })

  it('blocks finalizing stale proposal bundles in the review modal', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-stale',
              feedback_event_id: 13,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'high',
              risk: 'medium',
              finding: 'Update stale prompt',
              normalized_lesson: 'Refresh first.',
              rationale: 'The target prompt moved after the recommendation was made.',
              attribution_confidence: 'exact',
              updated_at: '2026-06-02T14:00:00Z',
              proposal_bundle: {
                id: 'bundle-stale',
                recommendation_id: 'rec-stale',
                recommendation_changed: true,
                status: 'pending',
                items: [
                  {
                    id: 'item-stale',
                    operation: 'update_existing',
                    asset_type: 'prompt',
                    asset_id: 'prompt-coder',
                    base_version_id: 'promptver-old',
                    proposed_body: 'new prompt body',
                    analyst_proposed_body: 'new prompt body',
                    decision: 'accepted',
                    current_version_id: 'promptver-current',
                    stale: true,
                    base_version: { id: 'promptver-old', version: 1, state: 'published', description: 'old', content: 'old prompt body' },
                  },
                ],
              },
            },
          ]),
        } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))
    fireEvent.click(await screen.findByRole('button', { name: 'Inspect' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/This bundle cannot be finalized because the source analysis changed, one of its target versions changed, or another pending bundle already has a staged change/i)).toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: 'Finalize Bundle' })).not.toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Discard Bundle' })).toBeInTheDocument()
  })

  it('preflights stale recommendation targets and offers re-analysis instead of bundle review', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-stale-target',
              feedback_event_id: 14,
              type: 'prompt_guidance',
              status: 'recommended',
              confidence: 'high',
              risk: 'low',
              finding: 'Refresh stale prompt recommendation',
              normalized_lesson: 'Re-analyze stale targets.',
              rationale: 'The recommendation was created against an older prompt version.',
              attribution_confidence: 'exact',
              target_asset_type: 'prompt',
              target_asset_id: 'prompt-coder',
              target_base_version_id: 'promptver-old',
              proposed_new_body: 'new prompt body',
              updated_at: '2026-06-02T15:00:00Z',
            },
          ]),
        } as Response)
      }
      if (url === '/prompts/prompt-coder') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({ id: 'prompt-coder', version_id: 'promptver-current' }) } as Response)
      }
      if (url === '/improvements/feedback/14/analyze' && init?.method === 'POST') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]), text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))

    expect(await screen.findByText('stale target')).toBeInTheDocument()
    expect(screen.getByText('Target changed since analysis: prompt/prompt-coder moved from promptver-old to promptver-current.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create Proposal' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Re-analyze' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/improvements/feedback/14/analyze', expect.objectContaining({ method: 'POST' })))
    expect(await screen.findByText('Re-analysis queued.')).toBeInTheDocument()
  })

  it('blocks stale bundle recommendations and offers re-analysis', async () => {
    let resolveSkill: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-bundle-stale-target',
              feedback_event_id: 16,
              type: 'catalog_patch_bundle',
              status: 'recommended',
              confidence: 'high',
              risk: 'low',
              finding: 'Refresh stale bundle recommendation',
              normalized_lesson: 'Re-analyze stale bundle targets.',
              rationale: 'One of the bundle targets moved after analysis.',
              attribution_confidence: 'exact',
              structured_output: {
                changes: [
                  { operation: 'update_existing', asset_type: 'skill', asset_id: 'go-api', base_version_id: 'skillver-old' },
                ],
              },
              updated_at: '2026-06-02T16:10:00Z',
            },
          ]),
        } as Response)
      }
      if (url === '/skills/go-api') {
        return new Promise<Response>(resolve => {
          resolveSkill = resolve
        })
      }
      if (url === '/improvements/feedback/16/analyze' && init?.method === 'POST') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]), text: () => Promise.resolve('not found') } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ImprovementsPage />)

    fireEvent.click(await screen.findByRole('button', { name: /Proposals/ }))

    expect(await screen.findByText('Checking target versions')).toBeInTheDocument()
    resolveSkill?.({ ok: true, json: () => Promise.resolve({ id: 'go-api', version_id: 'skillver-current' }) } as Response)
    expect(await screen.findByText('Proposal blocked: target changed')).toBeInTheDocument()
    expect(screen.getByText('Target changed since analysis: skill/go-api moved from skillver-old to skillver-current.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Create Proposal' })).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Re-analyze' }))

    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/improvements/feedback/16/analyze', expect.objectContaining({ method: 'POST' })))
    expect(await screen.findByText('Re-analysis queued.')).toBeInTheDocument()
  })

  it('limits needs-input actions and loads full clarification context', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (matchesEndpoint(url, '/improvements/feedback')) {
        return Promise.resolve({ ok: true, json: () => Promise.resolve([]) } as Response)
      }
      if (matchesEndpoint(url, '/improvements/recommendations')) {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve([
            {
              id: 'rec-clarify',
              feedback_event_id: 12,
              type: 'catalog_recommendation',
              status: 'needs_user_input',
              confidence: 'low',
              risk: 'low',
              finding: 'Feedback is unclear',
              normalized_lesson: 'Ask for specifics.',
              rationale: 'The feedback did not identify the desired behavior.',
              attribution_confidence: 'exact',
              target_asset_type: 'prompt',
              target_base_version_id: 'promptver-base',
              updated_at: '2026-06-02T13:00:00Z',
            },
          ]),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-clarify') {
        return Promise.resolve({
          ok: true,
          json: () => Promise.resolve({
            id: 'rec-clarify',
            feedback_event_id: 12,
            type: 'catalog_recommendation',
            status: 'needs_user_input',
            confidence: 'low',
            risk: 'low',
            finding: 'Feedback is unclear',
            normalized_lesson: 'Ask for specifics.',
            rationale: 'The feedback did not identify the desired behavior.',
            attribution_confidence: 'exact',
            target_asset_type: 'prompt',
            target_base_version_id: 'promptver-base',
            updated_at: '2026-06-02T13:00:00Z',
            feedback: {
              id: 12,
              workspace: 'lab',
              repo_owner: 'acme',
              repo_name: 'repo',
              source_type: 'issue_comment',
              source_url: 'https://example.test/comment',
              author_login: 'maintainer',
              author_authorized: true,
              raw_body: 'nonsense /agents improve',
              link_confidence: 'exact',
              linked_agent_name: 'lab-coder',
              linked_prompt_version_id: 'promptver-base',
              linked_skill_version_ids: ['skillver-1'],
              linked_guardrail_version_ids: ['guardrailver-1'],
              status: 'analyzed',
              ingested_at: '2026-06-02T12:59:00Z',
            },
          }),
        } as Response)
      }
      if (url === '/improvements/recommendations/rec-clarify/status') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve({}) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve([]) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<ImprovementsPage />)

    expect(await screen.findByText('Feedback is unclear')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'accepted' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reject' })).toBeInTheDocument()

    fireEvent.click(screen.getByRole('button', { name: 'Clarify' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('#12 · rec-clarify · needs_user_input')).toBeInTheDocument()
    expect(await within(dialog).findByText('nonsense /agents improve')).toBeInTheDocument()
    expect(within(dialog).getByText('lab-coder')).toBeInTheDocument()
    expect(within(dialog).getAllByText('promptver-base').length).toBeGreaterThan(0)
    expect(within(dialog).getByText('skillver-1')).toBeInTheDocument()
    expect(within(dialog).getByText('guardrailver-1')).toBeInTheDocument()

	    fireEvent.click(within(dialog).getByRole('button', { name: 'Close' }))
	    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
	    const confirmDialog = screen.getByRole('dialog')
	    expect(within(confirmDialog).queryByRole('button', { name: 'Close' })).not.toBeInTheDocument()
	    fireEvent.change(within(confirmDialog).getByLabelText('Proposal decision reason'), { target: { value: 'Not actionable' } })
	    fireEvent.click(within(confirmDialog).getByRole('button', { name: 'Reject Proposal' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledWith(
      '/improvements/recommendations/rec-clarify/status',
      expect.objectContaining({ method: 'POST', body: JSON.stringify({ status: 'rejected', reason: 'Not actionable' }) }),
    ))
  })
})
