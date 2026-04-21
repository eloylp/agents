'use client'
import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import {
  ReactFlow,
  Background,
  Controls,
  MarkerType,
  Position,
  type Node,
  type Edge,
  type NodeProps,
  Handle,
} from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import dagre from 'dagre'
import Card from '@/components/Card'

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
  name: string
  current_status: string
  description?: string
  can_dispatch?: string[]
  allow_dispatch?: boolean
  skills?: string[]
}

// Custom agent node
function AgentNode({ data }: NodeProps) {
  const d = data as { label: string; status: string; description: string; dispatchable: boolean; skills: string[] }
  const statusColors: Record<string, { bg: string; border: string; icon: string }> = {
    running: { bg: 'var(--success-bg)', border: 'var(--success)', icon: '⚡' },
    error:   { bg: 'var(--error-bg)', border: 'var(--text-danger)', icon: '⚠' },
    idle:    { bg: 'var(--accent-bg)', border: 'var(--accent)', icon: '●' },
  }
  const c = statusColors[d.status] ?? statusColors.idle

  return (
    <>
      <Handle type="target" position={Position.Top} style={{ background: 'var(--text-faint)', border: 'none', width: 6, height: 6 }} />
      <div style={{
        background: 'var(--bg-card)',
        border: `2px solid ${c.border}`,
        borderRadius: '12px',
        padding: '10px 20px',
        minWidth: '180px',
        textAlign: 'center',
        boxShadow: '0 2px 8px rgba(0,0,0,0.3)',
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
          <div style={{ fontSize: '9px', color: 'var(--text-faint)', marginTop: '4px' }}>
            {d.skills.join(' · ')}
          </div>
        )}
      </div>
      <Handle type="source" position={Position.Bottom} style={{ background: 'var(--text-faint)', border: 'none', width: 6, height: 6 }} />
    </>
  )
}

const nodeTypes = { agent: AgentNode }

function layoutGraph(nodes: Node[], edges: Edge[]): Node[] {
  const g = new dagre.graphlib.Graph()
  g.setDefaultEdgeLabel(() => ({}))
  g.setGraph({ rankdir: 'TB', ranksep: 90, nodesep: 40 })

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
  const [selectedEdge, setSelectedEdge] = useState<{ from: string; to: string; count: number; dispatches: DispatchRecord[]; isActive: boolean } | null>(null)
  const [selectedNode, setSelectedNode] = useState<AgentInfo | null>(null)
  const [loading, setLoading] = useState(true)

  const loadedOnce = useRef(false)
  const load = useCallback(() => {
    if (!loadedOnce.current) setLoading(true)
    loadedOnce.current = true
    Promise.all([
      fetch('/graph').then(r => r.json()),
      fetch('/agents').then(r => r.json()),
    ]).then(([gd, ad]) => {
      setGraphData(gd)
      setAgents(ad)
      setLoading(false)
    }).catch(() => setLoading(false))
  }, [])

  useEffect(() => {
    load()
    const interval = setInterval(load, 5000)
    return () => clearInterval(interval)
  }, [load])

  const activeEdgeMap = useMemo(() => {
    const m = new Map<string, GraphEdge>()
    graphData.edges.forEach(e => m.set(`${e.from}->${e.to}`, e))
    return m
  }, [graphData.edges])

  const { flowNodes, flowEdges, wiringInfo } = useMemo(() => {
    // Build combined edge set
    const allEdges: Array<{ from: string; to: string; isActive: boolean; count: number; dispatches: DispatchRecord[] }> = []
    const seen = new Set<string>()

    agents.forEach(a => {
      (a.can_dispatch ?? []).forEach(target => {
        const key = `${a.name}->${target}`
        seen.add(key)
        const active = activeEdgeMap.get(key)
        allEdges.push({ from: a.name, to: target, isActive: !!active, count: active?.count ?? 0, dispatches: active?.dispatches ?? [] })
      })
    })

    graphData.edges.forEach(e => {
      const key = `${e.from}->${e.to}`
      if (!seen.has(key)) {
        allEdges.push({ from: e.from, to: e.to, isActive: true, count: e.count, dispatches: e.dispatches })
      }
    })

    // Build nodes from all agents
    const nodes: Node[] = agents.map(a => ({
      id: a.name,
      type: 'agent',
      position: { x: 0, y: 0 },
      data: {
        label: a.name,
        status: a.current_status ?? 'idle',
        description: a.description ?? '',
        dispatchable: a.allow_dispatch ?? false,
        skills: a.skills ?? [],
      },
    }))

    // Build edges
    const edges: Edge[] = allEdges.map((e, i) => ({
      id: `e-${i}`,
      source: e.from,
      target: e.to,
      type: 'default',
      selectable: true,
      animated: e.isActive && e.count > 0,
      interactionWidth: 40,
      style: {
        stroke: e.isActive ? 'var(--accent)' : 'var(--border)',
        strokeWidth: e.isActive ? 2.5 : 1.5,
        strokeDasharray: e.isActive ? undefined : '6 4',
        cursor: 'pointer',
      },
      markerEnd: {
        type: MarkerType.ArrowClosed,
        color: e.isActive ? '#38bdf8' : '#1e3a5f',
        width: 16,
        height: 12,
      },
      label: e.count > 0 ? `${e.count}` : undefined,
      labelStyle: { fill: '#38bdf8', fontWeight: 700, fontSize: 11 },
      labelBgStyle: { fill: '#111d2e', fillOpacity: 0.9 },
      labelBgPadding: [4, 4] as [number, number],
      labelBgBorderRadius: 4,
      data: e,
    }))

    const laid = layoutGraph(nodes, edges)

    return {
      flowNodes: laid,
      flowEdges: edges,
      wiringInfo: { active: allEdges.filter(e => e.isActive).length, total: allEdges.length },
    }
  }, [agents, graphData.edges, activeEdgeMap])

  const onEdgeClick = useCallback((_: React.MouseEvent, edge: Edge) => {
    const d = edge.data as { from: string; to: string; isActive: boolean; count: number; dispatches: DispatchRecord[] }
    setSelectedEdge(d)
    setSelectedNode(null)
  }, [])

  const onNodeClick = useCallback((_: React.MouseEvent, node: Node) => {
    const agent = agents.find(a => a.name === node.id)
    if (agent) {
      setSelectedNode(agent)
      setSelectedEdge(null)
    }
  }, [agents])

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: 'var(--text-heading)' }}>Agent Interaction Graph</h1>
          <p style={{ color: 'var(--text-muted)', fontSize: '0.875rem', marginTop: '4px' }}>
            {agents.length} agents · {wiringInfo.active} active / {wiringInfo.total} wired edges
          </p>
        </div>
        <button onClick={load} style={{ background: 'var(--bg-card)', border: '1px solid var(--border)', color: 'var(--accent)', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem', fontWeight: 500 }}>
          Refresh
        </button>
      </div>

      {loading && <p style={{ color: 'var(--text-muted)' }}>Loading...</p>}

      {/* Modal for edge details */}
      {selectedEdge && (
        <div
          onClick={() => setSelectedEdge(null)}
          style={{
            position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
            background: 'var(--bg-modal-overlay)', zIndex: 1000,
            display: 'flex', alignItems: 'center', justifyContent: 'center',
          }}
        >
          <div onClick={e => e.stopPropagation()} style={{
            background: 'var(--bg-card)', borderRadius: '12px', padding: '1.5rem',
            maxWidth: '480px', width: '90%', maxHeight: '80vh', overflowY: 'auto',
            boxShadow: '0 8px 32px rgba(0,0,0,0.4)', border: '1px solid var(--border)',
          }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--text-heading)' }}>{selectedEdge.from} → {selectedEdge.to}</h2>
              <button onClick={() => setSelectedEdge(null)} style={{
                background: 'none', border: 'none', fontSize: '1.2rem', cursor: 'pointer', color: 'var(--text-faint)',
              }}>x</button>
            </div>
            <div style={{
              display: 'inline-block', padding: '2px 8px', borderRadius: '999px', fontSize: '0.75rem', fontWeight: 500,
              background: selectedEdge.isActive ? 'var(--accent-bg)' : 'rgba(100,116,139,0.15)',
              color: selectedEdge.isActive ? 'var(--accent)' : 'var(--text-muted)',
              border: `1px solid ${selectedEdge.isActive ? 'var(--btn-primary-border)' : 'var(--border-subtle)'}`,
              marginBottom: '1rem',
            }}>
              {selectedEdge.isActive ? `${selectedEdge.count} dispatch${selectedEdge.count !== 1 ? 'es' : ''}` : 'wired — no dispatches yet'}
            </div>
            {selectedEdge.dispatches.length > 0 && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem' }}>
                {selectedEdge.dispatches.slice().reverse().map((d, i) => (
                  <div key={i} style={{ background: 'var(--bg)', borderRadius: '6px', padding: '10px', fontSize: '0.8rem', border: '1px solid var(--border-subtle)' }}>
                    <div style={{ color: 'var(--text)', fontWeight: 500 }}>{new Date(d.at).toLocaleString()}</div>
                    <div style={{ color: 'var(--text-muted)' }}>{d.repo} #{d.number}</div>
                    {d.reason && <div style={{ color: 'var(--text-faint)', fontStyle: 'italic', marginTop: '4px' }}>{d.reason}</div>}
                  </div>
                ))}
              </div>
            )}
          </div>
        </div>
      )}

      {/* Modal for node (agent) details */}
      {selectedNode && (
        <div
          onClick={() => setSelectedNode(null)}
          style={{
            position: 'fixed', top: 0, left: 0, right: 0, bottom: 0,
            background: 'var(--bg-modal-overlay)', zIndex: 1000,
            display: 'flex', alignItems: 'center', justifyContent: 'center',
          }}
        >
          <div onClick={e => e.stopPropagation()} style={{
            background: 'var(--bg-card)', borderRadius: '12px', padding: '1.5rem',
            maxWidth: '480px', width: '90%', maxHeight: '80vh', overflowY: 'auto',
            boxShadow: '0 8px 32px rgba(0,0,0,0.4)', border: '1px solid var(--border)',
          }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1rem' }}>
              <h2 style={{ fontSize: '1.1rem', fontWeight: 700, color: 'var(--text-heading)' }}>{selectedNode.name}</h2>
              <button onClick={() => setSelectedNode(null)} style={{ background: 'none', border: 'none', fontSize: '1.2rem', cursor: 'pointer', color: 'var(--text-faint)' }}>x</button>
            </div>
            {selectedNode.description && (
              <p style={{ color: 'var(--text-faint)', fontSize: '0.875rem', marginBottom: '1rem' }}>{selectedNode.description}</p>
            )}
            <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '1rem' }}>
              <span style={{ background: 'var(--accent-bg)', border: '1px solid var(--btn-primary-border)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--accent)' }}>
                {selectedNode.current_status}
              </span>
              {selectedNode.allow_dispatch && (
                <span style={{ background: 'var(--accent-bg)', border: '1px solid var(--btn-primary-border)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--accent)' }}>dispatchable</span>
              )}
            </div>
            {(selectedNode.skills ?? []).length > 0 && (
              <div style={{ marginBottom: '1rem' }}>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.25rem' }}>Skills</div>
                <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                  {selectedNode.skills!.map(s => (
                    <span key={s} style={{ background: 'rgba(100,116,139,0.15)', border: '1px solid var(--border-subtle)', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: 'var(--text-faint)' }}>{s}</span>
                  ))}
                </div>
              </div>
            )}
            {(selectedNode.can_dispatch ?? []).length > 0 && (
              <div>
                <div style={{ fontSize: '0.75rem', fontWeight: 600, color: 'var(--accent)', textTransform: 'uppercase', marginBottom: '0.25rem' }}>Can dispatch</div>
                <div style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap' }}>
                  {selectedNode.can_dispatch!.map(a => (
                    <span key={a} style={{ background: 'rgba(245,158,11,0.15)', border: '1px solid #78350f', borderRadius: '4px', padding: '2px 8px', fontSize: '0.75rem', color: '#fcd34d' }}>{a}</span>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      )}

      {!loading && (
        <Card style={{ padding: 0, overflow: 'hidden' }}>
          <div style={{ height: 'calc(100vh - 200px)', minHeight: '500px' }}>
              <ReactFlow
                nodes={flowNodes}
                edges={flowEdges}
                nodeTypes={nodeTypes}
                onEdgeClick={onEdgeClick}
                onNodeClick={onNodeClick}
                fitView
                proOptions={{ hideAttribution: true }}
                nodesDraggable={true}
                nodesConnectable={false}
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
