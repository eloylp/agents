'use client'
import { useState, useEffect, useMemo } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'
import BadgePicker from '@/components/BadgePicker'
import RunButton from '@/components/RunButton'
import { Binding, groupByAgent, bindingsEqual } from '@/lib/bindings'

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

const SUPPORTED_EVENTS = [
  'issues.labeled', 'issues.opened', 'issues.edited', 'issues.reopened', 'issues.closed',
  'pull_request.labeled', 'pull_request.opened', 'pull_request.synchronize',
  'pull_request.ready_for_review', 'pull_request.closed',
  'issue_comment.created',
  'pull_request_review.submitted', 'pull_request_review_comment.created',
  'push',
]

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
  width: '100%', padding: '6px 8px', border: '1px solid var(--border)', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: 'var(--bg-input)', color: 'var(--text)',
}
const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: 'var(--text-muted)', display: 'block', marginBottom: '3px' }

function bindingTrigger(b: Binding): string {
  if (b.cron) return `cron: ${b.cron}`
  if (b.labels && b.labels.length > 0) return `labels: ${b.labels.join(', ')}`
  if (b.events && b.events.length > 0) return `events: ${b.events.join(', ')}`
  return '-'
}

// TriggerEditor edits one trigger row (type + value + enabled + delete).
// The agent name is managed by the parent AgentBindingGroup.
function TriggerEditor({ trigger, onChange, onRemove, knownLabels }: {
  trigger: TriggerBinding
  onChange: (t: TriggerBinding) => void
  onRemove: () => void
  knownLabels: string[]
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
          <BadgePicker
            options={knownLabels}
            selected={trigger.labels ?? []}
            onChange={v => onChange({ ...trigger, labels: v })}
            placeholder="Add label…"
            freeText
          />
        )}
        {triggerType === 'events' && (
          <BadgePicker
            options={SUPPORTED_EVENTS}
            selected={trigger.events ?? []}
            onChange={v => onChange({ ...trigger, events: v })}
            placeholder="Add event…"
          />
        )}
        {triggerType === 'cron' && (
          <div>
            <input
              style={{ ...inputStyle, borderColor: (trigger.cron && !isValidCron(trigger.cron)) ? 'var(--text-danger)' : 'var(--border)' }}
              value={trigger.cron ?? ''}
              onChange={e => onChange({ ...trigger, cron: e.target.value })}
              placeholder="0 9 * * *"
            />
            {trigger.cron && !isValidCron(trigger.cron) && (
              <p style={{ color: 'var(--text-danger)', fontSize: '0.75rem', marginTop: '3px' }}>
                Invalid cron, expected 5 fields: minute hour day month weekday (e.g. 0 9 * * 1-5)
              </p>
            )}
          </div>
        )}
      </div>
      <label style={{ display: 'flex', alignItems: 'center', gap: '0.3rem', fontSize: '0.78rem', color: 'var(--text-muted)', cursor: 'pointer', flexShrink: 0, paddingTop: '7px' }}>
        <input type="checkbox" checked={trigger.enabled !== false} onChange={e => onChange({ ...trigger, enabled: e.target.checked })} />
        on
      </label>
      <button
        onClick={onRemove}
        style={{ padding: '4px 7px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', borderRadius: '5px', cursor: 'pointer', fontSize: '0.72rem', color: 'var(--text-danger)', flexShrink: 0 }}
      >
        ✕
      </button>
    </div>
  )
}

// AgentBindingGroup shows all trigger bindings for one agent.
function AgentBindingGroup({ group, agentNames, knownLabels, onChange, onAddTrigger, onRemoveTrigger }: {
  group: AgentGroup
  agentNames: string[]
  knownLabels: string[]
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
    <div style={{ border: '1px solid var(--border)', borderRadius: '8px', padding: '0.75rem', marginBottom: '0.65rem', background: 'var(--bg-input)' }}>
      <div style={{ marginBottom: '0.5rem' }}>
        <label style={labelStyle}>Agent</label>
        {agentNames.length > 0 ? (
          <select
            style={{ ...inputStyle, fontWeight: 600 }}
            value={group.agent}
            onChange={e => onChange({ ...group, agent: e.target.value })}
          >
            <option value="">Select agent…</option>
            {agentNames.map(n => <option key={n} value={n}>{n}</option>)}
          </select>
        ) : (
          <input
            style={{ ...inputStyle, fontWeight: 600 }}
            value={group.agent}
            onChange={e => onChange({ ...group, agent: e.target.value })}
            placeholder="agent-name"
          />
        )}
      </div>
      {group.triggers.map((t, i) => (
        <TriggerEditor key={i} trigger={t} knownLabels={knownLabels} onChange={t2 => updateTrigger(i, t2)} onRemove={() => onRemoveTrigger(i)} />
      ))}
      <button
        onClick={onAddTrigger}
        style={{ padding: '2px 9px', borderRadius: '5px', border: '1px solid var(--border)', background: 'var(--bg-input)', cursor: 'pointer', fontSize: '0.73rem', color: 'var(--accent)', marginTop: '0.15rem' }}
      >
        + Add trigger
      </button>
    </div>
  )
}

function RepoForm({ initial, isNew, agentNames, knownLabels, existingRepos, onSave, onCancel, saving, error }: {
  initial: Repo
  isNew: boolean
  agentNames: string[]
  knownLabels: string[]
  existingRepos: Repo[]
  onSave: (r: Repo) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<{ name: string; enabled: boolean }>({ name: initial.name, enabled: initial.enabled })
  const [groups, setGroups] = useState<AgentGroup[]>(() => bindingsToGroups(initial.bindings))
  const [cloneSource, setCloneSource] = useState('')

  const applyClone = () => {
    const src = existingRepos.find(r => r.name === cloneSource)
    if (src) setGroups(bindingsToGroups(src.bindings))
  }

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
      <label style={{ fontSize: '0.85rem', color: 'var(--text)', display: 'flex', alignItems: 'center', gap: '0.4rem', cursor: 'pointer' }}>
        <input type="checkbox" checked={form.enabled} onChange={e => setForm(f => ({ ...f, enabled: e.target.checked }))} />
        Enabled
      </label>

      {existingRepos.length > 0 && (
        <div>
          <label style={labelStyle}>Clone bindings from</label>
          <div style={{ display: 'flex', gap: '0.5rem' }}>
            <select
              style={{ ...inputStyle, flex: 1 }}
              value={cloneSource}
              onChange={e => setCloneSource(e.target.value)}
            >
              <option value="">(none)</option>
              {existingRepos.map(r => <option key={r.name} value={r.name}>{r.name}</option>)}
            </select>
            <button
              onClick={applyClone}
              disabled={!cloneSource}
              style={{ padding: '6px 12px', borderRadius: '6px', border: '1px solid #1e3a5f', background: cloneSource ? '#0f1d32' : '#080f1a', cursor: cloneSource ? 'pointer' : 'not-allowed', fontSize: '0.8rem', color: cloneSource ? '#38bdf8' : '#334155', flexShrink: 0 }}
            >
              Apply
            </button>
          </div>
        </div>
      )}

      <div>
        <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.4rem' }}>
          <label style={{ ...labelStyle, marginBottom: 0 }}>Agent bindings</label>
          <button
            onClick={addGroup}
            style={{ padding: '2px 10px', borderRadius: '5px', border: '1px solid var(--border)', background: 'var(--bg-input)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--accent)' }}
          >
            + Add agent binding
          </button>
        </div>
        {groups.length === 0 && <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem' }}>No bindings yet.</p>}
        {groups.map((g, gi) => (
          <AgentBindingGroup
            key={gi}
            group={g}
            agentNames={agentNames}
            knownLabels={knownLabels}
            onChange={ng => updateGroup(gi, ng)}
            onAddTrigger={() => addTrigger(gi)}
            onRemoveTrigger={ti => removeTrigger(gi, ti)}
          />
        ))}
      </div>

      {error && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
        <button onClick={onCancel} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
          Cancel
        </button>
        <button
          onClick={handleSave}
          disabled={saving || !form.name.trim() || hasCronError}
          style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: (saving || hasCronError) ? 'not-allowed' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
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
  const [agentNames, setAgentNames] = useState<string[]>([])

  const knownLabels = useMemo(() => {
    const set = new Set<string>()
    for (const r of repos) {
      for (const b of r.bindings) {
        for (const l of (b.labels ?? [])) set.add(l)
      }
    }
    return Array.from(set).sort()
  }, [repos])

  const load = () => {
    setLoading(true)
    fetch('/repos')
      .then(r => r.json())
      .then((data: Repo[]) => { setRepos(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }

  useEffect(() => {
    load()
    fetch('/agents')
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setAgentNames(data.map(a => a.name)))
      .catch(() => { /* store not configured, no-op */ })
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

  // saveRepo persists changes to a repo. For new repos it POSTs the whole
  // payload (repo + bindings) in one shot, the existing full-replace path.
  // For existing repos it diffs the bindings against the original load and
  // issues atomic CREATE/PATCH/DELETE calls so a single edit doesn't clobber
  // unrelated bindings (e.g. between two tabs). The enabled flag, when it
  // changed, is updated via PATCH so bindings are preserved.
  const saveRepo = async (form: Repo) => {
    setSaving(true)
    setSaveError('')
    try {
      const isNew = !repos.some(r => r.name === form.name)
      if (isNew) {
        const res = await fetch('/repos', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(form),
        })
        if (!res.ok) {
          setSaveError((await res.text()) || 'Save failed')
          setSaving(false)
          return
        }
      } else {
        const original = repos.find(r => r.name === form.name)!
        // Map originals by id for fast diff lookups. Bindings without ids
        // should not exist on an already-persisted repo, but the guard makes
        // the diff safe against stale local state.
        const originalById = new Map<number, Binding>()
        for (const b of original.bindings) {
          if (typeof b.id === 'number') originalById.set(b.id, b)
        }
        const seenIDs = new Set<number>()
        const ops: Promise<Response>[] = []
        const encRepo = (n: string) => {
          const [o, r] = n.split('/')
          return `/repos/${encodeURIComponent(o)}/${encodeURIComponent(r)}`
        }
        for (const b of form.bindings) {
          if (typeof b.id === 'number' && originalById.has(b.id)) {
            seenIDs.add(b.id)
            if (!bindingsEqual(originalById.get(b.id)!, b)) {
              ops.push(fetch(`${encRepo(form.name)}/bindings/${b.id}`, {
                method: 'PATCH',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify(b),
              }))
            }
          } else {
            ops.push(fetch(`${encRepo(form.name)}/bindings`, {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify(b),
            }))
          }
        }
        for (const b of original.bindings) {
          if (typeof b.id === 'number' && !seenIDs.has(b.id)) {
            ops.push(fetch(`${encRepo(form.name)}/bindings/${b.id}`, { method: 'DELETE' }))
          }
        }
        if (form.enabled !== original.enabled) {
          ops.push(fetch(encRepo(form.name), {
            method: 'PATCH',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ enabled: form.enabled }),
          }))
        }
        const results = await Promise.all(ops)
        for (const r of results) {
          if (!r.ok && r.status !== 204) {
            setSaveError((await r.text()) || 'Save failed')
            setSaving(false)
            return
          }
        }
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
      const res = await fetch(`/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`, { method: 'DELETE' })
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
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Repos</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {repos.length} repo{repos.length !== 1 ? 's' : ''} configured
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem' }}>
          <button
            onClick={openCreate}
            style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
          >
            + Add repo
          </button>
          <button onClick={load} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-muted)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
            Refresh
          </button>
        </div>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
      {error && <p style={{ color: 'var(--text-danger)' }}>Error: {error}</p>}
      {!loading && !error && repos.length === 0 && (
        <p style={{ color: 'var(--text-muted)' }}>No repos configured.</p>
      )}

      <div style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
        {repos.map(repo => {
          const activeBindings = repo.bindings.filter(b => b.enabled !== false)
          const disabledBindings = repo.bindings.filter(b => b.enabled === false)
          const activeGroups = groupByAgent(activeBindings)
          const disabledGroups = groupByAgent(disabledBindings)
          const cardMuted = !repo.enabled
          return (
            <Card key={repo.name} style={{ opacity: cardMuted ? 0.65 : 1 }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: '0.75rem' }}>
                <div>
                  <div style={{ fontWeight: 700, color: 'var(--text-heading)', fontSize: '1rem' }}>{repo.name}</div>
                  <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center', marginTop: '3px', flexWrap: 'wrap' }}>
                    <span style={{
                      display: 'inline-block', fontSize: '0.75rem', fontWeight: 700,
                      padding: '2px 9px', borderRadius: '10px',
                      background: repo.enabled ? 'var(--success-bg)' : 'rgba(100,116,139,0.2)',
                      color: repo.enabled ? 'var(--success)' : 'var(--text-muted)',
                      border: `1px solid ${repo.enabled ? 'var(--success-border)' : 'var(--border-subtle)'}`,
                      textTransform: 'uppercase', letterSpacing: '0.03em',
                    }}>
                      {repo.enabled ? 'enabled' : 'disabled'}
                    </span>
                    <span style={{ fontSize: '0.72rem', color: 'var(--text-muted)' }}>
                      {activeBindings.length} active{disabledBindings.length > 0 ? `, ${disabledBindings.length} disabled` : ''}
                    </span>
                  </div>
                </div>
                <div style={{ display: 'flex', gap: '0.5rem' }}>
                  <button onClick={() => openEdit(repo)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border)', background: 'var(--bg-input)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--accent)' }}>Edit</button>
                  <button onClick={() => confirmDelete(repo.name)} style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', cursor: 'pointer', fontSize: '0.75rem', color: 'var(--text-danger)' }}>Delete</button>
                </div>
              </div>

              {repo.bindings.length === 0 && (
                <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No bindings.</p>
              )}

              {activeGroups.map(([agent, bindings]) => (
                <div key={`active-${agent}`} style={{ marginBottom: '0.5rem' }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '0.25rem', gap: '0.5rem' }}>
                    <span style={{ fontSize: '0.82rem', fontWeight: 600, color: 'var(--accent)' }}>{agent}</span>
                    {repo.enabled && <RunButton agent={agent} repos={[repo.name]} />}
                  </div>
                  {bindings.map((b, i) => (
                    <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', fontSize: '0.78rem', padding: '2px 0 2px 0.75rem', borderLeft: '2px solid var(--border)' }}>
                      <span style={{ color: 'var(--text-faint)' }}>{bindingTrigger(b)}</span>
                      <span style={{ fontSize: '0.7rem', color: 'var(--success)' }}>on</span>
                    </div>
                  ))}
                </div>
              ))}

              {disabledGroups.length > 0 && (
                <>
                  <div style={{
                    fontSize: '0.7rem', fontWeight: 600, color: 'var(--text-muted)',
                    textTransform: 'uppercase', letterSpacing: '0.05em',
                    marginTop: activeGroups.length > 0 ? '0.75rem' : 0, marginBottom: '0.4rem',
                    paddingTop: activeGroups.length > 0 ? '0.5rem' : 0,
                    borderTop: activeGroups.length > 0 ? '1px solid var(--border-subtle)' : 'none',
                  }}>
                    Disabled
                  </div>
                  {disabledGroups.map(([agent, bindings]) => (
                    <div key={`disabled-${agent}`} style={{ marginBottom: '0.5rem', opacity: 0.6 }}>
                      <div style={{ fontSize: '0.82rem', fontWeight: 600, color: 'var(--text-muted)', marginBottom: '0.25rem' }}>{agent}</div>
                      {bindings.map((b, i) => (
                        <div key={i} style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', fontSize: '0.78rem', padding: '2px 0 2px 0.75rem', borderLeft: '2px solid var(--border-subtle)' }}>
                          <span style={{ color: 'var(--text-faint)' }}>{bindingTrigger(b)}</span>
                          <span style={{ fontSize: '0.7rem', color: 'var(--text-muted)' }}>off</span>
                        </div>
                      ))}
                    </div>
                  ))}
                </>
              )}
            </Card>
          )
        })}
      </div>

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Add repo' : `Edit, ${selected.name}`} onClose={() => setModal(null)}>
          <RepoForm
            initial={selected}
            isNew={modal === 'create'}
            agentNames={agentNames}
            knownLabels={knownLabels}
            existingRepos={repos.filter(r => r.name !== selected.name)}
            onSave={saveRepo}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete repo" onClose={() => setModal(null)}>
          <p style={{ color: 'var(--text)', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete <strong>{deleteTarget}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}>
              Cancel
            </button>
            <button onClick={deleteRepo} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
