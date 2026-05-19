import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import AgentForm, { emptyAgentForm } from './AgentForm'

const baseAgent = {
  ...emptyAgentForm,
  name: 'coder',
  backend: 'claude',
  description: 'Implements approved work',
  prompt_ref: 'missing',
}

describe('<AgentForm />', () => {
  it('does not show a missing prompt error before prompt lookups load', () => {
    render(
      <AgentForm
        initial={baseAgent}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[]}
        promptOptionsLoaded={false}
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByDisplayValue('missing')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('keeps the selected prompt while lookups are loading', () => {
    const { rerender } = render(
      <AgentForm
        initial={baseAgent}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[]}
        promptOptionsLoaded={false}
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    rerender(
      <AgentForm
        initial={baseAgent}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[{ name: 'missing' }]}
        promptOptionsLoaded
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('keeps a missing prompt selected after prompt lookups load', async () => {
    render(
      <AgentForm
        initial={baseAgent}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[{ name: 'approved' }]}
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    await waitFor(() => {
      expect(screen.getByDisplayValue('missing (not visible)')).toBeInTheDocument()
    })
    expect(screen.getByRole('alert')).toHaveTextContent('Selected prompt is no longer in the catalog.')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('shows a missing prompt error after prompt lookups load', () => {
    render(
      <AgentForm
        initial={baseAgent}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[{ name: 'approved' }]}
        promptOptionsLoaded
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    expect(screen.getByRole('alert')).toHaveTextContent('Selected prompt is no longer in the catalog.')
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
  })

  it('enables saving when the selected prompt_ref is in the prompt catalog', () => {
    render(
      <AgentForm
        initial={{ ...baseAgent, prompt_ref: 'approved' }}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[]}
        agentNames={[]}
        promptOptions={[{ name: 'approved' }]}
        repoNames={[]}
        onSave={vi.fn()}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    expect(screen.getByRole('button', { name: 'Save' })).toBeEnabled()
  })

  it('clears catalog refs when the repo scope changes', () => {
    const onSave = vi.fn()
    render(
      <AgentForm
        initial={{
          ...baseAgent,
          scope_type: 'repo',
          scope_repo: 'owner/a',
          prompt_id: 'prompt_owner_a_review',
          prompt_ref: '',
          skills: ['skill_owner_a_security'],
        }}
        isNew
        workspace="default"
        backends={[{ name: 'claude', detected: true }]}
        skillOptions={[
          { id: 'skill_owner_a_security', name: 'security', workspace_id: 'default', repo: 'owner/a' },
          { id: 'skill_owner_b_security', name: 'security', workspace_id: 'default', repo: 'owner/b' },
        ]}
        agentNames={[]}
        promptOptions={[
          { id: 'prompt_owner_a_review', name: 'review', workspace_id: 'default', repo: 'owner/a' },
          { id: 'prompt_owner_b_review', name: 'review', workspace_id: 'default', repo: 'owner/b' },
        ]}
        repoNames={['owner/a', 'owner/b']}
        onSave={onSave}
        onCancel={vi.fn()}
        saving={false}
        error=""
      />,
    )

    fireEvent.change(screen.getByDisplayValue('owner/a'), { target: { value: 'owner/b' } })
    fireEvent.change(screen.getByDisplayValue('Select prompt...'), { target: { value: 'prompt_owner_b_review' } })
    fireEvent.click(screen.getByRole('button', { name: 'Save' }))

    expect(onSave).toHaveBeenCalledOnce()
    expect(onSave.mock.calls[0][0]).toMatchObject({
      scope_repo: 'owner/b',
      prompt_id: '',
      prompt_ref: 'review',
      prompt_scope: 'default/owner/b',
      skills: [],
    })
  })
})
