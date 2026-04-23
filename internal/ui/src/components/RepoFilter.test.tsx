import { render, renderHook, act, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import RepoFilter, { useRepoFilter } from './RepoFilter'

const STORAGE_KEY = 'agents_repo_filter'

describe('useRepoFilter', () => {
  beforeEach(() => {
    localStorage.clear()
  })

  afterEach(() => {
    localStorage.clear()
  })

  it('returns empty string when no value is stored', () => {
    const { result } = renderHook(() => useRepoFilter())
    // First render is '' before the effect hydrates from localStorage
    // (documented: SSR-safe pattern causes a one-render flash of '')
    expect(result.current[0]).toBe('')
  })

  it('hydrates from localStorage after mount', async () => {
    localStorage.setItem(STORAGE_KEY, 'owner/my-repo')
    const { result } = renderHook(() => useRepoFilter())
    // After the effect fires the stored value should be reflected
    await act(async () => {})
    expect(result.current[0]).toBe('owner/my-repo')
  })

  it('writes to localStorage when set is called with a value', () => {
    const { result } = renderHook(() => useRepoFilter())
    act(() => { result.current[1]('owner/repo-a') })
    expect(localStorage.getItem(STORAGE_KEY)).toBe('owner/repo-a')
    expect(result.current[0]).toBe('owner/repo-a')
  })

  it('removes from localStorage when set is called with empty string', () => {
    localStorage.setItem(STORAGE_KEY, 'owner/old-repo')
    const { result } = renderHook(() => useRepoFilter())
    act(() => { result.current[1]('') })
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(result.current[0]).toBe('')
  })
})

function mockReposResponse(repos: string[]) {
  const fetchMock = vi.fn(() =>
    Promise.resolve({
      ok: true,
      json: () => Promise.resolve(repos.map(name => ({ name }))),
    } as unknown as Response),
  )
  vi.stubGlobal('fetch', fetchMock)
  return fetchMock
}

describe('<RepoFilter />', () => {
  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('renders nothing when there are zero repos', async () => {
    mockReposResponse([])
    const onChange = vi.fn()
    const { container } = render(<RepoFilter selected="" onChange={onChange} />)
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/repos'))
    expect(container).toBeEmptyDOMElement()
  })

  it('renders nothing when there is a single repo (no meaningful choice to offer)', async () => {
    mockReposResponse(['owner/solo'])
    const onChange = vi.fn()
    const { container } = render(<RepoFilter selected="" onChange={onChange} />)
    await waitFor(() => expect(fetch).toHaveBeenCalledWith('/repos'))
    // Give React a tick to run effects after fetch resolves.
    await act(async () => {})
    expect(container).toBeEmptyDOMElement()
  })

  it('renders the select when there are two or more repos', async () => {
    mockReposResponse(['owner/a', 'owner/b'])
    const onChange = vi.fn()
    render(<RepoFilter selected="" onChange={onChange} />)
    const select = await screen.findByRole('combobox')
    expect(select).toBeInTheDocument()
    // 'All repos' + one option per repo.
    expect(screen.getAllByRole('option')).toHaveLength(3)
  })

  it("evicts a stale selected value once /repos resolves without it", async () => {
    mockReposResponse(['owner/a', 'owner/b'])
    const onChange = vi.fn()
    render(<RepoFilter selected="owner/renamed" onChange={onChange} />)
    // Wait for the eviction effect to run after repos load.
    await waitFor(() => expect(onChange).toHaveBeenCalledWith(''))
  })

  it('does not evict when selected matches a known repo', async () => {
    mockReposResponse(['owner/a', 'owner/b'])
    const onChange = vi.fn()
    render(<RepoFilter selected="owner/a" onChange={onChange} />)
    await screen.findByRole('combobox')
    await act(async () => {})
    expect(onChange).not.toHaveBeenCalled()
  })
})
