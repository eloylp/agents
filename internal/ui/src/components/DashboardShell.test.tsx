import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SIDEBAR_COLLAPSE_EVENT } from '@/lib/shell-events'
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

function mockViewport(matches: boolean) {
  vi.stubGlobal('matchMedia', vi.fn(() => ({
    matches,
    media: '(max-width: 860px)',
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
  })))
}

function renderedCSS() {
  return Array.from(document.querySelectorAll('style'))
    .map(style => style.textContent ?? '')
    .join('\n')
}

describe('<DashboardShell />', () => {
  afterEach(() => {
    window.localStorage.clear()
    vi.unstubAllGlobals()
  })

  it('renders the desktop navigation toggle inside the sidebar', () => {
    mockShellFetch()
    mockViewport(false)
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    const button = screen.getByTitle('Collapse navigation')
    expect(container.querySelector('.shell-sidebar .shell-menu-button')).toBe(button)
    expect(container.querySelector('.shell-mobilebar')).not.toBeInTheDocument()
    expect(button).toHaveAttribute('aria-label', 'Collapse navigation')
    expect(button).toHaveAttribute('title', 'Collapse navigation')
    expect(button).not.toHaveTextContent('Menu')
    expect(button.querySelector('.shell-menu-icon')).toHaveAttribute('aria-hidden', 'true')
    expect(button.querySelectorAll('.shell-menu-icon span')).toHaveLength(3)

    fireEvent.click(button)
    expect(container.querySelector('.dashboard-shell.nav-collapsed')).toBeInTheDocument()
    expect(button).toHaveAttribute('aria-label', 'Open navigation')
  })

  it('keeps collapsed desktop navigation as a narrow rail', () => {
    mockShellFetch()
    mockViewport(false)
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    fireEvent.click(screen.getByTitle('Collapse navigation'))

    const css = renderedCSS()
    expect(container.querySelector('.dashboard-shell.nav-collapsed')).toBeInTheDocument()
    expect(css).toContain('--sidebar-rail-width: 52px')
    expect(css).toContain('.dashboard-shell.nav-collapsed .shell-sidebar { width: var(--sidebar-rail-width); visibility: visible; transform: none; }')
    expect(css).toContain('.dashboard-shell.nav-collapsed .shell-main { margin-left: var(--sidebar-rail-width); }')
    expect(css).toContain('.dashboard-shell.nav-collapsed .shell-brand-text')
    expect(css).toContain('.dashboard-shell.nav-collapsed .shell-nav')
    expect(css).toContain('.dashboard-shell.nav-collapsed .shell-sidebar-footer { display: none; }')
  })

  it('persists desktop navigation collapse and responds to graph focus events', () => {
    mockShellFetch()
    mockViewport(false)
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    fireEvent(window, new Event(SIDEBAR_COLLAPSE_EVENT))

    expect(container.querySelector('.dashboard-shell.nav-collapsed')).toBeInTheDocument()
    expect(window.localStorage.getItem('agents.sidebarCollapsed')).toBe('true')
  })

  it('restores collapsed desktop navigation after reload', async () => {
    window.localStorage.setItem('agents.sidebarCollapsed', 'true')
    mockShellFetch()
    mockViewport(false)
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    await waitFor(() => {
      expect(container.querySelector('.dashboard-shell.nav-collapsed')).toBeInTheDocument()
    })
    expect(screen.getByTitle('Open navigation')).toBeInTheDocument()
    expect(window.localStorage.getItem('agents.sidebarCollapsed')).toBe('true')
  })

  it('keeps the mobile hamburger opening mobile navigation without a section title', async () => {
    mockShellFetch()
    mockViewport(true)
    const { container } = render(<DashboardShell><div>Content</div></DashboardShell>)

    await waitFor(() => {
      expect(container.querySelector('.shell-mobilebar .shell-menu-button')).toBeInTheDocument()
    })
    const button = screen.getByTitle('Open navigation')
    expect(container.querySelector('.shell-mobilebar .shell-menu-button')).toBe(button)
    expect(container.querySelector('.shell-mobilebar')).not.toHaveTextContent('Fleet')
    expect(container.querySelector('.shell-sidebar .shell-menu-button')).not.toBeInTheDocument()

    fireEvent.click(button)

    expect(container.querySelector('.shell-sidebar.open')).toBeInTheDocument()
  })
})
