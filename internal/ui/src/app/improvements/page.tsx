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
  linked_prompt_version_id?: string
  linked_skill_version_ids?: string[]
  linked_guardrail_version_ids?: string[]
  status: string
  ingested_at: string
}

interface Clarification {
  recommendation_id: string
  author: string
  body: string
  created_at: string
  updated_at: string
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
  clarification?: Clarification
}

interface CatalogVersion {
  id: string
  asset_id?: string
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

interface ImprovementProposal {
  recommendation_id: string
  target_asset_type: string
  target_asset_id: string
  base_version_id?: string
  base_version?: CatalogVersion
  version: CatalogVersion
}

type Tab = 'inbox' | 'recommendations' | 'history'

const nonConvertibleTypes = ['needs_more_context', 'no_action', 'split_agent', 'change_dispatch_wiring']

function versionBody(type: string, version?: CatalogVersion) {
  if (!version) return ''
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
  for (; i < oldLines.length; i += 1) rows.push({ kind: 'del', text: `-${oldLines[i]}` })
  for (; j < newLines.length; j += 1) rows.push({ kind: 'add', text: `+${newLines[j]}` })
  return rows
}

function proposalReadiness(row: Recommendation) {
  if (row.status !== 'accepted') return 'Accept before creating a proposal.'
  if (nonConvertibleTypes.includes(row.type)) return 'Design recommendation only in v1.'
  if (!['prompt', 'skill', 'guardrail'].includes(row.target_asset_type || '')) return 'No proposal-convertible catalog target.'
  if (!row.target_base_version_id) return 'Missing target base version.'
  if (!row.proposed_new_body) return 'Missing proposed catalog body.'
  return ''
}

export default function ImprovementsPage() {
  const { workspace } = useSelectedWorkspace()
  const [feedback, setFeedback] = useState<FeedbackEvent[]>([])
  const [recommendations, setRecommendations] = useState<Recommendation[]>([])
  const [proposals, setProposals] = useState<Record<string, ImprovementProposal[]>>({})
  const [tab, setTab] = useState<Tab>('inbox')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)
  const [clarifying, setClarifying] = useState<Recommendation | null>(null)
  const [clarificationBody, setClarificationBody] = useState('')
  const [clarificationSaving, setClarificationSaving] = useState(false)

  const load = () => {
    setLoading(true)
    const suffix = status ? `?status=${encodeURIComponent(status)}` : ''
    Promise.all([
      fetch(withWorkspace(`/improvements/feedback${suffix}`, workspace), { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
      fetch(withWorkspace(`/improvements/recommendations${suffix}`, workspace), { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
    ])
      .then(([feedbackRows, recommendationRows]) => {
        setFeedback(feedbackRows ?? [])
        const recs = recommendationRows ?? []
        setRecommendations(recs)
        return Promise.all(recs.map((row: Recommendation) =>
          fetch(`/improvements/recommendations/${encodeURIComponent(row.id)}/proposal`, { cache: 'no-store' })
            .then(r => r.ok ? r.json() : [])
            .then(rows => [row.id, rows ?? []] as const)
        ))
      })
      .then(rows => {
        if (!rows) return
        setProposals(Object.fromEntries(rows))
      })
      .catch(() => {
        setFeedback([])
        setRecommendations([])
        setProposals({})
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

  const openClarification = (row: Recommendation) => {
    setClarifying(row)
    setClarificationBody(row.clarification?.body ?? '')
  }

  const submitClarification = async () => {
    if (!clarifying || clarificationSaving) return
    setClarificationSaving(true)
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(clarifying.id)}/clarification`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body: clarificationBody }),
    })
    setClarificationSaving(false)
    if (res.ok) {
      setClarifying(null)
      setClarificationBody('')
      load()
    }
  }

  const createProposal = async (id: string) => {
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(id)}/proposal`, { method: 'POST' })
    if (res.ok) load()
  }

  const canCreateProposal = (row: Recommendation) =>
    row.status === 'accepted' &&
    ['prompt', 'skill', 'guardrail'].includes(row.target_asset_type || '') &&
    Boolean(row.target_base_version_id && row.proposed_new_body) &&
    !nonConvertibleTypes.includes(row.type)

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
          {shownRecommendations.map(row => {
            const proposal = proposals[row.id]?.[0]
            const readiness = proposalReadiness(row)
            const diff = proposal ? diffLines(versionBody(proposal.target_asset_type, proposal.base_version), versionBody(proposal.target_asset_type, proposal.version)) : []
            return (
            <article key={row.id} style={{ display: 'grid', gridTemplateColumns: '130px 92px 1fr 150px 190px', gap: '0.75rem', padding: '0.75rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', fontSize: '0.8rem', alignItems: 'start' }}>
              <time style={{ color: 'var(--text-faint)' }}>{new Date(row.updated_at).toLocaleString()}</time>
              <span style={{ color: row.status === 'recommended' ? 'var(--success)' : 'var(--text-muted)', fontWeight: 700 }}>{row.status}</span>
              <div style={{ display: 'grid', gap: 6 }}>
                <strong style={{ color: 'var(--text-heading)' }}>{row.finding}</strong>
                <span style={{ color: 'var(--text)' }}>{row.rationale}</span>
                {row.feedback?.source_url && <a href={row.feedback.source_url} target="_blank" rel="noreferrer">{row.feedback.repo_owner}/{row.feedback.repo_name} feedback #{row.feedback_event_id}</a>}
                {(row.proposed_patch || row.proposed_new_body) && <pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', fontSize: '0.76rem' }}>{row.proposed_patch || row.proposed_new_body}</pre>}
                {proposal && (
                  <div style={{ display: 'grid', gap: 8, borderTop: '1px solid var(--border-subtle)', paddingTop: 8 }}>
                    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(150px, 1fr))', gap: 6, color: 'var(--text-muted)' }}>
                      <span>Recommendation {proposal.recommendation_id}</span>
                      <span>Target {proposal.target_asset_type}/{proposal.target_asset_id}</span>
                      <span>Base v{proposal.base_version?.version ?? proposal.base_version_id}</span>
                      <span>Proposal v{proposal.version.version}</span>
                      <span>State {proposal.version.state}</span>
                      <span>Source {proposal.version.source_type || 'unknown'} {proposal.version.source_ref || ''}</span>
                    </div>
                    <pre aria-label={`Proposal diff for ${row.id}`} style={{ margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxHeight: 360, overflow: 'auto', background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: 8, fontSize: '0.76rem', lineHeight: 1.45 }}>
                      {diff.map((line, i) => (
                        <span key={`${i}-${line.kind}`} style={{ display: 'block', color: line.kind === 'add' ? 'var(--success)' : line.kind === 'del' ? 'var(--text-danger)' : 'var(--text-muted)' }}>{line.text || ' '}</span>
                      ))}
                    </pre>
                    <div style={{ color: 'var(--text-muted)' }}>
                      {proposal.version.author && <span>Author {proposal.version.author}. </span>}
                      {proposal.version.changelog && <span>{proposal.version.changelog}</span>}
                    </div>
                  </div>
                )}
              </div>
              <div style={{ display: 'grid', gap: 3, color: 'var(--text-muted)' }}>
                <span>{row.type}</span>
                <span>{row.target_asset_type || 'design review'}</span>
                {proposal && <span>proposal v{proposal.version.version}</span>}
                {!proposal && row.status === 'accepted' && readiness && <span>{readiness}</span>}
                <span>{row.attribution_confidence}</span>
              </div>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {row.status === 'needs_user_input' && (
                  <button onClick={() => openClarification(row)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>clarify</button>
                )}
                {['accepted', 'rejected', 'deferred', 'duplicate'].map(next => (
                  <button key={next} onClick={() => updateStatus(row.id, next)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>{next}</button>
                ))}
                {canCreateProposal(row) && !proposals[row.id]?.length && (
                  <button onClick={() => createProposal(row.id)} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Create Proposal</button>
                )}
              </div>
            </article>
          )})}
          {!loading && shownRecommendations.length === 0 && (
            <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>No matching recommendations.</div>
          )}
        </section>
      )}

      {clarifying && (
        <div role="dialog" aria-modal="true" style={{ position: 'fixed', inset: 0, background: 'rgba(0,0,0,0.42)', display: 'grid', placeItems: 'center', zIndex: 50, padding: '1rem' }}>
          <section style={{ width: 'min(920px, 100%)', maxHeight: 'min(760px, 92vh)', overflow: 'auto', background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 8, boxShadow: '0 18px 48px rgba(0,0,0,0.35)' }}>
            <header style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'start', padding: '1rem', borderBottom: '1px solid var(--border-subtle)' }}>
              <div>
                <h2 style={{ color: 'var(--text-heading)', fontSize: '1rem', marginBottom: 4 }}>Clarify recommendation</h2>
                <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>{clarifying.id} · {clarifying.status}</div>
              </div>
              <button onClick={() => setClarifying(null)} style={{ border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6, padding: '6px 9px' }}>Close</button>
            </header>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 320px), 1fr))', gap: '1rem', padding: '1rem' }}>
              <div style={{ display: 'grid', gap: '0.85rem' }}>
                <section style={{ display: 'grid', gap: 6 }}>
                  <h3 style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>Recommendation</h3>
                  <strong style={{ color: 'var(--text-heading)' }}>{clarifying.finding}</strong>
                  <p style={{ color: 'var(--text)', margin: 0, fontSize: '0.84rem', lineHeight: 1.5 }}>{clarifying.rationale}</p>
                </section>
                <section style={{ display: 'grid', gap: 6 }}>
                  <h3 style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>Original feedback</h3>
                  <pre style={{ margin: 0, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', color: 'var(--text)', background: 'var(--bg-input)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: '0.7rem', fontSize: '0.8rem', lineHeight: 1.45 }}>{clarifying.feedback?.raw_body ?? 'No feedback body available.'}</pre>
                  {clarifying.feedback?.source_url && <a href={clarifying.feedback.source_url} target="_blank" rel="noreferrer">{clarifying.feedback.source_url}</a>}
                </section>
                <section style={{ display: 'grid', gap: 6 }}>
                  <h3 style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>Your clarification</h3>
                  <textarea
                    value={clarificationBody}
                    onChange={e => setClarificationBody(e.target.value)}
                    rows={9}
                    style={{ resize: 'vertical', minHeight: 150, background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 6, padding: '0.7rem', font: 'inherit', fontSize: '0.85rem', lineHeight: 1.45 }}
                    autoFocus
                  />
                  <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end' }}>
                    <button onClick={() => setClarifying(null)} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Cancel</button>
                    <button disabled={clarificationSaving || clarificationBody.trim() === ''} onClick={submitClarification} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6, opacity: clarificationSaving || clarificationBody.trim() === '' ? 0.6 : 1 }}>{clarificationSaving ? 'Queueing...' : 'Send and re-analyze'}</button>
                  </div>
                </section>
              </div>
              <aside style={{ display: 'grid', gap: '0.75rem', alignContent: 'start', color: 'var(--text-muted)', fontSize: '0.8rem' }}>
                <InfoRow label="Type" value={clarifying.type} />
                <InfoRow label="Confidence" value={clarifying.confidence} />
                <InfoRow label="Risk" value={clarifying.risk} />
                <InfoRow label="Attribution" value={clarifying.attribution_confidence} />
                <InfoRow label="Agent" value={clarifying.feedback?.linked_agent_name || 'unresolved'} />
                <InfoRow label="Prompt version" value={clarifying.feedback?.linked_prompt_version_id || 'unresolved'} />
                <InfoRow label="Skill versions" value={(clarifying.feedback?.linked_skill_version_ids ?? []).join(', ') || 'none'} />
                <InfoRow label="Guardrail versions" value={(clarifying.feedback?.linked_guardrail_version_ids ?? []).join(', ') || 'none'} />
                <InfoRow label="Target" value={clarifying.target_asset_type || 'design review'} />
                <InfoRow label="Base version" value={clarifying.target_base_version_id || 'unresolved'} />
                {clarifying.clarification && <InfoRow label="Last clarified" value={new Date(clarifying.clarification.updated_at).toLocaleString()} />}
              </aside>
            </div>
          </section>
        </div>
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

function InfoRow({ label, value }: { label: string; value: string }) {
  return (
    <div style={{ display: 'grid', gap: 2 }}>
      <span style={{ color: 'var(--text-faint)', fontSize: '0.7rem', textTransform: 'uppercase', fontWeight: 700 }}>{label}</span>
      <span style={{ color: 'var(--text)', overflowWrap: 'anywhere' }}>{value}</span>
    </div>
  )
}
