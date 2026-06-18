'use client'

import { useEffect, useState } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import PaginationControls from '@/components/PaginationControls'
import { itemsFromResponse } from '@/lib/pagination'
import { defaultWorkspaceID } from '@/lib/workspace'

interface Workspace {
  id: string
  name: string
  description?: string
}

const emptyWorkspace: Workspace = { id: '', name: '', description: '' }

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  border: '1px solid var(--border)',
  borderRadius: '6px',
  fontSize: '0.85rem',
  fontFamily: 'inherit',
  background: 'var(--bg-input)',
  color: 'var(--text)',
}

const labelStyle: React.CSSProperties = {
  fontSize: '0.8rem',
  color: 'var(--text-muted)',
  display: 'block',
  marginBottom: '3px',
}

function WorkspaceForm({ initial, isNew, saving, error, onSave, onCancel }: {
  initial: Workspace
  isNew: boolean
  saving: boolean
  error: string
  onSave: (workspace: Workspace) => void
  onCancel: () => void
}) {
  const [form, setForm] = useState<Workspace>(initial)

  useEffect(() => setForm(initial), [initial])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      {isNew && (
        <div>
          <label style={labelStyle}>ID</label>
          <input
            style={inputStyle}
            value={form.id}
            onChange={e => setForm(f => ({ ...f, id: e.target.value }))}
            placeholder="team-a, optional"
          />
          <p style={{ color: 'var(--text-faint)', fontSize: '0.76rem', margin: '0.35rem 0 0' }}>
            Leave empty to derive it from the workspace name.
          </p>
        </div>
      )}
      {!isNew && (
        <div>
          <label style={labelStyle}>ID</label>
          <input style={inputStyle} value={form.id} disabled />
        </div>
      )}
      <div>
        <label style={labelStyle}>Name *</label>
        <input
          style={inputStyle}
          value={form.name}
          onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          placeholder="Team A"
        />
      </div>
      <div>
        <label style={labelStyle}>Description</label>
        <textarea
          style={{ ...inputStyle, minHeight: 84, resize: 'vertical' }}
          value={form.description ?? ''}
          onChange={e => setForm(f => ({ ...f, description: e.target.value }))}
          placeholder="Optional operational context for this workspace"
        />
      </div>
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', margin: 0 }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button
          onClick={onCancel}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
        >
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim()}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

export default function WorkspacesPage() {
  const [workspaces, setWorkspaces] = useState<Workspace[]>([])
  const [total, setTotal] = useState(0)
  const [limit, setLimit] = useState(50)
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [saveError, setSaveError] = useState('')
  const [saving, setSaving] = useState(false)
  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Workspace>(emptyWorkspace)

  const load = () => {
    setLoading(true)
    setError('')
    fetch(`/workspaces?limit=${limit}&offset=${offset}`, { cache: 'no-store' })
      .then(async res => {
        if (!res.ok) throw new Error((await res.text()) || 'Failed to load workspaces')
        return res.json()
      })
      .then((data) => {
        const items = itemsFromResponse<Workspace>(data)
        const page = data as { total?: number }
        setWorkspaces(items.slice().sort((a, b) => (a.name || a.id).localeCompare(b.name || b.id)))
        setTotal(page.total ?? items.length)
        setLoading(false)
      })
      .catch(e => {
        setError(String(e))
        setLoading(false)
      })
  }

  useEffect(() => load(), [limit, offset])

  const openCreate = () => {
    setSaveError('')
    setSelected(emptyWorkspace)
    setModal('create')
  }

  const openEdit = (workspace: Workspace) => {
    setSaveError('')
    setSelected(workspace)
    setModal('edit')
  }

  const openDelete = (workspace: Workspace) => {
    setSaveError('')
    setSelected(workspace)
    setModal('delete')
  }

  const saveWorkspace = async (workspace: Workspace) => {
    setSaving(true)
    setSaveError('')
    const isNew = modal === 'create'
    const body = isNew
      ? { id: workspace.id.trim(), name: workspace.name.trim(), description: (workspace.description ?? '').trim() }
      : { name: workspace.name.trim(), description: (workspace.description ?? '').trim() }
    const url = isNew ? '/workspaces' : `/workspaces/${encodeURIComponent(selected.id)}`
    try {
      const res = await fetch(url, {
        method: isNew ? 'POST' : 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
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

  const deleteWorkspace = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch(`/workspaces/${encodeURIComponent(selected.id)}`, { method: 'DELETE' })
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

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', gap: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Workspaces</h1>
        </div>
        <button
          onClick={openCreate}
          style={{ padding: '6px 14px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          + Add workspace
        </button>
      </div>

      <Card>
        <div style={{ marginBottom: '1rem' }}>
          <PaginationControls
            total={total}
            limit={limit}
            offset={offset}
            onLimitChange={(next) => { setLimit(next); setOffset(0) }}
            onOffsetChange={setOffset}
          />
        </div>
        {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
        {error && <p style={{ color: 'var(--text-danger)' }}>{error}</p>}
        {!loading && !error && workspaces.length === 0 && (
          <p style={{ color: 'var(--text-faint)', fontSize: '0.9rem' }}>No workspaces configured.</p>
        )}
        {!loading && !error && workspaces.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.85rem' }}>
              <thead>
                <tr style={{ borderBottom: '1px solid var(--border-subtle)', color: 'var(--text-muted)', textAlign: 'left' }}>
                  <th style={{ padding: '6px 8px', fontWeight: 600 }}>Name</th>
                  <th style={{ padding: '6px 8px', fontWeight: 600 }}>ID</th>
                  <th style={{ padding: '6px 8px', fontWeight: 600 }}>Description</th>
                  <th style={{ padding: '6px 8px', fontWeight: 600, textAlign: 'right' }}>Actions</th>
                </tr>
              </thead>
              <tbody>
                {workspaces.map(workspace => (
                  <tr key={workspace.id} style={{ borderBottom: '1px solid var(--border-subtle)' }}>
                    <td style={{ padding: '7px 8px', color: 'var(--text-heading)', fontWeight: 700 }}>{workspace.name || workspace.id}</td>
                    <td style={{ padding: '7px 8px', color: 'var(--text-muted)', fontFamily: 'monospace' }}>{workspace.id}</td>
                    <td style={{ padding: '7px 8px', color: 'var(--text-muted)' }}>{workspace.description || '-'}</td>
                    <td style={{ padding: '7px 8px' }}>
                      <div style={{ display: 'flex', gap: '0.4rem', justifyContent: 'flex-end' }}>
                        <button
                          onClick={() => openEdit(workspace)}
                          style={{ padding: '4px 10px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.78rem' }}
                        >
                          Edit
                        </button>
                        <button
                          onClick={() => openDelete(workspace)}
                          disabled={workspace.id === defaultWorkspaceID}
                          title={workspace.id === defaultWorkspaceID ? 'The default workspace cannot be deleted.' : 'Delete workspace'}
                          style={{ padding: '4px 10px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', cursor: workspace.id === defaultWorkspaceID ? 'not-allowed' : 'pointer', opacity: workspace.id === defaultWorkspaceID ? 0.55 : 1, fontSize: '0.78rem' }}
                        >
                          Delete
                        </button>
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Add Workspace' : `Edit ${selected.name || selected.id}`} onClose={() => setModal(null)}>
          <WorkspaceForm
            initial={selected}
            isNew={modal === 'create'}
            saving={saving}
            error={saveError}
            onSave={saveWorkspace}
            onCancel={() => setModal(null)}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete Workspace" onClose={() => setModal(null)}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            <p style={{ color: 'var(--text)', fontSize: '0.9rem', margin: 0 }}>
              Delete workspace <strong>{selected.name || selected.id}</strong>?
            </p>
            <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem', margin: 0 }}>
              Workspace-scoped repos, agents, memory, runners, traces, and budgets are tied to this workspace.
            </p>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', margin: 0 }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setModal(null)}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
              >
                Cancel
              </button>
              <button
                onClick={deleteWorkspace}
                disabled={saving || selected.id === defaultWorkspaceID}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  )
}
