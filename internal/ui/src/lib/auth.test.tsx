import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { AuthTokenSettings } from './auth'

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

describe('AuthTokenSettings', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('changes the signed-in user password and clears password fields', async () => {
    const fetchMock = mockFetch((url, init) => {
      if (url === '/auth/status') {
        return jsonResponse({ authenticated: true, bootstrap_required: false, user: { username: 'operator', is_admin: false } })
      }
      if (url === '/auth/users') return jsonResponse([])
      if (url === '/auth/tokens') return jsonResponse([])
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
      if (url === '/auth/users') return jsonResponse([])
      if (url === '/auth/tokens') return jsonResponse([])
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
})
