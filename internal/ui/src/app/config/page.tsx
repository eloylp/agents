'use client'
import { useState, useEffect } from 'react'
import Card from '@/components/Card'

type Config = Record<string, unknown>

function JsonTree({ value, depth = 0 }: { value: unknown; depth?: number }) {
  if (value === null) return <span style={{ color: '#64748b' }}>null</span>
  if (typeof value === 'boolean') return <span style={{ color: '#f59e0b' }}>{String(value)}</span>
  if (typeof value === 'number') return <span style={{ color: '#34d399' }}>{value}</span>
  if (typeof value === 'string') {
    const isRedacted = value === '[redacted]'
    return <span style={{ color: isRedacted ? '#f87171' : '#86efac' }}>{JSON.stringify(value)}</span>
  }
  if (Array.isArray(value)) {
    if (value.length === 0) return <span style={{ color: '#94a3b8' }}>[]</span>
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
    if (entries.length === 0) return <span style={{ color: '#94a3b8' }}>{'{}'}</span>
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
  return <span style={{ color: '#94a3b8' }}>{JSON.stringify(value)}</span>
}

export default function ConfigPage() {
  const [config, setConfig] = useState<Config | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [raw, setRaw] = useState(false)

  useEffect(() => {
    fetch('/api/config')
      .then(r => r.json())
      .then(data => { setConfig(data); setLoading(false) })
      .catch(e => { setError(String(e)); setLoading(false) })
  }, [])

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#f1f5f9' }}>Config Inspector</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>Effective parsed config · secrets redacted</p>
        </div>
        {config && (
          <button
            onClick={() => setRaw(r => !r)}
            style={{ background: '#1e293b', border: '1px solid #334155', color: '#94a3b8', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}
          >
            {raw ? 'Tree view' : 'Raw JSON'}
          </button>
        )}
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}
      {error && <p style={{ color: '#f87171' }}>Error: {error}. (Is the API key set? Check Authorization header.)</p>}

      {config && (
        <Card>
          <pre style={{
            background: '#0f172a',
            borderRadius: '6px',
            padding: '1rem',
            fontSize: '0.8rem',
            lineHeight: '1.6',
            overflowX: 'auto',
            maxHeight: '700px',
            overflowY: 'auto',
          }}>
            {raw ? (
              <code style={{ color: '#e2e8f0' }}>{JSON.stringify(config, null, 2)}</code>
            ) : (
              <JsonTree value={config} />
            )}
          </pre>
        </Card>
      )}
    </div>
  )
}
