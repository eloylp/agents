'use client'

import { useEffect, useState } from 'react'
import BadgePicker from '@/components/BadgePicker'
import type { StoreAgent } from '@/lib/dispatch-wiring'
import { catalogLabel, catalogScope, catalogValue, visibleCatalogItems, type CatalogItem } from '@/lib/workspace'

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

interface CatalogVersionOption {
  id: string
  version: number
  state: string
}

const skillBaseRef = (value: string) => value.split('@')[0]

const publishedVersionOptions = (versions: CatalogVersionOption[]) => versions.filter(v => v.state === 'published')

// allow_memory defaults to true so newly created agents preserve the
// historical behaviour where autonomous runs persist memory by default.
export const emptyAgentForm: StoreAgent = {
  name: '', backend: '', model: '', skills: [], prompt_id: '', prompt_ref: '', prompt_scope: '', scope_type: 'workspace', scope_repo: '',
  allow_prs: false, allow_dispatch: false, allow_memory: true,
  can_dispatch: [], description: '',
}

export default function AgentForm({
  initial, isNew, workspace, backends, skillOptions, agentNames, promptOptions, repoNames, onSave, onCancel, saving, error,
  promptOptionsLoaded = true,
}: {
  initial: StoreAgent
  isNew: boolean
  workspace: string
  backends: BackendOption[]
  skillOptions: CatalogItem[]
  agentNames: string[]
  promptOptions: PromptOption[]
  promptOptionsLoaded?: boolean
  repoNames: string[]
  onSave: (a: StoreAgent) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<StoreAgent>(initial)
  const [promptVersions, setPromptVersions] = useState<CatalogVersionOption[]>([])
  const [skillVersions, setSkillVersions] = useState<Record<string, CatalogVersionOption[]>>({})
  const [versionError, setVersionError] = useState('')

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
  const selectedSkillRefs = form.skills.map(skillBaseRef)
  const unresolvedSkillPins = Object.entries(form.skill_version_ids ?? {}).some(([skill, versionID]) =>
    versionID !== '' && selectedSkillRefs.includes(skill) && !(skillVersions[skill] ?? []).some(v => v.id === versionID),
  )
  const selectedPromptByRef = visiblePrompts.find(p => p.name === form.prompt_ref && catalogScope(p) === form.prompt_scope)
  const selectedPrompt = (form.prompt_id || (selectedPromptByRef ? catalogValue(selectedPromptByRef) : form.prompt_ref)).trim()
  const promptRefHidden = selectedPrompt !== '' && !promptValues.includes(selectedPrompt)
  const promptRefMissing = promptOptionsLoaded && promptRefHidden
  const canSave = !saving && form.name.trim() !== '' && form.backend.trim() !== '' && form.description.trim() !== '' &&
    selectedPrompt !== '' && promptOptionsLoaded && !promptRefMissing && !unresolvedSkillPins && (form.scope_type !== 'repo' || scopeRepo !== '')

  const setPrompt = (value: string) => {
    const prompt = visiblePrompts.find(p => catalogValue(p) === value)
    setForm(f => ({
      ...f,
      prompt_id: '',
      prompt_ref: prompt?.name ?? value,
      prompt_scope: prompt ? catalogScope(prompt) : '',
      prompt_version_id: '',
    }))
  }

  const setSkills = (values: string[]) => {
    setForm(f => {
      const pins = f.skill_version_ids ?? {}
      return {
        ...f,
        skills: values,
        skill_version_ids: Object.fromEntries(values.filter(value => pins[value]).map(value => [value, pins[value]])),
      }
    })
  }

  const setSkillVersion = (skill: string, versionID: string) => {
    setForm(f => {
      const pins = { ...(f.skill_version_ids ?? {}) }
      if (versionID) {
        pins[skill] = versionID
      } else {
        delete pins[skill]
      }
      return { ...f, skill_version_ids: pins }
    })
  }

  const saveForm = () => {
    const pins = form.skill_version_ids ?? {}
    const skills = form.skills.map(skill => {
      const base = skillBaseRef(skill)
      const pin = pins[base]
      if (!pin) return base
      const version = (skillVersions[base] ?? []).find(v => v.id === pin)
      return version ? `${base}@${version.version}` : base
    })
    onSave({
      ...form,
      skills,
      prompt_version_id: form.prompt_version_id || '',
    })
  }

  useEffect(() => {
    setForm(f => {
      const nextSkills = f.skills.map(skillBaseRef).filter(s => skillValues.includes(s))
      if (nextSkills.length === f.skills.length && nextSkills.every((skill, i) => skill === f.skills[i])) return f
      const pins = f.skill_version_ids ?? {}
      return {
        ...f,
        skills: nextSkills,
        skill_version_ids: Object.fromEntries(nextSkills.filter(skill => pins[skill]).map(skill => [skill, pins[skill]])),
      }
    })
  }, [skillValues.join('|')])

  useEffect(() => {
    const controller = new AbortController()
    setPromptVersions([])
    if (!selectedPrompt || promptRefHidden) {
      return () => controller.abort()
    }
    setVersionError('')
    fetch(`/prompts/${encodeURIComponent(selectedPrompt)}/versions`, { cache: 'no-store', signal: controller.signal })
      .then(r => {
        if (!r.ok) throw new Error(`load prompt versions: ${r.status}`)
        return r.json()
      })
      .then((data: CatalogVersionOption[]) => setPromptVersions(data ?? []))
      .catch(e => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        setVersionError(String(e))
      })
    return () => controller.abort()
  }, [selectedPrompt, promptRefHidden])

  useEffect(() => {
    const controller = new AbortController()
    const skills = Array.from(new Set(selectedSkillRefs.filter(skill => skillValues.includes(skill))))
    if (skills.length === 0) {
      setSkillVersions({})
      return () => controller.abort()
    }
    setVersionError('')
    Promise.all(skills.map(skill =>
      fetch(`/skills/${encodeURIComponent(skill)}/versions`, { cache: 'no-store', signal: controller.signal })
        .then(r => {
          if (!r.ok) throw new Error(`load skill versions: ${skill}: ${r.status}`)
          return r.json()
        })
        .then((data: CatalogVersionOption[]) => [skill, data ?? []] as const),
    ))
      .then(entries => setSkillVersions(Object.fromEntries(entries)))
      .catch(e => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        setVersionError(String(e))
      })
    return () => controller.abort()
  }, [selectedSkillRefs.join('|'), skillValues.join('|')])

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
        <BadgePicker options={skillValues} selected={selectedSkillRefs.filter(s => skillValues.includes(s))} onChange={setSkills} placeholder="Add skill..." />
        {selectedSkillRefs.length > 0 && (
          <div style={{ display: 'grid', gap: '0.45rem', marginTop: '0.5rem' }}>
            {selectedSkillRefs.filter(skill => skillValues.includes(skill)).map(skill => {
              const options = publishedVersionOptions(skillVersions[skill] ?? [])
              return (
                <div key={skill} style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr) minmax(170px, 220px)', gap: '0.5rem', alignItems: 'center' }}>
                  <span style={{ color: 'var(--text-muted)', fontSize: '0.78rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{skill}</span>
                  <select
                    aria-label={`${skill} version`}
                    style={inputStyle}
                    value={form.skill_version_ids?.[skill] ?? ''}
                    onChange={e => setSkillVersion(skill, e.target.value)}
                  >
                    <option value="">Track latest published</option>
                    {options.map(v => <option key={v.id} value={v.id}>Pin v{v.version}</option>)}
                  </select>
                </div>
              )
            })}
          </div>
        )}
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
          {promptRefHidden && <option value={selectedPrompt}>{selectedPrompt}{promptRefMissing ? ' (not visible)' : ''}</option>}
          {visiblePrompts.map(prompt => <option key={catalogValue(prompt)} value={catalogValue(prompt)}>{catalogLabel(prompt)}</option>)}
        </select>
        {promptRefMissing && (
          <div role="alert" style={{ marginTop: '4px', fontSize: '0.78rem', color: 'var(--text-danger)' }}>
            Selected prompt is no longer in the catalog.
          </div>
        )}
        {selectedPrompt && !promptRefMissing && (
          <div style={{ marginTop: '0.5rem' }}>
            <label style={labelStyle}>Prompt version</label>
            <select aria-label="Prompt version" style={inputStyle} value={form.prompt_version_id ?? ''} onChange={e => set('prompt_version_id', e.target.value)}>
              <option value="">Track latest published</option>
              {publishedVersionOptions(promptVersions).map(v => <option key={v.id} value={v.id}>Pin v{v.version}</option>)}
            </select>
          </div>
        )}
      </div>
      <div style={{ display: 'grid', gridTemplateColumns: form.scope_type === 'repo' ? '1fr 1fr' : '1fr', gap: '0.75rem' }}>
        <div>
          <label style={labelStyle}>Scope</label>
          <select
            style={inputStyle}
            value={form.scope_type || 'workspace'}
            onChange={e => setForm(f => ({ ...f, scope_type: e.target.value, scope_repo: e.target.value === 'repo' ? f.scope_repo : '', prompt_id: '', prompt_ref: '', prompt_scope: '', prompt_version_id: '', skills: [], skill_version_ids: {} }))}
          >
            <option value="workspace">Workspace</option>
            <option value="repo">Repo</option>
          </select>
        </div>
        {form.scope_type === 'repo' && (
          <div>
            <label style={labelStyle}>Scoped repo *</label>
            <select style={inputStyle} value={form.scope_repo} onChange={e => setForm(f => ({ ...f, scope_repo: e.target.value, prompt_id: '', prompt_ref: '', prompt_scope: '', prompt_version_id: '', skills: [], skill_version_ids: {} }))}>
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
      {versionError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{versionError}</p>}
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end', marginTop: '0.25rem' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
          Cancel
        </button>
        <button
          onClick={saveForm}
          disabled={!canSave}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving...' : 'Save'}
        </button>
      </div>
    </div>
  )
}
