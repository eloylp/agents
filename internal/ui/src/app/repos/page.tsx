'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import BadgePicker from '@/components/BadgePicker'
import { authHeaders } from '@/lib/apiKey'

// Supported webhook event kinds emitted by the daemon.
const SUPPORTED_EVENTS = [
  'issues.labeled',
  'issues.opened',
  'issues.edited',
  'issues.reopened',
  'issues.closed',
  'pull_request.labeled',
  'pull_request.opened',
  'pull_request.synchronize',
  'pull_request.ready_for_review',
  'pull_request.closed',
  'issue_comment.created',
  'pull_request_review.submitted',
  'pull_request_review_comment.created',
  'push',
]

interface Binding {
  agent: string
  labels?: string[]
  events?: string[]
  cron?: string
  enabled?: boolean
}

interface Repo {
  name: string
  enabled: boolean
  bindings: Binding[]
}

const emptyBinding: Binding = { agent: '', labels: [], events: [], cron: '', enabled: true }
const emptyRepo: Repo = { name: '', enabled: true, bindings: [] }

// isValidCron returns true for standard 5-field cron expressions with range validation.
// Fields: minute(0-59) hour(0-23) day-of-month(1-31) month(1-12) weekday(0-7)
// Each field may be: * | number | range (a-b) | step (*/n or a-b/n) | list (a,b,c).
// strictInt rejects strings that contain non-digit characters (e.g. "2-3", "2/3")
// so that malformed tokens with extra separators are not silently accepted.
function strictInt(s: string): number {
  return /^\d+$/.test(s) ? parseInt(s, 10) : NaN
}
function cronInRange(n: number, min: number, max: number): boolean {
  return Number.isInteger(n) && n >= min && n <= max
}
function cronValidItem(item: string, min: number, max: number): boolean {
  if (item === '*') return true
  if (item.startsWith('*/')) {
    const step = strictInt(item.slice(2))
    return !isNaN(step) && step >= 1
  }
  const slashIdx = item.indexOf('/')
  if (slashIdx !== -1) {
    const step = strictInt(item.slice(slashIdx + 1))
    if (isNaN(step) || step < 1) return false
    return cronValidItem(item.slice(0, slashIdx), min, max)
  }
  const dashIdx = item.indexOf('-')
  if (dashIdx !== -1) {
    const lo = strictInt(item.slice(0, dashIdx))
    const hi = strictInt(item.slice(dashIdx + 1))
    return cronInRange(lo, min, max) && cronInRange(hi, min, max) && lo <= hi
  }
  const n = strictInt(item)
  return cronInRange(n, min, max)
}
function isValidCron(expr: string): boolean {
  const parts = expr.trim().split(/\s+/)
  if (parts.length !== 5) return false
  const bounds: [number, number][] = [[0, 59], [0, 23], [1, 31], [1, 12], [0, 7]]
  return parts.every((f, i) => f.split(',').every(item => cronValidItem(item, bounds[i][0], bounds[i][1])))
}

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '6px 8px', border: '1px solid #bfdbfe', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: '#f8fafc', color: '#1e293b',
}
const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: '#64748b', display: 'block', marginBottom: '3px' }

function bindingTrigger(b: Binding): string {
  if (b.cron) return `cron: ${b.cron}`
  if (b.labels && b.labels.length > 0) return `labels: ${b.labels.join(', ')}`
  if (b.events && b.events.length > 0) return `events: ${b.events.join(', ')}`
  return '—'
}

function BindingEditor({ binding, onChange, onRemove, agentNames }: {
  binding: Binding
  onChange: (b: Binding) => void
  onRemove: () => void
  agentNames: string[]
}) {
  const [triggerType, setTriggerType] = useState<'labels' | 'events' | 'cron'>(
    binding.cron ? 'cron' : binding.events && binding.events.length > 0 ? 'events' : 'labels'
  )

  const setType = (t: 'labels' | 'events' | 'cron') => {
    setTriggerType(t)
    onChange({ ...binding, labels: [], events: [], cron: '' })
  }

  return (
    <div style={{ border: '1px solid #e2e8f0', borderRadius: '6px', padding: '0.75rem', background: '#f8fafc', marginBottom: '0.5rem' }}>
      <div style={{ display: 'flex', gap: '0.5rem', marginBottom: '0.5rem' }}>
        <div style={{ flex: 1 }}>
          <label style={labelStyle}>Agent</label>
          {agentNames.length > 0 ? (
            <select
              style={inputStyle}
              value={binding.agent}
              onChange={e => onChange({ ...binding, agent: e.target.value })}
            >
              <option value="">Select agent…</option>
              {/* Include current value as a fallback option when it is not in the cached list */}
              {binding.agent && !agentNames.includes(binding.agent) && (
                <option key={binding.agent} value={binding.agent}>{binding.agent}</option>
              )}
              {agentNames.map(n => <option key={n} value={n}>{n}</option>)}
            </select>
          ) : (
            <input
              style={inputStyle}
              value={binding.agent}
              onChange={e => onChange({ ...binding, agent: e.target.value })}
              placeholder="agent-name"
            />
          )}
        </div>
        <div style={{ flex: 1 }}>
          <label style={labelStyle}>Trigger type</label>
          <select style={inputStyle} value={triggerType} onChange={e => setType(e.target.value as 'labels' | 'events' | 'cron')}>
            <option value="labels">labels</option>
            <option value="events">events</option>
            <option value="cron">cron</option>
          </select>
        </div>
        <div style={{ display: 'flex', alignItems: 'flex-end' }}>
          <button onClick={onRemove} style={{ padding: '4px 8px', border: '1px solid #fecaca', background: '#fff5f5', borderRadius: '5px', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}>
            ✕
          </button>
        </div>
      </div>
      {triggerType === 'labels' && (
        <div>
          <label style={labelStyle}>Labels (comma-separated)</label>
          <input
            style={inputStyle}
            value={(binding.labels ?? []).join(', ')}
            onChange={e => onChange({ ...binding, labels: e.target.value.split(',').map(s => s.trim()).filter(Boolean) })}
            placeholder="ai:review:arch-reviewer"
          />
        </div>
      )}
      {triggerType === 'events' && (
        <div>
          <label style={labelStyle}>Events</label>
          <BadgePicker
            options={SUPPORTED_EVENTS}
            selected={binding.events ?? []}
            onChange={v => onChange({ ...binding, events: v })}
            placeholder="Add event…"
          />
        </div>
      )}
      {triggerType === 'cron' && (
        <div>
          <label style={labelStyle}>Cron expression</label>
          <input
            style={{ ...inputStyle, borderColor: (binding.cron && !isValidCron(binding.cron)) ? '#f87171' : '#bfdbfe' }}
            value={binding.cron ?? ''}
            onChange={e => onChange({ ...binding, cron: e.target.value })}
            placeholder="0 9 * * *"
          />
          {binding.cron && !isValidCron(binding.cron) && (
            <p style={{ color: '#f87171', fontSize: '0.75rem', marginTop: '3px' }}>
              Invalid cron expression — expected 5 fields: minute hour day month weekday (e.g. 0 9 * * 1-5)
            </p>
          )}
        </div>
      )}
      <div style={{ marginTop: '0.5rem' }}>
        <label style={{ fontSize: '0.82rem', color: '#64748b', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
          <input type="checkbox" checked={binding.enabled !== false} onChange={e => onChange({ ...binding, enabled: e.target.checked })} />
          Enabled
        </label>
      </div>
    </div>
  )
}

function RepoForm({ initial, isNew, agentNames, onSave, onCancel, saving, error }: {
  initial: Repo
  isNew: boolean
  agentNames: string[]
  onSave: (r: Repo) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<Repo>(initial)

  const addBinding = () => setForm(f => ({ ...f, bindings: [...f.bindings, { ...emptyBinding }] }))
  const updateBinding = (i: number, b: Binding) => setForm(f => {
    const bindings = [...f.bindings]
    bindings[i] = b
    return { ...f, bindings }
  })
  const removeBinding = (i: number) => setForm(f => ({ ...f, bindings: f.bindings.filter((_, idx) => idx !== i) }))

  const hasCronError = form.bindings.some(b => !!b.cron && !isValidCron(b.cron))

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Repo name * (owner/repo)</label>
        <input
          style={inputStyle}
          value={form.name}
          onChange={e => setForm(f => ({ ...f, name: e.target.value }))}
          placeholder="owner/repo"
          disabled={!isNew}
        />
      </div>
      <label style={{ fontSize: '0.85rem', color: '#1e293b', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
        <input type="checkbox" checked={form.enabled} onChange={e => setForm(f => ({ ...f, enabled: e.target.checked }))} />
        Enabled
      </label>

      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.4rem' }}>
          <label style={{ ...labelStyle, marginBottom: 0 }}>Bindings</label>
          <button
            onClick={addBinding}
            style={{ padding: '2px 10px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#eff6ff', cursor: 'pointer', fontSize: '0.75rem', color: '#2563eb' }}
          >
            + Add binding
          </button>
        </div>
        {form.bindings.length === 0 && <p style={{ color: '#94a3b8', fontSize: '0.8rem' }}>No bindings yet.</p>}
        {form.bindings.map((b, i) => (
          <BindingEditor key={i} binding={b} onChange={nb => updateBinding(i, nb)} onRemove={() => removeBinding(i)} agentNames={agentNames} />
        ))}
      </div>

      {error && <p style={{ color: '#f87171', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
          Cancel
        </button>
        <button
          onClick={() => onSave(form)}
          disabled={saving || !form.name.trim() || hasCronError}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #93c5fd', background: '#2563eb', color: '#fff', cursor: (saving || hasCronError) ? 'not-allowed' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </div>
    </div>
  )
}

export default function ReposPage() {
  const [repos, setRepos] = useState<Repo[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [agentNames, setAgentNames] = useState<string[]>([])

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Repo>(emptyRepo)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const load = () => {
    setLoading(true)
    fetch('/api/store/repos')
      .then(r => r.json())
      .then((data: Repo[]) => { setRepos(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
    // Refresh agent list so the binding dropdown stays current after agents are created/deleted.
    fetch('/api/store/agents')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setAgentNames(data.map(a => a.name).sort()))
      .catch(() => { /* store not configured — no-op */ })
  }

  useEffect(() => {
    load()
  }, [])

  const openCreate = () => {
    setSaveError('')
    setSelected({ ...emptyRepo })
    setModal('create')
  }

  const openEdit = (repo: Repo) => {
    setSaveError('')
    setSelected(repo)
    setModal('edit')
  }

  const confirmDelete = (name: string) => {
    setDeleteTarget(name)
    setSaveError('')
    setModal('delete')
  }

  const saveRepo = async (form: Repo) => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch('/api/store/repos', {
        method: 'POST',
        headers: authHeaders({ 'Content-Type': 'application/json' }),
        body: JSON.stringify(form),
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

  const deleteRepo = async () => {
    setSaving(true)
    const [owner, repo] = deleteTarget.split('/')
    try {
      const res = await fetch(`/api/store/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`, { method: 'DELETE', headers: authHeaders() })
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f' }}>Repos</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {repos.length} repo{repos.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <button
            onClick={openCreate}
            style={{ background: '#2563eb', border: '1px solid #1d4ed8', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Add repo
          </button>
          <button onClick={load} style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#64748b', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {error && <p style={{ color: '#f87171' }}>Error: {error}</p>}
      {!loading && !error && repos.length === 0 && (
        <p style={{ color: '#64748b' }}>No repos configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
        {repos.map(repo => (
          <Card key={repo.name}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '0.75rem' }}>
              <div>
                <div style={{ fontWeight: 700, color: '#1e3a5f', fontSize: '1rem' }}>{repo.name}</div>
                <span style={{
                  display: 'inline-block', marginTop: '3px', fontSize: '0.72rem', fontWeight: 600,
                  padding: '1px 7px', borderRadius: '10px',
                  background: repo.enabled ? '#dcfce7' : '#f1f5f9',
                  color: repo.enabled ? '#15803d' : '#94a3b8',
                  border: `1px solid ${repo.enabled ? '#bbf7d0' : '#e2e8f0'}`,
                }}>
                  {repo.enabled ? 'enabled' : 'disabled'}
                </span>
              </div>
              <div style={{ display: 'flex', gap: '0.5rem' }}>
                <button onClick={() => openEdit(repo)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#f8fafc', cursor: 'pointer', fontSize: '0.75rem', color: '#2563eb' }}>Edit</button>
                <button onClick={() => confirmDelete(repo.name)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #fecaca', background: '#fff5f5', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}>Delete</button>
              </div>
            </div>

            {repo.bindings.length > 0 ? (
              <table style={{ width: '100%', borderCollapse: 'collapse', fontSize: '0.8rem' }}>
                <thead>
                  <tr style={{ color: '#64748b' }}>
                    <th style={{ textAlign: 'left', padding: '3px 0', fontWeight: 400 }}>Agent</th>
                    <th style={{ textAlign: 'left', padding: '3px 0', fontWeight: 400 }}>Trigger</th>
                    <th style={{ textAlign: 'left', padding: '3px 0', fontWeight: 400 }}>Status</th>
                  </tr>
                </thead>
                <tbody>
                  {repo.bindings.map((b, i) => (
                    <tr key={i} style={{ borderTop: '1px solid #f8fafc' }}>
                      <td style={{ padding: '3px 0', color: '#1e3a5f', fontWeight: 500 }}>{b.agent}</td>
                      <td style={{ padding: '3px 0', color: '#64748b' }}>{bindingTrigger(b)}</td>
                      <td style={{ padding: '3px 0' }}>
                        <span style={{ fontSize: '0.72rem', color: b.enabled !== false ? '#15803d' : '#94a3b8' }}>
                          {b.enabled !== false ? 'on' : 'off'}
                        </span>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p style={{ color: '#94a3b8', fontSize: '0.8rem' }}>No bindings.</p>
            )}
          </Card>
        ))}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Add repo' : `Edit — ${selected.name}`} onClose={() => setModal(null)}>
          <RepoForm
            initial={selected}
            isNew={modal === 'create'}
            agentNames={agentNames}
            onSave={saveRepo}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete repo" onClose={() => setModal(null)}>
          <p style={{ color: '#1e293b', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete <strong>{deleteTarget}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: '#f87171', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
              Cancel
            </button>
            <button onClick={deleteRepo} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #fca5a5', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
