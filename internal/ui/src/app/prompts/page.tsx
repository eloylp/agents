'use client'

import { useEffect, useState } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import MarkdownEditor from '@/components/MarkdownEditor'

interface Prompt {
  id?: string
  name: string
  description: string
  content: string
}

const emptyPrompt: Prompt = { name: '', description: '', content: '' }

export default function PromptsPage() {
  const [prompts, setPrompts] = useState<Prompt[]>([])
  const [loading, setLoading] = useState(true)
  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Prompt>(emptyPrompt)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/prompts', { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then((data: Prompt[]) => { setPrompts(data ?? []); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => { load() }, [])

  const save = async () => {
    setSaving(true)
    setError('')
    const isNew = modal === 'create'
    const url = isNew ? '/prompts' : `/prompts/${encodeURIComponent(selected.name)}`
    const body = isNew
      ? selected
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
      const res = await fetch(`/prompts/${encodeURIComponent(selected.name)}`, { method: 'DELETE' })
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

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Prompt Catalog</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: 4 }}>
            {prompts.length} global prompt{prompts.length === 1 ? '' : 's'}
          </p>
        </div>
        <button
          onClick={() => { setSelected(emptyPrompt); setError(''); setModal('create') }}
          style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 14px', borderRadius: 6, cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          + New prompt
        </button>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading...</p>}
      {!loading && prompts.length === 0 && <p style={{ color: 'var(--text-muted)' }}>No prompts configured.</p>}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(280px, 1fr))', gap: '1rem' }}>
        {prompts.map(p => (
          <Card key={p.name} style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
            <div>
              <div style={{ fontWeight: 700, color: 'var(--text-heading)' }}>{p.name}</div>
              {p.description && <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', marginTop: 2 }}>{p.description}</div>}
            </div>
            <pre style={{ whiteSpace: 'pre-wrap', overflow: 'hidden', color: 'var(--text-muted)', fontSize: '0.78rem', lineHeight: 1.4, maxHeight: 110, margin: 0 }}>
              {p.content || '-'}
            </pre>
            <div style={{ display: 'flex', gap: '0.5rem', justifyContent: 'flex-end', marginTop: 'auto' }}>
              <button onClick={() => { setSelected(p); setError(''); setModal('edit') }} style={{ padding: '4px 10px', borderRadius: 5, border: '1px solid var(--border)', background: 'var(--bg)', cursor: 'pointer', color: 'var(--accent)' }}>Edit</button>
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
              <button onClick={save} disabled={saving || !selected.name.trim() || !selected.content.trim()} style={{ padding: '6px 16px', borderRadius: 6, border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', fontWeight: 600 }}>{saving ? 'Saving...' : 'Save'}</button>
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
