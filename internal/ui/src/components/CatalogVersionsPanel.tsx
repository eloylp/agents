'use client'

import { useCallback, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import { formatDateTime } from '@/lib/datetime'

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
  source_ref?: string
  author?: string
  changelog?: string
  base_version_id?: string
  body_hash?: string
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
  if (type === 'guardrail') {
    return [
      version.description ? `description: ${version.description}` : '',
      version.content || '',
      `enabled: ${version.enabled ? 'true' : 'false'}`,
      `position: ${version.position ?? 0}`,
    ].filter(Boolean).join('\n')
  }
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

function shortHash(hash?: string) {
  return hash ? hash.slice(0, 12) : ''
}

function shortRef(ref?: string) {
  if (!ref) return ''
  const match = ref.match(/[0-9a-f]{40}/i)
  if (!match) return ref
  if (ref.length === 40) return ref.slice(0, 7)
  return ref.replace(match[0], match[0].slice(0, 7))
}

function formatDate(value?: string) {
  return formatDateTime(value)
}

function sameTimestamp(a?: string, b?: string) {
  if (!a || !b) return false
  const left = new Date(a).getTime()
  const right = new Date(b).getTime()
  if (Number.isNaN(left) || Number.isNaN(right)) return a === b
  return Math.abs(left - right) < 1000
}

function countRefs(refs: CatalogVersionReference[]) {
  return {
    tracking: refs.filter(ref => ref.tracking).length,
  }
}

function diffLabel(type: AssetType, base?: CatalogVersion) {
  const subject = type === 'prompt' ? 'description and content' : type === 'skill' ? 'prompt' : 'content and settings'
  if (!base) return `Initial ${subject}`
  return `Diff: ${subject} against v${base.version}`
}

function versionStateLabel(version: CatalogVersion) {
  if (version.state === 'published') return 'Published'
  return version.state
}

function TimelineDot({ state }: { state: string }) {
  const published = state === 'published'
  return (
    <span
      aria-hidden="true"
      style={{
        width: 12,
        height: 12,
        borderRadius: '50%',
        border: '2px solid var(--accent)',
        background: published ? 'var(--accent)' : 'var(--bg-card)',
        flex: '0 0 auto',
        marginTop: 3,
        position: 'relative',
        zIndex: 1,
        boxSizing: 'border-box',
      }}
    />
  )
}

function Badge({ children }: { children: ReactNode }) {
  return (
    <span style={{ border: panelBorder, borderRadius: 4, padding: '1px 5px', fontSize: '0.68rem', color: 'var(--text-muted)', background: 'var(--bg)' }}>
      {children}
    </span>
  )
}

export default function CatalogVersionsPanel({
  type, assetID, currentVersionID, onRestoreVersion,
}: {
  type: AssetType
  assetID: string
  currentVersionID?: string
  onRestoreVersion?: (version: CatalogVersion) => void
}) {
  const [versions, setVersions] = useState<CatalogVersion[]>([])
  const [references, setReferences] = useState<Record<string, CatalogVersionReference[]>>({})
  const [expanded, setExpanded] = useState('')
  const [error, setError] = useState('')

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
            .then((refs: CatalogVersionReference[]) => ({ id: v.id, refs: refs ?? [], error: '' }))
            .catch(e => {
              if (e instanceof DOMException && e.name === 'AbortError') throw e
              return { id: v.id, refs: [], error: String(e) }
            })
        ))
      })
      .then(entries => {
        setReferences(Object.fromEntries(entries.map(entry => [entry.id, entry.refs])))
        const failures = entries.map(entry => entry.error).filter(Boolean)
        if (failures.length > 0) setError(failures.join('; '))
      })
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

  if (!assetID) return null

  return (
    <section style={{ border: panelBorder, borderRadius: 6, overflow: 'hidden' }}>
      <div style={{ padding: '0.65rem 0.75rem', borderBottom: panelBorder, background: 'var(--bg)', display: 'flex', justifyContent: 'space-between', gap: '0.75rem' }}>
        <strong style={{ color: 'var(--text-heading)', fontSize: '0.9rem' }}>Versions</strong>
        <span style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>{versions.length} total</span>
      </div>
      {error && <p style={{ color: 'var(--text-danger)', margin: '0.65rem 0.75rem', fontSize: '0.8rem' }}>{error}</p>}
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(220px, 280px) minmax(0, 1fr)', minHeight: 220 }}>
        <div style={{ borderRight: panelBorder, background: 'var(--bg-card)', padding: '0.5rem 0', maxHeight: 360, overflow: 'auto' }}>
          {versions.map(v => {
            const refs = references[v.id] || []
            const counts = countRefs(refs)
            const isCurrent = v.id === currentVersionID
            const base = versions.find(candidate => candidate.id === v.base_version_id)
            const label = versionStateLabel(v)
            const createdLabel = formatDate(v.created_at) || 'created time unknown'
            const publishedLabel = formatDate(v.published_at)
            const showCreated = v.state !== 'published' || !v.published_at || !sameTimestamp(v.created_at, v.published_at)
            return (
              <button
                key={v.id}
                onClick={() => setExpanded(v.id)}
                style={{
                  width: '100%', textAlign: 'left', padding: '0.45rem 0.75rem', border: 0,
                  background: expanded === v.id ? 'var(--bg-input)' : 'transparent',
                  color: 'var(--text)', cursor: 'pointer', display: 'grid', gridTemplateColumns: '22px minmax(0, 1fr)',
                }}
              >
                <span style={{ position: 'relative', display: 'flex', justifyContent: 'center' }}>
                  <span style={{ position: 'absolute', top: -8, bottom: -8, width: 1, background: 'var(--border)', left: '50%', zIndex: 0 }} />
                  <TimelineDot state={v.state} />
                </span>
                <span>
                  <span style={{ display: 'flex', alignItems: 'center', gap: 6, flexWrap: 'wrap', fontWeight: 700 }}>
                    v{v.version}
                    {isCurrent && <Badge>Current</Badge>}
                    {counts.tracking > 0 && <Badge>{counts.tracking} tracking</Badge>}
                  </span>
                  <span style={{ display: 'block', color: 'var(--text-muted)', fontSize: '0.75rem', marginTop: 2 }}>
                    {label} · {v.state === 'published' && publishedLabel ? publishedLabel : createdLabel}
                  </span>
                  {v.published_at && showCreated && <span style={{ display: 'block', color: 'var(--text-faint)', fontSize: '0.72rem', marginTop: 1 }}>created {createdLabel}</span>}
                  {(v.source_type || v.source_ref) && (
                    <span title={v.source_ref || undefined} style={{ display: 'block', color: 'var(--text-faint)', fontSize: '0.72rem', marginTop: 1 }}>
                      {v.source_type || 'source'}{v.source_ref ? ` · ${shortRef(v.source_ref)}` : ''}
                    </span>
                  )}
                  {v.author && <span style={{ display: 'block', color: 'var(--text-faint)', fontSize: '0.72rem', marginTop: 1 }}>{v.author}</span>}
                  {v.changelog && <span style={{ display: 'block', color: 'var(--text)', fontSize: '0.75rem', marginTop: 4, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>{v.changelog}</span>}
                  <span style={{ display: 'block', color: 'var(--text-faint)', fontSize: '0.72rem', marginTop: 3 }}>
                    {base ? `base v${base.version}` : v.base_version_id ? `base ${shortRef(v.base_version_id)}` : 'no base'}{shortHash(v.body_hash) ? ` · body ${shortHash(v.body_hash)}` : ''}
                  </span>
                </span>
              </button>
            )
          })}
        </div>
        <div style={{ padding: '0.75rem', minWidth: 0 }}>
          {versions.filter(v => v.id === expanded).map(v => {
            const base = versions.find(candidate => candidate.id === v.base_version_id) || versions.find(candidate => candidate.version === v.version - 1)
            const refs = references[v.id] || []
            const canRestore = !!onRestoreVersion && !!current && v.version < current.version
            const diff = diffLines(base ? versionBody(type, base) : '', versionBody(type, v))
            return (
              <div key={v.id} style={{ display: 'flex', flexDirection: 'column', gap: '0.75rem' }}>
                <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', alignItems: 'flex-start' }}>
                  <div>
                    <div style={{ color: 'var(--text-heading)', fontWeight: 700 }}>Version {v.version}</div>
                    <div title={v.source_ref || undefined} style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>
                      {versionStateLabel(v)}{v.source_type ? ` · ${v.source_type}` : ''}{v.source_ref ? ` · ${shortRef(v.source_ref)}` : ''}{v.published_at ? ` · published ${formatDate(v.published_at)}` : ''}
                    </div>
                  </div>
                  <div style={{ display: 'flex', gap: '0.45rem', flexWrap: 'wrap', justifyContent: 'flex-end' }}>
                    {canRestore && (
                      <button onClick={() => onRestoreVersion(v)} style={{ padding: '4px 9px', borderRadius: 5, border: '1px solid var(--border)', background: 'var(--bg)', color: 'var(--accent)', cursor: 'pointer' }}>
                        Rollback to this version
                      </button>
                    )}
                  </div>
                </div>
                {refs.length > 1 && (
                  <div style={{ border: '1px solid var(--border-warning, #f59e0b)', background: 'var(--bg-warning, rgba(245,158,11,0.08))', color: 'var(--text)', borderRadius: 6, padding: '0.55rem 0.65rem', fontSize: '0.8rem' }}>
                    Changes to this catalog item can affect {refs.length} live references.
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
              </div>
            )
          })}
        </div>
      </div>
      <div style={{ borderTop: panelBorder, padding: '0.75rem', background: 'var(--bg-card)' }}>
        {versions.filter(v => v.id === expanded).map(v => {
          const base = versions.find(candidate => candidate.id === v.base_version_id) || versions.find(candidate => candidate.version === v.version - 1)
          const diff = diffLines(base ? versionBody(type, base) : '', versionBody(type, v))
          return (
            <div key={v.id} style={{ border: panelBorder, borderRadius: 6, overflow: 'hidden', background: 'var(--bg)' }}>
              <div style={{ padding: '0.5rem 0.65rem', borderBottom: panelBorder, color: 'var(--text-heading)', fontSize: '0.8rem', fontWeight: 700 }}>
                {diffLabel(type, base)}
              </div>
              <div role="table" aria-label={diffLabel(type, base)} style={{ maxHeight: 'min(56vh, 620px)', overflow: 'auto', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, monospace', fontSize: '0.78rem', lineHeight: 1.5 }}>
                {diff.map((row, i) => {
                  const add = row.kind === 'add'
                  const del = row.kind === 'del'
                  return (
                    <div
                      key={i}
                      role="row"
                      style={{
                        display: 'grid',
                        gridTemplateColumns: '3rem minmax(0, 1fr)',
                        background: add ? 'rgba(22, 163, 74, 0.09)' : del ? 'rgba(220, 38, 38, 0.08)' : 'transparent',
                        color: add ? '#15803d' : del ? 'var(--text-danger)' : 'var(--text-muted)',
                      }}
                    >
                      <span role="cell" style={{ userSelect: 'none', textAlign: 'right', padding: '0 0.55rem', color: 'var(--text-faint)', borderRight: panelBorder }}>{i + 1}</span>
                      <span role="cell" style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', padding: '0 0.65rem' }}>{row.text || ' '}</span>
                    </div>
                  )
                })}
              </div>
            </div>
          )
        })}
      </div>
    </section>
  )
}
