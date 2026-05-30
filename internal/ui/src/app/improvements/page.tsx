'use client'
import { useEffect, useMemo, useState } from 'react'
import WorkspaceSelect from '@/components/WorkspaceSelect'
import { useSelectedWorkspace, withWorkspace } from '@/lib/workspace'

interface FeedbackEvent {
  id: number
  workspace: string
  repo_owner: string
  repo_name: string
  source_type: string
  source_url: string
  author_login: string
  author_authorized: boolean
  issue_number?: number
  pr_number?: number
  raw_body: string
  tag: string
  file_path?: string
  line?: number
  commit_sha?: string
  link_confidence: string
  link_diagnostics?: string
  linked_agent_name?: string
  linked_prompt_version_id?: string
  status: string
  ingested_at: string
}

export default function ImprovementsPage() {
  const { workspace } = useSelectedWorkspace()
  const [rows, setRows] = useState<FeedbackEvent[]>([])
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    const qs = status ? `/improvements/feedback?status=${encodeURIComponent(status)}` : '/improvements/feedback'
    fetch(withWorkspace(qs, workspace), { cache: 'no-store' })
      .then(r => r.ok ? r.json() : [])
      .then(data => {
        if (!cancelled) setRows(data ?? [])
      })
      .catch(() => {
        if (!cancelled) setRows([])
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => { cancelled = true }
  }, [workspace, status])

  const counts = useMemo(() => rows.reduce<Record<string, number>>((acc, row) => {
    acc[row.status] = (acc[row.status] ?? 0) + 1
    return acc
  }, {}), [rows])

  return (
    <main style={{ display: 'grid', gap: '1rem' }}>
      <section style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <div>
          <h1 style={{ fontSize: '1.45rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }}>Improvements</h1>
          <div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
            {loading ? 'Loading feedback' : `${rows.length} feedback events`}
            {Object.keys(counts).length > 0 ? ` · ${Object.entries(counts).map(([k, v]) => `${k}: ${v}`).join(' · ')}` : ''}
          </div>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <WorkspaceSelect />
          <select value={status} onChange={e => setStatus(e.target.value)} style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '7px 9px' }}>
            <option value="">All statuses</option>
            <option value="new">New</option>
            <option value="ignored">Ignored</option>
            <option value="analyzed">Analyzed</option>
            <option value="linked_to_recommendation">Linked</option>
          </select>
        </div>
      </section>

      <section style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
        <div style={{ display: 'grid', gridTemplateColumns: '135px 150px 92px 1fr 150px 110px', gap: '0.75rem', padding: '0.65rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', color: 'var(--text-muted)', fontSize: '0.72rem', fontWeight: 700, textTransform: 'uppercase' }}>
          <span>Captured</span>
          <span>Source</span>
          <span>Status</span>
          <span>Feedback</span>
          <span>Attribution</span>
          <span>Author</span>
        </div>
        {rows.map(row => (
          <article key={row.id} style={{ display: 'grid', gridTemplateColumns: '135px 150px 92px 1fr 150px 110px', gap: '0.75rem', padding: '0.75rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', fontSize: '0.8rem', alignItems: 'start' }}>
            <time style={{ color: 'var(--text-faint)' }}>{new Date(row.ingested_at).toLocaleString()}</time>
            <div style={{ display: 'grid', gap: 3 }}>
              <a href={row.source_url} target="_blank" rel="noreferrer">{row.repo_owner}/{row.repo_name}</a>
              <span style={{ color: 'var(--text-faint)' }}>{row.source_type}{row.pr_number ? ` · PR #${row.pr_number}` : row.issue_number ? ` · issue #${row.issue_number}` : ''}</span>
              {row.file_path && <span style={{ color: 'var(--text-faint)', overflowWrap: 'anywhere' }}>{row.file_path}{row.line ? `:${row.line}` : ''}</span>}
            </div>
            <span style={{ color: row.status === 'new' ? 'var(--success)' : 'var(--text-muted)', fontWeight: 700 }}>{row.status}</span>
            <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', fontSize: '0.78rem', lineHeight: 1.45 }}>{row.raw_body}</pre>
            <div style={{ display: 'grid', gap: 3 }}>
              <span style={{ color: row.link_confidence === 'exact' ? 'var(--success)' : row.link_confidence === 'inferred' ? 'var(--accent)' : 'var(--text-muted)', fontWeight: 700 }}>{row.link_confidence}</span>
              {row.linked_agent_name && <span style={{ color: 'var(--text-faint)' }}>{row.linked_agent_name}</span>}
              {row.link_diagnostics && <span style={{ color: 'var(--text-faint)', overflowWrap: 'anywhere' }}>{row.link_diagnostics}</span>}
            </div>
            <span style={{ color: row.author_authorized ? 'var(--text)' : 'var(--text-danger)', overflowWrap: 'anywhere' }}>{row.author_login}</span>
          </article>
        ))}
        {!loading && rows.length === 0 && (
          <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>No matching feedback events.</div>
        )}
      </section>
    </main>
  )
}
