'use client'
import { useState, useEffect } from 'react'
import { itemsFromResponse } from '@/lib/pagination'
import { withWorkspace } from '@/lib/workspace'

const STORAGE_KEY = 'agents_repo_filter'

export function useRepoFilter(): [string, (r: string) => void] {
  const [selected, setSelected] = useState('')

  useEffect(() => {
    const stored = localStorage.getItem(STORAGE_KEY) ?? ''
    setSelected(stored)
  }, [])

  const set = (repo: string) => {
    setSelected(repo)
    if (repo) localStorage.setItem(STORAGE_KEY, repo)
    else localStorage.removeItem(STORAGE_KEY)
  }

  return [selected, set]
}

export default function RepoFilter({ selected, onChange, workspace }: { selected: string; onChange: (r: string) => void; workspace?: string }) {
  const [repos, setRepos] = useState<string[]>([])
  const [loaded, setLoaded] = useState(false)

  useEffect(() => {
    setLoaded(false)
    fetch(workspace ? withWorkspace('/repos', workspace) : '/repos')
      .then(r => r.ok ? r.json() : [])
      .then((data) => {
        setRepos(itemsFromResponse<{ name: string }>(data).map(r => r.name))
        setLoaded(true)
      })
      .catch(() => setLoaded(true))
  }, [workspace])

  // Evict stale localStorage values after the workspace repo list loads. This
  // matters when switching to a workspace with no repos: the filter is hidden,
  // but a stale selected repo would still filter every graph node out.
  useEffect(() => {
    if (loaded && selected && !repos.includes(selected)) {
      onChange('')
    }
  }, [loaded, repos, selected, onChange])

  // Hide the filter when there's nothing to choose between (0 or 1 repos).
  // Single-repo installs shouldn't carry extra chrome for a filter that has no effect.
  if (repos.length <= 1) return null

  return (
    <select
      value={selected}
      onChange={e => onChange(e.target.value)}
      style={{
        background: 'var(--bg-input)',
        border: '1px solid var(--border)',
        color: 'var(--text)',
        padding: '6px 10px',
        borderRadius: '6px',
        fontSize: '0.875rem',
        cursor: 'pointer',
      }}
    >
      <option value="">All repos</option>
      {repos.map(r => <option key={r} value={r}>{r}</option>)}
    </select>
  )
}
