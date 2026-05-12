'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import MarkdownEditor from '@/components/MarkdownEditor'

interface Skill {
  id?: string
  workspace_id?: string
  repo?: string
  name: string
  prompt: string
}

interface Workspace {
  id: string
  name: string
}

interface Repo {
  name: string
}

const emptyForm: Skill = { name: '', prompt: '' }

function scopeType(item: { workspace_id?: string; repo?: string }): 'global' | 'workspace' | 'repo' {
  if (item.repo) return 'repo'
  if (item.workspace_id) return 'workspace'
  return 'global'
}

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: 'var(--bg-input)', color: 'var(--text)',
}
const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: 'var(--text-muted)', display: 'block', marginBottom: '3px' }

function SkillForm({
  initial, isNew, workspaces, onSave, onCancel, saving, error,
}: {
  initial: Skill
  isNew: boolean
  workspaces: Workspace[]
  onSave: (s: Skill) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<Skill>(initial)
  const [selectedScope, setSelectedScope] = useState<'global' | 'workspace' | 'repo'>(scopeType(initial))
  const [repoOptions, setRepoOptions] = useState<Repo[]>([])

  useEffect(() => {
    if (selectedScope !== 'repo' || !form.workspace_id) {
      setRepoOptions([])
      return
    }
    fetch(`/repos?workspace=${encodeURIComponent(form.workspace_id)}`, { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Repo[]) => setRepoOptions(data ?? []))
      .catch(() => setRepoOptions([]))
  }, [selectedScope, form.workspace_id])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Name *</label>
        <input
          style={inputStyle}
          value={form.name}
          onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          placeholder="skill-name"
          disabled={!isNew}
        />
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: selectedScope === 'repo' ? '1fr 1fr 1fr' : selectedScope === 'workspace' ? '1fr 1fr' : '1fr', gap: '0.75rem' }}>
        <div>
          <label style={labelStyle}>Scope</label>
          <select
            style={inputStyle}
            value={selectedScope}
            disabled={!isNew}
            onChange={e => {
              const next = e.target.value as 'global' | 'workspace' | 'repo'
              setSelectedScope(next)
              setForm(f => ({ ...f, workspace_id: next === 'global' ? '' : f.workspace_id, repo: next === 'repo' ? f.repo : '' }))
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
              value={form.workspace_id || ''}
              disabled={!isNew}
              onChange={e => setForm(f => ({ ...f, workspace_id: e.target.value, repo: '' }))}
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
              value={form.repo || ''}
              disabled={!isNew || !form.workspace_id}
              onChange={e => setForm(f => ({ ...f, repo: e.target.value }))}
            >
              <option value="">Select repo...</option>
              {repoOptions.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
            </select>
          </div>
        )}
      </div>
      <div>
        <label style={labelStyle}>Prompt</label>
        <MarkdownEditor
          value={form.prompt}
          onChange={v => setForm(f => ({ ...f, prompt: v }))}
          placeholder="Skill guidance text…"
          minHeight={200}
        />
      </div>
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim() || (selectedScope !== 'global' && !form.workspace_id) || (selectedScope === 'repo' && !form.repo)}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

export default function SkillsPage() {
  const [skills, setSkills] = useState<Skill[]>([])
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [filterScope, setFilterScope] = useState<'all' | 'global' | 'workspace' | 'repo'>('all')
  const [filterWorkspace, setFilterWorkspace] = useState('')
  const [filterRepo, setFilterRepo] = useState('')
  const [filterRepos, setFilterRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Skill>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<Skill | null>(null)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/skills')
      .then(r => r.json())
      .then((data: Skill[]) => {
        setSkills((data ?? []).map(s => ({ ...s, id: s.id || s.name })))
        setLoading(false)
      })
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
    if (filterScope !== 'repo' || !filterWorkspace) {
      setFilterRepos([])
      return
    }
    fetch(`/repos?workspace=${encodeURIComponent(filterWorkspace)}`, { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Repo[]) => setFilterRepos(data ?? []))
      .catch(() => setFilterRepos([]))
  }, [filterScope, filterWorkspace])

  const openEdit = (skill: Skill) => {
    setSaveError('')
    setSelected(skill)
    setModal('edit')
  }

  const openCreate = () => {
    setSaveError('')
    setSelected(emptyForm)
    setModal('create')
  }

  const saveSkill = async (form: Skill) => {
    setSaving(true)
    setSaveError('')
    try {
      const isNew = modal === 'create'
      const res = await fetch(isNew ? '/skills' : `/skills/${encodeURIComponent(form.id || form.name)}`, {
        method: isNew ? 'POST' : 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(isNew ? {
          name: form.name,
          workspace_id: form.workspace_id || '',
          repo: form.repo || '',
          prompt: form.prompt,
        } : { prompt: form.prompt }),
      })
      if (!res.ok) {
        setSaveError((await res.text()) || 'Save failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const confirmDelete = (skill: Skill) => {
    setDeleteTarget(skill)
    setSaveError('')
    setModal('delete')
  }

  const deleteSkill = async () => {
    if (!deleteTarget) return
    setSaving(true)
    try {
      const res = await fetch(`/skills/${encodeURIComponent(deleteTarget.id || deleteTarget.name)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setSaveError((await res.text()) || 'Delete failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const scopeLabel = (sk: Skill) => {
    if (sk.repo) return `${sk.workspace_id || 'default'} / ${sk.repo}`
    if (sk.workspace_id) return `${sk.workspace_id} workspace`
    return 'Global'
  }

  const visibleSkills = skills.filter(sk => {
    const type = scopeType(sk)
    if (filterScope !== 'all' && type !== filterScope) return false
    if ((filterScope === 'workspace' || filterScope === 'repo') && filterWorkspace && sk.workspace_id !== filterWorkspace) return false
    if (filterScope === 'repo' && filterRepo && sk.repo !== filterRepo) return false
    return true
  })

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Skills</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {visibleSkills.length} of {skills.length} skill{skills.length !== 1 ? 's' : ''} configured
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
            onClick={openCreate}
            style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Create skill
          </button>
          <button onClick={load} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-muted)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
      {error && <p style={{ color: 'var(--text-danger)' }}>Error: {error}</p>}
      {!loading && !error && skills.length === 0 && (
        <p style={{ color: 'var(--text-muted)' }}>No skills configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
        {visibleSkills.map(sk => (
          <Card key={sk.id || sk.name}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 700, color: 'var(--text-heading)', marginBottom: '0.2rem' }}>{sk.name}</div>
                <div style={{ color: 'var(--text-muted)', fontSize: '0.72rem', marginBottom: '0.35rem' }}>{scopeLabel(sk)}</div>
                <pre style={{
                  fontSize: '0.78rem', color: 'var(--text-faint)', background: 'var(--bg)',
                  border: '1px solid var(--border-subtle)', borderRadius: '4px', padding: '0.5rem',
                  maxHeight: '80px', overflow: 'hidden', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                  fontFamily: 'inherit',
                }}>
                  {sk.prompt ? sk.prompt.slice(0, 200) + (sk.prompt.length > 200 ? '…' : '') : '-'}
                </pre>
              </div>
              <div style={{ display: 'flex', gap: '0.5rem', flexShrink: 0 }}>
                <button onClick={() => openEdit(sk)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border)', background: 'var(--bg)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--accent)' }}>Edit</button>
                <button onClick={() => confirmDelete(sk)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--text-danger)' }}>Delete</button>
              </div>
            </div>
          </Card>
        ))}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Create skill' : `Edit, ${selected.name}`} onClose={() => setModal(null)}>
          <SkillForm
            initial={selected}
            isNew={modal === 'create'}
            workspaces={workspaces}
            onSave={saveSkill}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete skill" onClose={() => setModal(null)}>
          <p style={{ color: 'var(--text)', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete skill <strong>{deleteTarget?.name}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
              Cancel
            </button>
            <button onClick={deleteSkill} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
