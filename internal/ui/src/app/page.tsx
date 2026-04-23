'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'
import StatusBadge from '@/components/StatusBadge'
import Modal from '@/components/Modal'
import Link from 'next/link'
import BadgePicker from '@/components/BadgePicker'
import MarkdownEditor from '@/components/MarkdownEditor'
import RepoFilter, { useRepoFilter } from '@/components/RepoFilter'

interface Binding {
  repo: string
  labels?: string[]
  events?: string[]
  cron?: string
  enabled: boolean
  schedule?: {
    last_run?: string
    next_run: string
    last_status?: string
  }
}

interface Agent {
  name: string
  backend: string
  model?: string
  skills?: string[]
  description?: string
  allow_dispatch: boolean
  can_dispatch?: string[]
  allow_prs: boolean
  current_status: string
  bindings?: Binding[]
}

interface StoreAgent {
  name: string
  backend: string
  model: string
  skills: string[]
  prompt: string
  allow_prs: boolean
  allow_dispatch: boolean
  can_dispatch: string[]
  description: string
}

function fmt(iso?: string) {
  if (!iso) return '—'
  return new Date(iso).toLocaleString()
}

function RunButton({ agent, repo }: { agent: string; repo: string }) {
  const [state, setState] = useState<'idle' | 'running' | 'done' | 'error'>('idle')
  const [eventId, setEventId] = useState('')

  const run = async () => {
    if (!repo) return
    setState('running')
    try {
      const res = await fetch('/run', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ agent, repo }),
      })
      if (res.status === 202) {
        const data = await res.json()
        setEventId(data.event_id ?? '')
        setState('done')
        setTimeout(() => setState('idle'), 3000)
      } else {
        setState('error')
        setTimeout(() => setState('idle'), 3000)
      }
    } catch {
      setState('error')
      setTimeout(() => setState('idle'), 3000)
    }
  }

  if (!repo) return null

  const label = state === 'running' ? 'Queuing...' : state === 'done' ? `Queued ${eventId.slice(0, 8)}` : state === 'error' ? 'Failed' : 'Run'
  const bg = state === 'done' ? 'var(--success-bg)' : state === 'error' ? 'var(--error-bg)' : 'var(--accent-bg)'
  const color = state === 'done' ? 'var(--success)' : state === 'error' ? 'var(--text-danger)' : 'var(--accent)'
  const border = state === 'done' ? 'var(--success-border)' : state === 'error' ? 'var(--border-danger)' : 'var(--btn-primary-border)'

  return (
    <button
      onClick={run}
      disabled={state === 'running'}
      style={{
        background: bg, color, border: `1px solid ${border}`,
        padding: '4px 12px', borderRadius: '6px', cursor: state === 'running' ? 'wait' : 'pointer',
        fontSize: '0.75rem', fontWeight: 600,
      }}
    >
      {label}
    </button>
  )
}

const emptyForm: StoreAgent = {
  name: '', backend: '', model: '', skills: [], prompt: '',
  allow_prs: false, allow_dispatch: false, can_dispatch: [], description: '',
}

interface BackendOption {
  name: string
  models?: string[]
  detected?: boolean
}

function AgentForm({
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
          <option value="">Select backend…</option>
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
        <BadgePicker options={skillNames} selected={form.skills} onChange={v => set('skills', v)} placeholder="Add skill…" />
      </div>
      <div>
        <label style={labelStyle}>Description</label>
        <input style={inputStyle} value={form.description} onChange={e => set('description', e.target.value)} placeholder="Short description" />
      </div>
      <div>
        <label style={labelStyle}>Prompt</label>
        <MarkdownEditor
          value={form.prompt}
          onChange={v => set('prompt', v)}
          placeholder="Agent system prompt…"
          minHeight={200}
        />
      </div>
      <div>
        <label style={labelStyle}>Can dispatch</label>
        <BadgePicker options={agentNames.filter(n => n !== form.name)} selected={form.can_dispatch} onChange={v => set('can_dispatch', v)} placeholder="Add agent…" />
      </div>
      <div style={{ display: 'flex', gap: '1.5rem' }}>
        <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_prs} onChange={e => set('allow_prs', e.target.checked)} />
          Allow PRs
        </label>
        <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_dispatch} onChange={e => set('allow_dispatch', e.target.checked)} />
          Allow dispatch
        </label>
      </div>
      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end', marginTop: '0.25rem' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim() || !form.backend.trim()}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

function AgentCard({ agent, onEdit, onDelete }: { agent: Agent; onEdit: () => void; onDelete: () => void }) {
  const scheduleStatuses = agent.bindings?.flatMap(b => b.schedule?.last_status ? [b.schedule.last_status] : []) ?? []
  const lastOutcome = scheduleStatuses.includes('error') ? 'error' : scheduleStatuses.includes('success') ? 'success' : 'idle'
  const currentStatus = agent.current_status === 'running' ? 'running' : lastOutcome

  return (
    <Card style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
        <div>
          <div style={{ fontWeight: 700, fontSize: '1rem', color: 'var(--text-heading)' }}>{agent.name}</div>
          {agent.description && <div style={{ fontSize: '0.8rem', color: 'var(--text-muted)', marginTop: '2px' }}>{agent.description}</div>}
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <StatusBadge status={currentStatus} />
          <button onClick={onEdit} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border)', background: 'var(--bg)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--accent)' }}>Edit</button>
          <button onClick={onDelete} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--text-danger)' }}>Delete</button>
        </div>
      </div>

      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
        <span style={{ background: 'var(--bg)', border: '1px solid var(--border)', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
          {agent.backend}{agent.model ? ` · ${agent.model}` : ''}
        </span>
        {agent.skills?.map(s => (
          <span key={s} style={{ background: 'var(--badge-skill-bg)', border: '1px solid var(--badge-skill-border)', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: 'var(--badge-skill-text)' }}>
            {s}
          </span>
        ))}
      </div>

      {(agent.bindings ?? []).length > 0 && (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8rem' }}>
          <thead>
            <tr style={{ color: 'var(--text-muted)' }}>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Repo</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Trigger</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Last run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Next run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Status</th>
            </tr>
          </thead>
          <tbody>
            {(agent.bindings ?? []).map((b, i) => (
              <tr key={i} style={{ borderTop: '1px solid var(--border-subtle)' }}>
                <td style={{ padding: '4px 0', color: 'var(--text-muted)' }}>{b.repo}</td>
                <td style={{ padding: '4px 0', color: 'var(--text-muted)' }}>
                  {b.cron ? `cron: ${b.cron}` : b.labels?.join(', ') ?? b.events?.join(', ') ?? '—'}
                </td>
                <td style={{ padding: '4px 0', color: 'var(--text-muted)' }}>{fmt(b.schedule?.last_run)}</td>
                <td style={{ padding: '4px 0', color: 'var(--text-muted)' }}>{b.schedule ? fmt(b.schedule.next_run) : '—'}</td>
                <td style={{ padding: '4px 0' }}>
                  {b.schedule?.last_status ? <StatusBadge status={b.schedule.last_status} /> : <span style={{ color: 'var(--text-faint)' }}>—</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderTop: '1px solid var(--border)', paddingTop: '0.5rem' }}>
        <div style={{ display: 'flex', gap: '1rem', fontSize: '0.75rem', color: 'var(--text-faint)' }}>
          {agent.allow_prs && <span>✓ PRs</span>}
          {agent.allow_dispatch && <span>✓ dispatch</span>}
          {(agent.can_dispatch ?? []).length > 0 && <span>→ {agent.can_dispatch!.join(', ')}</span>}
        </div>
        <RunButton agent={agent.name} repo={(agent.bindings ?? [])[0]?.repo ?? ''} />
      </div>
    </Card>
  )
}

export default function FleetPage() {
  const [agents, setAgents] = useState<Agent[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [backendOptions, setBackendOptions] = useState<BackendOption[]>([])
  const [skillNames, setSkillNames] = useState<string[]>([])
  const [agentNames, setAgentNames] = useState<string[]>([])
  const [repoFilter, setRepoFilter] = useRepoFilter()

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<StoreAgent>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState<{ name: string; bindings: Binding[] }>({ name: '', bindings: [] })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const loadRef = useRef(false)
  const loadLookups = () => {
    fetch('/backends')
      .then(r => r.ok ? r.json() : [])
      .then((data: BackendOption[]) => setBackendOptions((data ?? []).filter(b => b.detected !== false)))
      .catch(() => {})
    fetch('/skills')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setSkillNames(data.map(s => s.name)))
      .catch(() => {})
    fetch('/agents')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setAgentNames(data.map(a => a.name)))
      .catch(() => {})
  }

  const load = () => {
    if (!loadRef.current) setLoading(true)
    loadRef.current = true
    fetch('/agents')
      .then(r => r.json())
      .then(data => { setAgents(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => {
    load()
    loadLookups()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  const openEdit = async (agentName: string) => {
    setSaveError('')
    const a = agents.find(agent => agent.name === agentName)
    setSelected({
      name: agentName,
      backend: a?.backend ?? '',
      model: a?.model ?? '',
      skills: a?.skills ?? [],
      prompt: '',
      allow_prs: a?.allow_prs ?? false,
      allow_dispatch: a?.allow_dispatch ?? false,
      can_dispatch: a?.can_dispatch ?? [],
      description: a?.description ?? '',
    })
    setModal('edit')
    try {
      const res = await fetch(`/agents/${encodeURIComponent(agentName)}`)
      if (res.ok) {
        const data = await res.json() as Partial<StoreAgent>
        setSelected({
          ...emptyForm,
          ...data,
          name: data.name ?? agentName,
          backend: data.backend ?? '',
          model: data.model ?? '',
          skills: data.skills ?? [],
          can_dispatch: data.can_dispatch ?? [],
        })
      }
    } catch {
      // Keep optimistic modal data on fetch failures.
    }
  }

  const openCreate = () => {
    setSaveError('')
    setSelected(emptyForm)
    setModal('create')
  }

  const saveAgent = async (form: StoreAgent) => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch('/agents', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      })
      if (!res.ok) {
        const msg = await res.text()
        setSaveError(msg || 'Save failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
      loadLookups()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const confirmDelete = (name: string) => {
    const a = agents.find(agent => agent.name === name)
    setDeleteTarget({ name, bindings: a?.bindings ?? [] })
    setSaveError('')
    setModal('delete')
  }

  const deleteAgent = async () => {
    setSaving(true)
    try {
      const cascade = deleteTarget.bindings.length > 0
      const url = `/agents/${encodeURIComponent(deleteTarget.name)}${cascade ? '?cascade=true' : ''}`
      const res = await fetch(url, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        const msg = await res.text()
        setSaveError(msg || 'Delete failed')
        setSaving(false)
        return
      }
      setModal(null)
      load()
      loadLookups()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const bindingTriggerLabel = (b: Binding): string => {
    if (b.cron) return `cron: ${b.cron}`
    if (b.labels && b.labels.length > 0) return `labels: ${b.labels.join(', ')}`
    if (b.events && b.events.length > 0) return `events: ${b.events.join(', ')}`
    return '—'
  }

  const visibleAgents = repoFilter
    ? agents.filter(a => (a.bindings ?? []).some(b => b.repo === repoFilter))
    : agents

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Fleet Dashboard</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {visibleAgents.length} agent{visibleAgents.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          <RepoFilter selected={repoFilter} onChange={setRepoFilter} />
          <Link href="/traces/" style={{ fontSize: '0.875rem', color: 'var(--accent)' }}>View traces →</Link>
          <button
            onClick={openCreate}
            style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Create agent
          </button>
          <button
            onClick={load}
            style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-muted)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
      {error && <p style={{ color: 'var(--text-danger)' }}>Error: {error}</p>}
      {!loading && !error && visibleAgents.length === 0 && (
        <p style={{ color: 'var(--text-muted)' }}>No agents configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        {visibleAgents.map(a => (
          <AgentCard
            key={a.name}
            agent={a}
            onEdit={() => openEdit(a.name)}
            onDelete={() => confirmDelete(a.name)}
          />
        ))}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Create agent' : `Edit — ${selected.name}`} onClose={() => setModal(null)}>
          <AgentForm
            key={`${modal}:${selected.name}`}
            initial={selected}
            isNew={modal === 'create'}
            backends={backendOptions}
            skillNames={skillNames}
            agentNames={agentNames}
            onSave={saveAgent}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (() => {
        const bindings = deleteTarget.bindings
        const repoCount = new Set(bindings.map(b => b.repo)).size
        const hasBindings = bindings.length > 0
        return (
          <Modal title="Delete agent" onClose={() => setModal(null)}>
            <p style={{ color: 'var(--text)', fontSize: '0.9rem', marginBottom: hasBindings ? '0.75rem' : '1.25rem' }}>
              Delete <strong>{deleteTarget.name}</strong>? This cannot be undone.
            </p>
            {hasBindings && (
              <div style={{ marginBottom: '1.25rem' }}>
                <p style={{ color: 'var(--text-danger)', fontSize: '0.85rem', fontWeight: 600, marginBottom: '0.5rem' }}>
                  ⚠ This will also remove {bindings.length} binding{bindings.length !== 1 ? 's' : ''} from {repoCount} repo{repoCount !== 1 ? 's' : ''}:
                </p>
                <div style={{ border: '1px solid var(--border)', borderRadius: '6px', background: 'var(--bg-input)', padding: '0.5rem 0.75rem', maxHeight: '12rem', overflowY: 'auto' }}>
                  {bindings.map((b, i) => (
                    <div key={i} style={{ fontSize: '0.8rem', color: 'var(--text-muted)', padding: '2px 0', display: 'flex', gap: '0.6rem' }}>
                      <span style={{ color: 'var(--text)', fontWeight: 500 }}>{b.repo}</span>
                      <span style={{ color: 'var(--text-faint)' }}>{bindingTriggerLabel(b)}</span>
                      {b.enabled === false && <span style={{ color: 'var(--text-faint)', fontSize: '0.7rem' }}>(off)</span>}
                    </div>
                  ))}
                </div>
              </div>
            )}
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
                Cancel
              </button>
              <button onClick={deleteAgent} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
                {saving ? 'Deleting…' : hasBindings ? `Delete agent + ${bindings.length} binding${bindings.length !== 1 ? 's' : ''}` : 'Delete'}
              </button>
            </div>
          </Modal>
        )
      })()}
    </div>
  )
}
