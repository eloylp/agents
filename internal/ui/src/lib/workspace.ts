'use client'

import { useEffect, useMemo, useState } from 'react'

export interface Workspace {
  id: string
  name: string
  description?: string
}

const storageKey = 'agents.workspace'
export const defaultWorkspaceID = 'default'

export function workspaceQuery(workspace: string): string {
  const id = workspace.trim() || defaultWorkspaceID
  return `workspace=${encodeURIComponent(id)}`
}

export function withWorkspace(path: string, workspace: string): string {
  const separator = path.includes('?') ? '&' : '?'
  return `${path}${separator}${workspaceQuery(workspace)}`
}

export function useSelectedWorkspace() {
  const [workspace, setWorkspaceState] = useState(defaultWorkspaceID)
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])

  useEffect(() => {
    const stored = window.localStorage.getItem(storageKey)
    if (stored) setWorkspaceState(stored)
  }, [])

  useEffect(() => {
    let cancelled = false
    fetch('/workspaces', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Workspace[]) => {
        if (cancelled) return
        const rows = data ?? []
        setWorkspaces(rows)
        if (rows.length > 0 && !rows.some(w => w.id === workspace)) {
          setWorkspaceState(rows[0].id)
          window.localStorage.setItem(storageKey, rows[0].id)
        }
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [workspace])

  const setWorkspace = (next: string) => {
    const id = next || defaultWorkspaceID
    setWorkspaceState(id)
    window.localStorage.setItem(storageKey, id)
    window.dispatchEvent(new CustomEvent('agents:workspace', { detail: id }))
  }

  useEffect(() => {
    const onChange = (event: Event) => {
      const detail = (event as CustomEvent<string>).detail
      if (detail) setWorkspaceState(detail)
    }
    window.addEventListener('agents:workspace', onChange)
    return () => window.removeEventListener('agents:workspace', onChange)
  }, [])

  return useMemo(() => ({ workspace, workspaces, setWorkspace }), [workspace, workspaces])
}
