import { render, screen, within } from '@testing-library/react'
import type React from 'react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import GuardrailsManager from './GuardrailsManager'

vi.mock('@/lib/workspace', () => ({
  useSelectedWorkspace: () => ({
    workspace: 'team-a',
    workspaces: [{ id: 'team-a', name: 'Team A' }],
  }),
}))

vi.mock('@/components/Card', () => ({
  default: ({ title, children }: { title: string, children: React.ReactNode }) => (
    <section aria-label={title}>{children}</section>
  ),
}))

vi.mock('@/components/Modal', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

vi.mock('@/components/FullscreenModal', () => ({
  default: ({ children }: { children: React.ReactNode }) => <div>{children}</div>,
}))

vi.mock('@/components/MarkdownEditor', () => ({
  default: () => <textarea aria-label="Content" />,
}))

const catalog = [
  { name: 'a', description: '', content: 'A', default_content: 'A', is_builtin: true, enabled: true, position: 0 },
  { name: 'b', description: '', content: 'B', default_content: 'B', is_builtin: true, enabled: true, position: 1 },
  { name: 'c', description: '', content: 'C', default_content: 'C', is_builtin: true, enabled: true, position: 2 },
]

const workspaceRefs = [
  { workspace_id: 'team-a', guardrail_name: 'b', position: 0, enabled: true },
  { workspace_id: 'team-a', guardrail_name: 'a', position: 1, enabled: true },
]

describe('<GuardrailsManager />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders selected workspace guardrails in workspace position order', async () => {
    const fetchMock = vi.fn((url: string) => {
      if (url === '/guardrails') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(catalog) } as Response)
      }
      if (url === '/workspaces/team-a/guardrails') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(workspaceRefs) } as Response)
      }
      return Promise.resolve({ ok: false, status: 404, json: () => Promise.resolve({}) } as Response)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<GuardrailsManager />)

    const workspaceCard = await screen.findByRole('region', { name: 'Workspace guardrails: Team A' })
    const selectedRows = within(workspaceCard).getAllByLabelText(/Remove .* from workspace/)
    expect(selectedRows).toHaveLength(2)
    expect(selectedRows[0]).toHaveAccessibleName('Remove b from workspace')
    expect(selectedRows[1]).toHaveAccessibleName('Remove a from workspace')
    expect(within(workspaceCard).getByText('#1')).toBeInTheDocument()
    expect(within(workspaceCard).getByText('#2')).toBeInTheDocument()
    expect(within(workspaceCard).getByLabelText('Add c to workspace')).toBeInTheDocument()
  })
})
