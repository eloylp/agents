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
  file_path?: string
  line?: number
  link_confidence: string
  link_diagnostics?: string
  linked_agent_name?: string
  status: string
  ingested_at: string
}

interface Recommendation {
  id: string
  feedback_event_id: number
  type: string
  status: string
  confidence: string
  risk: string
  finding: string
  normalized_lesson: string
  rationale: string
  attribution_confidence: string
  target_asset_type?: string
  target_base_version_id?: string
  proposed_patch?: string
  proposed_new_body?: string
  suggested_rollout_scope?: string
  updated_at: string
  feedback?: FeedbackEvent
}

type Tab = 'inbox' | 'recommendations' | 'history'

export default function ImprovementsPage() {
  const { workspace } = useSelectedWorkspace()
  const [feedback, setFeedback] = useState<FeedbackEvent[]>([])
  const [recommendations, setRecommendations] = useState<Recommendation[]>([])
  const [tab, setTab] = useState<Tab>('inbox')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)

  const load = () => {
    setLoading(true)
    const suffix = status ? `?status=${encodeURIComponent(status)}` : ''
    Promise.all([
      fetch(withWorkspace(`/improvements/feedback${suffix}`, workspace), { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
      fetch(withWorkspace(`/improvements/recommendations${suffix}`, workspace), { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
    ])
      .then(([feedbackRows, recommendationRows]) => {
        setFeedback(feedbackRows ?? [])
        setRecommendations(recommendationRows ?? [])
      })
      .catch(() => {
        setFeedback([])
        setRecommendations([])
      })
      .finally(() => setLoading(false))
  }

  useEffect(() => { load() }, [workspace, status])

  const counts = useMemo(() => recommendations.reduce<Record<string, number>>((acc, row) => {
    acc[row.status] = (acc[row.status] ?? 0) + 1
    return acc
  }, {}), [recommendations])

  const updateStatus = async (id: string, next: string) => {
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(id)}/status`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status: next }),
    })
    if (res.ok) load()
  }

  const analyze = async (id: number) => {
    const res = await fetch(`/improvements/feedback/${id}/analyze`, { method: 'POST' })
    if (res.ok) {
      setTab('recommendations')
      load()
    }
  }

  const shownRecommendations = tab === 'inbox'
    ? recommendations.filter(row => row.status === 'recommended' || row.status === 'needs_user_input')
    : recommendations

  return (
    <main style={{ display: 'grid', gap: '1rem' }}>
      <section style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <div>
          <h1 style={{ fontSize: '1.45rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }}>Improvements</h1>
          <div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
            {loading ? 'Loading' : `${shownRecommendations.length} recommendations · ${feedback.length} feedback events`}
            {Object.keys(counts).length > 0 ? ` · ${Object.entries(counts).map(([k, v]) => `${k}: ${v}`).join(' · ')}` : ''}
          </div>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <WorkspaceSelect />
          <select value={status} onChange={e => setStatus(e.target.value)} style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '7px 9px' }}>
            <option value="">All statuses</option>
            <option value="recommended">Recommended</option>
            <option value="needs_user_input">Needs input</option>
            <option value="accepted">Accepted</option>
            <option value="rejected">Rejected</option>
            <option value="deferred">Deferred</option>
            <option value="duplicate">Duplicate</option>
          </select>
        </div>
      </section>

      <nav style={{ display: 'flex', gap: 8 }}>
        {(['inbox', 'recommendations', 'history'] as Tab[]).map(next => (
          <button key={next} onClick={() => setTab(next)} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: tab === next ? 'var(--bg-active)' : 'var(--bg-card)', color: 'var(--text)', borderRadius: 6, textTransform: 'capitalize' }}>{next}</button>
        ))}
      </nav>

      {tab !== 'history' && (
        <section style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
          <div style={{ display: 'grid', gridTemplateColumns: '130px 92px 1fr 150px 190px', gap: '0.75rem', padding: '0.65rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', color: 'var(--text-muted)', fontSize: '0.72rem', fontWeight: 700, textTransform: 'uppercase' }}>
            <span>Updated</span>
            <span>Status</span>
            <span>Recommendation</span>
            <span>Target</span>
            <span>Actions</span>
          </div>
          {shownRecommendations.map(row => (
            <article key={row.id} style={{ display: 'grid', gridTemplateColumns: '130px 92px 1fr 150px 190px', gap: '0.75rem', padding: '0.75rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', fontSize: '0.8rem', alignItems: 'start' }}>
              <time style={{ color: 'var(--text-faint)' }}>{new Date(row.updated_at).toLocaleString()}</time>
              <span style={{ color: row.status === 'recommended' ? 'var(--success)' : 'var(--text-muted)', fontWeight: 700 }}>{row.status}</span>
              <div style={{ display: 'grid', gap: 6 }}>
                <strong style={{ color: 'var(--text-heading)' }}>{row.finding}</strong>
                <span style={{ color: 'var(--text)' }}>{row.rationale}</span>
                {row.feedback?.source_url && <a href={row.feedback.source_url} target="_blank" rel="noreferrer">{row.feedback.repo_owner}/{row.feedback.repo_name} feedback #{row.feedback_event_id}</a>}
                {(row.proposed_patch || row.proposed_new_body) && <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', fontSize: '0.76rem' }}>{row.proposed_patch || row.proposed_new_body}</pre>}
              </div>
              <div style={{ display: 'grid', gap: 3, color: 'var(--text-muted)' }}>
                <span>{row.type}</span>
                <span>{row.target_asset_type || 'design review'}</span>
                <span>{row.attribution_confidence}</span>
              </div>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {['accepted', 'rejected', 'deferred', 'duplicate'].map(next => (
                  <button key={next} onClick={() => updateStatus(row.id, next)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>{next}</button>
                ))}
              </div>
            </article>
          ))}
          {!loading && shownRecommendations.length === 0 && (
            <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>No matching recommendations.</div>
          )}
        </section>
      )}

      {tab === 'history' && (
        <section style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
          {feedback.map(row => (
            <article key={row.id} style={{ display: 'grid', gridTemplateColumns: '135px 170px 92px 1fr 96px', gap: '0.75rem', padding: '0.75rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', fontSize: '0.8rem', alignItems: 'start' }}>
              <time style={{ color: 'var(--text-faint)' }}>{new Date(row.ingested_at).toLocaleString()}</time>
              <div style={{ display: 'grid', gap: 3 }}>
                <a href={row.source_url} target="_blank" rel="noreferrer">{row.repo_owner}/{row.repo_name}</a>
                <span style={{ color: 'var(--text-faint)' }}>{row.source_type}{row.pr_number ? ` · PR #${row.pr_number}` : row.issue_number ? ` · issue #${row.issue_number}` : ''}</span>
              </div>
              <span style={{ color: row.status === 'new' ? 'var(--success)' : 'var(--text-muted)', fontWeight: 700 }}>{row.status}</span>
              <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', fontSize: '0.78rem', lineHeight: 1.45 }}>{row.raw_body}</pre>
              {row.status === 'new' && <button onClick={() => analyze(row.id)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Analyze</button>}
            </article>
          ))}
          {!loading && feedback.length === 0 && (
            <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>No matching feedback events.</div>
          )}
        </section>
      )}
    </main>
  )
}
