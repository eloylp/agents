'use client'

import { useEffect, useState } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import MarkdownEditor from '@/components/MarkdownEditor'

interface Prompt {
  id?: string
  workspace_id?: string
  repo?: string
  name: string
  description: string
  content: string
}

interface Workspace {
  id: string
  name: string
}

interface Repo {
  name: string
}

const emptyPrompt: Prompt = { name: '', description: '', content: '' }

function scopeType(item: { workspace_id?: string; repo?: string }): 'global' | 'workspace' | 'repo' {
  if (item.repo) return 'repo'
  if (item.workspace_id) return 'workspace'
  return 'global'
}

export default function PromptsPage() {
  const [prompts, setPrompts] = useState<Prompt[]>([])
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Prompt>(emptyPrompt)
  const [selectedScope, setSelectedScope] = useState<'global' | 'workspace' | 'repo'>('global')
  const [filterScope, setFilterScope] = useState<'all' | 'global' | 'workspace' | 'repo'>('all')
  const [filterWorkspace, setFilterWorkspace] = useState('')
  const [filterRepo, setFilterRepo] = useState('')
  const [filterRepos, setFilterRepos] = useState<Repo[]>([])
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/prompts', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Prompt[]) => { setPrompts(data ?? []); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => {
    load()
    fetch('/workspaces', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Workspace[]) => setWorkspaces(data ?? []))
      .catch(() => setWorkspaces([]))
  }, [])

  useEffect(() => {
    if (selectedScope !== 'repo' || !selected.workspace_id) {
      setRepos([])
      return
    }
    fetch(`/repos?workspace=${encodeURIComponent(selected.workspace_id)}`, { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Repo[]) => setRepos(data ?? []))
      .catch(() => setRepos([]))
  }, [selectedScope, selected.workspace_id])

  useEffect(() => {
    if (filterScope !== 'repo' || !filterWorkspace) {
      setFilterRepos([])
      return
    }
    fetch(`/repos?workspace=${encodeURIComponent(filterWorkspace)}`, { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Repo[]) => setFilterRepos(data ?? []))
      .catch(() => setFilterRepos([]))
  }, [filterScope, filterWorkspace])

  const save = async () => {
    setSaving(true)
    setError('')
    const isNew = modal === 'create'
    const url = isNew ? '/prompts' : `/prompts/${encodeURIComponent(selected.id || selected.name)}`
    const body = isNew
      ? {
          ...selected,
          workspace_id: selectedScope === 'global' ? '' : selected.workspace_id,
          repo: selectedScope === 'repo' ? selected.repo : '',
        }
      : { description: selected.description, content: selected.content }
    try {
      const res = await fetch(url, {
        method: isNew ? 'POST' : 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
      })
      if (!res.ok) {
        setError(await res.text() || 'Save failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
    } catch (e) {
      setError(String(e))
    }
    setSaving(false)
  }

  const remove = async () => {
    setSaving(true)
    setError('')
    try {
      const res = await fetch(`/prompts/${encodeURIComponent(selected.id || selected.name)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setError(await res.text() || 'Delete failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
    } catch (e) {
      setError(String(e))
    }
    setSaving(false)
  }

  const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: 'var(--text-muted)', display: 'block', marginBottom: 3 }
  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: 6,
    fontSize: '0.85rem', fontFamily: 'inherit', background: 'var(--bg-input)', color: 'var(--text)',
  }

  const visiblePrompts = prompts.filter(p => {
    const type = scopeType(p)
    if (filterScope !== 'all' && type !== filterScope) return false
    if ((filterScope === 'workspace' || filterScope === 'repo') && filterWorkspace && p.workspace_id !== filterWorkspace) return false
    if (filterScope === 'repo' && filterRepo && p.repo !== filterRepo) return false
    return true
  })

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Prompt Catalog</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: 4 }}>
            {visiblePrompts.length} of {prompts.length} prompt catalog entr{prompts.length === 1 ? 'y' : 'ies'}
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <select
            value={filterScope}
            onChange={e => {
              const next = e.target.value as typeof filterScope
              setFilterScope(next)
              if (next === 'all' || next === 'global') {
                setFilterWorkspace('')
                setFilterRepo('')
              }
              if (next === 'workspace') setFilterRepo('')
            }}
            style={{ ...inputStyle, width: '130px' }}
          >
            <option value="all">All scopes</option>
            <option value="global">Global</option>
            <option value="workspace">Workspace</option>
            <option value="repo">Repo</option>
          </select>
          {(filterScope === 'workspace' || filterScope === 'repo') && (
            <select
              value={filterWorkspace}
              onChange={e => { setFilterWorkspace(e.target.value); setFilterRepo('') }}
              style={{ ...inputStyle, width: '150px' }}
            >
              <option value="">All workspaces</option>
              {workspaces.map(w => <option key={w.id} value={w.id}>{w.name || w.id}</option>)}
            </select>
          )}
          {filterScope === 'repo' && (
            <select
              value={filterRepo}
              onChange={e => setFilterRepo(e.target.value)}
              disabled={!filterWorkspace}
              style={{ ...inputStyle, width: '180px' }}
            >
              <option value="">All repos</option>
              {filterRepos.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
            </select>
          )}
          <button
            onClick={() => { setSelected(emptyPrompt); setSelectedScope('global'); setError(''); setModal('create') }}
            style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 14px', borderRadius: 6, cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + New prompt
          </button>
        </div>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading...</p>}
      {!loading && prompts.length === 0 && <p style={{ color: 'var(--text-muted)' }}>No prompts configured.</p>}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' }}>
        {visiblePrompts.map(p => (
          <Card key={p.id || p.name} style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
            <div>
              <div style={{ fontWeight: 700, color: 'var(--text-heading)' }}>{p.name}</div>
              <div style={{ color: 'var(--text-muted)', fontSize: '0.72rem', marginTop: 2 }}>
                {p.repo ? `${p.workspace_id || 'default'} / ${p.repo}` : p.workspace_id ? `${p.workspace_id} workspace` : 'Global'}
              </div>
              {p.description && <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', marginTop: 2 }}>{p.description}</div>}
            </div>
            <pre style={{ whiteSpace: 'pre-wrap', overflow: 'hidden', color: 'var(--text-muted)', fontSize: '0.78rem', lineHeight: 1.4, maxHeight: 110, margin: 0 }}>
              {p.content || '-'}
            </pre>
            <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end', marginTop: 'auto' }}>
              <button onClick={() => { setSelected(p); setSelectedScope(scopeType(p)); setError(''); setModal('edit') }} style={{ padding: '4px 10px', borderRadius: 5, border: '1px solid var(--border)', background: 'var(--bg)', cursor: 'pointer', color: 'var(--accent)' }}>Edit</button>
              <button onClick={() => { setSelected(p); setError(''); setModal('delete') }} style={{ padding: '4px 10px', borderRadius: 5, border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', cursor: 'pointer', color: 'var(--text-danger)' }}>Delete</button>
            </div>
          </Card>
        ))}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'New prompt' : `Edit ${selected.name}`} onClose={() => setModal(null)}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            <div>
              <label style={labelStyle}>Name *</label>
              <input style={inputStyle} value={selected.name} onChange={e => setSelected(p => ({ ...p, name: e.target.value }))} disabled={modal === 'edit'} />
            </div>
            <div style={{ display: 'grid', gridTemplateColumns: selectedScope === 'repo' ? '1fr 1fr 1fr' : selectedScope === 'workspace' ? '1fr 1fr' : '1fr', gap: '0.75rem' }}>
              <div>
                <label style={labelStyle}>Scope</label>
                <select
                  style={inputStyle}
                  value={selectedScope}
                  disabled={modal === 'edit'}
                  onChange={e => {
                    const next = e.target.value as 'global' | 'workspace' | 'repo'
                    setSelectedScope(next)
                    setSelected(p => ({ ...p, workspace_id: next === 'global' ? '' : p.workspace_id, repo: next === 'repo' ? p.repo : '' }))
                  }}
                >
                  <option value="global">Global</option>
                  <option value="workspace">Workspace</option>
                  <option value="repo">Repo</option>
                </select>
              </div>
              {selectedScope !== 'global' && (
                <div>
                  <label style={labelStyle}>Workspace *</label>
                  <select
                    style={inputStyle}
                    value={selected.workspace_id || ''}
                    disabled={modal === 'edit'}
                    onChange={e => setSelected(p => ({ ...p, workspace_id: e.target.value, repo: '' }))}
                  >
                    <option value="">Select workspace...</option>
                    {workspaces.map(w => <option key={w.id} value={w.id}>{w.name || w.id}</option>)}
                  </select>
                </div>
              )}
              {selectedScope === 'repo' && (
                <div>
                  <label style={labelStyle}>Repo *</label>
                  <select
                    style={inputStyle}
                    value={selected.repo || ''}
                    disabled={modal === 'edit' || !selected.workspace_id}
                    onChange={e => setSelected(p => ({ ...p, repo: e.target.value }))}
                  >
                    <option value="">Select repo...</option>
                    {repos.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
                  </select>
                </div>
              )}
            </div>
            <div>
              <label style={labelStyle}>Description</label>
              <input style={inputStyle} value={selected.description} onChange={e => setSelected(p => ({ ...p, description: e.target.value }))} />
            </div>
            <div>
              <label style={labelStyle}>Content *</label>
              <MarkdownEditor value={selected.content} onChange={content => setSelected(p => ({ ...p, content }))} minHeight={260} />
            </div>
            {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
            <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.75rem' }}>
              <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: 6, border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text-muted)' }}>Cancel</button>
              <button onClick={save} disabled={saving || !selected.name.trim() || !selected.content.trim() || (selectedScope !== 'global' && !selected.workspace_id) || (selectedScope === 'repo' && !selected.repo)} style={{ padding: '6px 16px', borderRadius: 6, border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', fontWeight: 600 }}>{saving ? 'Saving...' : 'Save'}</button>
            </div>
          </div>
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete prompt" onClose={() => setModal(null)}>
          <p style={{ color: 'var(--text)', marginBottom: '1rem' }}>Delete <strong>{selected.name}</strong>? This fails while any agent references it.</p>
          {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
          <div style={{ display: 'flex', justifyContent: 'flex-end', gap: '0.75rem' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: 6, border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text-muted)' }}>Cancel</button>
            <button onClick={remove} disabled={saving} style={{ padding: '6px 16px', borderRadius: 6, border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', fontWeight: 600 }}>{saving ? 'Deleting...' : 'Delete'}</button>
          </div>
        </Modal>
      )}
    </div>
  )
}
