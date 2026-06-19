import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthProvider, AuthTokenSettings } from './auth'

function mockFetch(handler: (url: string, init?: RequestInit) => Response | Promise<Response>) {
  const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    return Promise.resolve(handler(url, init))
  })
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

function jsonResponse(body: unknown, init?: ResponseInit) {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

describe('AuthProvider', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders the checking session screen without an image-backed background', () => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(() => {})))

    const { container } = render(
      <AuthProvider>
        <div>Authenticated app</div>
      </AuthProvider>,
    )

    expect(screen.getByRole('heading', { name: 'Checking session' })).toBeInTheDocument()
    const redirectScreen = container.firstElementChild as HTMLElement
    expect(redirectScreen.style.background).not.toContain('agents.jpg')
    expect(redirectScreen.style.background).not.toContain('url(')
  })
})

describe('AuthTokenSettings', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('changes the signed-in user password and clears password fields', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/auth/status') {
        return jsonResponse({ authenticated: true, bootstrap_required: false, user: { username: 'operator', is_admin: false } })
      }
      if (url === '/auth/users?limit=50&offset=0') return jsonResponse({ items: [], total: 0, limit: 50, offset: 0 })
      if (url === '/auth/tokens?limit=50&offset=0') return jsonResponse({ items: [], total: 0, limit: 50, offset: 0 })
      if (url === '/auth/me/password' && init?.method === 'POST') return jsonResponse({ ok: true })
      return new Response('not found', { status: 404 })
    })

    render(<AuthTokenSettings />)

    const current = await screen.findByPlaceholderText('Current password')
    const next = screen.getByPlaceholderText('New password')
    const confirm = screen.getByPlaceholderText('Confirm new password')
    fireEvent.change(current, { target: { value: 'old password' } })
    fireEvent.change(next, { target: { value: 'new password' } })
    fireEvent.change(confirm, { target: { value: 'new password' } })
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }))

    await screen.findByText('Password changed.')
    expect(fetchMock).toHaveBeenCalledWith('/auth/me/password', expect.objectContaining({
      method: 'POST',
      body: JSON.stringify({ current_password: 'old password', new_password: 'new password' }),
    }))
    expect(current).toHaveValue('')
    expect(next).toHaveValue('')
    expect(confirm).toHaveValue('')
  })

  it('rejects mismatched confirmation before calling the API', async () => {
    const fetchMock = mockFetch(url => {
      if (url === '/auth/status') {
        return jsonResponse({ authenticated: true, bootstrap_required: false, user: { username: 'operator', is_admin: false } })
      }
      if (url === '/auth/users?limit=50&offset=0') return jsonResponse({ items: [], total: 0, limit: 50, offset: 0 })
      if (url === '/auth/tokens?limit=50&offset=0') return jsonResponse({ items: [], total: 0, limit: 50, offset: 0 })
      return new Response('not found', { status: 404 })
    })

    render(<AuthTokenSettings />)

    fireEvent.change(await screen.findByPlaceholderText('Current password'), { target: { value: 'old password' } })
    fireEvent.change(screen.getByPlaceholderText('New password'), { target: { value: 'new password' } })
    fireEvent.change(screen.getByPlaceholderText('Confirm new password'), { target: { value: 'typo' } })
    fetchMock.mockClear()
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }))

    await screen.findByText('New passwords do not match.')
    await waitFor(() => expect(fetchMock).not.toHaveBeenCalled())
  })

  it('paginates users and API tokens independently', async () => {
    const fetchMock = mockFetch(url => {
      if (url === '/auth/status') {
        return jsonResponse({ authenticated: true, bootstrap_required: false, user: { username: 'operator', is_admin: true } })
      }
      if (url === '/auth/users?limit=50&offset=0') {
        return jsonResponse({
          items: [{ id: 1, username: 'operator', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', is_admin: true }],
          total: 75,
          limit: 50,
          offset: 0,
        })
      }
      if (url === '/auth/users?limit=50&offset=50') {
        return jsonResponse({
          items: [{ id: 2, username: 'viewer', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', is_admin: false }],
          total: 75,
          limit: 50,
          offset: 50,
        })
      }
      if (url === '/auth/tokens?limit=50&offset=0') {
        return jsonResponse({
          items: [{ id: 10, kind: 'api', name: 'first token', prefix: 'agt_abc', created_at: '2026-01-01T00:00:00Z' }],
          total: 120,
          limit: 50,
          offset: 0,
        })
      }
      if (url === '/auth/tokens?limit=50&offset=50') {
        return jsonResponse({
          items: [{ id: 11, kind: 'api', name: 'second token', prefix: 'agt_def', created_at: '2026-01-01T00:00:00Z' }],
          total: 120,
          limit: 50,
          offset: 50,
        })
      }
      return new Response('not found', { status: 404 })
    })

    render(<AuthTokenSettings />)

    expect(await screen.findByText('operator')).toBeInTheDocument()
    expect(await screen.findByText('first token')).toBeInTheDocument()
    expect(screen.getByText('1-50 of 75')).toBeInTheDocument()
    expect(screen.getByText('1-50 of 120')).toBeInTheDocument()

    const nextButtons = screen.getAllByRole('button', { name: 'Next' })
    fireEvent.click(nextButtons[0])

    expect(await screen.findByText('viewer')).toBeInTheDocument()
    expect(screen.getByText('51-75 of 75')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/auth/users?limit=50&offset=50', { cache: 'no-store' })

    fireEvent.click(nextButtons[1])

    expect(await screen.findByText('second token')).toBeInTheDocument()
    expect(screen.getByText('51-100 of 120')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledWith('/auth/tokens?limit=50&offset=50', { cache: 'no-store' })
  })
})
