'use client'

import { getStoredBearerToken } from '@/lib/auth'

interface AuthenticatedSSEHandlers {
  onOpen?: () => void
  onMessage?: (data: string) => void
  onEvent?: (event: string, data: string) => void
  onError?: (error: Error) => void
}

interface AuthenticatedSSEConnection {
  close: () => void
}

export function openAuthenticatedSSE(url: string, handlers: AuthenticatedSSEHandlers): AuthenticatedSSEConnection {
  const controller = new AbortController()
  const headers = new Headers({ Accept: 'text/event-stream' })
  const token = getStoredBearerToken()
  if (token) headers.set('Authorization', `Bearer ${token}`)

  void readSSE(url, headers, controller.signal, handlers)
  return { close: () => controller.abort() }
}

async function readSSE(
  url: string,
  headers: Headers,
  signal: AbortSignal,
  handlers: AuthenticatedSSEHandlers,
) {
  try {
    const res = await fetch(url, { headers, cache: 'no-store', signal })
    if (!res.ok) throw new Error(`SSE ${url} returned HTTP ${res.status}`)
    if (!res.body) throw new Error(`SSE ${url} did not provide a response body`)

    handlers.onOpen?.()

    const reader = res.body.getReader()
    const decoder = new TextDecoder()
    let buffer = ''

    while (true) {
      const { value, done } = await reader.read()
      if (done) break
      buffer += decoder.decode(value, { stream: true })
      const parts = buffer.split(/\r?\n\r?\n/)
      buffer = parts.pop() ?? ''
      for (const part of parts) dispatchSSEBlock(part, handlers)
    }
    if (buffer.trim() !== '') dispatchSSEBlock(buffer, handlers)
  } catch (error) {
    if (signal.aborted) return
    handlers.onError?.(error instanceof Error ? error : new Error(String(error)))
  }
}

export function dispatchSSEBlockForTest(block: string, handlers: AuthenticatedSSEHandlers) {
  dispatchSSEBlock(block, handlers)
}

function dispatchSSEBlock(block: string, handlers: AuthenticatedSSEHandlers) {
  let event = 'message'
  const data: string[] = []

  for (const line of block.split(/\r?\n/)) {
    if (line === '' || line.startsWith(':')) continue
    if (line.startsWith('event:')) {
      event = line.slice('event:'.length).trim()
      continue
    }
    if (line.startsWith('data:')) {
      const value = line.slice('data:'.length)
      data.push(value.startsWith(' ') ? value.slice(1) : value)
    }
  }

  const payload = data.join('\n')
  if (event === 'message') {
    if (payload !== '') handlers.onMessage?.(payload)
    return
  }
  handlers.onEvent?.(event, payload)
}
