export interface RunnerTimeoutRow {
  id: number
  status: string
  error?: string
  agent?: string
  span_id?: string
}

export interface RunnerTimeoutTracker {
  initialized: boolean
  seen: Set<string>
}

export function newRunnerTimeoutTracker(): RunnerTimeoutTracker {
  return { initialized: false, seen: new Set() }
}

function runnerTimeoutKey(row: RunnerTimeoutRow): string {
  return row.span_id || `${row.id}:${row.agent || ''}`
}

function isRunnerTimeout(row: RunnerTimeoutRow): boolean {
  return row.status === 'error' && /timed out|timeout/i.test(row.error || '')
}

export function observeRunnerTimeouts<T extends RunnerTimeoutRow>(rows: T[], tracker: RunnerTimeoutTracker): T | null {
  const timeoutRows = rows.filter(isRunnerTimeout)
  if (!tracker.initialized) {
    for (const row of timeoutRows) tracker.seen.add(runnerTimeoutKey(row))
    tracker.initialized = true
    return null
  }

  const newlyObserved = timeoutRows.find(row => !tracker.seen.has(runnerTimeoutKey(row))) ?? null
  for (const row of timeoutRows) tracker.seen.add(runnerTimeoutKey(row))
  return newlyObserved
}
