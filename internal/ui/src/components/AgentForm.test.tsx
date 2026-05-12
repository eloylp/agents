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
  it('clears prompt_ref when it is no longer visible in the prompt catalog', async () => {
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
      expect(screen.getByDisplayValue('Select prompt...')).toBeInTheDocument()
    })
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
