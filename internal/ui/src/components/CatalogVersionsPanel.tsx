'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'

type AssetType = 'prompt' | 'skill' | 'guardrail'

interface CatalogVersion {
  id: string
  version: number
  state: string
  description?: string
  content?: string
  prompt?: string
  enabled?: boolean
  position?: number
  source_type?: string
  base_version_id?: string
  created_at?: string
  published_at?: string
}

interface CatalogVersionReference {
  kind: string
  workspace_id?: string
  name: string
  tracking: boolean
}

const panelBorder = '1px solid var(--border)'

function assetPath(type: AssetType) {
  if (type === 'prompt') return 'prompts'
  if (type === 'skill') return 'skills'
  return 'guardrails'
}

function versionBody(type: AssetType, version: CatalogVersion) {
  if (type === 'skill') return version.prompt || ''
  if (type === 'guardrail') return version.content || ''
  return [version.description, version.content].filter(Boolean).join('\n\n')
}

function diffLines(oldText: string, newText: string) {
  const oldLines = oldText.split('\n')
  const newLines = newText.split('\n')
  const lengths = Array.from({ length: oldLines.length + 1 }, () => Array(newLines.length + 1).fill(0) as number[])
  for (let i = oldLines.length - 1; i >= 0; i -= 1) {
    for (let j = newLines.length - 1; j >= 0; j -= 1) {
      lengths[i][j] = oldLines[i] === newLines[j] ? lengths[i + 1][j + 1] + 1 : Math.max(lengths[i + 1][j], lengths[i][j + 1])
    }
  }
  const rows: { kind: 'same' | 'add' | 'del'; text: string }[] = []
  let i = 0
  let j = 0
  while (i < oldLines.length && j < newLines.length) {
    if (oldLines[i] === newLines[j]) {
      rows.push({ kind: 'same', text: ` ${oldLines[i]}` })
      i += 1
      j += 1
    } else if (lengths[i + 1][j] >= lengths[i][j + 1]) {
      rows.push({ kind: 'del', text: `-${oldLines[i]}` })
      i += 1
    } else {
      rows.push({ kind: 'add', text: `+${newLines[j]}` })
      j += 1
    }
  }
  for (; i < oldLines.length; i += 1) {
    rows.push({ kind: 'del', text: `-${oldLines[i]}` })
  }
  for (; j < newLines.length; j += 1) {
    rows.push({ kind: 'add', text: `+${newLines[j]}` })
  }
  return rows
}

export default function CatalogVersionsPanel({
  type, assetID, currentVersionID, onChanged,
}: {
  type: AssetType
  assetID: string
  currentVersionID?: string
  onChanged?: () => void
}) {
  const [versions, setVersions] = useState<CatalogVersion[]>([])
  const [references, setReferences] = useState<Record<string, CatalogVersionReference[]>>({})
  const [expanded, setExpanded] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')

  const current = useMemo(() => versions.find(v => v.id === currentVersionID) || versions.find(v => v.state === 'published'), [versions, currentVersionID])

  const load = useCallback((signal?: AbortSignal) => {
    if (!assetID) return
    setError('')
    fetch(`/${assetPath(type)}/${encodeURIComponent(assetID)}/versions`, { cache: 'no-store', signal })
      .then(r => {
        if (!r.ok) throw new Error(`load versions: ${r.status}`)
        return r.json()
      })
      .then((data: CatalogVersion[]) => {
        const next = data ?? []
        setVersions(next)
        setExpanded(e => e || next[0]?.id || '')
        return Promise.all(next.map(v =>
          fetch(`/${assetPath(type)}/${encodeURIComponent(assetID)}/versions/${encodeURIComponent(v.id)}/references`, { cache: 'no-store', signal })
            .then(r => {
              if (!r.ok) throw new Error(`load references: ${r.status}`)
              return r.json()
            })
            .then((refs: CatalogVersionReference[]) => [v.id, refs ?? []] as const)
        ))
      })
      .then(entries => setReferences(Object.fromEntries(entries)))
      .catch(e => {
        if (e instanceof DOMException && e.name === 'AbortError') return
        setError(String(e))
      })
  }, [assetID, type])

  useEffect(() => {
    const controller = new AbortController()
    load(controller.signal)
    return () => controller.abort()
  }, [load])

  const publish = async (versionID: string) => {
    setBusy(versionID)
    setError('')
    try {
      const res = await fetch(`/${assetPath(type)}/${encodeURIComponent(assetID)}/versions/${encodeURIComponent(versionID)}/publish`, { method: 'POST' })
      if (!res.ok) throw new Error(await res.text() || 'Publish failed')
      load()
      onChanged?.()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy('')
    }
  }

  const rollout = async (fromVersionID: string, toVersionID: string) => {
    setBusy(fromVersionID)
    setError('')
    try {
      const res = await fetch(`/${assetPath(type)}/${encodeURIComponent(assetID)}/versions/${encodeURIComponent(fromVersionID)}/rollout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ to_version_id: toVersionID }),
      })
      if (!res.ok) throw new Error(await res.text() || 'Rollout failed')
      load()
      onChanged?.()
    } catch (e) {
      setError(String(e))
    } finally {
      setBusy('')
    }
  }

  if (!assetID) return null

  return (
    <section style={{ border: panelBorder, borderRadius: 6, overflow: 'hidden' }}>
      <div style={{ padding: '0.65rem 0.75rem', borderBottom: panelBorder, background: 'var(--bg)', display: 'flex', justifyContent: 'space-between', gap: '0.75rem' }}>
        <strong style={{ color: 'var(--text-heading)', fontSize: '0.9rem' }}>Versions</strong>
        <span style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>{versions.length} total</span>
      </div>
      {error && <p style={{ color: 'var(--text-danger)', margin: '0.65rem 0.75rem', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(150px, 220px) minmax(0, 1fr)', minHeight: 260 }}>
        <div style={{ borderRight: panelBorder, background: 'var(--bg-card)' }}>
          {versions.map(v => {
            const refs = references[v.id] || []
            return (
              <button
                key={v.id}
                onClick={() => setExpanded(v.id)}
                style={{
                  width: '100%', textAlign: 'left', padding: '0.65rem 0.75rem', border: 0,
                  borderBottom: '1px solid var(--border-subtle)', background: expanded === v.id ? 'var(--bg-input)' : 'transparent',
                  color: 'var(--text)', cursor: 'pointer',
                }}
              >
                <span style={{ display: 'block', fontWeight: 700 }}>v{v.version}</span>
                <span style={{ display: 'block', color: 'var(--text-muted)', fontSize: '0.75rem' }}>{v.state}{v.id === currentVersionID ? ' · current' : ''}</span>
                {refs.length > 0 && <span style={{ display: 'block', color: 'var(--accent)', fontSize: '0.72rem', marginTop: 2 }}>{refs.length} reference{refs.length === 1 ? '' : 's'}</span>}
              </button>
            )
          })}
        </div>
        <div style={{ padding: '0.75rem', minWidth: 0 }}>
          {versions.filter(v => v.id === expanded).map(v => {
            const base = versions.find(candidate => candidate.id === v.base_version_id) || versions.find(candidate => candidate.version === v.version - 1)
            const refs = references[v.id] || []
            const exactRefs = refs.filter(ref => !ref.tracking)
            const canRollout = current && current.id !== v.id && exactRefs.length > 0
            const diff = diffLines(base ? versionBody(type, base) : '', versionBody(type, v))
            return (
              <div key={v.id} style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ color: 'var(--text-heading)', fontWeight: 700 }}>Version {v.version}</div>
                    <div style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>
                      {v.state}{v.source_type ? ` · ${v.source_type}` : ''}{v.published_at ? ` · published ${v.published_at}` : ''}
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.45rem', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    {v.state !== 'published' && (
                      <button disabled={busy === v.id} onClick={() => publish(v.id)} style={{ padding: '4px 9px', borderRadius: 5, border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: 'pointer' }}>
                        Publish
                      </button>
                    )}
                    {canRollout && (
                      <button disabled={busy === v.id} onClick={() => rollout(v.id, current.id)} style={{ padding: '4px 9px', borderRadius: 5, border: '1px solid var(--border)', background: 'var(--bg)', color: 'var(--accent)', cursor: 'pointer' }}>
                        Upgrade {exactRefs.length} exact pin{exactRefs.length === 1 ? '' : 's'} to v{current.version}
                      </button>
                    )}
                  </div>
                </div>
                {refs.length > 1 && (
                  <div style={{ border: '1px solid var(--border-warning, #f59e0b)', background: 'var(--bg-warning, rgba(245,158,11,0.08))', color: 'var(--text)', borderRadius: 6, padding: '0.55rem 0.65rem', fontSize: '0.8rem' }}>
                    Publishing or rolling out changes can affect {refs.length} live references.
                  </div>
                )}
                <div>
                  <div style={{ color: 'var(--text-muted)', fontSize: '0.78rem', marginBottom: 4 }}>References</div>
                  {refs.length === 0 ? (
                    <div style={{ color: 'var(--text-faint)', fontSize: '0.8rem' }}>No live references.</div>
                  ) : (
                    <div style={{ display: 'flex', flexWrap: 'wrap', gap: '0.35rem' }}>
                      {refs.map((ref, i) => (
                        <span key={`${ref.kind}-${ref.workspace_id}-${ref.name}-${i}`} style={{ border: panelBorder, borderRadius: 4, padding: '2px 6px', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
                          {ref.workspace_id ? `${ref.workspace_id}/` : ''}{ref.name} · {ref.tracking ? 'tracking' : 'exact'}
                        </span>
                      ))}
                    </div>
                  )}
                </div>
                <pre style={{ margin: 0, maxHeight: 260, overflow: 'auto', border: panelBorder, borderRadius: 6, background: 'var(--bg)', color: 'var(--text-muted)', fontSize: '0.75rem', lineHeight: 1.45, padding: '0.6rem' }}>
                  {diff.map((row, i) => (
                    <div key={i} style={{ color: row.kind === 'add' ? '#15803d' : row.kind === 'del' ? 'var(--text-danger)' : 'var(--text-muted)' }}>{row.text || ' '}</div>
                  ))}
                </pre>
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}
