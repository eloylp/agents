import { render, screen } from '@testing-library/react'
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
  it('requires prompt_ref to exist in the prompt catalog before saving', () => {
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

    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled()
    expect(screen.getByRole('alert')).toHaveTextContent('Selected prompt is no longer in the catalog.')
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
})
