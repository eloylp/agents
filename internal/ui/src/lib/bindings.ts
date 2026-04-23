export interface Binding {
  agent: string
  labels?: string[]
  events?: string[]
  cron?: string
  enabled?: boolean
}

// groupByAgent groups bindings under their agent name while preserving
// first-seen order across the input list. Hoisted out of page components
// so it can be exercised by unit tests in isolation.
export function groupByAgent(bs: Binding[]): Array<[string, Binding[]]> {
  const g: Record<string, Binding[]> = {}
  const order: string[] = []
  for (const b of bs) {
    if (!g[b.agent]) { g[b.agent] = []; order.push(b.agent) }
    g[b.agent].push(b)
  }
  return order.map(a => [a, g[a]])
}
