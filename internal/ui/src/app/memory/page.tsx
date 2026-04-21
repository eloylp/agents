'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'

interface Agent {
  name: string
  bindings?: Array<{ repo: string; enabled: boolean }>
}

interface MemoryFile {
  agent: string
  repoKey: string  // "owner_repo"
  content: string
  mtime: string
}

export default function MemoryPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [selected, setSelected] = useState<{ agent: string; repoKey: string } | null>(null)
  const [file, setFile] = useState<MemoryFile | null>(null)
  const [loading, setLoading] = useState(false)
  const [streaming, setStreaming] = useState(false)

  useEffect(() => {
    fetch('/agents')
      .then(r => r.json())
      .then(data => setAgents(data ?? []))
      .catch(() => {})
  }, [])

  // Watch memory stream for change notifications
  useEffect(() => {
    const es = new EventSource('/memory/stream')
    setStreaming(true)
    es.onmessage = (e) => {
      try {
        const msg: { agent: string; repo: string } = JSON.parse(e.data)
        if (selected && msg.agent === selected.agent && msg.repo === selected.repoKey) {
          loadFile(selected.agent, selected.repoKey)
        }
      } catch { /* ignore */ }
    }
    es.onerror = () => setStreaming(false)
    return () => es.close()
  }, [selected]) // eslint-disable-line react-hooks/exhaustive-deps

  const loadFile = (agent: string, repoKey: string) => {
    setLoading(true)
    fetch(`/memory/${encodeURIComponent(agent)}/${encodeURIComponent(repoKey)}`)
      .then(async r => {
        if (!r.ok) throw new Error(`${r.status}`)
        const text = await r.text()
        const mtime = r.headers.get('X-Memory-Mtime') ?? ''
        setFile({ agent, repoKey, content: text, mtime })
        setLoading(false)
      })
      .catch(() => { setFile(null); setLoading(false) })
  }

  const handleSelect = (agent: string, repoKey: string) => {
    setSelected({ agent, repoKey })
    loadFile(agent, repoKey)
  }

  // Build the tree: agent → [repoKey, ...]
  // Include ALL agents — bound or unbound. For bound agents, use their repo bindings.
  // For unbound agents, we still show them so the user can click and check if memory exists.
  const tree: Record<string, string[]> = {}
  const allRepoKeys = new Set<string>()
  for (const a of agents) {
    const repos = Array.from(new Set(
      (a.bindings ?? []).map((b: { repo: string }) => b.repo.replace('/', '_'))
    ))
    repos.forEach((r: string) => allRepoKeys.add(r))
    if (repos.length > 0) {
      tree[a.name] = repos
    } else {
      // Unbound agent — show all known repos so user can check for memory
      tree[a.name] = []
    }
  }
  // For unbound agents, fill in all known repo keys so they appear clickable
  const knownRepos = Array.from(allRepoKeys)
  for (const name of Object.keys(tree)) {
    if (tree[name].length === 0 && knownRepos.length > 0) {
      tree[name] = knownRepos
    }
  }

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Agent Memory</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            Read-only view of agent memory · {streaming ? '🟢 watching for changes' : '🔴 disconnected'}
          </p>
        </div>
      </div>

      <div style={{ display: 'grid', gridTemplateColumns: '220px 1fr', gap: '1rem', alignItems: 'start' }}>
        {/* Tree sidebar */}
        <Card>
          {Object.keys(tree).length === 0 && (
            <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem' }}>No agents with bindings found.</p>
          )}
          {Object.entries(tree).map(([agent, repos]) => (
            <div key={agent} style={{ marginBottom: '0.5rem' }}>
              <div style={{ fontWeight: 600, fontSize: '0.8rem', color: 'var(--text-muted)', padding: '4px 0' }}>{agent}</div>
              {repos.map(r => {
                const isSelected = selected?.agent === agent && selected?.repoKey === r
                return (
                  <button
                    key={r}
                    onClick={() => handleSelect(agent, r)}
                    style={{
                      display: 'block',
                      width: '100%',
                      textAlign: 'left',
                      padding: '4px 8px',
                      background: isSelected ? 'var(--btn-primary-bg)' : 'transparent',
                      border: 'none',
                      borderRadius: '4px',
                      color: isSelected ? 'var(--text-heading)' : 'var(--text-muted)',
                      cursor: 'pointer',
                      fontSize: '0.78rem',
                    }}
                  >
                    📄 {r}
                  </button>
                )
              })}
            </div>
          ))}
        </Card>

        {/* File viewer */}
        <Card>
          {!selected && <p style={{ color: 'var(--text-faint)' }}>Select a memory entry to view its contents.</p>}
          {selected && loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
          {selected && !loading && !file && (
            <p style={{ color: 'var(--text-muted)' }}>Memory not found. The agent may not have written any memory yet.</p>
          )}
          {file && (
            <>
              <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: '0.75rem' }}>
                <span style={{ fontFamily: 'monospace', fontSize: '0.8rem', color: 'var(--accent)' }}>
                  {file.agent}/{file.repoKey}
                </span>
                {file.mtime && (
                  <span style={{ fontSize: '0.75rem', color: 'var(--text-faint)' }}>
                    last modified: {new Date(file.mtime).toLocaleString()}
                  </span>
                )}
              </div>
              <pre style={{
                background: 'var(--bg)',
                borderRadius: '6px',
                padding: '1rem',
                fontSize: '0.8rem',
                lineHeight: '1.6',
                color: 'var(--text)',
                overflowX: 'auto',
                whiteSpace: 'pre-wrap',
                wordBreak: 'break-word',
                maxHeight: '600px',
                overflowY: 'auto',
              }}>
                {file.content}
              </pre>
            </>
          )}
        </Card>
      </div>
    </div>
  )
}
