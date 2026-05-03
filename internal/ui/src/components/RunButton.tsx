'use client'

import { useEffect, useState } from 'react'

const ALL_REPOS = '__all__'

// RunButton fires POST /run for an agent against one or more bound repos.
// When `repos` has a single entry, it renders as just the button. With
// multiple, a small select appears so the operator picks which repo (or
// "All" to fan out one POST per repo). Used on both the Fleet view (one
// button per agent, repos = the agent's bindings) and the Repos view
// (one button per agent listed under each repo, repos = [repo.name]).
export default function RunButton({ agent, repos }: { agent: string; repos: string[] }) {
  // Dedupe (an agent may be bound to the same repo via multiple triggers).
  const uniqueRepos = Array.from(new Set(repos.filter(Boolean)))
  const [state, setState] = useState<'idle' | 'running' | 'done' | 'error'>('idle')
  const [statusMsg, setStatusMsg] = useState('')
  const [errorMsg, setErrorMsg] = useState('')
  const [target, setTarget] = useState(uniqueRepos[0] ?? '')

  // Keep target in sync if the bindings list changes (e.g. after a fleet reload).
  useEffect(() => {
    if (uniqueRepos.length === 0) {
      setTarget('')
      return
    }
    if (target === ALL_REPOS) return
    if (!uniqueRepos.includes(target)) setTarget(uniqueRepos[0])
  }, [uniqueRepos.join('|')]) // eslint-disable-line react-hooks/exhaustive-deps

  const run = async () => {
    if (!target) return
    const targets = target === ALL_REPOS ? uniqueRepos : [target]
    if (targets.length === 0) return
    setState('running')
    setErrorMsg('')
    setStatusMsg('')
    const results = await Promise.all(targets.map(async (repo) => {
      try {
        const res = await fetch('/run', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ agent, repo }),
        })
        if (res.status === 202) {
          const data = await res.json()
          return { repo, ok: true, eventId: data.event_id ?? '' }
        }
        const body = (await res.text()).trim()
        return { repo, ok: false, err: body || `HTTP ${res.status}` }
      } catch (e) {
        return { repo, ok: false, err: String(e) }
      }
    }))
    const ok = results.filter(r => r.ok)
    const failed = results.filter(r => !r.ok)
    if (failed.length === 0) {
      setStatusMsg(targets.length === 1 ? `Queued ${ok[0].eventId.slice(0, 8)}` : `Queued ${ok.length} runs`)
      setState('done')
      setTimeout(() => { setState('idle'); setStatusMsg('') }, 3000)
    } else {
      const first = failed[0]
      const tag = targets.length === 1 ? '' : ` (${ok.length}/${targets.length} ok)`
      setErrorMsg(`${first.repo}: ${first.err}${tag}`)
      setState('error')
      setTimeout(() => { setState('idle'); setErrorMsg('') }, 6000)
    }
  }

  if (uniqueRepos.length === 0) return null

  const label = state === 'running' ? 'Queuing...'
    : state === 'done' ? statusMsg
    : state === 'error' ? `Failed: ${errorMsg.slice(0, 60)}`
    : 'Run'
  const bg = state === 'done' ? 'var(--success-bg)' : state === 'error' ? 'var(--error-bg)' : 'var(--accent-bg)'
  const color = state === 'done' ? 'var(--success)' : state === 'error' ? 'var(--text-danger)' : 'var(--accent)'
  const border = state === 'done' ? 'var(--success-border)' : state === 'error' ? 'var(--border-danger)' : 'var(--btn-primary-border)'

  return (
    <div style={{ display: 'flex', gap: '4px', alignItems: 'center' }}>
      {uniqueRepos.length > 1 && (
        <select
          value={target}
          onChange={e => setTarget(e.target.value)}
          disabled={state === 'running'}
          style={{
            background: 'var(--bg-input)', color: 'var(--text)',
            border: '1px solid var(--border)', borderRadius: '6px',
            padding: '4px 6px', fontSize: '0.72rem',
            maxWidth: '180px', cursor: state === 'running' ? 'wait' : 'pointer',
          }}
          title="Pick the bound repo to fire on, or All to fan out to every binding."
        >
          {uniqueRepos.map(r => (
            <option key={r} value={r}>{r}</option>
          ))}
          <option value={ALL_REPOS}>All ({uniqueRepos.length})</option>
        </select>
      )}
      <button
        onClick={run}
        disabled={state === 'running' || !target}
        title={state === 'error' ? errorMsg : (target === ALL_REPOS ? `Fire on ${uniqueRepos.length} repos` : `Fire on ${target}`)}
        style={{
          background: bg, color, border: `1px solid ${border}`,
          padding: '4px 12px', borderRadius: '6px', cursor: state === 'running' ? 'wait' : 'pointer',
          fontSize: '0.75rem', fontWeight: 600,
          maxWidth: state === 'error' ? '320px' : undefined,
          overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap',
        }}
      >
        {label}
      </button>
    </div>
  )
}
