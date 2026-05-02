export interface Binding {
  id?: number
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

function arrEq(a?: string[], b?: string[]): boolean {
  const aa = a ?? []
  const bb = b ?? []
  if (aa.length !== bb.length) return false
  for (let i = 0; i < aa.length; i++) if (aa[i] !== bb[i]) return false
  return true
}

// bindingsEqual compares two bindings by their user-editable fields. The id
// field is intentionally excluded since equality drives the diff that decides
// whether to emit a PATCH, a PATCH with identical fields is a no-op so
// skipping them keeps the save flow lean. Enabled normalises undefined/true
// into a single state so a new binding isn't flagged as "changed" on every
// save.
export function bindingsEqual(a: Binding, b: Binding): boolean {
  if (a.agent !== b.agent) return false
  if (!arrEq(a.labels, b.labels)) return false
  if (!arrEq(a.events, b.events)) return false
  if ((a.cron ?? '') !== (b.cron ?? '')) return false
  const ea = a.enabled !== false
  const eb = b.enabled !== false
  return ea === eb
}
