'use client'
import { useState, useEffect } from 'react'

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

export default function RepoFilter({ selected, onChange }: { selected: string; onChange: (r: string) => void }) {
  const [repos, setRepos] = useState<string[]>([])

  useEffect(() => {
    fetch('/repos')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setRepos((data ?? []).map(r => r.name)))
      .catch(() => {})
  }, [])

  if (repos.length === 0) return null

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
