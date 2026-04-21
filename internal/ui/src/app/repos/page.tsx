'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'

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

// TriggerBinding is a binding with the agent field omitted (agent is tracked by the group).
type TriggerBinding = Omit<Binding, 'agent'>

// AgentGroup groups one agent's trigger bindings together.
type AgentGroup = {
  agent: string
  triggers: TriggerBinding[]
}

const emptyTrigger: TriggerBinding = { labels: [], events: [], cron: '', enabled: true }
const emptyRepo: Repo = { name: '', enabled: true, bindings: [] }

function bindingsToGroups(bindings: Binding[]): AgentGroup[] {
  const groups: AgentGroup[] = []
  const idx = new Map<string, number>()
  for (const b of bindings) {
    const { agent, ...trigger } = b
    if (!idx.has(agent)) {
      idx.set(agent, groups.length)
      groups.push({ agent, triggers: [trigger] })
    } else {
      groups[idx.get(agent)!].triggers.push(trigger)
    }
  }
  return groups
}

function groupsToBindings(groups: AgentGroup[]): Binding[] {
  return groups.flatMap(g => g.triggers.map(t => ({ ...t, agent: g.agent })))
}

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

// TriggerEditor edits one trigger row (type + value + enabled + delete).
// The agent name is managed by the parent AgentBindingGroup.
function TriggerEditor({ trigger, onChange, onRemove }: {
  trigger: TriggerBinding
  onChange: (t: TriggerBinding) => void
  onRemove: () => void
}) {
  const [triggerType, setTriggerType] = useState<'labels' | 'events' | 'cron'>(
    trigger.cron ? 'cron' : trigger.events && trigger.events.length > 0 ? 'events' : 'labels'
  )

  const setType = (t: 'labels' | 'events' | 'cron') => {
    setTriggerType(t)
    onChange({ labels: [], events: [], cron: '', enabled: trigger.enabled })
  }

  return (
    <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'flex-start', marginBottom: '0.4rem' }}>
      <div style={{ width: '80px', flexShrink: 0 }}>
        <select
          style={{ ...inputStyle, fontSize: '0.78rem', padding: '6px 4px' }}
          value={triggerType}
          onChange={e => setType(e.target.value as 'labels' | 'events' | 'cron')}
        >
          <option value="labels">labels</option>
          <option value="events">events</option>
          <option value="cron">cron</option>
        </select>
      </div>
      <div style={{ flex: 1 }}>
        {triggerType === 'labels' && (
          <input
            style={inputStyle}
            value={(trigger.labels ?? []).join(', ')}
            onChange={e => onChange({ ...trigger, labels: e.target.value.split(',').map(s => s.trim()).filter(Boolean) })}
            placeholder="ai:review:pr-reviewer"
          />
        )}
        {triggerType === 'events' && (
          <input
            style={inputStyle}
            value={(trigger.events ?? []).join(', ')}
            onChange={e => onChange({ ...trigger, events: e.target.value.split(',').map(s => s.trim()).filter(Boolean) })}
            placeholder="pull_request.opened, push"
          />
        )}
        {triggerType === 'cron' && (
          <div>
            <input
              style={{ ...inputStyle, borderColor: (trigger.cron && !isValidCron(trigger.cron)) ? '#f87171' : '#bfdbfe' }}
              value={trigger.cron ?? ''}
              onChange={e => onChange({ ...trigger, cron: e.target.value })}
              placeholder="0 9 * * *"
            />
            {trigger.cron && !isValidCron(trigger.cron) && (
              <p style={{ color: '#f87171', fontSize: '0.75rem', marginTop: '3px' }}>
                Invalid cron — expected 5 fields: minute hour day month weekday (e.g. 0 9 * * 1-5)
              </p>
            )}
          </div>
        )}
      </div>
      <label style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', fontSize: '0.78rem', color: '#64748b', cursor: 'pointer', flexShrink: 0, paddingTop: '7px' }}>
        <input type="checkbox" checked={trigger.enabled !== false} onChange={e => onChange({ ...trigger, enabled: e.target.checked })} />
        on
      </label>
      <button
        onClick={onRemove}
        style={{ padding: '4px 7px', border: '1px solid #fecaca', background: '#fff5f5', borderRadius: '5px', cursor: 'pointer', fontSize: '0.72rem', color: '#dc2626', flexShrink: 0 }}
      >
        ✕
      </button>
    </div>
  )
}

// AgentBindingGroup shows all trigger bindings for one agent.
function AgentBindingGroup({ group, onChange, onAddTrigger, onRemoveTrigger }: {
  group: AgentGroup
  onChange: (g: AgentGroup) => void
  onAddTrigger: () => void
  onRemoveTrigger: (i: number) => void
}) {
  const updateTrigger = (i: number, t: TriggerBinding) => {
    const triggers = [...group.triggers]
    triggers[i] = t
    onChange({ ...group, triggers })
  }

  return (
    <div style={{ border: '1px solid #bfdbfe', borderRadius: '8px', padding: '0.75rem', marginBottom: '0.65rem', background: '#fafcff' }}>
      <div style={{ marginBottom: '0.5rem' }}>
        <label style={labelStyle}>Agent</label>
        <input
          style={{ ...inputStyle, fontWeight: 600 }}
          value={group.agent}
          onChange={e => onChange({ ...group, agent: e.target.value })}
          placeholder="agent-name"
        />
      </div>
      {group.triggers.map((t, i) => (
        <TriggerEditor key={i} trigger={t} onChange={t2 => updateTrigger(i, t2)} onRemove={() => onRemoveTrigger(i)} />
      ))}
      <button
        onClick={onAddTrigger}
        style={{ padding: '2px 9px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#eff6ff', cursor: 'pointer', fontSize: '0.73rem', color: '#2563eb', marginTop: '0.15rem' }}
      >
        + Add trigger
      </button>
    </div>
  )
}

function RepoForm({ initial, isNew, onSave, onCancel, saving, error }: {
  initial: Repo
  isNew: boolean
  onSave: (r: Repo) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<{ name: string; enabled: boolean }>({ name: initial.name, enabled: initial.enabled })
  const [groups, setGroups] = useState<AgentGroup[]>(() => bindingsToGroups(initial.bindings))

  const addGroup = () => setGroups(gs => [...gs, { agent: '', triggers: [{ ...emptyTrigger }] }])

  const updateGroup = (i: number, g: AgentGroup) => setGroups(gs => {
    const ng = [...gs]
    ng[i] = g
    return ng
  })

  const addTrigger = (gi: number) => setGroups(gs => {
    const ng = [...gs]
    ng[gi] = { ...ng[gi], triggers: [...ng[gi].triggers, { ...emptyTrigger }] }
    return ng
  })

  const removeTrigger = (gi: number, ti: number) => setGroups(gs => {
    const ng = [...gs]
    const triggers = ng[gi].triggers.filter((_, idx) => idx !== ti)
    if (triggers.length === 0) {
      // Auto-remove the group when its last trigger is deleted.
      return gs.filter((_, idx) => idx !== gi)
    }
    ng[gi] = { ...ng[gi], triggers }
    return ng
  })

  const hasCronError = groups.some(g => g.triggers.some(t => !!t.cron && !isValidCron(t.cron)))

  const handleSave = () => {
    onSave({ name: form.name, enabled: form.enabled, bindings: groupsToBindings(groups) })
  }

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
          <label style={{ ...labelStyle, marginBottom: 0 }}>Agent bindings</label>
          <button
            onClick={addGroup}
            style={{ padding: '2px 10px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#eff6ff', cursor: 'pointer', fontSize: '0.75rem', color: '#2563eb' }}
          >
            + Add agent binding
          </button>
        </div>
        {groups.length === 0 && <p style={{ color: '#94a3b8', fontSize: '0.8rem' }}>No bindings yet.</p>}
        {groups.map((g, gi) => (
          <AgentBindingGroup
            key={gi}
            group={g}
            onChange={ng => updateGroup(gi, ng)}
            onAddTrigger={() => addTrigger(gi)}
            onRemoveTrigger={ti => removeTrigger(gi, ti)}
          />
        ))}
      </div>

      {error && <p style={{ color: '#f87171', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
          Cancel
        </button>
        <button
          onClick={handleSave}
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
  }

  useEffect(() => { load() }, [])

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
        headers: { 'Content-Type': 'application/json' },
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
      const res = await fetch(`/api/store/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`, { method: 'DELETE' })
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
