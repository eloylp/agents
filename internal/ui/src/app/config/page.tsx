'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'
import Modal from '@/components/Modal'

type Config = Record<string, unknown>

interface Backend {
  name: string
  command: string
  args: string[]
  env: Record<string, string>
  timeout_seconds: number
  max_prompt_chars: number
  redaction_salt_env: string
}

const emptyBackend: Backend = {
  name: '', command: '', args: [], env: {}, timeout_seconds: 600, max_prompt_chars: 12000, redaction_salt_env: '',
}

const inputStyle: React.CSSProperties = {
  width: '100%', padding: '6px 8px', border: '1px solid #bfdbfe', borderRadius: '6px',
  fontSize: '0.85rem', fontFamily: 'inherit', background: '#f8fafc', color: '#1e293b',
}
const labelStyle: React.CSSProperties = { fontSize: '0.8rem', color: '#64748b', display: 'block', marginBottom: '3px' }

function JsonTree({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null) return <span style={{ color: '#64748b' }}>null</span>
  if (typeof value === 'boolean') return <span style={{ color: '#f59e0b' }}>{String(value)}</span>
  if (typeof value === 'number') return <span style={{ color: '#34d399' }}>{value}</span>
  if (typeof value === 'string') {
    const isRedacted = value === '[redacted]'
    return <span style={{ color: isRedacted ? '#f87171' : '#86efac' }}>{JSON.stringify(value)}</span>
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span style={{ color: '#64748b' }}>[]</span>
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
    if (entries.length === 0) return <span style={{ color: '#64748b' }}>{'{}'}</span>
    return (
      <span>
        {'{'}
        <div style={{ paddingLeft: '1.25rem' }}>
          {entries.map(([k, v], i) => (
            <div key={k}>
              <span style={{ color: '#93c5fd' }}>{JSON.stringify(k)}</span>
              <span style={{ color: '#64748b' }}>: </span>
              <JsonTree value={v} depth={depth + 1} />
              {i < entries.length - 1 ? ',' : ''}
            </div>
          ))}
        </div>
        {'}'}
      </span>
    )
  }
  return <span style={{ color: '#64748b' }}>{JSON.stringify(value)}</span>
}

function EnvEditor({ env, onChange }: { env: Record<string, string>; onChange: (e: Record<string, string>) => void }) {
  const [pairs, setPairs] = useState<[string, string][]>(() => Object.entries(env))

  const update = (newPairs: [string, string][]) => {
    setPairs(newPairs)
    onChange(Object.fromEntries(newPairs.filter(([k]) => k.trim())))
  }

  return (
    <div>
      {pairs.map(([k, v], i) => (
        <div key={i} style={{ display: 'flex', gap: '0.4rem', marginBottom: '0.35rem' }}>
          <input
            style={{ ...inputStyle, flex: 1 }}
            placeholder="KEY"
            value={k}
            onChange={e => { const p = [...pairs]; p[i] = [e.target.value, v]; update(p) }}
          />
          <input
            style={{ ...inputStyle, flex: 2 }}
            placeholder="value"
            value={v}
            onChange={e => { const p = [...pairs]; p[i] = [k, e.target.value]; update(p) }}
          />
          <button
            onClick={() => update(pairs.filter((_, idx) => idx !== i))}
            style={{ padding: '3px 8px', border: '1px solid #fecaca', background: '#fff5f5', borderRadius: '5px', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}
          >✕</button>
        </div>
      ))}
      <button
        onClick={() => update([...pairs, ['', '']])}
        style={{ fontSize: '0.75rem', color: '#2563eb', background: 'none', border: 'none', cursor: 'pointer', padding: 0 }}
      >
        + Add env var
      </button>
    </div>
  )
}

function BackendForm({ initial, isNew, onSave, onCancel, saving, error }: {
  initial: Backend
  isNew: boolean
  onSave: (b: Backend) => void
  onCancel: () => void
  saving: boolean
  error: string
}) {
  const [form, setForm] = useState<Backend>(initial)
  const set = (k: keyof Backend, v: unknown) => setForm(f => ({ ...f, [k]: v }))

  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
      <div>
        <label style={labelStyle}>Name *</label>
        <input style={inputStyle} value={form.name} onChange={e => set('name', e.target.value)} placeholder="claude" disabled={!isNew} />
      </div>
      <div>
        <label style={labelStyle}>Command</label>
        <input style={inputStyle} value={form.command} onChange={e => set('command', e.target.value)} placeholder="claude" />
      </div>
      <div>
        <label style={labelStyle}>Args (comma-separated)</label>
        <input
          style={inputStyle}
          value={form.args.join(', ')}
          onChange={e => set('args', e.target.value.split(',').map(s => s.trim()).filter(Boolean))}
          placeholder="-p, --dangerously-skip-permissions"
        />
      </div>
      <div style={{ display: 'flex', gap: '1rem' }}>
        <div style={{ flex: 1 }}>
          <label style={labelStyle}>Timeout (seconds)</label>
          <input style={inputStyle} type="number" value={form.timeout_seconds} onChange={e => set('timeout_seconds', Number(e.target.value))} />
        </div>
        <div style={{ flex: 1 }}>
          <label style={labelStyle}>Max prompt chars</label>
          <input style={inputStyle} type="number" value={form.max_prompt_chars} onChange={e => set('max_prompt_chars', Number(e.target.value))} />
        </div>
      </div>
      <div>
        <label style={labelStyle}>Redaction salt env var</label>
        <input style={inputStyle} value={form.redaction_salt_env} onChange={e => set('redaction_salt_env', e.target.value)} placeholder="LOG_SALT" />
      </div>
      <div>
        <label style={{ ...labelStyle, marginBottom: '0.4rem' }}>Environment variables</label>
        <EnvEditor env={form.env} onChange={env => set('env', env)} />
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

export default function ConfigPage() {
  const [config, setConfig] = useState<Config | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [raw, setRaw] = useState(false)
  const [tab, setTab] = useState<'inspector' | 'backends' | 'import-export'>('inspector')

  const [backends, setBackends] = useState<Backend[]>([])
  const [backendsLoading, setBackendsLoading] = useState(false)

  const [modal, setModal] = useState<'create' | 'edit' | 'delete' | null>(null)
  const [selected, setSelected] = useState<Backend>(emptyBackend)
  const [deleteTarget, setDeleteTarget] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState('')

  const [importStatus, setImportStatus] = useState('')
  const [importError, setImportError] = useState('')
  const fileInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    fetch('/api/config')
      .then(r => r.json())
      .then(data => { setConfig(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }, [])

  const loadBackends = () => {
    setBackendsLoading(true)
    fetch('/api/store/backends')
      .then(r => r.json())
      .then((data: Backend[]) => { setBackends(data); setBackendsLoading(false) })
      .catch(() => setBackendsLoading(false))
  }

  useEffect(() => {
    if (tab === 'backends') loadBackends()
  }, [tab])

  const saveBackend = async (form: Backend) => {
    setSaving(true)
    setSaveError('')
    try {
      const res = await fetch('/api/store/backends', {
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
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const deleteBackend = async () => {
    setSaving(true)
    try {
      const res = await fetch(`/api/store/backends/${encodeURIComponent(deleteTarget)}`, { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setSaveError((await res.text()) || 'Delete failed')
        setSaving(false)
        return
      }
      setModal(null)
      loadBackends()
    } catch (e) {
      setSaveError(String(e))
    }
    setSaving(false)
  }

  const handleExport = async () => {
    const res = await fetch('/api/store/export')
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
    const res = await fetch('/api/store/import', {
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
    background: tab === t ? '#ffffff' : 'transparent',
    border: tab === t ? '1px solid #bfdbfe' : '1px solid transparent',
    borderBottom: tab === t ? '1px solid #ffffff' : '1px solid #bfdbfe',
    color: tab === t ? '#1e3a5f' : '#64748b', fontWeight: tab === t ? 600 : 400,
    marginBottom: '-1px',
  })

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#1e3a5f' }}>Config</h1>
        </div>
        {tab === 'inspector' && config && (
          <button
            onClick={() => setRaw(r => !r)}
            style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#64748b', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            {raw ? 'Tree view' : 'Raw JSON'}
          </button>
        )}
      </div>

      <div style={{ display: 'flex', gap: '0', marginBottom: '0', borderBottom: '1px solid #bfdbfe' }}>
        <button style={tabStyle('inspector')} onClick={() => setTab('inspector')}>Inspector</button>
        <button style={tabStyle('backends')} onClick={() => setTab('backends')}>Backends</button>
        <button style={tabStyle('import-export')} onClick={() => setTab('import-export')}>Import / Export</button>
      </div>

      {tab === 'inspector' && (
        <Card style={{ borderTopLeftRadius: 0 }}>
          {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
          {error && <p style={{ color: '#f87171' }}>Error: {error}. (Is the API key set? Check Authorization header.)</p>}
          {config && (
            <pre style={{
              background: '#f8fafc', borderRadius: '6px', padding: '1rem',
              fontSize: '0.8rem', lineHeight: '1.6', overflowX: 'auto',
              maxHeight: '700px', overflowY: 'auto',
            }}>
              {raw ? (
                <code style={{ color: '#1e293b' }}>{JSON.stringify(config, null, 2)}</code>
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
            <span style={{ color: '#64748b', fontSize: '0.875rem' }}>
              {backends.length} backend{backends.length !== 1 ? 's' : ''} configured
            </span>
            <div style={{ display: 'flex', gap: '0.5rem' }}>
              <button
                onClick={() => { setSaveError(''); setSelected({ ...emptyBackend }); setModal('create') }}
                style={{ background: '#2563eb', border: '1px solid #1d4ed8', color: '#fff', padding: '5px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600 }}
              >
                + Add backend
              </button>
              <button onClick={loadBackends} style={{ background: '#ffffff', border: '1px solid #bfdbfe', color: '#64748b', padding: '5px 10px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.8rem' }}>
                Refresh
              </button>
            </div>
          </div>
          {backendsLoading && <p style={{ color: '#64748b', fontSize: '0.85rem' }}>Loading…</p>}
          {!backendsLoading && backends.length === 0 && (
            <p style={{ color: '#94a3b8', fontSize: '0.85rem' }}>No backends configured.</p>
          )}
          <div style={{ display: 'flex', flexDirection: 'column', gap: '0.6rem' }}>
            {backends.map(b => (
              <div key={b.name} style={{ border: '1px solid #e2e8f0', borderRadius: '6px', padding: '0.75rem', background: '#f8fafc' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ fontWeight: 700, color: '#1e3a5f' }}>{b.name}</div>
                    <div style={{ fontSize: '0.78rem', color: '#64748b', marginTop: '2px' }}>
                      {b.command} {b.args.slice(0, 3).join(' ')}{b.args.length > 3 ? ' …' : ''} · {b.timeout_seconds}s · {b.max_prompt_chars} chars
                    </div>
                    {Object.keys(b.env).length > 0 && (
                      <div style={{ fontSize: '0.75rem', color: '#94a3b8', marginTop: '2px' }}>
                        env: {Object.keys(b.env).join(', ')}
                      </div>
                    )}
                  </div>
                  <div style={{ display: 'flex', gap: '0.5rem' }}>
                    <button
                      onClick={() => { setSaveError(''); setSelected(b); setModal('edit') }}
                      style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.75rem', color: '#2563eb' }}
                    >Edit</button>
                    <button
                      onClick={() => { setDeleteTarget(b.name); setSaveError(''); setModal('delete') }}
                      style={{ padding: '3px 10px', borderRadius: '5px', border: '1px solid #fecaca', background: '#fff5f5', cursor: 'pointer', fontSize: '0.75rem', color: '#dc2626' }}
                    >Delete</button>
                  </div>
                </div>
              </div>
            ))}
          </div>
        </Card>
      )}

      {tab === 'import-export' && (
        <Card style={{ borderTopLeftRadius: 0 }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: '1.5rem' }}>
            <div>
              <h3 style={{ fontSize: '0.95rem', fontWeight: 600, color: '#1e3a5f', marginBottom: '0.5rem' }}>Export YAML</h3>
              <p style={{ color: '#64748b', fontSize: '0.85rem', marginBottom: '0.75rem' }}>
                Download the current fleet configuration (agents, skills, repos, backends) as a YAML file.
              </p>
              <button
                onClick={handleExport}
                style={{ padding: '7px 18px', borderRadius: '6px', border: '1px solid #93c5fd', background: '#eff6ff', color: '#2563eb', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                Export config.yaml
              </button>
            </div>

            <div style={{ borderTop: '1px solid #e2e8f0', paddingTop: '1.25rem' }}>
              <h3 style={{ fontSize: '0.95rem', fontWeight: 600, color: '#1e3a5f', marginBottom: '0.5rem' }}>Import YAML</h3>
              <p style={{ color: '#64748b', fontSize: '0.85rem', marginBottom: '0.75rem' }}>
                Upload a YAML file to merge agents, skills, repos, and backends into the store.
                Existing records with the same name will be overwritten.
              </p>
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
                style={{ padding: '7px 18px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#ffffff', color: '#1e293b', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
              >
                Choose YAML file…
              </button>
              {importStatus && <p style={{ color: '#15803d', fontSize: '0.85rem', marginTop: '0.75rem' }}>{importStatus}</p>}
              {importError && <p style={{ color: '#f87171', fontSize: '0.85rem', marginTop: '0.75rem' }}>{importError}</p>}
            </div>
          </div>
        </Card>
      )}

      {(modal === 'create' || modal === 'edit') && (
        <Modal title={modal === 'create' ? 'Add backend' : `Edit — ${selected.name}`} onClose={() => setModal(null)}>
          <BackendForm
            initial={selected}
            isNew={modal === 'create'}
            onSave={saveBackend}
            onCancel={() => setModal(null)}
            saving={saving}
            error={saveError}
          />
        </Modal>
      )}

      {modal === 'delete' && (
        <Modal title="Delete backend" onClose={() => setModal(null)}>
          <p style={{ color: '#1e293b', fontSize: '0.9rem', marginBottom: '1.25rem' }}>
            Delete backend <strong>{deleteTarget}</strong>? This cannot be undone.
          </p>
          {saveError && <p style={{ color: '#f87171', fontSize: '0.8rem', marginBottom: '0.75rem' }}>{saveError}</p>}
          <div style={{ display: 'flex', gap: '0.75rem', justifyContent: 'flex-end' }}>
            <button onClick={() => setModal(null)} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #bfdbfe', background: '#fff', cursor: 'pointer', fontSize: '0.875rem', color: '#64748b' }}>
              Cancel
            </button>
            <button onClick={deleteBackend} disabled={saving} style={{ padding: '6px 16px', borderRadius: '6px', border: '1px solid #fca5a5', background: '#dc2626', color: '#fff', cursor: saving ? 'wait' : 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
              {saving ? 'Deleting…' : 'Delete'}
            </button>
          </div>
        </Modal>
      )}
    </div>
  )
}
