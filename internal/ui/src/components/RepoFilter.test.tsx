import { renderHook, act } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { useRepoFilter } from './RepoFilter'

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
