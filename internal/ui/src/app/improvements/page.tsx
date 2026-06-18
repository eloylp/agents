'use client'
import { type CSSProperties, type ReactNode, useCallback, useEffect, useMemo, useState } from 'react'
import MarkdownEditor from '@/components/MarkdownEditor'
import Modal from '@/components/Modal'
import { formatDateTime } from '@/lib/datetime'
import { withWorkspace } from '@/lib/workspace'

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
  workspace?: string
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
  target_asset_id?: string
  target_base_version_id?: string
  proposed_patch?: string
  proposed_new_body?: string
  error?: string
  structured_output?: {
    changes?: RecommendationChange[]
  }
  decision_reason?: string
  updated_at: string
  feedback?: FeedbackEvent
  clarification?: Clarification
  proposal_bundle?: ProposalBundle
}

interface RecommendationChange {
  operation?: string
  asset_type?: string
  asset_id?: string
  base_version_id?: string
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

interface ProposalBundleItem {
  id: string
  operation: string
  asset_type: string
  asset_id?: string
  base_version_id?: string
  proposed_ref?: string
  proposed_name?: string
  proposed_scope?: string
  proposed_body: string
  proposed_description?: string
  proposed_enabled?: boolean
  proposed_position?: number
  analyst_proposed_body: string
  duplicate_risk?: string
  rationale?: string
  decision: string
  decision_reason?: string
  published_version_id?: string
  base_version?: CatalogVersion
  current_version_id?: string
  stale?: boolean
}

interface ProposalBundle {
  id?: string
  recommendation_id?: string
  recommendation_changed?: boolean
  status?: string
  updated_at?: string
  items?: ProposalBundleItem[]
}

interface CatalogLinkAsset {
  id: string
  name: string
  scope: string
  workspace_id?: string
  repo?: string
}

type Tab = 'proposals' | 'history'

interface VersionCheckTarget {
  assetType: string
  assetID: string
  baseVersionID: string
}

interface CatalogVersionCheck {
  current_version_id?: string
  stale: boolean
  loading?: boolean
  error?: string
}

interface BundleItemDraft {
  proposed_ref: string
  proposed_name: string
  proposed_scope: string
  proposed_body: string
  proposed_description: string
  proposed_enabled: boolean
  proposed_position: number
}

type BundleDecisionModal =
  | {
      kind: 'reject'
      bundleID: string
      itemID: string
      assetLabel: string
      reason: string
      closesProposal?: boolean
    }
  | {
      kind: 'link'
      bundleID: string
      itemID: string
      assetLabel: string
      assetType: string
      proposedAsset: string
      assetID: string
      assets: CatalogLinkAsset[]
      reason: string
    }

type ConfirmationModal = {
  title: string
  body: string
  confirmLabel: string
  onConfirm: () => Promise<void>
}

type RecommendationRejectModal = {
  row: Recommendation
  reason: string
}

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

function catalogAssetEndpoint(type: string, id: string) {
  const encoded = encodeURIComponent(id)
  if (type === 'prompt') return `/prompts/${encoded}`
  if (type === 'skill') return `/skills/${encoded}`
  if (type === 'guardrail') return `/guardrails/${encoded}`
  return ''
}

function versionCheckTargets(row: Recommendation): VersionCheckTarget[] {
  const targets: VersionCheckTarget[] = []
  if (row.target_asset_type && row.target_asset_id && row.target_base_version_id) {
    targets.push({ assetType: row.target_asset_type, assetID: row.target_asset_id, baseVersionID: row.target_base_version_id })
  }
  for (const change of row.structured_output?.changes ?? []) {
    if (change.operation !== 'update_existing') continue
    if (!change.asset_type || !change.asset_id || !change.base_version_id) continue
    targets.push({ assetType: change.asset_type, assetID: change.asset_id, baseVersionID: change.base_version_id })
  }
  const seen = new Set<string>()
  return targets.filter(target => {
    const key = versionCheckKey(target)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function versionCheckKey(target: VersionCheckTarget) {
  return `${target.assetType}:${target.assetID}:${target.baseVersionID}`
}

function staleVersionCheck(row: Recommendation, checks: Record<string, CatalogVersionCheck>) {
  for (const target of versionCheckTargets(row)) {
    const check = checks[versionCheckKey(target)]
    if (check?.stale) return { target, check }
  }
  return null
}

function isTerminalRecommendationPayload(row: Recommendation) {
  const bundleStatus = row.proposal_bundle?.status
  return row.status === 'rejected' || bundleStatus === 'published' || bundleStatus === 'resolved' || bundleStatus === 'discarded'
}

function hasPendingVersionCheck(row: Recommendation, checks: Record<string, CatalogVersionCheck>) {
  return versionCheckTargets(row).some(target => checks[versionCheckKey(target)]?.loading)
}

function displayProposalStatus(status: string) {
  if (status === 'recommended') return 'ready'
  if (status === 'needs_user_input') return 'needs input'
  if (status === 'clarifying') return 'clarifying'
  if (status === 'analyzing') return 'analyzing'
  if (status === 'failed') return 'failed'
  return status
}

function displayRowStatus(row: Recommendation, bundle?: ProposalBundle) {
  if (bundle?.status === 'published') return 'published'
  if (bundle?.status === 'resolved') return 'resolved'
  if (bundle?.status === 'discarded') return 'discarded'
  return displayProposalStatus(row.status)
}

function rowStatusColor(row: Recommendation, bundle?: ProposalBundle) {
  if (bundle?.status === 'published') return 'var(--success)'
  if (bundle?.status === 'resolved') return 'var(--accent)'
  if (row.status === 'recommended') return 'var(--success)'
  if (row.status === 'clarifying' || row.status === 'analyzing') return 'var(--accent)'
  if (row.status === 'failed') return 'var(--text-danger)'
  return 'var(--text-muted)'
}

function bundleItemDraft(item: ProposalBundleItem, drafts: Record<string, BundleItemDraft>): BundleItemDraft {
  return drafts[item.id] ?? {
    proposed_ref: item.proposed_ref ?? '',
    proposed_name: item.proposed_name ?? '',
    proposed_scope: item.operation === 'create_new' ? (item.proposed_scope || 'global') : item.proposed_scope ?? '',
    proposed_body: item.proposed_body,
    proposed_description: item.proposed_description ?? '',
    proposed_enabled: item.proposed_enabled ?? true,
    proposed_position: item.proposed_position ?? 100,
  }
}

function normalizedDraftText(value?: string) {
  return (value ?? '').trim()
}

function bundleItemDraftChanged(item: ProposalBundleItem, draft: BundleItemDraft) {
  return normalizedDraftText(draft.proposed_ref) !== normalizedDraftText(item.proposed_ref) ||
    normalizedDraftText(draft.proposed_name) !== normalizedDraftText(item.proposed_name) ||
    normalizedDraftText(draft.proposed_scope) !== normalizedDraftText(item.proposed_scope) ||
    normalizedDraftText(draft.proposed_body) !== normalizedDraftText(item.proposed_body) ||
    normalizedDraftText(draft.proposed_description) !== normalizedDraftText(item.proposed_description) ||
    draft.proposed_enabled !== (item.proposed_enabled ?? true) ||
    draft.proposed_position !== (item.proposed_position ?? 100)
}

function bundleItemDraftBody(item: ProposalBundleItem, draft: BundleItemDraft) {
  if (item.asset_type !== 'guardrail') return draft.proposed_body
  return [
    draft.proposed_description ? `description: ${draft.proposed_description}` : '',
    draft.proposed_body,
    `enabled: ${draft.proposed_enabled ? 'true' : 'false'}`,
    `position: ${draft.proposed_position}`,
  ].filter(Boolean).join('\n')
}

function capitalizeLabel(value: string) {
  if (!value) return value
  return value.slice(0, 1).toUpperCase() + value.slice(1).replaceAll('_', ' ')
}

function bundleItemStateLabel(decision: string) {
  switch (decision) {
    case 'accepted':
      return 'Included'
    case 'pending':
      return 'Pending review'
    case 'rejected':
      return 'Rejected'
    case 'linked_existing':
      return 'Uses existing asset'
    case 'published':
      return 'Published'
    case 'discarded':
      return 'Discarded'
    default:
      return capitalizeLabel(decision)
  }
}

function bundleItemStateHighlight(decision: string) {
  return decision === 'linked_existing' || decision === 'published' || decision === 'resolved'
}

function bundleItemBaseLabel(item: ProposalBundleItem) {
  if (item.published_version_id) return `Published ${item.published_version_id}`
  if (item.operation === 'create_new') return 'New asset'
  if (item.stale) return 'Stale base'
  return item.base_version_id || 'Unresolved'
}

function bundleItemPendingConflictKey(item: ProposalBundleItem, draft?: BundleItemDraft) {
  if (item.operation === 'update_existing') {
    const assetID = normalizedDraftText(item.asset_id)
    if (!item.asset_type || !assetID) return ''
    return `${item.asset_type}\u0000${assetID}`
  }
  if (item.operation === 'create_new') {
    const scope = normalizedDraftText(draft?.proposed_scope ?? item.proposed_scope).toLowerCase()
    const ref = normalizedDraftText(draft?.proposed_ref ?? item.proposed_ref).toLowerCase()
    if (!item.asset_type || !scope || !ref) return ''
    return `${item.asset_type}\u0000${scope}\u0000${ref}`
  }
  return ''
}

function isPendingBundleEditItem(item: ProposalBundleItem) {
	return item.decision === 'accepted'
}

function pendingBundleEditConflict(item: ProposalBundleItem, bundleID: string, bundles: Record<string, ProposalBundle>, draft?: BundleItemDraft) {
  const key = bundleItemPendingConflictKey(item, draft)
  if (!key) return null
  for (const candidate of Object.values(bundles)) {
    if (!candidate.id || candidate.id === bundleID || candidate.status !== 'pending') continue
    for (const candidateItem of candidate.items ?? []) {
      if (!isPendingBundleEditItem(candidateItem)) continue
      if (bundleItemPendingConflictKey(candidateItem) === key) {
        return { bundleID: candidate.id, item: candidateItem }
      }
    }
  }
  return null
}

function recommendationActivityAt(row: Recommendation) {
  return row.proposal_bundle?.updated_at || row.updated_at
}

function sortRecommendationsByActivity(rows: Recommendation[]) {
  return [...rows].sort((a, b) => {
    const aTime = new Date(recommendationActivityAt(a)).getTime()
    const bTime = new Date(recommendationActivityAt(b)).getTime()
    if (Number.isFinite(aTime) && Number.isFinite(bTime) && aTime !== bTime) return bTime - aTime
    if (Number.isFinite(aTime) !== Number.isFinite(bTime)) return Number.isFinite(aTime) ? -1 : 1
    return b.id.localeCompare(a.id)
  })
}

function recommendationWorkspace(row: Recommendation) {
  return row.feedback?.workspace || row.workspace || 'default'
}

function recommendationRepoScope(row: Recommendation) {
  const owner = row.feedback?.repo_owner?.trim()
  const name = row.feedback?.repo_name?.trim()
  if (!owner || !name) return ''
  return `${recommendationWorkspace(row)}/${owner}/${name}`
}

function bundleScopeOptions(row: Recommendation) {
  const workspace = recommendationWorkspace(row)
  const repoScope = recommendationRepoScope(row)
  return [
    { value: 'global', label: 'Global' },
    { value: workspace, label: `Workspace: ${workspace}` },
    ...(repoScope ? [{ value: repoScope, label: `Repo: ${repoScope}` }] : []),
  ]
}

function resolvedBundleScopeValue(raw: string | undefined, row: Recommendation) {
  const value = (raw || 'global').trim()
  const lowered = value.toLowerCase()
  if (lowered === 'workspace') return recommendationWorkspace(row)
  if (lowered === 'repo') return recommendationRepoScope(row) || recommendationWorkspace(row)
  return value || 'global'
}

function recommendationRepoName(row: Recommendation) {
  const owner = row.feedback?.repo_owner?.trim()
  const name = row.feedback?.repo_name?.trim()
  if (!owner || !name) return ''
  return `${owner}/${name}`
}

function visibleCatalogLinkAssets(assets: CatalogLinkAsset[], row: Recommendation | null) {
  if (!row) return assets.filter(asset => !asset.workspace_id && !asset.repo)
  const workspace = recommendationWorkspace(row)
  const repo = recommendationRepoName(row)
  return assets.filter(asset => {
    const assetWorkspace = (asset.workspace_id ?? '').trim()
    const assetRepo = (asset.repo ?? '').trim()
    if (!assetWorkspace && !assetRepo) return true
    if (assetWorkspace !== workspace) return false
    return !assetRepo || (repo !== '' && assetRepo === repo)
  })
}

function catalogLinkAssets(raw: unknown): CatalogLinkAsset[] {
  const rows = Array.isArray(raw)
    ? raw.map(row => ({ row, fallbackID: '' }))
    : raw && typeof raw === 'object'
      ? Object.entries(raw as Record<string, unknown>).map(([fallbackID, row]) => ({ row, fallbackID }))
      : []
  return rows.flatMap(({ row, fallbackID }) => {
    const item = row as { id?: string; ref?: string; name?: string; workspace_id?: string; repo?: string }
    const id = String(item.ref || item.id || fallbackID || item.name || '').trim()
    if (!id) return []
    const scope = item.repo ? `${item.workspace_id || 'default'} / ${item.repo}` : item.workspace_id ? `${item.workspace_id} workspace` : 'Global'
    return [{ id, name: String(item.name || id), scope, workspace_id: item.workspace_id, repo: item.repo }]
  })
}

function catalogAssetLabel(assetType: string | undefined, assetID: string | undefined, catalogAssets: Record<string, CatalogLinkAsset[]>) {
  const id = (assetID ?? '').trim()
  if (!id) return 'unresolved'
  const asset = catalogAssets[assetType ?? '']?.find(candidate => candidate.id === id)
  const name = (asset?.name ?? '').trim()
  if (!name || name === id) return id
  return `${id} · ${name}`
}

function catalogTargetLabel(assetType: string | undefined, assetID: string | undefined, catalogAssets: Record<string, CatalogLinkAsset[]>) {
  if (!assetType) return 'design review'
  return `${assetType}/${catalogAssetLabel(assetType, assetID, catalogAssets)}`
}

function catalogEndpointForType(assetType: string) {
  if (assetType === 'prompt') return '/prompts'
  if (assetType === 'skill') return '/skills'
  if (assetType === 'guardrail') return '/guardrails'
  return ''
}

async function fetchCatalogAssetsForType(assetType: string): Promise<CatalogLinkAsset[]> {
  const endpoint = catalogEndpointForType(assetType)
  if (!endpoint) return []
  const res = await fetch(endpoint, { cache: 'no-store' })
  if (!res.ok) return []
  return catalogLinkAssets(await res.json())
}

export default function ImprovementsPage() {
  const [feedback, setFeedback] = useState<FeedbackEvent[]>([])
  const [recommendations, setRecommendations] = useState<Recommendation[]>([])
  const [bundles, setBundles] = useState<Record<string, ProposalBundle>>({})
  const [catalogAssets, setCatalogAssets] = useState<Record<string, CatalogLinkAsset[]>>({ prompt: [], skill: [], guardrail: [] })
  const [catalogVersionChecks, setCatalogVersionChecks] = useState<Record<string, CatalogVersionCheck>>({})
  const [itemDrafts, setItemDrafts] = useState<Record<string, BundleItemDraft>>({})
  const [tab, setTab] = useState<Tab>('proposals')
  const [status, setStatus] = useState('')
  const [loading, setLoading] = useState(true)
  const [actionMessage, setActionMessage] = useState<string | null>(null)
  const [linkingSkillItem, setLinkingSkillItem] = useState<string | null>(null)
  const [clarifying, setClarifying] = useState<Recommendation | null>(null)
  const [clarificationBody, setClarificationBody] = useState('')
  const [clarificationSaving, setClarificationSaving] = useState(false)
  const [bundleDecision, setBundleDecision] = useState<BundleDecisionModal | null>(null)
  const [bundleDecisionSaving, setBundleDecisionSaving] = useState(false)
  const [rejectingRecommendation, setRejectingRecommendation] = useState<RecommendationRejectModal | null>(null)
  const [recommendationRejectSaving, setRecommendationRejectSaving] = useState(false)
  const [confirming, setConfirming] = useState<ConfirmationModal | null>(null)
  const [confirmSaving, setConfirmSaving] = useState(false)
  const [reviewingProposal, setReviewingProposal] = useState<Recommendation | null>(null)

  const load = useCallback((showLoading = true) => {
    if (showLoading) setLoading(true)
    const suffix = status ? `?status=${encodeURIComponent(status)}` : ''
    Promise.all([
      fetch(`/improvements/feedback${suffix}`, { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
      fetch(`/improvements/recommendations${suffix}`, { cache: 'no-store' }).then(r => r.ok ? r.json() : []),
    ])
      .then(([feedbackRows, recommendationRows]) => {
        setFeedback(feedbackRows ?? [])
        const recs = sortRecommendationsByActivity(recommendationRows ?? [])
        setRecommendations(recs)
        setBundles(Object.fromEntries(recs.map((row: Recommendation) => [row.id, row.proposal_bundle ?? {}])))
      })
      .catch(() => {
        if (!showLoading) return
        setFeedback([])
        setRecommendations([])
        setBundles({})
      })
      .finally(() => {
        if (showLoading) setLoading(false)
      })
  }, [status])

  useEffect(() => { load() }, [load])

  useEffect(() => {
    let cancelled = false
    Promise.all(['prompt', 'skill', 'guardrail'].map(async assetType => [assetType, await fetchCatalogAssetsForType(assetType)] as const))
      .then(entries => {
        if (cancelled) return
        setCatalogAssets(current => ({ ...current, ...Object.fromEntries(entries) }))
      })
      .catch(() => {})
    return () => { cancelled = true }
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => load(false), 5000)
    return () => window.clearInterval(timer)
  }, [load])

  useEffect(() => {
    const targets = recommendations.filter(row => !isTerminalRecommendationPayload(row)).flatMap(versionCheckTargets)
      .filter(target => ['prompt', 'skill', 'guardrail'].includes(target.assetType))
    const missing = targets.filter(target => !catalogVersionChecks[versionCheckKey(target)])
    if (missing.length === 0) return
    const unique = Array.from(new Map(missing.map(target => [versionCheckKey(target), target])).values())
    setCatalogVersionChecks(current => {
      const next = { ...current }
      for (const target of unique) {
        const key = versionCheckKey(target)
        if (!next[key]) next[key] = { stale: false, loading: true }
      }
      return next
    })
    let cancelled = false
    Promise.all(unique.map(async target => {
      const endpoint = catalogAssetEndpoint(target.assetType, target.assetID)
      if (!endpoint) return [versionCheckKey(target), { stale: false, error: 'Unsupported catalog target' }] as const
      try {
        const res = await fetch(endpoint, { cache: 'no-store' })
        if (!res.ok) return [versionCheckKey(target), { stale: false, error: `HTTP ${res.status}` }] as const
        const asset = await res.json()
        const currentVersionID = String(asset.version_id || asset.current_version_id || '')
        return [versionCheckKey(target), {
          current_version_id: currentVersionID,
          stale: currentVersionID !== '' && currentVersionID !== target.baseVersionID,
        }] as const
      } catch (err) {
        return [versionCheckKey(target), { stale: false, error: (err as Error).message }] as const
      }
    })).then(entries => {
      if (cancelled) return
      setCatalogVersionChecks(current => ({ ...current, ...Object.fromEntries(entries) }))
    })
    return () => { cancelled = true }
  }, [recommendations])

  const counts = useMemo(() => recommendations.reduce<Record<string, number>>((acc, row) => {
    acc[row.status] = (acc[row.status] ?? 0) + 1
    return acc
  }, {}), [recommendations])

  const updateStatus = async (id: string, next: string, reason = '') => {
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(id)}/status`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status: next, reason }),
    })
    if (res.ok) {
      load()
      return
    }
    const detail = await res.text()
    setActionMessage(detail.trim() || `Could not update proposal: HTTP ${res.status}`)
  }

  const confirmRejectRecommendation = (row: Recommendation) => {
    setRejectingRecommendation({ row, reason: row.decision_reason || '' })
  }

  const submitRecommendationReject = async () => {
    if (!rejectingRecommendation || recommendationRejectSaving) return
    setRecommendationRejectSaving(true)
    await updateStatus(rejectingRecommendation.row.id, 'rejected', rejectingRecommendation.reason)
    setRecommendationRejectSaving(false)
    setRejectingRecommendation(null)
  }

  const openClarification = async (row: Recommendation) => {
    setClarifying(row)
    setClarificationBody(row.clarification?.body ?? '')
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(row.id)}`, { cache: 'no-store' })
    if (!res.ok) return
    const detail = await res.json()
    setClarifying(detail)
    setClarificationBody(detail.clarification?.body ?? row.clarification?.body ?? '')
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

  const reanalyzeRecommendation = async (row: Recommendation) => {
    setActionMessage(null)
    const res = await fetch(`/improvements/feedback/${row.feedback_event_id}/analyze`, { method: 'POST' })
    if (res.ok) {
      setActionMessage('Re-analysis queued.')
      load()
      return
    }
    const detail = await res.text()
    setActionMessage(detail.trim() || `Could not queue re-analysis: HTTP ${res.status}`)
  }

  const retryClarification = async (row: Recommendation) => {
    setActionMessage(null)
    const body = row.clarification?.body?.trim()
    if (!body) {
      await reanalyzeRecommendation(row)
      return
    }
    const res = await fetch(`/improvements/recommendations/${encodeURIComponent(row.id)}/clarification`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ body }),
    })
    if (res.ok) {
      setActionMessage('Clarification retry queued.')
      load()
      return
    }
    const detail = await res.text()
    setActionMessage(detail.trim() || `Could not queue clarification retry: HTTP ${res.status}`)
  }

  const editBundleItem = async (bundleID: string, item: ProposalBundleItem) => {
    const draft = bundleItemDraft(item, itemDrafts)
    const proposedScope = item.operation === 'create_new' && reviewingProposal ? resolvedBundleScopeValue(draft.proposed_scope, reviewingProposal) : draft.proposed_scope
    const res = await fetch(`/improvements/proposal-bundles/${encodeURIComponent(bundleID)}/items/${encodeURIComponent(item.id)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        proposed_body: draft.proposed_body,
        proposed_ref: draft.proposed_ref,
        proposed_name: draft.proposed_name,
        proposed_scope: proposedScope,
        proposed_description: draft.proposed_description,
        proposed_enabled: draft.proposed_enabled,
        proposed_position: draft.proposed_position,
      }),
    })
    if (res.ok) {
      load()
      return
    }
    const detail = await res.text()
    setActionMessage(detail.trim() || `Could not save changes: HTTP ${res.status}`)
  }

  const openRejectBundleItem = (bundleID: string, item: ProposalBundleItem) => {
    const bundle = Object.values(bundles).find(candidate => candidate.id === bundleID)
    setBundleDecision({
      kind: 'reject',
      bundleID,
      itemID: item.id,
      assetLabel: `${item.asset_type}/${item.operation === 'create_new' ? catalogAssetLabel(item.asset_type, item.proposed_ref || item.asset_id || item.id, catalogAssets) : catalogAssetLabel(item.asset_type, item.asset_id || item.proposed_ref || item.id, catalogAssets)}`,
      reason: item.decision_reason || '',
      closesProposal: (bundle?.items ?? []).length === 1,
    })
  }

  const refreshCatalogAssets = async (assetType: string) => {
    const assets = await fetchCatalogAssetsForType(assetType)
    setCatalogAssets(current => ({ ...current, [assetType]: assets }))
    return assets
  }

  const openLinkBundleItem = async (bundleID: string, item: ProposalBundleItem) => {
    const assets = await refreshCatalogAssets(item.asset_type).catch(() => catalogAssets[item.asset_type] ?? [])
    const options = visibleCatalogLinkAssets(assets, reviewingProposal)
    const proposed = (item.proposed_ref || item.proposed_name || item.asset_id || '').trim().toLowerCase()
    const matched = options.find(asset => asset.id.toLowerCase() === proposed || asset.name.toLowerCase() === proposed)
    setBundleDecision({
      kind: 'link',
      bundleID,
      itemID: item.id,
      assetLabel: `${item.asset_type}/${catalogAssetLabel(item.asset_type, item.proposed_ref || item.asset_id || item.id, catalogAssets)}`,
      assetType: item.asset_type,
      proposedAsset: item.proposed_ref || item.proposed_name || item.asset_id || '',
      assetID: item.asset_id || matched?.id || '',
      assets: options,
      reason: item.decision_reason || '',
    })
  }

  const submitBundleDecision = async () => {
    if (!bundleDecision || bundleDecisionSaving) return
    setBundleDecisionSaving(true)
    if (bundleDecision.kind === 'reject') {
      const res = await fetch(`/improvements/proposal-bundles/${encodeURIComponent(bundleDecision.bundleID)}/items/${encodeURIComponent(bundleDecision.itemID)}/reject`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ reason: bundleDecision.reason }),
      })
      setBundleDecisionSaving(false)
      if (res.ok) {
        const closesProposal = bundleDecision.closesProposal
        setBundleDecision(null)
        if (closesProposal) setReviewingProposal(null)
        load()
      }
      return
    }
    const assetID = bundleDecision.assetID.trim()
    if (!assetID) {
      setBundleDecisionSaving(false)
      return
    }
    const res = await fetch(`/improvements/proposal-bundles/${encodeURIComponent(bundleDecision.bundleID)}/items/${encodeURIComponent(bundleDecision.itemID)}/link-existing`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ asset_id: assetID, reason: bundleDecision.reason }),
    })
    setBundleDecisionSaving(false)
    if (res.ok) {
      setBundleDecision(null)
      load()
    }
  }

  const postBundleAction = async (bundleID: string, action: 'publish' | 'discard') => {
    const res = await fetch(`/improvements/proposal-bundles/${encodeURIComponent(bundleID)}/${action}`, { method: 'POST' })
    if (res.ok) {
      load()
      return
    }
    const detail = await res.text()
    setActionMessage(detail.trim() || `Could not ${action} bundle: HTTP ${res.status}`)
  }

  const confirmBundleAction = (bundleID: string, action: 'publish' | 'discard') => {
    setConfirming({
      title: action === 'publish' ? 'Finalize bundle?' : 'Discard bundle?',
      body: action === 'publish'
        ? 'Finalizing publishes accepted catalog changes. If every item is resolved as existing or rejected, the bundle is closed as resolved instead.'
        : 'Discarding closes this bundle without publishing its proposed catalog changes.',
      confirmLabel: action === 'publish' ? 'Finalize' : 'Discard',
      onConfirm: () => postBundleAction(bundleID, action),
    })
  }

  const submitConfirmation = async () => {
    if (!confirming || confirmSaving) return
    setConfirmSaving(true)
    await confirming.onConfirm()
    setConfirmSaving(false)
    setConfirming(null)
  }

  const skillRefForItem = (item: ProposalBundleItem) => (item.asset_id || item.proposed_ref || '').trim()

  const canLinkPublishedSkill = (row: Recommendation, item: ProposalBundleItem) =>
    item.asset_type === 'skill' &&
    item.operation === 'create_new' &&
    item.decision === 'published' &&
    skillRefForItem(item) !== '' &&
    Boolean(row.feedback?.linked_agent_name)

  const linkPublishedSkillToAgent = async (row: Recommendation, item: ProposalBundleItem) => {
    const agentName = (row.feedback?.linked_agent_name || '').trim()
    const skillRef = skillRefForItem(item)
    const targetWorkspace = row.feedback?.workspace || 'default'
    if (!agentName || !skillRef || linkingSkillItem) return
    setLinkingSkillItem(item.id)
    setActionMessage(null)
    try {
      const url = withWorkspace(`/agents/${encodeURIComponent(agentName)}`, targetWorkspace)
      const read = await fetch(url, { cache: 'no-store' })
      if (!read.ok) throw new Error(`read agent returned HTTP ${read.status}`)
      const agent = await read.json()
      const currentSkills = Array.isArray(agent.skills) ? agent.skills : []
      if (currentSkills.includes(skillRef)) {
        setActionMessage(`${skillRef} is already linked to ${agentName}.`)
        return
      }
      const write = await fetch(url, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ skills: [...currentSkills, skillRef] }),
      })
      if (!write.ok) throw new Error(`update agent returned HTTP ${write.status}`)
      setActionMessage(`Linked ${skillRef} to ${agentName}.`)
    } catch (err) {
      setActionMessage(`Could not link ${skillRef} to ${agentName}: ${(err as Error).message}`)
    } finally {
      setLinkingSkillItem(null)
    }
  }

  const isCompletedRecommendation = (row: Recommendation) => {
    const bundleStatus = bundles[row.id]?.status
    return row.status === 'rejected' || bundleStatus === 'published' || bundleStatus === 'resolved' || bundleStatus === 'discarded'
  }

  const activeRecommendations = recommendations.filter(row => !isCompletedRecommendation(row))
  const completedRecommendations = recommendations.filter(isCompletedRecommendation)
  const proposalWorkRows = activeRecommendations.filter(row =>
    row.status === 'recommended' || row.status === 'needs_user_input' || row.status === 'clarifying' || row.status === 'analyzing' || row.status === 'failed' || Boolean(bundles[row.id]?.id),
  )
  const shownRecommendations = tab === 'history'
    ? completedRecommendations
    : tab === 'proposals'
      ? proposalWorkRows
      : []
  const flowSteps = [
    { key: 'proposals' as Tab, label: 'Proposals', count: proposalWorkRows.length },
    { key: 'history' as Tab, label: 'History', count: completedRecommendations.length },
  ]
  const bundleDecisionAssets = bundleDecision?.kind === 'link' ? bundleDecision.assets : []

  return (
    <main style={{ display: 'grid', gap: '1rem' }}>
      <section style={{ display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center', flexWrap: 'wrap' }}>
        <div>
	          <h1 style={{ fontSize: '1.45rem', color: 'var(--text-heading)', marginBottom: '0.25rem' }}>Improvements</h1>
	          <div style={{ color: 'var(--text-muted)', fontSize: '0.85rem' }}>
	            {loading ? 'Loading' : `${shownRecommendations.length} proposals · ${feedback.length} feedback events`}
	            {Object.keys(counts).length > 0 ? ` · ${Object.entries(counts).map(([k, v]) => `${k}: ${v}`).join(' · ')}` : ''}
	          </div>
        </div>
        <div style={{ display: 'flex', gap: '0.75rem', alignItems: 'center', flexWrap: 'wrap' }}>
          <select value={status} onChange={e => setStatus(e.target.value)} style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', padding: '7px 9px' }}>
            <option value="">All statuses</option>
            <option value="recommended">Ready</option>
            <option value="needs_user_input">Needs input</option>
            <option value="clarifying">Clarifying</option>
            <option value="failed">Failed</option>
            <option value="rejected">Rejected</option>
          </select>
        </div>
      </section>

      {actionMessage && (
        <section style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 6, padding: '0.75rem 0.85rem', color: 'var(--text)', fontSize: '0.84rem', display: 'flex', justifyContent: 'space-between', gap: '1rem', alignItems: 'center' }}>
          <span>{actionMessage}</span>
          <button onClick={() => setActionMessage(null)} style={{ padding: '5px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Dismiss</button>
        </section>
      )}

      <section aria-label="Improvement flow" style={{ display: 'flex', justifyContent: 'space-between', gap: 10, alignItems: 'center', flexWrap: 'wrap', color: 'var(--text-muted)', fontSize: '0.78rem' }}>
        <div style={{ display: 'flex', gap: 8, alignItems: 'center', flexWrap: 'wrap' }}>
          {flowSteps.map((step, index) => (
            <div key={`${step.label}-${index}`} style={{ display: 'flex', gap: 8, alignItems: 'center' }}>
              <button
                onClick={() => setTab(step.key)}
                style={{ display: 'flex', gap: 8, alignItems: 'center', padding: '7px 9px', border: '1px solid var(--border)', background: tab === step.key ? 'var(--bg-active)' : 'var(--bg-card)', color: 'var(--text)', borderRadius: 6 }}
              >
                <span>{step.label}</span>
                <strong style={{ color: 'var(--text-heading)' }}>{step.count}</strong>
              </button>
              {index < flowSteps.length - 1 && <span aria-hidden="true">-&gt;</span>}
            </div>
          ))}
        </div>
	      </section>

      {(tab === 'proposals' || tab === 'history') && (
        <section style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: 6, overflow: 'hidden' }}>
          <div style={{ display: 'grid', gridTemplateColumns: '92px 92px minmax(260px, 1fr) 170px 130px 170px', gap: '0.75rem', padding: '0.65rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', color: 'var(--text-muted)', fontSize: '0.72rem', fontWeight: 700, textTransform: 'uppercase' }}>
            <span>ID</span>
            <span>Status</span>
            <span>Proposal</span>
            <span>Target</span>
            <span>Updated</span>
            <span>Actions</span>
          </div>
          {shownRecommendations.map(row => {
	            const bundle = bundles[row.id]
	            const failed = row.status === 'failed'
	            const decisionLocked = row.status === 'rejected' || bundle?.status === 'published' || bundle?.status === 'resolved' || bundle?.status === 'discarded'
	            const stale = decisionLocked ? null : staleVersionCheck(row, catalogVersionChecks)
	            const versionCheckPending = decisionLocked ? false : hasPendingVersionCheck(row, catalogVersionChecks)
	            const needsInput = row.status === 'needs_user_input'
	            const waiting = row.status === 'clarifying' || row.status === 'analyzing'
	            const canInspect = Boolean(bundle?.id) || tab === 'history'
            const displayID = `#${row.feedback_event_id}`
            const secondaryID = bundle?.id || row.id
            const displayStatus = displayRowStatus(row, bundle)
            return (
            <article key={row.id} style={{ display: 'grid', gridTemplateColumns: '92px 92px minmax(260px, 1fr) 170px 130px 170px', gap: '0.75rem', padding: '0.75rem 0.8rem', borderBottom: '1px solid var(--border-subtle)', fontSize: '0.8rem', alignItems: 'start' }}>
              <div style={{ display: 'grid', gap: 3, minWidth: 0 }}>
                <span style={{ color: 'var(--text-heading)', fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace', fontSize: '0.86rem', fontWeight: 700 }}>{displayID}</span>
                <span title={secondaryID} style={{ color: 'var(--text-faint)', fontSize: '0.68rem', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{secondaryID}</span>
              </div>
              <span style={{ color: rowStatusColor(row, bundle), fontWeight: 700 }}>{displayStatus}</span>
              <div style={{ display: 'grid', gap: 6 }}>
                <strong style={{ color: 'var(--text-heading)' }}>{row.finding}</strong>
                <span style={{ color: 'var(--text)' }}>{row.rationale}</span>
                {row.feedback?.source_url && <a href={row.feedback.source_url} target="_blank" rel="noreferrer">{row.feedback.repo_owner}/{row.feedback.repo_name} feedback #{row.feedback_event_id}</a>}
                {row.error && (
                  <span style={{ border: '1px solid var(--border)', background: 'var(--bg-input)', borderRadius: 6, padding: '0.45rem 0.55rem', color: 'var(--text)' }}>Error: {row.error}</span>
                )}
                {row.decision_reason && (
                  <span style={{ color: 'var(--text-muted)', fontSize: '0.78rem' }}>Decision reason: {row.decision_reason}</span>
                )}
                {stale && (
                  <span style={{ border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', borderRadius: 6, padding: '0.45rem 0.55rem', color: 'var(--text-danger)' }}>
                    Target changed since analysis: {catalogTargetLabel(stale.target.assetType, stale.target.assetID, catalogAssets)} moved from {stale.target.baseVersionID} to {stale.check.current_version_id || 'the current version'}.
                  </span>
                )}
	                {bundle?.id && (
	                  <span style={{ color: 'var(--text-muted)' }}>Bundle {bundle.id} · {bundle.status} · {(bundle.items ?? []).length} items</span>
	                )}
              </div>
              <div style={{ display: 'grid', gap: 3, color: 'var(--text-muted)' }}>
                <span>{row.type}</span>
                <span>{catalogTargetLabel(row.target_asset_type, row.target_asset_id, catalogAssets)}</span>
                {stale && <span>stale target</span>}
                {!stale && versionCheckPending && <span>checking target</span>}
                <span>{row.attribution_confidence}</span>
              </div>
              <time style={{ color: 'var(--text-faint)' }}>{formatDateTime(recommendationActivityAt(row))}</time>
              <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                {waiting && <span style={{ color: 'var(--text-faint)', fontSize: '0.76rem', alignSelf: 'center' }}>Analysis in progress</span>}
                {needsInput && (
                  <button onClick={() => openClarification(row)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Clarify</button>
                )}
                {needsInput && (
                  <button onClick={() => confirmRejectRecommendation(row)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Reject</button>
                )}
                {failed && row.clarification?.body?.trim() && (
                  <button onClick={() => retryClarification(row)} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Retry</button>
                )}
                {failed && !row.clarification?.body?.trim() && (
                  <button onClick={() => reanalyzeRecommendation(row)} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Re-analyze</button>
                )}
                {!decisionLocked && !needsInput && !waiting && (
                  <button onClick={() => confirmRejectRecommendation(row)} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Reject</button>
                )}
                {!needsInput && !waiting && canInspect && (
                  <button onClick={() => setReviewingProposal(row)} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Inspect</button>
                )}
                {row.status === 'rejected' && <span style={{ color: 'var(--text-faint)', fontSize: '0.76rem', alignSelf: 'center' }}>Decision locked</span>}
                {stale && row.status === 'recommended' && (
                  <>
                    <span style={{ color: 'var(--text-danger)', fontSize: '0.76rem', alignSelf: 'center' }}>Proposal blocked: target changed</span>
                    <button onClick={() => reanalyzeRecommendation(row)} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Re-analyze</button>
                  </>
                )}
	                {!stale && versionCheckPending && row.status === 'recommended' && (
	                  <span style={{ color: 'var(--text-faint)', fontSize: '0.76rem', alignSelf: 'center' }}>Checking target versions</span>
	                )}
	              </div>
            </article>
          )})}
          {!loading && shownRecommendations.length === 0 && (
            <div style={{ padding: '1rem', color: 'var(--text-muted)', fontSize: '0.85rem' }}>No matching proposals.</div>
          )}
        </section>
      )}

      {reviewingProposal && (
        <Modal
          title="Review proposal"
          subtitle={`${reviewingProposal.id} · ${reviewingProposal.status}`}
          onClose={() => setReviewingProposal(null)}
          maxWidth="1100px"
          maxHeight="min(860px, 94vh)"
          overlayBackground="rgba(0,0,0,0.48)"
          align="center"
        >
            <div style={{ display: 'grid', gap: '1rem' }}>
              <section style={{ display: 'grid', gap: 6 }}>
                <strong style={{ color: 'var(--text-heading)' }}>{reviewingProposal.finding}</strong>
                <span style={{ color: 'var(--text)' }}>{reviewingProposal.rationale}</span>
                {reviewingProposal.feedback?.source_url && <a href={reviewingProposal.feedback.source_url} target="_blank" rel="noreferrer">{reviewingProposal.feedback.repo_owner}/{reviewingProposal.feedback.repo_name} feedback #{reviewingProposal.feedback_event_id}</a>}
              </section>

              {bundles[reviewingProposal.id]?.id && (() => {
                const bundle = bundles[reviewingProposal.id]
                const bundlePending = bundle.status === 'pending'
                const bundleItems = bundle.items ?? []
                const bundleBlocked = bundlePending && (
                  Boolean(bundle.recommendation_changed) ||
                  bundleItems.some(item => item.stale && item.decision === 'accepted') ||
                  bundleItems.some(item => isPendingBundleEditItem(item) && Boolean(pendingBundleEditConflict(item, bundle.id!, bundles)))
                )
                return (
                  <section style={{ display: 'grid', gap: 10, background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: 10 }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8, flexWrap: 'wrap', color: 'var(--text-muted)' }}>
                      <span>Bundle {bundle.id} · {bundle.status}{bundle.recommendation_changed ? ' · source changed' : ''}</span>
                      {bundlePending && (
                        <span style={{ display: 'flex', gap: 6 }}>
                          {!bundleBlocked && <button title="Finalize this bundle. Accepted items publish catalog versions; items resolved as existing are skipped, and a bundle with no new versions becomes resolved." onClick={() => confirmBundleAction(bundle.id!, 'publish')} style={{ padding: '6px 8px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6 }}>Finalize Bundle</button>}
                          <button onClick={() => confirmBundleAction(bundle.id!, 'discard')} style={{ padding: '6px 8px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Discard Bundle</button>
                        </span>
                      )}
                    </div>
                    {bundleBlocked && (
                      <div style={{ border: '1px solid var(--border)', background: 'var(--bg-card)', borderRadius: 6, padding: '0.65rem 0.75rem', color: 'var(--text)', fontSize: '0.82rem' }}>
                        This bundle cannot be finalized because the source analysis changed, one of its target versions changed, or another pending bundle already has a staged change for the same catalog item. Re-analyze or resolve the other staged change before publishing catalog changes.
                      </div>
                    )}
                    {bundleItems.map(item => {
                      const draft = bundleItemDraft(item, itemDrafts)
                      const updateDraft = (next: Partial<BundleItemDraft>) => setItemDrafts(current => ({ ...current, [item.id]: { ...bundleItemDraft(item, current), ...next } }))
                      const itemDiff = diffLines(versionBody(item.asset_type, item.base_version), bundleItemDraftBody(item, draft))
                      const draftConflict = bundle.id ? pendingBundleEditConflict(item, bundle.id, bundles, draft) : null
                      const saveDraftEnabled = bundleItemDraftChanged(item, draft) && !draftConflict
                      const scopeOptions = bundleScopeOptions(reviewingProposal)
                      const scopeValue = resolvedBundleScopeValue(draft.proposed_scope, reviewingProposal)
                      const linkedExisting = item.decision === 'linked_existing'
                      const itemAssetLabel = item.operation === 'create_new'
                        ? catalogAssetLabel(item.asset_type, draft.proposed_ref || item.proposed_ref || item.asset_id || item.id, catalogAssets)
                        : catalogAssetLabel(item.asset_type, item.asset_id || item.proposed_ref || item.id, catalogAssets)
                      return (
                        <section key={item.id} style={{ display: 'grid', gap: 7, background: 'var(--bg-card)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: 8 }}>
                          <div style={{ display: 'flex', justifyContent: 'space-between', gap: 10, alignItems: 'start', flexWrap: 'wrap' }}>
                            <div style={{ display: 'grid', gap: 3 }}>
                              <h3 style={{ margin: 0, color: 'var(--text-heading)', fontSize: '0.88rem' }}>{item.asset_type} · {item.operation}</h3>
                              <span style={{ color: 'var(--text-muted)', fontSize: '0.78rem', overflowWrap: 'anywhere' }}>{itemAssetLabel}</span>
                            </div>
                            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap', justifyContent: 'flex-end', color: 'var(--text-muted)', fontSize: '0.74rem' }}>
                              <StatusChip label="Item state" value={bundleItemStateLabel(item.decision)} highlight={bundleItemStateHighlight(item.decision)} />
                              <StatusChip label="Base" value={bundleItemBaseLabel(item)} />
                              {item.duplicate_risk && <StatusChip label="Duplicate risk" value={capitalizeLabel(item.duplicate_risk)} />}
                            </div>
                          </div>
                          {linkedExisting && (
                            <div style={{ border: '1px solid var(--accent)', background: 'var(--bg-active)', borderRadius: 6, padding: '0.55rem 0.65rem', color: 'var(--text)', fontSize: '0.82rem', lineHeight: 1.45 }}>
                              <strong style={{ color: 'var(--text-heading)' }}>Item state: Uses existing asset.</strong>{' '}
                              This proposal item will not publish the original generated diff. It resolves the proposed new {item.asset_type} as already covered by {itemAssetLabel}.
                            </div>
                          )}
                          {bundlePending && item.operation === 'create_new' && (
                            <div style={{ display: 'grid', gap: 7 }}>
                              <SectionLabel>Identity</SectionLabel>
                              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(170px, 1fr))', gap: 8 }}>
                                <EditableField label="Ref">
                                  <input aria-label={`Bundle item ref for ${item.id}`} value={draft.proposed_ref} onChange={e => updateDraft({ proposed_ref: e.target.value })} style={inputStyle} />
                                </EditableField>
                                <EditableField label="Name">
                                  <input aria-label={`Bundle item name for ${item.id}`} value={draft.proposed_name} onChange={e => updateDraft({ proposed_name: e.target.value })} style={inputStyle} />
                                </EditableField>
                                <EditableField label="Scope">
                                  <select aria-label={`Bundle item scope for ${item.id}`} value={scopeValue} onChange={e => updateDraft({ proposed_scope: e.target.value })} style={inputStyle}>
                                    {scopeOptions.map(option => (
                                      <option key={option.value} value={option.value}>{option.label}</option>
                                    ))}
                                  </select>
                                </EditableField>
                              </div>
                            </div>
                          )}
                          {bundlePending && item.asset_type === 'guardrail' && (
                            <div style={{ display: 'grid', gap: 7 }}>
                              <SectionLabel>Guardrail Settings</SectionLabel>
                              <div style={{ display: 'grid', gridTemplateColumns: 'minmax(min(100%, 220px), 1fr) max-content 82px', gap: 8, alignItems: 'end' }}>
                                <EditableField label="Description">
                                  <input aria-label={`Bundle item guardrail description for ${item.id}`} value={draft.proposed_description} onChange={e => updateDraft({ proposed_description: e.target.value })} style={inputStyle} />
                                </EditableField>
                                <label style={{ display: 'grid', gap: 5, color: 'var(--text-muted)', fontSize: '0.74rem', textTransform: 'uppercase', fontWeight: 700 }}>
                                  Enabled
                                  <span style={{ display: 'flex', gap: 7, alignItems: 'center', minHeight: 33, color: 'var(--text)', fontSize: '0.8rem', textTransform: 'none', fontWeight: 500 }}>
                                    <input aria-label="Enabled" type="checkbox" checked={draft.proposed_enabled} onChange={e => updateDraft({ proposed_enabled: e.target.checked })} />
                                    Enabled
                                  </span>
                                </label>
                                <EditableField label="Position">
                                  <input aria-label={`Bundle item guardrail position for ${item.id}`} type="number" value={draft.proposed_position} onChange={e => updateDraft({ proposed_position: Number(e.target.value) })} style={{ ...inputStyle, width: 72, boxSizing: 'border-box' }} />
                                </EditableField>
                              </div>
                            </div>
                          )}
                          {bundlePending && (
                            <div style={{ display: 'grid', gap: 7 }}>
                              <SectionLabel>Body</SectionLabel>
                              <div aria-label={`Bundle item body for ${item.id}`}>
                              <MarkdownEditor
                                value={draft.proposed_body}
                                onChange={proposed_body => updateDraft({ proposed_body })}
                                placeholder={`${item.asset_type} guidance text...`}
                                minHeight={260}
                                expandTitle={`${item.asset_type} ${itemAssetLabel}`}
                              />
                              </div>
                            </div>
                          )}
                          <details open={!linkedExisting} style={{ display: 'grid', gap: 7 }}>
                            <summary style={{ cursor: 'pointer', color: 'var(--text-heading)', fontSize: '0.76rem', textTransform: 'uppercase', fontWeight: 700 }}>
                              {linkedExisting ? 'Original proposed diff (not applied)' : 'Diff'}
                            </summary>
                            <pre aria-label={`Bundle item diff for ${item.id}`} style={{ margin: '7px 0 0', whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', maxHeight: bundlePending ? 320 : 440, overflow: 'auto', background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: 10, fontSize: '0.75rem', lineHeight: 1.5 }}>
                              {itemDiff.map((line, i) => (
                                <span key={`${i}-${line.kind}`} style={{ display: 'block', color: line.kind === 'add' ? 'var(--success)' : line.kind === 'del' ? 'var(--text-danger)' : 'var(--text-muted)' }}>{line.text || ' '}</span>
                              ))}
                            </pre>
                          </details>
                          {(item.rationale || item.decision_reason) && (
                            <div style={{ display: 'grid', gap: 7 }}>
                              {item.rationale && (
                                <div style={{ display: 'grid', gap: 4 }}>
                                  <SectionLabel>Rationale</SectionLabel>
                                  <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', lineHeight: 1.45 }}>{item.rationale}</div>
                                </div>
                              )}
                              {item.decision_reason && (
                                <div style={{ display: 'grid', gap: 4 }}>
                                  <SectionLabel>Decision Reason</SectionLabel>
                                  <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem', lineHeight: 1.45 }}>{item.decision_reason}</div>
                                </div>
                              )}
                            </div>
                          )}
                          {draftConflict && (
                            <div style={{ border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', borderRadius: 6, padding: '0.55rem 0.65rem', color: 'var(--text-danger)', fontSize: '0.8rem', lineHeight: 1.45 }}>
                              Another pending bundle already has a staged change for this catalog item: {draftConflict.bundleID}.
                            </div>
                          )}
                          {bundlePending && (
                            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                              <button
                                disabled={!saveDraftEnabled}
                                onClick={() => editBundleItem(bundle.id!, item)}
                                style={{ padding: '5px 7px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6, opacity: saveDraftEnabled ? 1 : 0.55 }}
                              >
                                Save Changes
                              </button>
                              <button onClick={() => setItemDrafts(current => ({ ...current, [item.id]: { ...bundleItemDraft(item, current), proposed_body: item.analyst_proposed_body } }))} style={{ padding: '5px 7px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Reset</button>
                              <button onClick={() => openRejectBundleItem(bundle.id!, item)} style={{ padding: '5px 7px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Reject</button>
                              {item.operation === 'create_new' && item.asset_type !== 'guardrail' && <button title="Use an existing catalog asset instead of creating this proposed new one. This resolves duplication; it does not attach the asset to any agent." onClick={() => void openLinkBundleItem(bundle.id!, item)} style={{ padding: '5px 7px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Use Existing Asset</button>}
                            </div>
                          )}
                          {canLinkPublishedSkill(reviewingProposal, item) && (
                            <div style={{ display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                              <button
                                disabled={linkingSkillItem === item.id}
                                onClick={() => linkPublishedSkillToAgent(reviewingProposal, item)}
                                title="Attach this published skill to the attributed agent so future runs include it. This changes the agent's skill list."
                                style={{ padding: '5px 7px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6, opacity: linkingSkillItem === item.id ? 0.6 : 1 }}
                              >
                                {linkingSkillItem === item.id ? 'Linking...' : `Add ${skillRefForItem(item)} to ${reviewingProposal.feedback?.linked_agent_name}`}
                              </button>
                            </div>
                          )}
                        </section>
                      )
                    })}
                  </section>
                )
              })()}
              {!bundles[reviewingProposal.id]?.id && (
                <section style={{ display: 'grid', gap: 6, background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: 6, padding: 10, color: 'var(--text-muted)', fontSize: '0.82rem' }}>
                  <strong style={{ color: 'var(--text-heading)' }}>No editable bundle is attached.</strong>
                  <span>This proposal is not ready for catalog changes. Clarify it when user input is requested, or re-analyze stale feedback before publishing.</span>
                </section>
              )}
            </div>
        </Modal>
      )}

      {clarifying && (
        <Modal
          title="Clarify proposal"
          subtitle={`#${clarifying.feedback_event_id} · ${clarifying.id} · ${clarifying.status}`}
          onClose={() => setClarifying(null)}
          maxWidth="920px"
          maxHeight="min(760px, 92vh)"
          zIndex={1300}
          overlayBackground="rgba(0,0,0,0.42)"
          align="center"
        >
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 320px), 1fr))', gap: '1rem' }}>
              <div style={{ display: 'grid', gap: '0.85rem' }}>
                <section style={{ display: 'grid', gap: 6 }}>
                  <h3 style={{ color: 'var(--text-heading)', fontSize: '0.82rem' }}>Proposal</h3>
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
                <InfoRow label="Target" value={catalogTargetLabel(clarifying.target_asset_type, clarifying.target_asset_id, catalogAssets)} />
                <InfoRow label="Base version" value={clarifying.target_base_version_id || 'unresolved'} />
                {clarifying.clarification && <InfoRow label="Last clarified" value={formatDateTime(clarifying.clarification.updated_at)} />}
              </aside>
            </div>
        </Modal>
      )}

      {bundleDecision && (
        <Modal
          title={bundleDecision.kind === 'reject'
            ? bundleDecision.closesProposal ? 'Reject proposal' : 'Reject bundle item'
            : 'Use existing asset'}
          subtitle={bundleDecision.assetLabel}
          onClose={() => setBundleDecision(null)}
          maxWidth="620px"
          maxHeight="min(620px, 92vh)"
          overlayBackground="rgba(0,0,0,0.42)"
          align="center"
          showHeaderClose={bundleDecision.kind === 'link'}
        >
            <div style={{ display: 'grid', gap: '0.85rem' }}>
              {bundleDecision.kind === 'reject' && bundleDecision.closesProposal && (
                <p style={{ color: 'var(--text-muted)', fontSize: '0.84rem', lineHeight: 1.45, margin: 0 }}>
                  This is the only item in the bundle, so rejecting it will close the whole proposal. The decision is terminal.
                </p>
              )}
              {bundleDecision.kind === 'link' && (
                <p style={{ color: 'var(--text-muted)', fontSize: '0.84rem', lineHeight: 1.45, margin: 0 }}>
                  This resolves the proposed new asset as already covered by an existing catalog asset. It does not attach that asset to any agent.
                </p>
              )}
              {bundleDecision.kind === 'link' && (
                <label style={{ display: 'grid', gap: 5, color: 'var(--text-muted)', fontSize: '0.78rem' }}>
                  Existing {bundleDecision.assetType}
                  <select
                    aria-label="Existing asset id/ref"
                    value={bundleDecision.assetID}
                    onChange={e => setBundleDecision(current => current && current.kind === 'link' ? { ...current, assetID: e.target.value } : current)}
                    style={{ background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 6, padding: 8, font: 'inherit', fontSize: '0.85rem' }}
                    autoFocus
                  >
                    <option value="">Select existing asset...</option>
                    {bundleDecisionAssets.map(asset => (
                      <option key={asset.id} value={asset.id}>{asset.name} ({asset.id}) · {asset.scope}</option>
                    ))}
                  </select>
                  {bundleDecisionAssets.length === 0 && (
                    <span style={{ color: 'var(--text-faint)', fontSize: '0.76rem' }}>No visible existing {bundleDecision.assetType} assets are available for this feedback scope.</span>
                  )}
                </label>
              )}
              <label style={{ display: 'grid', gap: 5, color: 'var(--text-muted)', fontSize: '0.78rem' }}>
                Reason
                <textarea
                  aria-label="Bundle item decision reason"
                  value={bundleDecision.reason}
                  onChange={e => setBundleDecision(current => current ? { ...current, reason: e.target.value } : current)}
                  rows={6}
                  style={{ resize: 'vertical', minHeight: 120, background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 6, padding: 8, font: 'inherit', fontSize: '0.85rem', lineHeight: 1.45 }}
                  autoFocus={bundleDecision.kind === 'reject'}
                />
              </label>
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
                <button onClick={() => setBundleDecision(null)} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Cancel</button>
                <button
                  disabled={bundleDecisionSaving || (bundleDecision.kind === 'link' && bundleDecision.assetID.trim() === '')}
                  onClick={submitBundleDecision}
                  style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6, opacity: bundleDecisionSaving || (bundleDecision.kind === 'link' && bundleDecision.assetID.trim() === '') ? 0.6 : 1 }}
                >
                  {bundleDecisionSaving ? 'Saving...' : bundleDecision.kind === 'reject' ? bundleDecision.closesProposal ? 'Reject Proposal' : 'Reject Item' : 'Use Existing Asset'}
                </button>
              </div>
            </div>
        </Modal>
      )}

      {rejectingRecommendation && (
        <Modal
          title="Reject proposal"
          subtitle="Rejecting this proposal is a terminal decision. You can optionally record why."
          onClose={() => setRejectingRecommendation(null)}
          maxWidth="560px"
          zIndex={1210}
          overlayBackground="rgba(0,0,0,0.42)"
          align="center"
          showHeaderClose={false}
        >
            <div style={{ display: 'grid', gap: '0.85rem' }}>
              <label style={{ display: 'grid', gap: 5, color: 'var(--text-muted)', fontSize: '0.78rem' }}>
                Reason
                <textarea
                  aria-label="Proposal decision reason"
                  value={rejectingRecommendation.reason}
                  onChange={e => setRejectingRecommendation(current => current ? { ...current, reason: e.target.value } : current)}
                  rows={5}
                  style={{ resize: 'vertical', minHeight: 110, background: 'var(--bg-input)', border: '1px solid var(--border)', color: 'var(--text)', borderRadius: 6, padding: 8, font: 'inherit', fontSize: '0.85rem', lineHeight: 1.45 }}
                  autoFocus
                />
              </label>
              <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
                <button onClick={() => setRejectingRecommendation(null)} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Cancel</button>
                <button disabled={recommendationRejectSaving} onClick={submitRecommendationReject} style={{ padding: '7px 10px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6, opacity: recommendationRejectSaving ? 0.6 : 1 }}>{recommendationRejectSaving ? 'Rejecting...' : 'Reject Proposal'}</button>
              </div>
            </div>
        </Modal>
      )}

      {confirming && (
        <Modal
          title={confirming.title}
          subtitle={confirming.body}
          onClose={() => setConfirming(null)}
          maxWidth="520px"
          zIndex={1210}
          overlayBackground="rgba(0,0,0,0.42)"
          align="center"
          showHeaderClose={false}
        >
            <div style={{ display: 'flex', gap: 8, justifyContent: 'flex-end', flexWrap: 'wrap' }}>
              <button onClick={() => setConfirming(null)} style={{ padding: '7px 10px', border: '1px solid var(--border)', background: 'var(--bg-input)', color: 'var(--text)', borderRadius: 6 }}>Cancel</button>
              <button disabled={confirmSaving} onClick={submitConfirmation} style={{ padding: '7px 10px', border: '1px solid var(--accent)', background: 'var(--bg-active)', color: 'var(--text)', borderRadius: 6, opacity: confirmSaving ? 0.6 : 1 }}>{confirmSaving ? 'Working...' : confirming.confirmLabel}</button>
            </div>
        </Modal>
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

const inputStyle: CSSProperties = {
  background: 'var(--bg-input)',
  border: '1px solid var(--border)',
  color: 'var(--text)',
  borderRadius: 6,
  padding: 8,
  font: 'inherit',
  fontSize: '0.8rem',
}

function SectionLabel({ children }: { children: ReactNode }) {
  return (
    <h4 style={{ margin: 0, color: 'var(--text-heading)', fontSize: '0.76rem', textTransform: 'uppercase', fontWeight: 700 }}>
      {children}
    </h4>
  )
}

function EditableField({ label, children }: { label: string; children: ReactNode }) {
  return (
    <label style={{ display: 'grid', gap: 5, color: 'var(--text-muted)', fontSize: '0.74rem', textTransform: 'uppercase', fontWeight: 700 }}>
      {label}
      {children}
    </label>
  )
}

function StatusChip({ label, value, highlight = false }: { label: string; value: string; highlight?: boolean }) {
  return (
    <span style={{ display: 'inline-flex', gap: 4, alignItems: 'center', border: highlight ? '1px solid var(--accent)' : '1px solid var(--border-subtle)', borderRadius: 6, padding: '3px 6px', background: highlight ? 'var(--bg-active)' : 'var(--bg)' }}>
      <span style={{ color: 'var(--text-faint)', textTransform: 'uppercase', fontWeight: 700 }}>{label}</span>
      <span style={{ color: highlight ? 'var(--text-heading)' : 'var(--text)' }}>{value}</span>
    </span>
  )
}
