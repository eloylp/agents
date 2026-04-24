'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'

type Config = Record<string, unknown>

interface Backend {
  name: string
  command: string
  version?: string
  models?: string[]
  healthy?: boolean
  health_detail?: string
  local_model_url?: string
  detected?: boolean
  timeout_seconds: number
  max_prompt_chars: number
}

interface BackendsDiscoveryResponse {
  backends?: Backend[]
}

interface OrphanedAgent {
  name: string
  backend: string
  model: string
  repos?: string[]
  available_models?: string[]
}

interface OrphanedAgentsResponse {
  count?: number
  agents?: OrphanedAgent[]
}

const orderedBackendNames = ['claude', 'codex']

const normalizeModels = (models?: string[]) => (models ?? []).map(m => m.trim()).filter(Boolean).sort()

const buildBackendDriftWarnings = (dbBackends: Backend[], diagnosticsBackends: Backend[]) => {
  const warnings: string[] = []
  const dbByName = new Map(dbBackends.map(b => [b.name, b]))
  const diagByName = new Map(diagnosticsBackends.map(b => [b.name, b]))
  const names = Array.from(new Set(Array.from(dbByName.keys()).concat(Array.from(diagByName.keys())))).sort()

  for (const name of names) {
    const db = dbByName.get(name)
    const diag = diagByName.get(name)

    if (!db && diag) {
      warnings.push(`${name}: detected by diagnostics but missing in database.`)
      continue
    }
    if (db && !diag) {
      warnings.push(`${name}: present in database but missing in diagnostics.`)
      continue
    }
    if (!db || !diag) continue

    if ((db.command || '') !== (diag.command || '')) {
      warnings.push(`${name}: command changed (db: ${db.command || 'empty'} → diagnostics: ${diag.command || 'empty'}).`)
    }
    if ((db.version || '') !== (diag.version || '')) {
      warnings.push(`${name}: version changed (db: ${db.version || 'empty'} → diagnostics: ${diag.version || 'empty'}).`)
    }
    if (!!db.healthy !== !!diag.healthy) {
      warnings.push(`${name}: health changed (db: ${db.healthy ? 'healthy' : 'failed'} → diagnostics: ${diag.healthy ? 'healthy' : 'failed'}).`)
    }
    if ((db.local_model_url || '') !== (diag.local_model_url || '')) {
      warnings.push(`${name}: local URL changed (db: ${db.local_model_url || 'empty'} → diagnostics: ${diag.local_model_url || 'empty'}).`)
    }
    const dbModels = normalizeModels(db.models)
    const diagModels = normalizeModels(diag.models)
    if (dbModels.join(',') !== diagModels.join(',')) {
      warnings.push(`${name}: models list changed (db: ${dbModels.length}, diagnostics: ${diagModels.length}).`)
    }
    if ((db.health_detail || '') !== (diag.health_detail || '')) {
      warnings.push(`${name}: health detail changed.`)
    }
  }
  return warnings
}

const inputStyle: React.CSSProperties = {
  width: '100%',
  padding: '6px 8px',
  border: '1px solid var(--border)',
  borderRadius: '6px',
  fontSize: '0.85rem',
  fontFamily: 'inherit',
  background: 'var(--bg-input)',
  color: 'var(--text)',
}

const labelStyle: React.CSSProperties = {
  fontSize: '0.8rem',
  color: 'var(--text-muted)',
  display: 'block',
  marginBottom: '3px',
}

const healthBadgeStyle = (ok: boolean | undefined): React.CSSProperties => ({
  display: 'inline-block',
  fontSize: '0.68rem',
  textTransform: 'uppercase',
  letterSpacing: '0.02em',
  padding: '1px 6px',
  borderRadius: '999px',
  border: `1px solid ${ok ? 'var(--success)' : 'var(--border-danger)'}`,
  color: ok ? 'var(--success)' : 'var(--text-danger)',
  background: ok ? 'rgba(16,185,129,0.1)' : 'var(--bg-danger)',
})

function JsonTree({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null) return <span style={{ color: 'var(--text-muted)' }}>null</span>
  if (typeof value === 'boolean') return <span style={{ color: '#f59e0b' }}>{String(value)}</span>
  if (typeof value === 'number') return <span style={{ color: 'var(--success)' }}>{value}</span>
  if (typeof value === 'string') {
    const isRedacted = value === '[redacted]'
    return <span style={{ color: isRedacted ? 'var(--text-danger)' : '#86efac' }}>{JSON.stringify(value)}</span>
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span style={{ color: 'var(--text-muted)' }}>[]</span>
    return (
      <span>
        {'['}
        <div style={{ paddingLeft: '1.25rem' }}>
          {value.map((v, i) => (
            <div key={i}><JsonTree value={v} depth={depth + 1} />{i < value.length - 1 ? ',' : ''}</div>
          ))}
        </div>
        {']'}
      </span>
    )
  }
  if (typeof value === 'object') {
    const entries = Object.entries(value as Record<string, unknown>)
    if (entries.length === 0) return <span style={{ color: 'var(--text-muted)' }}>{'{}'}</span>
    return (
      <span>
        {'{'}
        <div style={{ paddingLeft: '1.25rem' }}>
          {entries.map(([k, v], i) => (
            <div key={k}>
              <span style={{ color: '#93c5fd' }}>{JSON.stringify(k)}</span>
              <span style={{ color: 'var(--text-muted)' }}>: </span>
              <JsonTree value={v} depth={depth + 1} />
              {i < entries.length - 1 ? ',' : ''}
            </div>
          ))}
        </div>
        {'}'}
      </span>
    )
  }
  return <span style={{ color: 'var(--text-muted)' }}>{JSON.stringify(value)}</span>
}

export default function ConfigPage() {
  const [orphanFocus, setOrphanFocus] = useState(false)
  const [config, setConfig] = useState<Config | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [raw, setRaw] = useState(false)
  const [tab, setTab] = useState<'inspector' | 'backends' | 'import-export'>('inspector')

  const [backends, setBackends] = useState<Backend[]>([])
  const [backendsLoading, setBackendsLoading] = useState(false)
  const [backendDriftWarnings, setBackendDriftWarnings] = useState<string[]>([])
  const [orphanedAgents, setOrphanedAgents] = useState<OrphanedAgent[]>([])
  const [orphanModelSelection, setOrphanModelSelection] = useState<Record<string, string>>({})

  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [localBackendModalOpen, setLocalBackendModalOpen] = useState(false)
  const [localBackendName, setLocalBackendName] = useState('claude_local')
  const [localBackendURL, setLocalBackendURL] = useState('http://localhost:8080/v1/messages')
  const [deleteTarget, setDeleteTarget] = useState<Backend | null>(null)
  const [settingsTarget, setSettingsTarget] = useState<Backend | null>(null)
  const [settingsTimeout, setSettingsTimeout] = useState('600')
  const [settingsMaxPromptChars, setSettingsMaxPromptChars] = useState('12000')
  const [settingsLocalModelURL, setSettingsLocalModelURL] = useState('')

  const [importStatus, setImportStatus] = useState('')
  const [importError, setImportError] = useState('')
  const [importMode, setImportMode] = useState<'merge' | 'replace'>('merge')
  const fileInputRef = useRef<HTMLInputElement>(null)

  const sortBackends = (list: Backend[]) => {
    const rank = (name: string) => {
      const idx = orderedBackendNames.indexOf(name)
      if (idx >= 0) return idx
      return 100
    }
    return [...list].sort((a, b) => {
      const byRank = rank(a.name) - rank(b.name)
      if (byRank !== 0) return byRank
      return a.name.localeCompare(b.name)
    })
  }

  useEffect(() => {
    fetch('/config')
      .then(r => r.json())
      .then(data => { setConfig(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }, [])

  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    const requestedTab = params.get('tab')
    if (requestedTab === 'inspector' || requestedTab === 'backends' || requestedTab === 'import-export') {
      setTab(requestedTab)
    }
    setOrphanFocus(params.get('focus') === 'orphans')
  }, [])

  const loadBackends = () => {
    setBackendsLoading(true)
    Promise.all([fetch('/backends'), fetch('/backends/status'), fetch('/agents/orphans/status')])
      .then(async ([dbRes, diagRes, orphanRes]) => {
        if (!dbRes.ok) throw new Error((await dbRes.text()) || 'Failed to load backends from database')
        if (!diagRes.ok) throw new Error((await diagRes.text()) || 'Failed to load diagnostics')
        if (!orphanRes.ok) throw new Error((await orphanRes.text()) || 'Failed to load orphaned agents')

        const dbData = sortBackends(await dbRes.json() as Backend[])
        const diagData = await diagRes.json() as BackendsDiscoveryResponse
        const diagBackends = sortBackends(diagData.backends ?? [])
        const orphanData = await orphanRes.json() as OrphanedAgentsResponse
        const orphanAgents = orphanData.agents ?? []

        setBackends(dbData)
        setBackendDriftWarnings(buildBackendDriftWarnings(dbData, diagBackends))
        setOrphanedAgents(orphanAgents)
        setOrphanModelSelection(prev => {
          const next: Record<string, string> = {}
          for (const orphan of orphanAgents) {
            const suggested = orphan.available_models?.[0] ?? ''
            next[orphan.name] = prev[orphan.name] ?? suggested
          }
          return next
        })
        setBackendsLoading(false)
      })
      .catch((e: unknown) => {
        setSaveError(String(e))
        setBackendDriftWarnings([])
        setOrphanedAgents([])
        setOrphanModelSelection({})
        setBackends([])
        setBackendsLoading(false)
      })
  }

  useEffect(() => {
    if (tab === 'backends') loadBackends()
  }, [tab])

  const runDiscovery = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch('/backends/discover', { method: 'POST' })
      if (!res.ok) {
        setSaveError((await res.text()) || 'Discovery failed')
        setSaving(false)
        return
      }
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const addLocalBackend = async () => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch('/backends/local', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name: localBackendName, url: localBackendURL }),
      })
      if (!res.ok) {
        setSaveError((await res.text()) || 'Local backend save failed')
        setSaving(false)
        return
      }
      setLocalBackendModalOpen(false)
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const removeBackend = async () => {
    if (!deleteTarget) return
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch(`/backends/${encodeURIComponent(deleteTarget.name)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setSaveError((await res.text()) || 'Remove failed')
        setSaving(false)
        return
      }
      setDeleteTarget(null)
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const saveBackendRuntimeSettings = async () => {
    if (!settingsTarget) return
    const timeout = Number(settingsTimeout)
    const maxPromptChars = Number(settingsMaxPromptChars)
    const isLocalBackend = !!settingsTarget.local_model_url
    const localURL = settingsLocalModelURL.trim()
    if (!Number.isFinite(timeout) || timeout <= 0) {
      setSaveError('Timeout must be a positive number')
      return
    }
    if (!Number.isFinite(maxPromptChars) || maxPromptChars <= 0) {
      setSaveError('Max prompt chars must be a positive number')
      return
    }
    if (isLocalBackend && !localURL) {
      setSaveError('Local model URL is required')
      return
    }

    setSaving(true)
    setSaveError('')
    try {
      if (isLocalBackend) {
        const localRes = await fetch('/backends/local', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({
            name: settingsTarget.name,
            url: localURL,
          }),
        })
        if (!localRes.ok) {
          setSaveError((await localRes.text()) || 'Local backend URL update failed')
          setSaving(false)
          return
        }
      }
      const res = await fetch(`/backends/${encodeURIComponent(settingsTarget.name)}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          timeout_seconds: timeout,
          max_prompt_chars: maxPromptChars,
        }),
      })
      if (!res.ok) {
        setSaveError((await res.text()) || 'Runtime settings update failed')
        setSaving(false)
        return
      }
      setSettingsTarget(null)
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const upsertAgentModel = async (agentName: string, model: string) => {
    const readRes = await fetch(`/agents/${encodeURIComponent(agentName)}`)
    if (!readRes.ok) {
      throw new Error((await readRes.text()) || `Failed to load agent ${agentName}`)
    }
    const agent = await readRes.json() as Record<string, unknown>
    agent.model = model

    const writeRes = await fetch('/agents', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(agent),
    })
    if (!writeRes.ok) {
      throw new Error((await writeRes.text()) || `Failed to update agent ${agentName}`)
    }
  }

  const saveOrphanModel = async (agentName: string) => {
    const model = (orphanModelSelection[agentName] ?? '').trim()
    if (!model) {
      setSaveError(`Select a model for ${agentName} first`)
      return
    }
    setSaving(true)
    setSaveError('')
    try {
      await upsertAgentModel(agentName, model)
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const clearOrphanModel = async (agentName: string) => {
    setSaving(true)
    setSaveError('')
    try {
      await upsertAgentModel(agentName, '')
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const clearAllOrphanModels = async () => {
    if (orphanedAgents.length === 0) return
    setSaving(true)
    setSaveError('')
    try {
      const results = await Promise.allSettled(orphanedAgents.map(orphan => upsertAgentModel(orphan.name, '')))
      const failed = results.filter(r => r.status === 'rejected')
      if (failed.length > 0) {
        setSaveError(`Cleared ${orphanedAgents.length - failed.length}/${orphanedAgents.length} orphaned agents. Some updates failed.`)
      }
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const handleExport = async () => {
    const res = await fetch('/export')
    if (!res.ok) { alert('Export failed: ' + await res.text()); return }
    const blob = await res.blob()
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'config-export.yaml'
    a.click()
    URL.revokeObjectURL(url)
  }

  const handleImport = async (file: File) => {
    setImportStatus('')
    setImportError('')
    const text = await file.text()
    const url = importMode === 'replace' ? '/import?mode=replace' : '/import'
    const res = await fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-yaml' },
      body: text,
    })
    if (!res.ok) {
      setImportError((await res.text()) || 'Import failed')
      return
    }
    const summary = await res.json() as Record<string, number>
    setImportStatus(`Imported: ${summary.agents} agents, ${summary.skills} skills, ${summary.repos} repos, ${summary.backends} backends.`)
  }

  const tabStyle = (t: string): React.CSSProperties => ({
    padding: '6px 16px', borderRadius: '6px 6px 0 0', cursor: 'pointer', fontSize: '0.875rem',
    background: tab === t ? 'var(--bg-card)' : 'transparent',
    border: tab === t ? '1px solid var(--border)' : '1px solid transparent',
    borderBottom: tab === t ? '1px solid var(--bg-card)' : '1px solid var(--border)',
    color: tab === t ? 'var(--text-heading)' : 'var(--text-muted)', fontWeight: tab === t ? 600 : 400,
    marginBottom: '-1px',
  })

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Config</h1>
        </div>
        {tab === 'inspector' && config && (
          <button
            onClick={() => setRaw(r => !r)}
            style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text-muted)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            {raw ? 'Tree view' : 'Raw JSON'}
          </button>
        )}
      </div>

      <div style={{ display: 'flex', gap: '0', marginBottom: '0', borderBottom: '1px solid var(--border)' }}>
        <button style={tabStyle('inspector')} onClick={() => setTab('inspector')}>Inspector</button>
        <button style={tabStyle('backends')} onClick={() => setTab('backends')}>Backends and tools</button>
        <button style={tabStyle('import-export')} onClick={() => setTab('import-export')}>Import / Export</button>
      </div>

      {tab === 'inspector' && (
        <Card style={{ borderTopLeftRadius: 0 }}>
          {loading && <p style={{ color: 'var(--text-muted)' }}>Loading…</p>}
          {error && <p style={{ color: 'var(--text-danger)' }}>Error: {error}. (Is the API key set? Check Authorization header.)</p>}
          {config && (
            <pre style={{
              background: 'var(--bg)', borderRadius: '6px', padding: '1rem',
              fontSize: '0.8rem', lineHeight: '1.6', overflowX: 'auto',
              maxHeight: '700px', overflowY: 'auto',
            }}>
              {raw ? (
                <code style={{ color: 'var(--text)' }}>{JSON.stringify(config, null, 2)}</code>
              ) : (
                <JsonTree value={config} />
              )}
            </pre>
          )}
        </Card>
      )}

      {tab === 'backends' && (
        <Card style={{ borderTopLeftRadius: 0 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
            <span style={{ color: 'var(--text-muted)', fontSize: '0.875rem' }}>
              {backendsLoading ? 'Loading backends…' : `${backends.length} backend${backends.length !== 1 ? 's' : ''} configured`}
            </span>
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <button
                onClick={runDiscovery}
                style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '5px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600 }}
              >
                {saving ? 'Running…' : 'Run discovery'}
              </button>
              <button
                onClick={() => {
                  setSaveError('')
                  setLocalBackendName('claude_local')
                  setLocalBackendURL('http://localhost:8080/v1/messages')
                  setLocalBackendModalOpen(true)
                }}
                style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--text)', padding: '5px 10px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.8rem' }}
              >
                + Local backend
              </button>
            </div>
          </div>
          <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem', marginBottom: '0.75rem' }}>
            Backend arguments are managed by the daemon and are not user-editable.
          </p>
          {!backendsLoading && orphanedAgents.length > 0 && (
            <div style={{
              border: `1px solid ${orphanFocus ? 'var(--text-danger)' : 'var(--border-danger)'}`,
              borderRadius: '6px',
              padding: '0.75rem',
              background: 'var(--bg-danger)',
              marginBottom: '0.75rem',
            }}>
              <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.75rem', marginBottom: '0.5rem' }}>
                <div style={{ fontWeight: 700, color: 'var(--text-danger)' }}>
                  Orphaned agents detected
                </div>
                <button
                  onClick={clearAllOrphanModels}
                  disabled={saving}
                  style={{ padding: '5px 10px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-card)', color: 'var(--text-danger)', cursor: saving ? 'wait' : 'pointer', fontSize: '0.76rem', fontWeight: 600 }}
                >
                  Clear all pinned models
                </button>
              </div>
              <div style={{ color: 'var(--text)', fontSize: '0.8rem', marginBottom: '0.55rem' }}>
                These agents pin models not present in current backend models. Remap or clear the pin so backend defaults are used.
              </div>
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.45rem' }}>
                {orphanedAgents.map(orphan => (
                  <div key={orphan.name} style={{ border: '1px solid var(--border-danger)', borderRadius: '6px', padding: '0.55rem', background: 'var(--bg-card)' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.75rem' }}>
                      <div style={{ minWidth: 0, flex: 1 }}>
                        <div style={{ fontSize: '0.8rem', fontWeight: 700, color: 'var(--text-heading)' }}>
                          {orphan.name}
                        </div>
                        <div style={{ fontSize: '0.76rem', color: 'var(--text-muted)', marginTop: '2px' }}>
                          backend: {orphan.backend} · missing model: {orphan.model}
                        </div>
                        {!!(orphan.repos && orphan.repos.length > 0) && (
                          <div style={{ fontSize: '0.74rem', color: 'var(--text-faint)', marginTop: '2px' }}>
                            repos: {orphan.repos.join(', ')}
                          </div>
                        )}
                      </div>
                      <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center' }}>
                        <select
                          value={orphanModelSelection[orphan.name] ?? ''}
                          onChange={e => setOrphanModelSelection(prev => ({ ...prev, [orphan.name]: e.target.value }))}
                          style={{ ...inputStyle, width: '200px', fontSize: '0.76rem' }}
                        >
                          <option value="">Select replacement model</option>
                          {(orphan.available_models ?? []).map(model => (
                            <option key={model} value={model}>{model}</option>
                          ))}
                        </select>
                        <button
                          onClick={() => saveOrphanModel(orphan.name)}
                          disabled={saving}
                          style={{ padding: '5px 9px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.75rem', fontWeight: 600 }}
                        >
                          Remap
                        </button>
                        <button
                          onClick={() => clearOrphanModel(orphan.name)}
                          disabled={saving}
                          style={{ padding: '5px 9px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-card)', color: 'var(--text-danger)', cursor: saving ? 'wait' : 'pointer', fontSize: '0.75rem', fontWeight: 600 }}
                        >
                          Clear
                        </button>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}
          {!backendsLoading && backendDriftWarnings.length > 0 && (
            <div style={{ border: '1px solid var(--border-danger)', borderRadius: '6px', padding: '0.75rem', background: 'var(--bg-danger)', marginBottom: '0.75rem' }}>
              <div style={{ fontWeight: 700, color: 'var(--text-danger)', marginBottom: '0.35rem' }}>
                Diagnostics differ from stored backend configuration
              </div>
              <div style={{ color: 'var(--text)', fontSize: '0.8rem', marginBottom: '0.35rem' }}>
                Run discovery to persist the latest runtime state.
              </div>
              <ul style={{ margin: 0, paddingLeft: '1rem', color: 'var(--text-danger)', fontSize: '0.78rem', lineHeight: 1.45 }}>
                {backendDriftWarnings.map(w => <li key={w}>{w}</li>)}
              </ul>
            </div>
          )}
          <div style={{ display: 'flex', gap: '0.75rem', flexWrap: 'wrap', alignItems: 'flex-start' }}>
            <div style={{ flex: '2 1 540px', minWidth: 0 }}>
              <div style={{ border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '0.75rem', background: 'var(--bg)' }}>
                <div style={{ fontWeight: 700, color: 'var(--text-heading)', marginBottom: '0.4rem' }}>Backends</div>
                {!backendsLoading && backends.length === 0 && (
                  <p style={{ color: 'var(--text-faint)', fontSize: '0.85rem' }}>No backends configured.</p>
                )}
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
                  {backends.map(b => (
                    <div key={b.name} style={{ border: `1px solid ${b.healthy ? 'var(--border-subtle)' : 'var(--border-danger)'}`, borderRadius: '6px', padding: '0.75rem', background: 'var(--bg)' }}>
                      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start', gap: '0.75rem' }}>
                        <div style={{ minWidth: 0, flex: 1 }}>
                          <div style={{ display: 'flex', alignItems: 'center', gap: '0.45rem' }}>
                            <div style={{ fontWeight: 700, color: 'var(--text-heading)' }}>{b.name}</div>
                            <span style={healthBadgeStyle(b.healthy)}>{b.healthy ? 'healthy' : 'failed'}</span>
                          </div>
                          <div style={{ fontSize: '0.78rem', color: 'var(--text-muted)', marginTop: '2px' }}>
                            {b.command || 'not detected'}{b.version ? ` · ${b.version}` : ''}
                          </div>
                          {!!b.local_model_url && (
                            <div style={{ fontSize: '0.75rem', color: 'var(--text-faint)', marginTop: '2px', overflowWrap: 'anywhere' }}>
                              local URL: {b.local_model_url}
                            </div>
                          )}
                          <div style={{ fontSize: '0.75rem', color: 'var(--text-faint)', marginTop: '2px' }}>
                            timeout: {b.timeout_seconds}s · max prompt chars: {b.max_prompt_chars}
                          </div>
                          {b.health_detail && <div style={{ fontSize: '0.75rem', color: b.healthy ? 'var(--text-faint)' : 'var(--text-danger)', marginTop: '2px' }}>{b.health_detail}</div>}
                          {!!(b.models && b.models.length > 0) && (
                            <div style={{ marginTop: '0.5rem' }}>
                              <div style={{ fontSize: '0.74rem', color: 'var(--text-faint)', marginBottom: '0.3rem' }}>Models</div>
                              <ul style={{ margin: 0, padding: 0, listStyle: 'none', display: 'flex', flexDirection: 'column', gap: '0.25rem' }}>
                                {b.models.map(model => (
                                  <li key={model} style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: '0.5rem', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '0.2rem 0.4rem', background: 'var(--bg-card)' }}>
                                    <span style={{ fontSize: '0.76rem', color: 'var(--text)' }}>{model}</span>
                                    <span style={{ fontSize: '0.68rem', color: 'var(--text-faint)', border: '1px solid var(--border-subtle)', borderRadius: '999px', padding: '1px 6px', textTransform: 'uppercase', letterSpacing: '0.02em' }}>
                                      Read only
                                    </span>
                                  </li>
                                ))}
                              </ul>
                            </div>
                          )}
                        </div>
                        <div style={{ display: 'flex', gap: '0.35rem', alignItems: 'center' }}>
                          <button
                            onClick={() => {
                              setSaveError('')
                              setSettingsTarget(b)
                              setSettingsTimeout(String(b.timeout_seconds))
                              setSettingsMaxPromptChars(String(b.max_prompt_chars))
                              setSettingsLocalModelURL(b.local_model_url ?? '')
                            }}
                            style={{ padding: '5px 10px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.78rem', fontWeight: 600 }}
                          >
                            Runtime
                          </button>
                          {!!b.local_model_url && (
                            <button
                              onClick={() => {
                                setSaveError('')
                                setDeleteTarget(b)
                              }}
                              style={{ padding: '5px 10px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', cursor: 'pointer', fontSize: '0.78rem', fontWeight: 600 }}
                            >
                              Remove
                            </button>
                          )}
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              </div>
            </div>

          </div>
          {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.85rem', marginTop: '0.75rem' }}>{saveError}</p>}
        </Card>
      )}

      {tab === 'import-export' && (
        <Card style={{ borderTopLeftRadius: 0 }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '1.5rem' }}>
            <div>
              <h3 style={{ fontSize: '0.95rem', fontWeight: 600, color: 'var(--text-heading)', marginBottom: '0.5rem' }}>Export YAML</h3>
              <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem', marginBottom: '0.75rem' }}>
                Download the current fleet configuration (agents, skills, repos, backends) as a YAML file.
              </p>
              <button
                onClick={handleExport}
                style={{ padding: '7px 18px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--bg-input)', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                Export config.yaml
              </button>
            </div>

            <div style={{ borderTop: '1px solid var(--border-subtle)', paddingTop: '1.25rem' }}>
              <h3 style={{ fontSize: '0.95rem', fontWeight: 600, color: 'var(--text-heading)', marginBottom: '0.5rem' }}>Import YAML</h3>
              <p style={{ color: 'var(--text-muted)', fontSize: '0.85rem', marginBottom: '0.75rem' }}>
                Upload a YAML file to import agents, skills, repos, and backends into the store.
              </p>
              <div style={{ display: 'flex', gap: '1.5rem', marginBottom: '0.75rem' }}>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.85rem', cursor: 'pointer', color: 'var(--text)' }}>
                  <input type="radio" name="importMode" value="merge" checked={importMode === 'merge'} onChange={() => setImportMode('merge')} />
                  Merge — upsert records; existing records not in the file are kept
                </label>
                <label style={{ display: 'flex', alignItems: 'center', gap: '0.4rem', fontSize: '0.85rem', cursor: 'pointer', color: 'var(--text-danger)' }}>
                  <input type="radio" name="importMode" value="replace" checked={importMode === 'replace'} onChange={() => setImportMode('replace')} />
                  Replace — delete all existing records and replace with file contents
                </label>
              </div>
              <input
                ref={fileInputRef}
                type="file"
                accept=".yaml,.yml"
                style={{ display: 'none' }}
                onChange={e => {
                  const file = e.target.files?.[0]
                  if (file) handleImport(file)
                  if (fileInputRef.current) fileInputRef.current.value = ''
                }}
              />
              <button
                onClick={() => fileInputRef.current?.click()}
                style={{ padding: '7px 18px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--text)', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                Choose YAML file…
              </button>
              {importStatus && <p style={{ color: 'var(--success)', fontSize: '0.85rem', marginTop: '0.75rem' }}>{importStatus}</p>}
              {importError && <p style={{ color: 'var(--text-danger)', fontSize: '0.85rem', marginTop: '0.75rem' }}>{importError}</p>}
            </div>
          </div>
        </Card>
      )}

      {localBackendModalOpen && (
        <Modal title="Add Local Backend" onClose={() => setLocalBackendModalOpen(false)}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            <div>
              <label style={labelStyle}>Backend name</label>
              <input
                style={inputStyle}
                value={localBackendName}
                onChange={e => setLocalBackendName(e.target.value)}
                placeholder="qwen_local"
              />
            </div>
            <div>
              <label style={labelStyle}>Local model URL</label>
              <input
                style={inputStyle}
                value={localBackendURL}
                onChange={e => setLocalBackendURL(e.target.value)}
                placeholder="http://localhost:8080/v1/messages"
              />
            </div>
            <div style={{ border: '1px solid var(--border-subtle)', borderRadius: '6px', background: 'var(--bg)', padding: '0.7rem' }}>
              <div style={{ fontSize: '0.8rem', color: 'var(--text-heading)', fontWeight: 600, marginBottom: '0.4rem' }}>What to expect with local models</div>
              <ul style={{ margin: 0, paddingLeft: '1rem', color: 'var(--text-faint)', fontSize: '0.78rem', lineHeight: 1.45 }}>
                <li>Strong fit today: reviewer/scout specialists. Action-heavy coder flows can be more conservative with write tools.</li>
                <li>Local models can hallucinate templated facts (for example SHAs/statuses). Prompt for live verification before posting results.</li>
                <li>This backend reuses your discovered Claude CLI and routes it to your local OpenAI-compatible endpoint.</li>
                <li>Structured JSON schema is still enforced by the daemon, but output quality still depends on model capability.</li>
                <li>If runs are long, raise timeouts (proxy/backend) so tool loops do not fail mid-run.</li>
              </ul>
            </div>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setLocalBackendModalOpen(false)}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
              >
                Cancel
              </button>
              <button
                onClick={addLocalBackend}
                disabled={saving || !localBackendName.trim() || !localBackendURL.trim()}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Saving…' : 'Save local backend'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {deleteTarget && (
        <Modal title="Remove Local Backend" onClose={() => setDeleteTarget(null)}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            <p style={{ color: 'var(--text)', fontSize: '0.9rem', margin: 0 }}>
              Remove backend <strong>{deleteTarget.name}</strong>?
            </p>
            <p style={{ color: 'var(--text-faint)', fontSize: '0.8rem', margin: 0 }}>
              Any agents using this backend will fail until you reconfigure them.
            </p>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', margin: 0 }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setDeleteTarget(null)}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
              >
                Cancel
              </button>
              <button
                onClick={removeBackend}
                disabled={saving}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Removing…' : 'Remove'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {settingsTarget && (
        <Modal title={`Runtime settings — ${settingsTarget.name}`} onClose={() => setSettingsTarget(null)}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
            <div>
              <label style={labelStyle}>Timeout (seconds)</label>
              <input
                style={inputStyle}
                type="number"
                min={1}
                value={settingsTimeout}
                onChange={e => setSettingsTimeout(e.target.value)}
              />
            </div>
            <div>
              <label style={labelStyle}>Max prompt chars</label>
              <input
                style={inputStyle}
                type="number"
                min={1}
                value={settingsMaxPromptChars}
                onChange={e => setSettingsMaxPromptChars(e.target.value)}
              />
            </div>
            {!!settingsTarget.local_model_url && (
              <div>
                <label style={labelStyle}>Local model URL</label>
                <input
                  style={inputStyle}
                  type="url"
                  value={settingsLocalModelURL}
                  onChange={e => setSettingsLocalModelURL(e.target.value)}
                  placeholder="http://localhost:8080/v1/messages"
                />
              </div>
            )}
            <p style={{ color: 'var(--text-faint)', fontSize: '0.78rem', margin: 0 }}>
              Only runtime limits are editable here{settingsTarget.local_model_url ? ', plus local URL for local backends' : ''}. Backend command and runner flags remain managed by discovery/runtime.
            </p>
            {saveError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', margin: 0 }}>{saveError}</p>}
            <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
              <button
                onClick={() => setSettingsTarget(null)}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', cursor: 'pointer', fontSize: '0.875rem', color: 'var(--text-muted)' }}
              >
                Cancel
              </button>
              <button
                onClick={saveBackendRuntimeSettings}
                disabled={saving}
                style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                {saving ? 'Saving…' : 'Save settings'}
              </button>
            </div>
          </div>
        </Modal>
      )}

    </div>
  )
}
