import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import DashboardShell from './DashboardShell'

vi.mock('next/navigation', () => ({
  usePathname: () => '/',
}))

function mockShellFetch() {
  vi.stubGlobal('fetch', vi.fn((url: string) => {
    if (url === '/status') {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ orphaned_agents: { count: 0 } }),
      } as Response)
    }
    if (url === '/token_budgets/alerts') {
      return Promise.resolve({
        ok: true,
        json: () => Promise.resolve({ count: 0 }),
      } as Response)
    }
    return Promise.resolve({ ok: false, json: () => Promise.resolve({}) } as Response)
  }))
}

describe('<DashboardShell />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders the collapsed navigation toggle as an accessible hamburger icon', () => {
    mockShellFetch()
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    const button = screen.getByTitle('Open navigation')
    expect(button).toHaveAttribute('aria-label', 'Open navigation')
    expect(button).toHaveAttribute('title', 'Open navigation')
    expect(button).not.toHaveTextContent('Menu')
    expect(button.querySelectorAll('.shell-menu-icon span')).toHaveLength(3)

    fireEvent.click(button)
    expect(container.querySelector('.shell-sidebar.open')).toBeInTheDocument()
  })
})
