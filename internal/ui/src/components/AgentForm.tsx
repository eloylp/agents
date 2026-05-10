'use client'

import { useEffect, useState } from 'react'
import BadgePicker from '@/components/BadgePicker'
import type { StoreAgent } from '@/lib/dispatch-wiring'
import { catalogLabel, catalogValue, visibleCatalogItems, type CatalogItem } from '@/lib/workspace'

export type { StoreAgent }

export interface BackendOption {
  name: string
  models?: string[]
  detected?: boolean
}

export interface PromptOption {
  id?: string
  workspace_id?: string
  repo?: string
  name: string
}

// allow_memory defaults to true so newly created agents preserve the
// historical behaviour where autonomous runs persist memory by default.
export const emptyAgentForm: StoreAgent = {
  name: '', backend: '', model: '', skills: [], prompt_id: '', prompt_ref: '', scope_type: 'workspace', scope_repo: '',
  allow_prs: false, allow_dispatch: false, allow_memory: true,
  can_dispatch: [], description: '',
}

export default function AgentForm({
  initial, isNew, workspace, backends, skillOptions, agentNames, promptOptions, repoNames, onSave, onCancel, saving, error,
}: {
  initial: StoreAgent
  isNew: boolean
  workspace: string
  backends: BackendOption[]
  skillOptions: CatalogItem[]
  agentNames: string[]
  promptOptions: PromptOption[]
  repoNames: string[]
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
  const scopeRepo = form.scope_repo.trim()
  const catalogRepo = form.scope_type === 'repo' ? scopeRepo : ''
  const visiblePrompts = visibleCatalogItems(promptOptions, workspace, catalogRepo)
  const visibleSkills = visibleCatalogItems(skillOptions, workspace, catalogRepo)
  const promptValues = visiblePrompts.map(catalogValue)
  const skillValues = visibleSkills.map(catalogValue)
  const selectedPrompt = (form.prompt_id || form.prompt_ref).trim()
  const promptRefMissing = selectedPrompt !== '' && !promptValues.includes(selectedPrompt)
  const canSave = !saving && form.name.trim() !== '' && form.backend.trim() !== '' && form.description.trim() !== '' &&
    selectedPrompt !== '' && !promptRefMissing && (form.scope_type !== 'repo' || scopeRepo !== '')

  const setPrompt = (value: string) => {
    const prompt = visiblePrompts.find(p => catalogValue(p) === value)
    setForm(f => ({
      ...f,
      prompt_id: prompt?.id && prompt.id !== prompt.name ? prompt.id : '',
      prompt_ref: prompt?.id && prompt.id !== prompt.name ? '' : value,
    }))
  }

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
        <BadgePicker options={skillValues} selected={form.skills.filter(s => skillValues.includes(s))} onChange={v => set('skills', v)} placeholder="Add skill..." />
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
        <label style={labelStyle}>Prompt *</label>
        <select style={inputStyle} value={selectedPrompt} onChange={e => setPrompt(e.target.value)}>
          <option value="">Select prompt...</option>
          {promptRefMissing && <option value={selectedPrompt}>{selectedPrompt} (not visible)</option>}
          {visiblePrompts.map(prompt => <option key={catalogValue(prompt)} value={catalogValue(prompt)}>{catalogLabel(prompt)}</option>)}
        </select>
        {promptRefMissing && (
          <div role="alert" style={{ marginTop: '4px', fontSize: '0.78rem', color: 'var(--text-danger)' }}>
            Selected prompt is no longer in the catalog.
          </div>
        )}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: form.scope_type === 'repo' ? '1fr 1fr' : '1fr', gap: '0.75rem' }}>
        <div>
          <label style={labelStyle}>Scope</label>
          <select
            style={inputStyle}
            value={form.scope_type || 'workspace'}
            onChange={e => setForm(f => ({ ...f, scope_type: e.target.value, scope_repo: e.target.value === 'repo' ? f.scope_repo : '', prompt_id: '', prompt_ref: '', skills: [] }))}
          >
            <option value="workspace">Workspace</option>
            <option value="repo">Repo</option>
          </select>
        </div>
        {form.scope_type === 'repo' && (
          <div>
            <label style={labelStyle}>Scoped repo *</label>
            <select style={inputStyle} value={form.scope_repo} onChange={e => set('scope_repo', e.target.value)}>
              <option value="">Select repo...</option>
              {repoNames.map(name => <option key={name} value={name}>{name}</option>)}
            </select>
          </div>
        )}
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
          disabled={!canSave}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>
  )
}
