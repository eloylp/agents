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
  const [workspaceNotice, setWorkspaceNotice] = useState('')

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
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    if (workspaces.length === 0 || workspaces.some(w => w.id === workspace)) {
      return
    }
    const next = workspaces[0].id
    setWorkspaceNotice(`Workspace ${workspace} is no longer available. Switched to ${workspaces[0].name}.`)
    setWorkspaceState(next)
    window.localStorage.setItem(storageKey, next)
  }, [workspace, workspaces])

  const setWorkspace = (next: string) => {
    const id = next || defaultWorkspaceID
    setWorkspaceNotice('')
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

  return useMemo(() => ({ workspace, workspaces, workspaceNotice, setWorkspace }), [workspace, workspaces, workspaceNotice])
}
