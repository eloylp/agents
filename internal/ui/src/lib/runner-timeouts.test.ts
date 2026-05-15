import { describe, expect, it } from 'vitest'
import { newRunnerTimeoutTracker, observeRunnerTimeouts, type RunnerTimeoutRow } from './runner-timeouts'

function row(id: number, error: string, span_id?: string): RunnerTimeoutRow {
  return {
    id,
    span_id,
    status: 'error',
    error,
    agent: 'coder',
  }
}

describe('observeRunnerTimeouts', () => {
  it('primes historical timeout rows on initial load without returning a toast row', () => {
    const tracker = newRunnerTimeoutTracker()
    const got = observeRunnerTimeouts([
      row(1, 'codex command timed out after 10m0s', 'span-1'),
      row(2, 'codex command timeout', 'span-2'),
    ], tracker)

    expect(got).toBeNull()
    expect(tracker.initialized).toBe(true)
    expect(tracker.seen.has('span-1')).toBe(true)
    expect(tracker.seen.has('span-2')).toBe(true)
  })

  it('returns the first newly observed timeout after initial load and dedupes later polls', () => {
    const tracker = newRunnerTimeoutTracker()
    observeRunnerTimeouts([row(1, 'codex command timed out after 10m0s', 'span-1')], tracker)

    const first = observeRunnerTimeouts([
      row(1, 'codex command timed out after 10m0s', 'span-1'),
      row(2, 'codex command timed out after 10m0s', 'span-2'),
    ], tracker)
    expect(first?.span_id).toBe('span-2')

    const second = observeRunnerTimeouts([
      row(1, 'codex command timed out after 10m0s', 'span-1'),
      row(2, 'codex command timed out after 10m0s', 'span-2'),
    ], tracker)
    expect(second).toBeNull()
  })

  it('marks all timeout rows in a poll as seen while returning only one toast candidate', () => {
    const tracker = newRunnerTimeoutTracker()
    observeRunnerTimeouts([], tracker)

    const got = observeRunnerTimeouts([
      row(1, 'codex command timed out after 10m0s', 'span-1'),
      row(2, 'codex command timeout', 'span-2'),
    ], tracker)

    expect(got?.span_id).toBe('span-1')
    expect(observeRunnerTimeouts([
      row(1, 'codex command timed out after 10m0s', 'span-1'),
      row(2, 'codex command timeout', 'span-2'),
    ], tracker)).toBeNull()
  })
})
