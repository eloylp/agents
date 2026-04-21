'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'
import StatusBadge from '@/components/StatusBadge'
import Modal from '@/components/Modal'
import Link from 'next/link'
import BadgePicker from '@/components/BadgePicker'

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
  const bg = state === 'done' ? 'rgba(52,211,153,0.15)' : state === 'error' ? 'rgba(248,113,113,0.15)' : 'rgba(56,189,248,0.12)'
  const color = state === 'done' ? '#34d399' : state === 'error' ? '#f87171' : '#38bdf8'
  const border = state === 'done' ? '#065f46' : state === 'error' ? '#7f1d1d' : '#0e7490'

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
  name: '', backend: 'auto', skills: [], prompt: '',
  allow_prs: false, allow_dispatch: false, can_dispatch: [], description: '',
}

function AgentForm({
  initial, isNew, backends, skillNames, agentNames, onSave, onCancel, saving, error,
}: {
  initial: StoreAgent
  isNew: boolean
  backends: string[]
  skillNames: string[]
  agentNames: string[]
  onSave: (a: StoreAgent) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<StoreAgent>(initial)

  const set = (k: keyof StoreAgent, v: unknown) => setForm(f => ({ ...f, [k]: v }))

  const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: '#64748b', display: 'block', marginBottom: '3px' }
  const inputStyle: React.CSSProperties = {
    width: '100%', padding: '6px 8px', border: '1px solid #1e3a5f', borderRadius: '6px',
    fontSize: '0.85rem', fontFamily: 'inherit', background: '#0f1d32', color: '#cbd5e1',
  }

  // Derive options: always include "auto", then any store-configured backends.
  const backendOptions = ['auto', ...backends.filter(b => b !== 'auto')]

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Name *</label>
        <input style={inputStyle} value={form.name} onChange={e => set('name', e.target.value)} placeholder="agent-name" disabled={!isNew} />
      </div>
      <div>
        <label style={labelStyle}>Backend</label>
        <select style={inputStyle} value={form.backend} onChange={e => set('backend', e.target.value)}>
          {backendOptions.map(b => <option key={b} value={b}>{b}</option>)}
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
        <textarea
          style={{ ...inputStyle, minHeight: '120px', resize: 'vertical' }}
          value={form.prompt}
          onChange={e => set('prompt', e.target.value)}
          placeholder="Agent system prompt…"
        />
      </div>
      <div>
        <label style={labelStyle}>Can dispatch</label>
        <BadgePicker options={agentNames.filter(n => n !== form.name)} selected={form.can_dispatch} onChange={v => set('can_dispatch', v)} placeholder="Add agent…" />
      </div>
      <div style={{ display: 'flex', gap: '1.5rem' }}>
        <label style={{ fontSize: '0.85rem', color: '#cbd5e1', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_prs} onChange={e => set('allow_prs', e.target.checked)} />
          Allow PRs
        </label>
        <label style={{ fontSize: '0.85rem', color: '#cbd5e1', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={form.allow_dispatch} onChange={e => set('allow_dispatch', e.target.checked)} />
          Allow dispatch
        </label>
      </div>
      {error && <p style={{ color: '#f87171', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end', marginTop: '0.25rem' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #1e3a5f', background: '#111d2e', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim()}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #0e7490', background: '#0e7490', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
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
          <div style={{ fontWeight: 700, fontSize: '1rem', color: '#e2e8f0' }}>{agent.name}</div>
          {agent.description && <div style={{ fontSize: '0.8rem', color: '#64748b', marginTop: '2px' }}>{agent.description}</div>}
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <StatusBadge status={currentStatus} />
          <button onClick={onEdit} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #1e3a5f', background: '#0a1628', cursor: 'pointer', fontSize: '0.75rem', color: '#38bdf8' }}>Edit</button>
          <button onClick={onDelete} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #7f1d1d', background: '#1c1017', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}>Delete</button>
        </div>
      </div>

      <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
        <span style={{ background: '#0a1628', border: '1px solid #1e3a5f', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: '#64748b' }}>
          {agent.backend}
        </span>
        {agent.skills?.map(s => (
          <span key={s} style={{ background: '#1e3a5f', border: '1px solid #1d4ed8', borderRadius: '4px', padding: '2px 6px', fontSize: '0.75rem', color: '#93c5fd' }}>
            {s}
          </span>
        ))}
      </div>

      {(agent.bindings ?? []).length > 0 && (
        <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8rem' }}>
          <thead>
            <tr style={{ color: '#64748b' }}>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Repo</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Trigger</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Last run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Next run</th>
              <th style={{ textAlign: 'left', padding: '4px 0', fontWeight: 400 }}>Status</th>
            </tr>
          </thead>
          <tbody>
            {(agent.bindings ?? []).map((b, i) => (
              <tr key={i} style={{ borderTop: '1px solid #1e293b' }}>
                <td style={{ padding: '4px 0', color: '#64748b' }}>{b.repo}</td>
                <td style={{ padding: '4px 0', color: '#64748b' }}>
                  {b.cron ? `cron: ${b.cron}` : b.labels?.join(', ') ?? b.events?.join(', ') ?? '—'}
                </td>
                <td style={{ padding: '4px 0', color: '#64748b' }}>{fmt(b.schedule?.last_run)}</td>
                <td style={{ padding: '4px 0', color: '#64748b' }}>{b.schedule ? fmt(b.schedule.next_run) : '—'}</td>
                <td style={{ padding: '4px 0' }}>
                  {b.schedule?.last_status ? <StatusBadge status={b.schedule.last_status} /> : <span style={{ color: '#94a3b8' }}>—</span>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', borderTop: '1px solid #1e3a5f', paddingTop: '0.5rem' }}>
        <div style={{ display: 'flex', gap: '1rem', fontSize: '0.75rem', color: '#94a3b8' }}>
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
  const [backendNames, setBackendNames] = useState<string[]>([])
  const [skillNames, setSkillNames] = useState<string[]>([])
  const [agentNames, setAgentNames] = useState<string[]>([])

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<StoreAgent>(emptyForm)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const loadRef = useRef(false)
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
    fetch('/backends')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setBackendNames(data.map(b => b.name)))
      .catch(() => {})
    fetch('/skills')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setSkillNames(data.map(s => s.name)))
      .catch(() => {})
    fetch('/agents')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setAgentNames(data.map(a => a.name)))
      .catch(() => {})
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [])

  const openEdit = async (agentName: string) => {
    setSaveError('')
    try {
      const res = await fetch(`/agents/${encodeURIComponent(agentName)}`)
      if (res.ok) {
        const data = await res.json()
        setSelected(data)
      } else {
        const a = agents.find(a => a.name === agentName)
        setSelected({
          name: agentName,
          backend: a?.backend ?? 'claude',
          skills: a?.skills ?? [],
          prompt: '',
          allow_prs: a?.allow_prs ?? false,
          allow_dispatch: a?.allow_dispatch ?? false,
          can_dispatch: a?.can_dispatch ?? [],
          description: a?.description ?? '',
        })
      }
    } catch {
      setSelected(emptyForm)
    }
    setModal('edit')
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
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const confirmDelete = (name: string) => {
    setDeleteTarget(name)
    setModal('delete')
  }

  const deleteAgent = async () => {
    setSaving(true)
    try {
      const res = await fetch(`/agents/${encodeURIComponent(deleteTarget)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        const msg = await res.text()
        setSaveError(msg || 'Delete failed')
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#e2e8f0' }}>Fleet Dashboard</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {agents.length} agent{agents.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center' }}>
          <Link href="/traces/" style={{ fontSize: '0.875rem', color: '#38bdf8' }}>View traces →</Link>
          <button
            onClick={openCreate}
            style={{ background: '#0e7490', border: '1px solid #0e7490', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Create agent
          </button>
          <button
            onClick={load}
            style={{ background: '#111d2e', border: '1px solid #1e3a5f', color: '#64748b', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {error && <p style={{ color: '#f87171' }}>Error: {error}</p>}
      {!loading && !error && agents.length === 0 && (
        <p style={{ color: '#64748b' }}>No agents configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        {agents.map(a => (
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
            initial={selected}
            isNew={modal === 'create'}
            backends={backendNames}
            skillNames={skillNames}
            agentNames={agentNames}
            onSave={saveAgent}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete agent" onClose={() => setModal(null)}>
          <p style={{ color: '#cbd5e1', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete <strong>{deleteTarget}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: '#f87171', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #1e3a5f', background: '#111d2e', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
              Cancel
            </button>
            <button onClick={deleteAgent} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #fca5a5', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
