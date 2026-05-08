'use client'

import { useEffect, useState } from 'react'
import BadgePicker from '@/components/BadgePicker'
import MarkdownEditor from '@/components/MarkdownEditor'
import type { StoreAgent } from '@/lib/dispatch-wiring'

export type { StoreAgent }

export interface BackendOption {
  name: string
  models?: string[]
  detected?: boolean
}

// allow_memory defaults to true so newly created agents preserve the
// historical behaviour where autonomous runs persist memory by default.
export const emptyAgentForm: StoreAgent = {
  name: '', backend: '', model: '', skills: [], prompt: '',
  allow_prs: false, allow_dispatch: false, allow_memory: true,
  can_dispatch: [], description: '',
}

export default function AgentForm({
  initial, isNew, backends, skillNames, agentNames, onSave, onCancel, saving, error,
}: {
  initial: StoreAgent
  isNew: boolean
  backends: BackendOption[]
  skillNames: string[]
  agentNames: string[]
  onSave: (a: StoreAgent) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<StoreAgent>(initial)

  const set = (k: keyof StoreAgent, v: unknown) => setForm(f => ({ ...f, [k]: v }))

  useEffect(() => {
    setForm(initial)
  }, [initial])

  const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: 'var(--text-muted)', display: 'block', marginBottom: '3px' }
  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: '6px',
    fontSize: '0.85rem', fontFamily: 'inherit', background: 'var(--bg-input)', color: 'var(--text)',
  }

  const backendOptions = backends.filter(b => b.detected !== false)
  const modelsForBackend = backendOptions.find(b => b.name === form.backend)?.models ?? []

  useEffect(() => {
    if (!form.model) return
    if (modelsForBackend.length === 0) return
    if (!modelsForBackend.includes(form.model)) {
      set('model', '')
    }
  }, [form.backend, form.model, modelsForBackend.join('|')])

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Name *</label>
        <input style={inputStyle} value={form.name} onChange={e => set('name', e.target.value)} placeholder="agent-name" disabled={!isNew} />
      </div>
      <div>
        <label style={labelStyle}>Backend</label>
        <select style={inputStyle} value={form.backend} onChange={e => set('backend', e.target.value)}>
          <option value="">Select backend...</option>
          {backendOptions.map(b => <option key={b.name} value={b.name}>{b.name}</option>)}
        </select>
      </div>
      <div>
        <label style={labelStyle}>Model</label>
        <select style={inputStyle} value={form.model} onChange={e => set('model', e.target.value)} disabled={!form.backend || modelsForBackend.length === 0}>
          <option value="">Default (backend decides)</option>
          {modelsForBackend.map(m => <option key={m} value={m}>{m}</option>)}
        </select>
      </div>
      <div>
        <label style={labelStyle}>Skills</label>
        <BadgePicker options={skillNames} selected={form.skills} onChange={v => set('skills', v)} placeholder="Add skill..." />
      </div>
      <div>
        <label style={labelStyle}>Description *</label>
        <input
          style={inputStyle}
          value={form.description}
          onChange={e => set('description', e.target.value)}
          placeholder="Used for identification and inter-agent routing context"
        />
      </div>
      <div>
        <label style={labelStyle}>Prompt</label>
        <MarkdownEditor
          value={form.prompt}
          onChange={v => set('prompt', v)}
          placeholder="Agent system prompt..."
          minHeight={200}
        />
      </div>
      <div>
        <label style={labelStyle}>Can dispatch</label>
        <BadgePicker options={agentNames.filter(n => n !== form.name)} selected={form.can_dispatch} onChange={v => set('can_dispatch', v)} placeholder="Add agent..." />
      </div>
      <div style={{ display: 'flex', gap: '1.5rem', flexWrap: 'wrap' }}>
        <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_prs} onChange={e => set('allow_prs', e.target.checked)} />
          Allow PRs
        </label>
        <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_dispatch} onChange={e => set('allow_dispatch', e.target.checked)} />
          Allow dispatch
        </label>
        <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_memory} onChange={e => set('allow_memory', e.target.checked)} />
          Allow memory
        </label>
      </div>
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end', marginTop: '0.25rem' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim() || !form.backend.trim() || !form.description.trim()}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>
  )
}
