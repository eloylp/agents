'use client'
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import Link from 'next/link'
import RepoFilter, { useRepoFilter } from '@/components/RepoFilter'
import {
  ReactFlow,
  Background,
  Controls,
  MarkerType,
  Position,
  BaseEdge,
  EdgeLabelRenderer,
  type Node,
  type Edge,
  type NodeProps,
  type EdgeProps,
  type Connection,
  getBezierPath,
  Handle,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import dagre from 'dagre'
import Card from '@/components/Card'
import AgentForm, { emptyAgentForm, type BackendOption } from '@/components/AgentForm'
import BadgePicker from '@/components/BadgePicker'
import RunButton from '@/components/RunButton'
import { useSelectedWorkspace, withWorkspace, type CatalogItem } from '@/lib/workspace'
import { type Binding } from '@/lib/bindings'
import { fmtDuration } from '@/lib/format'
import {
  addCanDispatch,
  availableDispatchTargets,
  enableAllowDispatch,
  incomingDispatchSources,
  outgoingDispatchTargets,
  removeCanDispatch,
  storeAgentFromResponse,
  validateConnection,
  type DispatchRelationship,
  type StoreAgent,
} from '@/lib/dispatch-wiring'

interface DispatchRecord {
  at: string
  repo: string
  number: number
  reason: string
}

interface GraphEdge {
  from: string
  to: string
  count: number
  dispatches: DispatchRecord[]
}

interface GraphData {
  nodes: Array<{ id: string; status?: string }>
  edges: GraphEdge[]
}

interface AgentInfo {
  id: string
  name: string
  backend?: string
  model?: string
  current_status: string
  description?: string
  can_dispatch?: string[]
  allow_dispatch?: boolean
  allow_prs?: boolean
  allow_memory?: boolean
  prompt_id?: string
  prompt_ref?: string
  prompt_scope?: string
  scope_type?: string
  scope_repo?: string
  skills?: string[]
  bindings?: Array<{ repo: string }>
}

interface RepoInfo {
  name: string
  enabled: boolean
  bindings: Binding[]
}

interface RunnerRow {
  id: number
  event_id: string
  kind: string
  repo: string
  number: number
  status: 'enqueued' | 'running' | 'success' | 'error'
  agent?: string
  target_agent?: string
  span_id?: string
  started_at?: string
  completed_at?: string
  run_duration_ms?: number
  summary?: string
}

interface BindingDraft {
  repo: string
  kind: 'labels' | 'events' | 'cron'
  labels: string[]
  events: string[]
  cron: string
  enabled: boolean
}

const emptyBindingDraft: BindingDraft = { repo: '', kind: 'labels', labels: [], events: [], cron: '', enabled: true }

type DispatchEdgeData = {
  from: string
  to: string
  count: number
  dispatches: DispatchRecord[]
  isActive: boolean
  selected?: boolean
  onOpen?: (edge: DispatchEdgeData) => void
}

const SUPPORTED_EVENTS = [
  'issues.labeled', 'issues.opened', 'issues.edited', 'issues.reopened', 'issues.closed',
  'pull_request.labeled', 'pull_request.opened', 'pull_request.synchronize',
  'pull_request.ready_for_review', 'pull_request.closed',
  'issue_comment.created',
  'pull_request_review.submitted', 'pull_request_review_comment.created',
  'push',
]

function repoPath(name: string): string {
  const [owner, ...rest] = name.split('/')
  const repo = rest.join('/')
  return `/repos/${encodeURIComponent(owner)}/${encodeURIComponent(repo)}`
}

function bindingTrigger(b: Binding): string {
  if (b.cron) return `cron: ${b.cron}`
  if (b.labels && b.labels.length > 0) return `labels: ${b.labels.join(', ')}`
  if (b.events && b.events.length > 0) return `events: ${b.events.join(', ')}`
  return 'no trigger'
}

function bindingFromDraft(agent: string, draft: BindingDraft): Binding {
  const base: Binding = { agent, enabled: draft.enabled }
  if (draft.kind === 'labels') return { ...base, labels: draft.labels }
  if (draft.kind === 'events') return { ...base, events: draft.events }
  return { ...base, cron: draft.cron.trim() }
}

function fmtTime(s?: string) {
  if (!s) return '-'
  return new Date(s).toLocaleString()
}

const handleStyle = {
  background: 'var(--accent)',
  border: '2px solid var(--bg-card)',
  borderRadius: 3,
  width: 34,
  height: 12,
  opacity: 0.9,
}

function DispatchEdge(props: EdgeProps) {
  const data = props.data as DispatchEdgeData
  const [edgePath, labelX, labelY] = getBezierPath({
    sourceX: props.sourceX,
    sourceY: props.sourceY,
    sourcePosition: props.sourcePosition,
    targetX: props.targetX,
    targetY: props.targetY,
    targetPosition: props.targetPosition,
  })
  const selected = data.selected || props.selected
  const label = data.count > 0 ? `${data.count}` : 'wire'

  return (
    <>
      <BaseEdge
        id={props.id}
        path={edgePath}
        markerEnd={props.markerEnd}
        style={props.style}
        interactionWidth={72}
      />
      <EdgeLabelRenderer>
        <button
          className="nodrag nopan"
          onClick={(event) => {
            event.stopPropagation()
            data.onOpen?.(data)
          }}
          title={`${data.from} can dispatch ${data.to}`}
          style={{
            position: 'absolute',
            transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)`,
            pointerEvents: 'all',
            background: selected ? '#78350f' : data.isActive ? 'var(--accent-bg)' : 'var(--bg-card)',
            border: `1px solid ${selected ? '#f59e0b' : data.isActive ? 'var(--btn-primary-border)' : 'var(--border)'}`,
            color: selected ? '#fbbf24' : data.isActive ? 'var(--accent)' : 'var(--text-muted)',
            borderRadius: 3,
            padding: '3px 9px',
            fontSize: '0.7rem',
            fontWeight: 700,
            cursor: 'pointer',
            boxShadow: selected ? '0 0 0 3px rgba(245,158,11,0.18)' : '0 2px 8px rgba(0,0,0,0.25)',
          }}
        >
          {label}
        </button>
      </EdgeLabelRenderer>
    </>
  )
}

function AgentNode({ data }: NodeProps) {
  const d = data as { label: string; status: string; description: string; dispatchable: boolean; skills: string[]; highlight?: 'source' | 'target' | 'selected' }
  const statusColors: Record<string, { bg: string; border: string; icon: string }> = {
    running: { bg: 'var(--success-bg)', border: 'var(--success)', icon: '⚡' },
    error:   { bg: 'var(--error-bg)', border: 'var(--text-danger)', icon: '⚠' },
    idle:    { bg: 'var(--accent-bg)', border: 'var(--accent)', icon: '●' },
  }
  const c = statusColors[d.status] ?? statusColors.idle
  const highlightBorder = d.highlight === 'source'
    ? '#f59e0b'
    : d.highlight === 'target'
      ? '#38bdf8'
      : d.highlight === 'selected'
        ? 'var(--accent)'
        : c.border

  return (
    <>
      <Handle type="target" position={Position.Top} title="Drop dispatch wiring here" style={{ ...handleStyle, top: -7 }} />
      <div style={{
        background: 'var(--bg-card)',
        border: `2px solid ${highlightBorder}`,
        borderRadius: '12px',
        padding: '10px 20px',
        minWidth: '180px',
        maxWidth: '220px',
        textAlign: 'center',
        boxShadow: d.highlight ? `0 0 0 3px rgba(56,189,248,0.18), 0 2px 8px rgba(0,0,0,0.3)` : '0 2px 8px rgba(0,0,0,0.3)',
        position: 'relative',
      }}>
        {d.dispatchable && (
          <div style={{
            position: 'absolute', top: -8, right: -8,
            width: 18, height: 18, borderRadius: '50%',
            background: 'var(--btn-primary-bg)', color: '#fff',
            fontSize: '10px', lineHeight: '18px', textAlign: 'center',
            fontWeight: 700,
          }}>D</div>
        )}
        <div style={{ fontSize: '11px', marginBottom: '2px' }}>{c.icon}</div>
        <div style={{
          fontWeight: 700, fontSize: '13px', color: 'var(--text)',
          whiteSpace: 'nowrap',
        }}>
          {d.label}
        </div>
        <div style={{ fontSize: '10px', color: 'var(--text-muted)', marginTop: '2px' }}>{d.status}</div>
        {d.skills.length > 0 && (
          <div
            title={d.skills.join(', ')}
            style={{
              fontSize: '9px',
              color: 'var(--text-faint)',
              marginTop: '4px',
              cursor: 'help',
            }}
          >
            {d.skills.length} skill{d.skills.length === 1 ? '' : 's'}
          </div>
        )}
      </div>
      <Handle type="source" position={Position.Bottom} title="Drag to another agent to add dispatch wiring" style={{ ...handleStyle, bottom: -7 }} />
    </>
  )
}

const nodeTypes = { agent: AgentNode }
const edgeTypes = { dispatch: DispatchEdge }

function layoutGraph(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph()
  g.setDefaultEdgeLabel(() => ({}))
  g.setGraph({ rankdir: 'TB', ranksep: 90, nodesep: 60 })

  nodes.forEach(n => g.setNode(n.id, { width: 200, height: 90 }))
  edges.forEach(e => g.setEdge(e.source, e.target))

  dagre.layout(g)

  return nodes.map(n => {
    const pos = g.node(n.id)
    return { ...n, position: { x: pos.x - 80, y: pos.y - 40 } }
  })
}

export default function GraphPage() {
  const [graphData, setGraphData] = useState<GraphData>({ nodes: [], edges: [] })
  const [agents, setAgents] = useState<AgentInfo[]>([])
  const [repos, setRepos] = useState<RepoInfo[]>([])
  const [layoutPositions, setLayoutPositions] = useState<Record<string, { x: number; y: number }>>({})
  const [selectedEdge, setSelectedEdge] = useState<{ from: string; to: string; count: number; dispatches: DispatchRecord[]; isActive: boolean } | null>(null)
  const [selectedNodeName, setSelectedNodeName] = useState<string | null>(null)
  const [hoveredEdge, setHoveredEdge] = useState<{ from: string; to: string } | null>(null)
  const [addTargetName, setAddTargetName] = useState('')
  const [loading, setLoading] = useState(true)
  const [repoFilter, setRepoFilter] = useRepoFilter()
  const [wiringError, setWiringError] = useState('')
  const [wiringBusy, setWiringBusy] = useState(false)
  const [backendOptions, setBackendOptions] = useState<BackendOption[]>([])
  const [skillOptions, setSkillOptions] = useState<CatalogItem[]>([])
  const [agentNames, setAgentNames] = useState<string[]>([])
  const [promptOptions, setPromptOptions] = useState<CatalogItem[]>([])
  const [panelMode, setPanelMode] = useState<'details' | 'edge' | 'create' | 'edit' | null>(null)
  const [agentForm, setAgentForm] = useState<StoreAgent>(emptyAgentForm)
  const [agentSaving, setAgentSaving] = useState(false)
  const [agentSaveError, setAgentSaveError] = useState('')
  const [agentRuns, setAgentRuns] = useState<RunnerRow[]>([])
  const [agentActivityLoading, setAgentActivityLoading] = useState(false)
  const [bindingDraft, setBindingDraft] = useState<BindingDraft>(emptyBindingDraft)
  const [bindingSaving, setBindingSaving] = useState(false)
  const [bindingError, setBindingError] = useState('')
  const { workspace } = useSelectedWorkspace()

  const loadedOnce = useRef(false)
  const relationshipAgents = useMemo<DispatchRelationship[]>(() => agents.map(a => ({
    name: a.name,
    description: a.description ?? '',
    allow_dispatch: a.allow_dispatch ?? false,
    can_dispatch: a.can_dispatch ?? [],
    status: a.current_status,
  })), [agents])

  useEffect(() => {
    setAddTargetName('')
    setBindingDraft(emptyBindingDraft)
    setBindingError('')
  }, [selectedNodeName])

  useEffect(() => {
    if (bindingDraft.repo || repos.length === 0) return
    setBindingDraft(d => ({ ...d, repo: repos[0].name }))
  }, [repos, bindingDraft.repo])

  const loadLookups = useCallback(() => {
    fetch('/backends')
      .then(r => r.ok ? r.json() : [])
      .then((data: BackendOption[]) => setBackendOptions((data ?? []).filter(b => b.detected !== false)))
      .catch(() => {})
    fetch('/skills')
      .then(r => r.ok ? r.json() : [])
      .then((data: CatalogItem[]) => setSkillOptions(data ?? []))
      .catch(() => {})
    fetch(withWorkspace('/agents', workspace))
      .then(r => r.ok ? r.json() : [])
      .then((data: { name: string }[]) => setAgentNames(data.map(a => a.name)))
      .catch(() => {})
    fetch('/prompts')
      .then(r => r.ok ? r.json() : [])
      .then((data: CatalogItem[]) => setPromptOptions(data ?? []))
      .catch(() => {})
  }, [workspace])

  const load = useCallback(() => {
    if (!loadedOnce.current) setLoading(true)
    loadedOnce.current = true
    Promise.all([
      fetch(withWorkspace('/graph', workspace)).then(r => r.json()),
      fetch(withWorkspace('/agents', workspace)).then(r => r.json()),
      fetch(withWorkspace('/graph/layout', workspace)).then(r => r.ok ? r.json() : { positions: [] }),
      fetch(withWorkspace('/repos', workspace)).then(r => r.ok ? r.json() : []),
    ]).then(([gd, ad, ld, rd]) => {
      setGraphData(gd)
      setAgents(ad)
      setRepos(rd ?? [])
      setAgentNames((ad ?? []).map((a: AgentInfo) => a.name))
      const nextPositions: Record<string, { x: number; y: number }> = {}
      ;(ld.positions ?? []).forEach((p: { node_id: string; x: number; y: number }) => {
        nextPositions[p.node_id] = { x: p.x, y: p.y }
      })
      setLayoutPositions(nextPositions)
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [workspace])

  useEffect(() => {
    load()
    loadLookups()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [load, loadLookups])

  const loadAgentActivity = useCallback(async (agentName: string) => {
    setAgentActivityLoading(true)
    try {
      const res = await fetch(withWorkspace('/runners?limit=200', workspace), { cache: 'no-store' })
      if (!res.ok) throw new Error(`runners ${res.status}`)
      const data = await res.json() as { runners?: RunnerRow[] }
      const rows = (data.runners ?? [])
        .filter(r => r.agent === agentName || r.target_agent === agentName)
        .slice(0, 8)
      setAgentRuns(rows)
    } catch {
      setAgentRuns([])
    } finally {
      setAgentActivityLoading(false)
    }
  }, [workspace])

  useEffect(() => {
    if (panelMode === 'details' && selectedNodeName) {
      loadAgentActivity(selectedNodeName)
    }
  }, [panelMode, selectedNodeName, loadAgentActivity])

  const activeEdgeMap = useMemo(() => {
    const m = new Map<string, GraphEdge>()
    graphData.edges.forEach(e => {
      // When a repo filter is active, only count dispatches whose record.repo matches.
      // An edge with zero matching dispatches drops out of the active map so it renders
      // as a wired-but-inactive edge (or not at all if the agents aren't visible).
      const dispatches = repoFilter ? e.dispatches.filter(d => d.repo === repoFilter) : e.dispatches
      if (repoFilter && dispatches.length === 0) return
      m.set(`${e.from}->${e.to}`, { ...e, count: dispatches.length, dispatches })
    })
    return m
  }, [graphData.edges, repoFilter])

  const selectedEdgeKey = selectedEdge ? `${selectedEdge.from}->${selectedEdge.to}` : ''
  const hoveredEdgeKey = hoveredEdge ? `${hoveredEdge.from}->${hoveredEdge.to}` : ''
  const openEdge = useCallback((edge: DispatchEdgeData) => {
    setSelectedEdge({
      from: edge.from,
      to: edge.to,
      count: edge.count,
      dispatches: edge.dispatches,
      isActive: edge.isActive,
    })
    setSelectedNodeName(null)
    setPanelMode('edge')
  }, [])

  const { flowNodes, flowEdges, wiringInfo } = useMemo(() => {
    const visibleAgents = repoFilter
      ? agents.filter(a => (a.bindings ?? []).some(b => b.repo === repoFilter))
      : agents
    const visibleNames = new Set(visibleAgents.map(a => a.name))
    const idByName = new Map(visibleAgents.map(a => [a.name, a.id || a.name]))

    // Build combined edge set
    const allEdges: Array<{ from: string; to: string; isActive: boolean; count: number; dispatches: DispatchRecord[] }> = []
    const seen = new Set<string>()

    visibleAgents.forEach(a => {
      (a.can_dispatch ?? []).forEach(target => {
        if (!visibleNames.has(target)) return
        const key = `${a.name}->${target}`
        seen.add(key)
        const active = activeEdgeMap.get(key)
        allEdges.push({ from: a.name, to: target, isActive: !!active, count: active?.count ?? 0, dispatches: active?.dispatches ?? [] })
      })
    })

    graphData.edges.forEach(e => {
      const key = `${e.from}->${e.to}`
      const active = activeEdgeMap.get(key)
      if (!seen.has(key) && active && visibleNames.has(e.from) && visibleNames.has(e.to)) {
        allEdges.push({ from: e.from, to: e.to, isActive: true, count: active.count, dispatches: active.dispatches })
      }
    })

    // Build nodes from visible agents
    const highlightedEdge = selectedEdge ?? hoveredEdge

    const nodes: Node[] = visibleAgents.map(a => {
      const highlight = highlightedEdge?.from === a.name
        ? 'source'
        : highlightedEdge?.to === a.name
          ? 'target'
          : selectedNodeName === a.name
            ? 'selected'
            : undefined
      const nodeID = a.id || a.name
      return {
        id: nodeID,
        type: 'agent',
        position: layoutPositions[nodeID] ?? { x: 0, y: 0 },
        data: {
          name: a.name,
          label: a.name,
          status: a.current_status ?? 'idle',
          description: a.description ?? '',
          dispatchable: a.allow_dispatch ?? false,
          skills: a.skills ?? [],
          highlight,
        },
      }
    })

    // Build edges
    const edges: Edge[] = allEdges.map((e, i) => {
      const key = `${e.from}->${e.to}`
      const selected = key === selectedEdgeKey
      const hovered = key === hoveredEdgeKey
      const emphasized = selected || hovered
      const stroke = selected ? '#f59e0b' : e.isActive ? 'var(--accent)' : 'var(--border)'
      return {
        id: `dispatch:${e.from}->${e.to}`,
        source: idByName.get(e.from) ?? e.from,
        target: idByName.get(e.to) ?? e.to,
        type: 'dispatch',
        selectable: true,
        animated: e.isActive && e.count > 0,
        interactionWidth: 72,
        style: {
          stroke,
          strokeWidth: emphasized ? 4 : e.isActive ? 2.5 : 1.5,
          strokeDasharray: e.isActive ? undefined : '6 4',
          cursor: 'pointer',
        },
        markerEnd: {
          type: MarkerType.ArrowClosed,
          color: selected ? '#f59e0b' : e.isActive ? '#38bdf8' : '#1e3a5f',
          width: emphasized ? 20 : 16,
          height: emphasized ? 16 : 12,
        },
        data: { ...e, selected, onOpen: openEdge },
      }
    })

    const laid = layoutGraph(nodes, edges).map(n => {
      const saved = layoutPositions[n.id]
      return saved ? { ...n, position: saved } : n
    })

    return {
      flowNodes: laid,
      flowEdges: edges,
      wiringInfo: { active: allEdges.filter(e => e.isActive).length, total: allEdges.length },
    }
  }, [agents, graphData.edges, activeEdgeMap, repoFilter, selectedEdge, selectedEdgeKey, hoveredEdge, hoveredEdgeKey, selectedNodeName, layoutPositions, openEdge])

  const onEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    openEdge(edge.data as DispatchEdgeData)
  }, [openEdge])

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    const agent = agents.find(a => (a.id || a.name) === node.id)
    if (agent) {
      setSelectedNodeName(agent.name)
      setSelectedEdge(null)
      setPanelMode('details')
    }
  }, [agents])

  const onNodeDragStop = useCallback((_: React.MouseEvent, node: Node) => {
    const nodeID = node.id
    const next = { x: node.position.x, y: node.position.y }
    setLayoutPositions(prev => ({ ...prev, [nodeID]: next }))
    fetch(withWorkspace('/graph/layout', workspace), {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ positions: [{ node_id: nodeID, x: next.x, y: next.y }] }),
    }).catch(() => {
      // Layout persistence is operational UI state; a failed save should not
      // block graph editing or hide the local drag result.
    })
  }, [workspace])

  const autoLayout = useCallback(() => {
    fetch(withWorkspace('/graph/layout', workspace), { method: 'DELETE' }).finally(() => {
      setLayoutPositions({})
      load()
    })
  }, [load, workspace])

  const fetchStoreAgent = useCallback(async (name: string): Promise<StoreAgent> => {
    const res = await fetch(withWorkspace(`/agents/${encodeURIComponent(name)}`, workspace))
    if (!res.ok) throw new Error(`fetch ${name}: ${res.status}`)
    const data = await res.json() as Partial<StoreAgent>
    return storeAgentFromResponse(data, name)
  }, [workspace])

  const postStoreAgent = useCallback(async (a: StoreAgent): Promise<void> => {
    const res = await fetch(withWorkspace('/agents', workspace), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ ...a, workspace_id: workspace }),
    })
    if (!res.ok) {
      const msg = await res.text()
      throw new Error(msg || `save ${a.name} failed (${res.status})`)
    }
  }, [workspace])

  const agentNameForNodeID = useCallback((nodeID: string) => {
    return agents.find(a => (a.id || a.name) === nodeID)?.name ?? nodeID
  }, [agents])

  const isValidConnection = useCallback((c: Connection | Edge) => {
    if (!c.source || !c.target) return false
    const sourceName = agentNameForNodeID(c.source)
    const targetName = agentNameForNodeID(c.target)
    const src = agents.find(a => a.name === sourceName)
    const existing = src?.can_dispatch ?? []
    return validateConnection(sourceName, targetName, existing).ok
  }, [agents, agentNameForNodeID])

  const onConnect = useCallback(async (c: Connection) => {
    if (!c.source || !c.target || wiringBusy) return
    const sourceName = agentNameForNodeID(c.source)
    const targetName = agentNameForNodeID(c.target)
    const src = agents.find(a => a.name === sourceName)
    const existing = src?.can_dispatch ?? []
    const check = validateConnection(sourceName, targetName, existing)
    if (!check.ok) {
      setWiringError(check.reason ?? 'invalid connection')
      return
    }
    const targetInfo = agents.find(a => a.name === targetName)
    if (!targetInfo?.description) {
      setWiringError(`${targetName} needs a description before it can be used as a dispatch expert`)
      return
    }
    setWiringError('')
    setWiringBusy(true)
    try {
      const source = await fetchStoreAgent(sourceName)
      await postStoreAgent(addCanDispatch(source, targetName))
      const target = await fetchStoreAgent(targetName)
      if (!target.allow_dispatch) {
        await postStoreAgent(enableAllowDispatch(target))
      }
      load()
    } catch (e) {
      setWiringError(String(e))
    } finally {
      setWiringBusy(false)
    }
  }, [agents, fetchStoreAgent, postStoreAgent, load, wiringBusy, agentNameForNodeID])

  const removeEdge = useCallback(async (from: string, to: string) => {
    if (wiringBusy) return
    setWiringError('')
    setWiringBusy(true)
    try {
      const source = await fetchStoreAgent(from)
      await postStoreAgent(removeCanDispatch(source, to))
      setSelectedEdge(null)
      setPanelMode(null)
      load()
    } catch (e) {
      setWiringError(String(e))
    } finally {
      setWiringBusy(false)
    }
  }, [fetchStoreAgent, postStoreAgent, load, wiringBusy])

  const addWiring = useCallback(async (from: string, to: string) => {
    if (wiringBusy) return
    const sourceInfo = agents.find(a => a.name === from)
    const targetInfo = agents.find(a => a.name === to)
    const check = validateConnection(from, to, sourceInfo?.can_dispatch ?? [])
    if (!check.ok) {
      setWiringError(check.reason ?? 'invalid connection')
      return
    }
    if (!targetInfo?.description) {
      setWiringError(`${to} needs a description before it can be used as a dispatch expert`)
      return
    }

    setWiringError('')
    setWiringBusy(true)
    try {
      const source = await fetchStoreAgent(from)
      await postStoreAgent(addCanDispatch(source, to))
      const target = await fetchStoreAgent(to)
      if (!target.allow_dispatch) {
        await postStoreAgent(enableAllowDispatch(target))
      }
      setAddTargetName('')
      load()
    } catch (e) {
      setWiringError(String(e))
    } finally {
      setWiringBusy(false)
    }
  }, [agents, fetchStoreAgent, postStoreAgent, load, wiringBusy])

  const openAgent = useCallback((name: string) => {
    const agent = agents.find(a => a.name === name)
    if (!agent) return
    setSelectedNodeName(agent.name)
    setSelectedEdge(null)
    setPanelMode('details')
  }, [agents])

  const openCreateAgent = useCallback(() => {
    setSelectedEdge(null)
    setSelectedNodeName(null)
    setAgentSaveError('')
    setAgentForm(emptyAgentForm)
    setPanelMode('create')
    loadLookups()
  }, [loadLookups])

  const openEditAgent = useCallback(async (agentName: string) => {
    const agent = agents.find(a => a.name === agentName)
    setSelectedEdge(null)
    setSelectedNodeName(agentName)
    setAgentSaveError('')
    setAgentForm({
      ...emptyAgentForm,
      name: agentName,
      backend: agent?.backend ?? '',
      model: agent?.model ?? '',
      skills: agent?.skills ?? [],
      prompt_id: agent?.prompt_id ?? '',
      prompt_ref: agent?.prompt_ref ?? '',
      prompt_scope: agent?.prompt_scope ?? '',
      scope_type: agent?.scope_type ?? 'workspace',
      scope_repo: agent?.scope_repo ?? '',
      allow_prs: agent?.allow_prs ?? false,
      allow_dispatch: agent?.allow_dispatch ?? false,
      allow_memory: agent?.allow_memory ?? true,
      can_dispatch: agent?.can_dispatch ?? [],
      description: agent?.description ?? '',
    })
    setPanelMode('edit')
    loadLookups()
    try {
      const full = await fetchStoreAgent(agentName)
      setAgentForm({ ...emptyAgentForm, ...full, allow_memory: full.allow_memory ?? true })
    } catch {
      // The panel keeps the graph snapshot data so editing can still recover
      // if the detail fetch succeeds on the next save attempt.
    }
  }, [agents, fetchStoreAgent, loadLookups])

  const saveAgent = useCallback(async (form: StoreAgent) => {
    setAgentSaving(true)
    setAgentSaveError('')
    try {
      const res = await fetch(withWorkspace('/agents', workspace), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...form, workspace_id: workspace }),
      })
      if (!res.ok) {
        const msg = await res.text()
        setAgentSaveError(msg || 'Save failed')
        setAgentSaving(false)
        return
      }
      const saved = storeAgentFromResponse(await res.json() as Partial<StoreAgent>, form.name)
      setPanelMode('details')
      setSelectedNodeName(saved.name)
      load()
      loadLookups()
    } catch (e) {
      setAgentSaveError(String(e))
    } finally {
      setAgentSaving(false)
    }
  }, [load, loadLookups, workspace])

  const saveBinding = useCallback(async () => {
    const agent = selectedNodeName
    if (!agent) return
    const draft = bindingDraft
    if (!draft.repo) {
      setBindingError('Select a repo first.')
      return
    }
    if (draft.kind === 'labels' && draft.labels.length === 0) {
      setBindingError('Add at least one label.')
      return
    }
    if (draft.kind === 'events' && draft.events.length === 0) {
      setBindingError('Add at least one event.')
      return
    }
    if (draft.kind === 'cron' && !draft.cron.trim()) {
      setBindingError('Enter a cron expression.')
      return
    }
    setBindingSaving(true)
    setBindingError('')
    try {
      const res = await fetch(withWorkspace(`${repoPath(draft.repo)}/bindings`, workspace), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(bindingFromDraft(agent, draft)),
      })
      if (!res.ok) {
        setBindingError((await res.text()) || 'Binding save failed')
        return
      }
      setBindingDraft({ ...emptyBindingDraft, repo: draft.repo })
      load()
    } catch (e) {
      setBindingError(String(e))
    } finally {
      setBindingSaving(false)
    }
  }, [bindingDraft, load, selectedNodeName, workspace])

  const toggleBinding = useCallback(async (repo: string, binding: Binding, enabled: boolean) => {
    if (typeof binding.id !== 'number') return
    setBindingSaving(true)
    setBindingError('')
    try {
      const res = await fetch(withWorkspace(`${repoPath(repo)}/bindings/${binding.id}`, workspace), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ...binding, enabled }),
      })
      if (!res.ok) {
        setBindingError((await res.text()) || 'Binding update failed')
        return
      }
      load()
    } catch (e) {
      setBindingError(String(e))
    } finally {
      setBindingSaving(false)
    }
  }, [load, workspace])

  const deleteBinding = useCallback(async (repo: string, binding: Binding) => {
    if (typeof binding.id !== 'number') return
    setBindingSaving(true)
    setBindingError('')
    try {
      const res = await fetch(withWorkspace(`${repoPath(repo)}/bindings/${binding.id}`, workspace), { method: 'DELETE' })
      if (!res.ok && res.status !== 204) {
        setBindingError((await res.text()) || 'Binding delete failed')
        return
      }
      load()
    } catch (e) {
      setBindingError(String(e))
    } finally {
      setBindingSaving(false)
    }
  }, [load, workspace])

  const closePanel = useCallback(() => {
    setPanelMode(null)
    setSelectedNodeName(null)
    setSelectedEdge(null)
    setAgentSaveError('')
    setBindingError('')
  }, [])

  const selectedNode = selectedNodeName ? agents.find(a => a.name === selectedNodeName) ?? null : null
  const selectedNodeOutgoing = selectedNode ? outgoingDispatchTargets(selectedNode, relationshipAgents) : []
  const selectedNodeIncoming = selectedNode ? incomingDispatchSources(selectedNode.name, relationshipAgents) : []
  const selectedNodeTargets = selectedNode ? availableDispatchTargets(selectedNode.name, selectedNode.can_dispatch ?? [], relationshipAgents) : []
  const selectedAddTarget = selectedNodeTargets.find(a => a.name === addTargetName) ?? null
  const selectedNodeBindings = selectedNode ? repos.flatMap(repo => (repo.bindings ?? [])
    .filter(binding => binding.agent === selectedNode.name)
    .map(binding => ({ repo, binding }))) : []
  const selectedNodeRepos = Array.from(new Set(selectedNodeBindings.map(({ repo }) => repo.name)))
  const knownLabels = useMemo(() => {
    const set = new Set<string>()
    for (const repo of repos) {
      for (const binding of repo.bindings ?? []) {
        for (const label of binding.labels ?? []) set.add(label)
      }
    }
    return Array.from(set).sort()
  }, [repos])

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Agent Interaction Graph</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {flowNodes.length} agent{flowNodes.length !== 1 ? 's' : ''} · {wiringInfo.active} active / {wiringInfo.total} wired edges
          </p>
        </div>
        <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center' }}>
          <RepoFilter selected={repoFilter} onChange={setRepoFilter} workspace={workspace} />
          <button onClick={openCreateAgent} style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}>
            + Create agent
          </button>
          <button onClick={load} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--accent)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 500 }}>
            Refresh
          </button>
          <button onClick={autoLayout} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--accent)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 500 }}>
            Reset layout
          </button>
        </div>
      </div>

      {(wiringBusy || wiringError) && (
        <div style={{ marginBottom: '1rem', padding: '8px 12px', background: 'var(--accent-bg)', border: '1px solid var(--btn-primary-border)', borderRadius: '6px', fontSize: '0.8rem', color: 'var(--text)' }}>
          {wiringBusy && <span style={{ color: 'var(--text-muted)' }}>Saving dispatch wiring...</span>}
          {wiringError && <span style={{ color: 'var(--text-danger)' }}>{wiringError}</span>}
        </div>
      )}

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading...</p>}

      {panelMode && (
        <aside style={{
          position: 'fixed', top: 0, right: 0, bottom: 0, width: 'min(520px, 100vw)',
          background: 'var(--bg-card)', borderLeft: '1px solid var(--border)', zIndex: 1000,
          boxShadow: '-12px 0 32px rgba(0,0,0,0.32)', overflowY: 'auto', padding: '1.25rem',
        }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem', gap: '1rem' }}>
            <div>
              <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--text-heading)' }}>
                {panelMode === 'create'
                  ? 'Create agent'
                  : panelMode === 'edit'
                    ? `Edit ${agentForm.name}`
                    : panelMode === 'edge' && selectedEdge
                      ? `${selectedEdge.from} -> ${selectedEdge.to}`
                      : selectedNode?.name}
              </h2>
              {panelMode === 'details' && selectedNode?.description && (
                <p style={{ color: 'var(--text-faint)', fontSize: '0.875rem', marginTop: '0.25rem' }}>{selectedNode.description}</p>
              )}
            </div>
            <button onClick={closePanel} style={{ background: 'none', border: 'none', fontSize: '1.2rem', cursor: 'pointer', color: 'var(--text-faint)' }}>x</button>
          </div>

          {(panelMode === 'create' || panelMode === 'edit') && (
            <AgentForm
              key={`${panelMode}:${agentForm.name}`}
              initial={agentForm}
              isNew={panelMode === 'create'}
              workspace={workspace}
              backends={backendOptions}
              skillOptions={skillOptions}
              agentNames={agentNames}
              promptOptions={promptOptions}
              repoNames={repos.map(r => r.name)}
              onSave={saveAgent}
              onCancel={closePanel}
              saving={agentSaving}
              error={agentSaveError}
            />
          )}

          {panelMode === 'edge' && selectedEdge && (
            <div style={{ display: 'grid', gap: '1rem' }}>
              <div style={{
                display: 'inline-block', justifySelf: 'start', padding: '2px 8px', borderRadius: '999px', fontSize: '0.75rem', fontWeight: 500,
                background: selectedEdge.isActive ? 'var(--accent-bg)' : 'rgba(100,116,139,0.15)',
                color: selectedEdge.isActive ? 'var(--accent)' : 'var(--text-muted)',
                border: `1px solid ${selectedEdge.isActive ? 'var(--btn-primary-border)' : 'var(--border-subtle)'}`,
              }}>
                {selectedEdge.isActive ? `${selectedEdge.count} dispatch${selectedEdge.count !== 1 ? 'es' : ''}` : 'wired, no dispatches yet'}
              </div>
              <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem' }}>
                <strong style={{ color: 'var(--text)' }}>{selectedEdge.from}</strong> can dispatch{' '}
                <strong style={{ color: 'var(--text)' }}>{selectedEdge.to}</strong>.
              </p>
              <div style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', fontSize: '0.8rem', color: 'var(--text-muted)' }}>
                <div style={{ color: 'var(--text-faint)', marginBottom: '4px' }}>Remove config change</div>
                <code>{selectedEdge.from}.can_dispatch -= [&quot;{selectedEdge.to}&quot;]</code>
              </div>
              {wiringError && (
                <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{wiringError}</p>
              )}
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                <button
                  onClick={() => openAgent(selectedEdge.from)}
                  style={{ padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.8rem' }}
                >
                  Open source
                </button>
                <button
                  onClick={() => openAgent(selectedEdge.to)}
                  style={{ padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--border)', background: 'var(--bg-card)', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.8rem' }}
                >
                  Open target
                </button>
                <button
                  onClick={() => removeEdge(selectedEdge.from, selectedEdge.to)}
                  disabled={wiringBusy}
                  style={{ padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', cursor: wiringBusy ? 'wait' : 'pointer', fontSize: '0.8rem', fontWeight: 600 }}
                >
                  {wiringBusy ? 'Removing...' : 'Remove wiring'}
                </button>
              </div>
              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Recent dispatches</div>
                {selectedEdge.dispatches.length === 0 ? (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No runtime dispatches recorded for this edge yet.</p>
                ) : (
                  <div style={{ display: 'grid', gap: '0.5rem' }}>
                    {selectedEdge.dispatches.slice().reverse().map((d, i) => (
                      <div key={i} style={{ background: 'var(--bg)', borderRadius: '6px', padding: '10px', fontSize: '0.8rem', border: '1px solid var(--border-subtle)' }}>
                        <div style={{ color: 'var(--text)', fontWeight: 500 }}>{fmtTime(d.at)}</div>
                        <div style={{ color: 'var(--text-muted)' }}>{d.repo} #{d.number}</div>
                        {d.reason && <div style={{ color: 'var(--text-faint)', fontStyle: 'italic', marginTop: '4px' }}>{d.reason}</div>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {panelMode === 'details' && selectedNode && (
            <div style={{ display: 'grid', gap: '1rem' }}>
              <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                <span style={{ background: 'var(--accent-bg)', border: '1px solid var(--btn-primary-border)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--accent)' }}>
                  {selectedNode.current_status}
                </span>
                {selectedNode.allow_dispatch && (
                  <span style={{ background: 'var(--accent-bg)', border: '1px solid var(--btn-primary-border)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--accent)' }}>dispatchable</span>
                )}
              </div>
              <button
                onClick={() => openEditAgent(selectedNode.name)}
                style={{ justifySelf: 'start', padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: 'pointer', fontSize: '0.8rem', fontWeight: 600 }}
              >
                Edit agent
              </button>
              {selectedNodeRepos.length > 0 && (
                <RunButton agent={selectedNode.name} repos={selectedNodeRepos} />
              )}
              {(selectedNode.skills ?? []).length > 0 && (
                <div>
                  <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.25rem' }}>Skills</div>
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {selectedNode.skills!.map(s => (
                      <span key={s} style={{ background: 'rgba(100,116,139,0.15)', border: '1px solid var(--border-subtle)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--text-faint)' }}>{s}</span>
                    ))}
                  </div>
                </div>
              )}

              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Repo triggers</div>
                {selectedNodeBindings.length === 0 ? (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No repo triggers are bound to this agent.</p>
                ) : (
                  <div style={{ display: 'grid', gap: '0.5rem', marginBottom: '0.75rem' }}>
                    {selectedNodeBindings.map(({ repo, binding }) => (
                      <div key={`${repo.name}:${binding.id ?? bindingTrigger(binding)}`} style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', display: 'grid', gap: '6px' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', alignItems: 'center' }}>
                          <div>
                            <div style={{ color: 'var(--text)', fontWeight: 600, fontSize: '0.8rem' }}>{repo.name}</div>
                            <div style={{ color: binding.enabled === false ? 'var(--text-muted)' : 'var(--text-faint)', fontSize: '0.75rem' }}>{bindingTrigger(binding)}</div>
                          </div>
                          <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
                            <label style={{ color: 'var(--text-muted)', fontSize: '0.75rem', display: 'flex', gap: '0.25rem', alignItems: 'center' }}>
                              <input
                                type="checkbox"
                                checked={binding.enabled !== false}
                                disabled={bindingSaving || typeof binding.id !== 'number'}
                                onChange={e => toggleBinding(repo.name, binding, e.target.checked)}
                              />
                              on
                            </label>
                            <button
                              onClick={() => deleteBinding(repo.name, binding)}
                              disabled={bindingSaving || typeof binding.id !== 'number'}
                              style={{ padding: '4px 8px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', cursor: bindingSaving ? 'wait' : 'pointer', fontSize: '0.72rem', fontWeight: 600 }}
                            >
                              Delete
                            </button>
                          </div>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
                <div style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', display: 'grid', gap: '0.6rem' }}>
                  <div style={{ color: 'var(--text)', fontWeight: 600, fontSize: '0.8rem' }}>Add trigger</div>
                  {repos.length === 0 ? (
                    <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>Create a repo first from the Repos section, then bind this agent here.</p>
                  ) : (
                    <>
                      <select
                        value={bindingDraft.repo}
                        onChange={e => setBindingDraft(d => ({ ...d, repo: e.target.value }))}
                        style={{ background: 'var(--bg-card)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: '6px', padding: '6px 8px' }}
                      >
                        {repos.map(repo => <option key={repo.name} value={repo.name}>{repo.name}{repo.enabled ? '' : ' (disabled)'}</option>)}
                      </select>
                      <select
                        value={bindingDraft.kind}
                        onChange={e => setBindingDraft(d => ({ ...d, kind: e.target.value as BindingDraft['kind'], labels: [], events: [], cron: '' }))}
                        style={{ background: 'var(--bg-card)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: '6px', padding: '6px 8px' }}
                      >
                        <option value="labels">labels</option>
                        <option value="events">events</option>
                        <option value="cron">cron</option>
                      </select>
                      {bindingDraft.kind === 'labels' && (
                        <BadgePicker options={knownLabels} selected={bindingDraft.labels} onChange={labels => setBindingDraft(d => ({ ...d, labels }))} placeholder="Add label..." freeText />
                      )}
                      {bindingDraft.kind === 'events' && (
                        <BadgePicker options={SUPPORTED_EVENTS} selected={bindingDraft.events} onChange={events => setBindingDraft(d => ({ ...d, events }))} placeholder="Add event..." />
                      )}
                      {bindingDraft.kind === 'cron' && (
                        <input
                          value={bindingDraft.cron}
                          onChange={e => setBindingDraft(d => ({ ...d, cron: e.target.value }))}
                          placeholder="0 9 * * *"
                          style={{ background: 'var(--bg-card)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: '6px', padding: '6px 8px' }}
                        />
                      )}
                      <label style={{ color: 'var(--text-muted)', fontSize: '0.8rem', display: 'flex', gap: '0.35rem', alignItems: 'center' }}>
                        <input type="checkbox" checked={bindingDraft.enabled} onChange={e => setBindingDraft(d => ({ ...d, enabled: e.target.checked }))} />
                        enabled
                      </label>
                      {bindingError && <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem' }}>{bindingError}</p>}
                      <button
                        onClick={saveBinding}
                        disabled={bindingSaving}
                        style={{ justifySelf: 'start', padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: bindingSaving ? 'wait' : 'pointer', fontSize: '0.8rem', fontWeight: 600 }}
                      >
                        {bindingSaving ? 'Saving...' : 'Add trigger'}
                      </button>
                    </>
                  )}
                </div>
              </div>

              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Outgoing dispatch targets</div>
                {selectedNodeOutgoing.length === 0 ? (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No outgoing dispatch wiring.</p>
                ) : (
                  <div style={{ display: 'grid', gap: '0.5rem' }}>
                    {selectedNodeOutgoing.map(target => (
                      <div key={target.name} style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', display: 'grid', gap: '6px' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', alignItems: 'center' }}>
                          <div>
                            <div style={{ color: 'var(--text)', fontWeight: 600 }}>{target.name}</div>
                            <div style={{ color: target.allow_dispatch ? 'var(--text-muted)' : 'var(--text-danger)', fontSize: '0.75rem' }}>
                              {target.allow_dispatch ? 'can receive dispatches' : 'allow_dispatch is disabled'}
                              {!target.description && ' · missing description'}
                            </div>
                          </div>
                          <button
                            onClick={() => removeEdge(selectedNode.name, target.name)}
                            disabled={wiringBusy}
                            style={{ padding: '5px 10px', borderRadius: '6px', border: '1px solid var(--border-danger)', background: 'var(--bg-danger)', color: 'var(--text-danger)', cursor: wiringBusy ? 'wait' : 'pointer', fontSize: '0.75rem', fontWeight: 600 }}
                          >
                            Remove
                          </button>
                        </div>
                        <code style={{ color: 'var(--text-faint)', fontSize: '0.75rem' }}>{selectedNode.name}.can_dispatch -= [&quot;{target.name}&quot;]</code>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Incoming dispatch sources</div>
                {selectedNodeIncoming.length === 0 ? (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No agents dispatch to this agent.</p>
                ) : (
                  <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                    {selectedNodeIncoming.map(source => (
                      <span key={source.name} style={{ background: 'rgba(245,158,11,0.15)', border: '1px solid #78350f', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: '#fcd34d' }}>{source.name}</span>
                    ))}
                  </div>
                )}
                <p style={{ color: selectedNode.allow_dispatch ? 'var(--text-muted)' : 'var(--text-danger)', fontSize: '0.75rem', marginTop: '0.5rem' }}>
                  {selectedNode.allow_dispatch ? `${selectedNode.name} can receive dispatches.` : `${selectedNode.name} cannot receive dispatches until allow_dispatch is enabled.`}
                </p>
              </div>

              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Add wiring</div>
                <div style={{ display: 'flex', gap: '0.5rem', alignItems: 'center', flexWrap: 'wrap' }}>
                  <select
                    value={addTargetName}
                    onChange={e => setAddTargetName(e.target.value)}
                    style={{ background: 'var(--bg)', color: 'var(--text)', border: '1px solid var(--border)', borderRadius: '6px', padding: '6px 8px', minWidth: '180px' }}
                  >
                    <option value="">Select target...</option>
                    {selectedNodeTargets.map(target => (
                      <option key={target.name} value={target.name}>{target.name}</option>
                    ))}
                  </select>
                  <button
                    onClick={() => selectedAddTarget && addWiring(selectedNode.name, selectedAddTarget.name)}
                    disabled={!selectedAddTarget || !selectedAddTarget.description || wiringBusy}
                    style={{ padding: '6px 12px', borderRadius: '6px', border: '1px solid var(--btn-primary-border)', background: 'var(--btn-primary-bg)', color: '#fff', cursor: !selectedAddTarget || !selectedAddTarget.description || wiringBusy ? 'not-allowed' : 'pointer', fontSize: '0.8rem', fontWeight: 600, opacity: !selectedAddTarget || !selectedAddTarget.description || wiringBusy ? 0.65 : 1 }}
                  >
                    {wiringBusy ? 'Saving...' : 'Add wiring'}
                  </button>
                </div>
                {selectedAddTarget && (
                  <div style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', marginTop: '0.5rem', color: 'var(--text-muted)', fontSize: '0.8rem' }}>
                    <div style={{ color: 'var(--text-faint)', marginBottom: '4px' }}>Apply config changes</div>
                    <div><code>{selectedNode.name}.can_dispatch += [&quot;{selectedAddTarget.name}&quot;]</code></div>
                    {!selectedAddTarget.allow_dispatch && <div><code>{selectedAddTarget.name}.allow_dispatch = true</code></div>}
                    {!selectedAddTarget.description && <div style={{ color: 'var(--text-danger)', marginTop: '6px' }}>This target needs a description before it can be used as a dispatch expert.</div>}
                  </div>
                )}
                {selectedNodeTargets.length === 0 && (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem', marginTop: '0.5rem' }}>No available targets remain.</p>
                )}
                {wiringError && (
                  <p style={{ color: 'var(--text-danger)', fontSize: '0.8rem', marginTop: '0.5rem' }}>{wiringError}</p>
                )}
              </div>

              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.5rem' }}>Latest runs and traces</div>
                {agentActivityLoading && <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>Loading recent runs...</p>}
                {!agentActivityLoading && agentRuns.length === 0 && (
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No recent runs for this agent.</p>
                )}
                {!agentActivityLoading && agentRuns.length > 0 && (
                  <div style={{ display: 'grid', gap: '0.5rem' }}>
                    {agentRuns.map(run => (
                      <div key={`${run.id}:${run.span_id ?? ''}`} style={{ background: 'var(--bg)', border: '1px solid var(--border-subtle)', borderRadius: '6px', padding: '10px', display: 'grid', gap: '4px' }}>
                        <div style={{ display: 'flex', justifyContent: 'space-between', gap: '0.75rem', alignItems: 'center' }}>
                          <div style={{ color: 'var(--text)', fontWeight: 600, fontSize: '0.8rem' }}>
                            {run.repo || '-'} {run.number > 0 ? `#${run.number}` : ''}
                          </div>
                          <span style={{ color: run.status === 'error' ? 'var(--text-danger)' : run.status === 'success' ? 'var(--success)' : 'var(--accent)', fontSize: '0.72rem', fontWeight: 700 }}>
                            {run.status}
                          </span>
                        </div>
                        <div style={{ color: 'var(--text-muted)', fontSize: '0.75rem' }}>
                          {run.kind || '-'} · {fmtTime(run.started_at ?? run.completed_at)} · {fmtDuration(run.run_duration_ms)}
                        </div>
                        {run.summary && <div style={{ color: 'var(--text-faint)', fontSize: '0.75rem', fontStyle: 'italic' }}>{run.summary}</div>}
                        {run.event_id && (
                          <Link href={`/traces/?id=${encodeURIComponent(run.event_id)}`} style={{ color: 'var(--accent)', fontSize: '0.75rem', textDecoration: 'none' }}>
                            Open trace →
                          </Link>
                        )}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}
        </aside>
      )}

      {!loading && (
        <Card style={{ padding: 0, overflow: 'hidden' }}>
          <div style={{ height: 'calc(100vh - 200px)', minHeight: '500px', position: 'relative' }}>
              {flowNodes.length === 0 && (
                <div style={{
                  position: 'absolute', zIndex: 5, left: '50%', top: '50%', transform: 'translate(-50%, -50%)',
                  display: 'grid', gap: '0.75rem', justifyItems: 'center', padding: '1rem',
                }}>
                  <p style={{ color: 'var(--text-muted)', fontSize: '0.9rem' }}>No agents configured.</p>
                  <button
                    onClick={openCreateAgent}
                    style={{ background: 'var(--btn-primary-bg)', border: '1px solid var(--btn-primary-border)', color: '#fff', padding: '7px 14px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 600 }}
                  >
                    Create first agent
                  </button>
                </div>
              )}
              <ReactFlow
                nodes={flowNodes}
                edges={flowEdges}
                nodeTypes={nodeTypes}
                edgeTypes={edgeTypes}
                onEdgeClick={onEdgeClick}
                onEdgeMouseEnter={(_, edge) => {
                  const d = edge.data as { from: string; to: string }
                  setHoveredEdge({ from: d.from, to: d.to })
                }}
                onEdgeMouseLeave={() => setHoveredEdge(null)}
                onNodeClick={onNodeClick}
                onNodeDragStop={onNodeDragStop}
                onConnect={onConnect}
                isValidConnection={isValidConnection}
                fitView
                proOptions={{ hideAttribution: true }}
                nodesDraggable={true}
                nodesConnectable={true}
                connectOnClick={true}
                connectionRadius={60}
                connectionDragThreshold={1}
                elementsSelectable={true}
                edgesFocusable={true}
                minZoom={0.3}
                maxZoom={2}
              >
                <Background color="#334155" gap={20} size={0.5} />
                <Controls showInteractive={false} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', borderRadius: '6px' }} />
              </ReactFlow>
            </div>
            <div style={{ padding: '8px 12px', borderTop: '1px solid var(--border-subtle)', display: 'flex', gap: '1.5rem', fontSize: '0.75rem', color: 'var(--text-muted)' }}>
              <span>━ active dispatch</span>
              <span>╌ wired (can_dispatch)</span>
              <span style={{ display: 'inline-flex', alignItems: 'center', gap: 3 }}>
                <span style={{ display: 'inline-block', width: 12, height: 12, borderRadius: '50%', background: 'var(--btn-primary-bg)', color: '#fff', fontSize: '8px', lineHeight: '12px', textAlign: 'center', fontWeight: 700 }}>D</span>
                dispatchable
              </span>
            </div>
          </Card>
      )}
    </div>
  )
}
