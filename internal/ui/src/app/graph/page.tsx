'use client'
import { useState, useEffect, useRef } from 'react'
import Card from '@/components/Card'

interface DispatchRecord {
  at: string
  repo: string
  number: number
  reason: string
}

interface Edge {
  from: string
  to: string
  count: number
  dispatches: DispatchRecord[]
}

interface Node {
  id: string
  status?: string // "idle" | "running" | "error" — last known runtime state
}

interface Graph {
  nodes: Node[]
  edges: Edge[]
}

// Simple SVG-based force-layout approximation using a fixed-iteration spring layout
function useLayout(nodes: Node[], edges: Edge[]) {
  const [positions, setPositions] = useState<Record<string, { x: number; y: number }>>({})

  useEffect(() => {
    if (nodes.length === 0) { setPositions({}); return }

    const W = 600, H = 400
    // Initialize positions on a circle
    const pos: Record<string, { x: number; y: number }> = {}
    nodes.forEach((n, i) => {
      const angle = (2 * Math.PI * i) / nodes.length
      pos[n.id] = { x: W / 2 + (W / 3) * Math.cos(angle), y: H / 2 + (H / 3) * Math.sin(angle) }
    })

    // Simple spring iterations
    for (let iter = 0; iter < 100; iter++) {
      const forces: Record<string, { fx: number; fy: number }> = {}
      nodes.forEach(n => { forces[n.id] = { fx: 0, fy: 0 } })

      // Repulsion
      for (let i = 0; i < nodes.length; i++) {
        for (let j = i + 1; j < nodes.length; j++) {
          const a = nodes[i].id, b = nodes[j].id
          const dx = pos[a].x - pos[b].x, dy = pos[a].y - pos[b].y
          const dist = Math.sqrt(dx * dx + dy * dy) || 1
          const force = 4000 / (dist * dist)
          forces[a].fx += (dx / dist) * force
          forces[a].fy += (dy / dist) * force
          forces[b].fx -= (dx / dist) * force
          forces[b].fy -= (dy / dist) * force
        }
      }

      // Attraction along edges
      edges.forEach(e => {
        const a = e.from, b = e.to
        if (!pos[a] || !pos[b]) return
        const dx = pos[b].x - pos[a].x, dy = pos[b].y - pos[a].y
        const dist = Math.sqrt(dx * dx + dy * dy) || 1
        const spring = (dist - 120) * 0.05
        forces[a].fx += (dx / dist) * spring
        forces[a].fy += (dy / dist) * spring
        forces[b].fx -= (dx / dist) * spring
        forces[b].fy -= (dy / dist) * spring
      })

      // Apply forces
      nodes.forEach(n => {
        pos[n.id].x = Math.max(40, Math.min(W - 40, pos[n.id].x + forces[n.id].fx * 0.1))
        pos[n.id].y = Math.max(40, Math.min(H - 40, pos[n.id].y + forces[n.id].fy * 0.1))
      })
    }

    setPositions({ ...pos })
  }, [nodes.length, edges.length]) // eslint-disable-line react-hooks/exhaustive-deps

  return positions
}

export default function GraphPage() {
  const [graph, setGraph] = useState<Graph>({ nodes: [], edges: [] })
  const [selectedEdge, setSelectedEdge] = useState<Edge | null>(null)
  const [loading, setLoading] = useState(true)

  const load = () => {
    setLoading(true)
    fetch('/api/graph')
      .then(r => r.json())
      .then(data => { setGraph(data); setLoading(false) })
      .catch(() => setLoading(false))
  }

  useEffect(() => { load() }, [])

  const positions = useLayout(graph.nodes, graph.edges)
  const W = 600, H = 400
  const maxCount = Math.max(...graph.edges.map(e => e.count), 1)

  return (
    <div>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', marginBottom: '1.5rem' }}>
        <div>
          <h1 style={{ fontSize: '1.4rem', fontWeight: 700, color: '#f1f5f9' }}>Agent Interaction Graph</h1>
          <p style={{ color: '#64748b', fontSize: '0.875rem', marginTop: '4px' }}>
            {graph.nodes.length} nodes · {graph.edges.length} edges (dispatches in current window)
          </p>
        </div>
        <button onClick={load} style={{ background: '#1e293b', border: '1px solid #334155', color: '#94a3b8', padding: '6px 12px', borderRadius: '6px', cursor: 'pointer', fontSize: '0.875rem' }}>
          Refresh
        </button>
      </div>

      {loading && <p style={{ color: '#64748b' }}>Loading…</p>}

      {!loading && graph.nodes.length === 0 && (
        <p style={{ color: '#64748b' }}>No dispatches recorded yet. Edges appear when agents invoke each other.</p>
      )}

      {!loading && graph.nodes.length > 0 && (
        <div style={{ display: 'grid', gridTemplateColumns: '1fr 320px', gap: '1rem' }}>
          <Card>
            <svg viewBox={`0 0 ${W} ${H}`} style={{ width: '100%', background: '#0f172a', borderRadius: '6px' }}>
              <defs>
                <marker id="arrow" markerWidth="8" markerHeight="6" refX="8" refY="3" orient="auto">
                  <polygon points="0 0, 8 3, 0 6" fill="#60a5fa" />
                </marker>
              </defs>

              {/* Edges */}
              {graph.edges.map((e, i) => {
                const a = positions[e.from], b = positions[e.to]
                if (!a || !b) return null
                const dx = b.x - a.x, dy = b.y - a.y
                const len = Math.sqrt(dx * dx + dy * dy) || 1
                const ex = b.x - (dx / len) * 22
                const ey = b.y - (dy / len) * 22
                const opacity = 0.3 + 0.7 * (e.count / maxCount)
                return (
                  <line
                    key={i}
                    x1={a.x} y1={a.y} x2={ex} y2={ey}
                    stroke={selectedEdge?.from === e.from && selectedEdge?.to === e.to ? '#f59e0b' : '#60a5fa'}
                    strokeWidth={1 + 2 * (e.count / maxCount)}
                    strokeOpacity={opacity}
                    markerEnd="url(#arrow)"
                    style={{ cursor: 'pointer' }}
                    onClick={() => setSelectedEdge(e)}
                  />
                )
              })}

              {/* Edge count labels */}
              {graph.edges.map((e, i) => {
                const a = positions[e.from], b = positions[e.to]
                if (!a || !b) return null
                const mx = (a.x + b.x) / 2, my = (a.y + b.y) / 2
                return (
                  <text key={i} x={mx} y={my} textAnchor="middle" fontSize="11" fill="#64748b">{e.count}</text>
                )
              })}

              {/* Nodes — coloured by runtime state */}
              {graph.nodes.map(n => {
                const p = positions[n.id]
                if (!p) return null
                const stroke = n.status === 'running' ? '#22c55e'
                             : n.status === 'error'   ? '#ef4444'
                             : '#60a5fa' // idle / unknown
                const fill = n.status === 'running' ? '#14532d'
                           : n.status === 'error'   ? '#450a0a'
                           : '#1e293b'
                return (
                  <g key={n.id} transform={`translate(${p.x},${p.y})`}>
                    <circle r={18} fill={fill} stroke={stroke} strokeWidth={1.5} />
                    <text textAnchor="middle" dy="0.35em" fontSize="11" fill="#e2e8f0"
                      style={{ overflow: 'hidden' }}>
                      {n.id.length > 8 ? n.id.slice(0, 7) + '…' : n.id}
                    </text>
                  </g>
                )
              })}
            </svg>
          </Card>

          <Card title={selectedEdge ? `${selectedEdge.from} → ${selectedEdge.to}` : 'Click an edge'}>
            {!selectedEdge && <p style={{ color: '#64748b', fontSize: '0.875rem' }}>Select an edge to see dispatch history.</p>}
            {selectedEdge && (
              <div>
                <div style={{ color: '#64748b', fontSize: '0.8rem', marginBottom: '0.75rem' }}>
                  {selectedEdge.count} dispatch{selectedEdge.count !== 1 ? 'es' : ''}
                </div>
                <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', maxHeight: '320px', overflowY: 'auto' }}>
                  {selectedEdge.dispatches.slice().reverse().map((d, i) => (
                    <div key={i} style={{ background: '#0f172a', borderRadius: '4px', padding: '8px', fontSize: '0.78rem' }}>
                      <div style={{ color: '#94a3b8' }}>{new Date(d.at).toLocaleString()}</div>
                      <div style={{ color: '#64748b' }}>{d.repo} #{d.number}</div>
                      {d.reason && <div style={{ color: '#475569', fontStyle: 'italic', marginTop: '2px' }}>{d.reason}</div>}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </Card>
        </div>
      )}
    </div>
  )
}
