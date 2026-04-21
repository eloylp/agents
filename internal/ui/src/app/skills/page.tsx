'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import { authHeaders } from '@/lib/apiKey'

interface Skill {
  name: string
  prompt: string
}

const emptyForm: Skill = { name: '', prompt: '' }

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '6px 8px', border: '1px solid #bfdbfe', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: '#f8fafc', color: '#1e293b',
}
const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: '#64748b', display: 'block', marginBottom: '3px' }

function SkillForm({
  initial, isNew, onSave, onCancel, saving, error,
}: {
  initial: Skill
  isNew: boolean
  onSave: (s: Skill) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<Skill>(initial)

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
      <div>
        <label style={labelStyle}>Prompt</label>
        <textarea
          style={{ ...inputStyle, minHeight: '200px', resize: 'vertical' }}
          value={form.prompt}
          onChange={e => setForm(f => ({ ...f, prompt: e.target.value }))}
          placeholder="Skill guidance text…"
        />
      </div>
      {error && <p style={{ color: '#f87171', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim()}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #93c5fd', background: '#2563eb', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

export default function SkillsPage() {
  const [skills, setSkills] = useState<Skill[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Skill>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/api/store/skills')
      .then(r => r.json())
      .then((data: { name: string; prompt: string }[]) => {
        setSkills(data.map(s => ({ name: s.name, prompt: s.prompt })))
        setLoading(false)
      })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => { load() }, [])

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
      const res = await fetch('/api/store/skills', {
        method: 'POST',
        headers: authHeaders({ 'Content-Type': 'application/json' }),
        body: JSON.stringify({ name: form.name, prompt: form.prompt }),
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

  const confirmDelete = (name: string) => {
    setDeleteTarget(name)
    setSaveError('')
    setModal('delete')
  }

  const deleteSkill = async () => {
    setSaving(true)
    try {
      const res = await fetch(`/api/store/skills/${encodeURIComponent(deleteTarget)}`, { method: 'DELETE', headers: authHeaders() })
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
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f' }}>Skills</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {skills.length} skill{skills.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <button
            onClick={openCreate}
            style={{ background: '#2563eb', border: '1px solid #1d4ed8', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Create skill
          </button>
          <button onClick={load} style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#64748b', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {error && <p style={{ color: '#f87171' }}>Error: {error}</p>}
      {!loading && !error && skills.length === 0 && (
        <p style={{ color: '#64748b' }}>No skills configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
        {skills.map(sk => (
          <Card key={sk.name}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '1rem' }}>
              <div style={{ flex: 1, minWidth: 0 }}>
                <div style={{ fontWeight: 700, color: '#1e3a5f', marginBottom: '0.35rem' }}>{sk.name}</div>
                <pre style={{
                  fontSize: '0.78rem', color: '#475569', background: '#f8fafc',
                  border: '1px solid #e2e8f0', borderRadius: '4px', padding: '0.5rem',
                  maxHeight: '80px', overflow: 'hidden', whiteSpace: 'pre-wrap', wordBreak: 'break-word',
                  fontFamily: 'inherit',
                }}>
                  {sk.prompt ? sk.prompt.slice(0, 200) + (sk.prompt.length > 200 ? '…' : '') : '—'}
                </pre>
              </div>
              <div style={{ display: 'flex', gap: '0.5rem', flexShrink: 0 }}>
                <button onClick={() => openEdit(sk)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#f8fafc', cursor: 'pointer', fontSize: '0.75rem', color: '#2563eb' }}>Edit</button>
                <button onClick={() => confirmDelete(sk.name)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #fecaca', background: '#fff5f5', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}>Delete</button>
              </div>
            </div>
          </Card>
        ))}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Create skill' : `Edit — ${selected.name}`} onClose={() => setModal(null)}>
          <SkillForm
            initial={selected}
            isNew={modal === 'create'}
            onSave={saveSkill}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete skill" onClose={() => setModal(null)}>
          <p style={{ color: '#1e293b', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete skill <strong>{deleteTarget}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: '#f87171', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
              Cancel
            </button>
            <button onClick={deleteSkill} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #fca5a5', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
