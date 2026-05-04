import { describe, expect, it, vi } from 'vitest'
import { dispatchSSEBlockForTest } from './sse'

describe('dispatchSSEBlockForTest', () => {
  it('dispatches message data blocks', () => {
    const onMessage = vi.fn()

    dispatchSSEBlockForTest('data: {"ok":true}', { onMessage })

    expect(onMessage).toHaveBeenCalledWith('{"ok":true}')
  })

  it('dispatches named events separately from normal messages', () => {
    const onMessage = vi.fn()
    const onEvent = vi.fn()

    dispatchSSEBlockForTest('event: end\ndata: done', { onMessage, onEvent })

    expect(onMessage).not.toHaveBeenCalled()
    expect(onEvent).toHaveBeenCalledWith('end', 'done')
  })

  it('joins multiline data payloads', () => {
    const onMessage = vi.fn()

    dispatchSSEBlockForTest('data: first\ndata: second', { onMessage })

    expect(onMessage).toHaveBeenCalledWith('first\nsecond')
  })
})
