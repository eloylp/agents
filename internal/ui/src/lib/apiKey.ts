const STORAGE_KEY = 'agents_api_key'

export function getApiKey(): string {
  if (typeof window === 'undefined') return ''
  return window.localStorage.getItem(STORAGE_KEY) ?? ''
}

export function setApiKey(key: string): void {
  if (typeof window === 'undefined') return
  if (key) {
    window.localStorage.setItem(STORAGE_KEY, key)
  } else {
    window.localStorage.removeItem(STORAGE_KEY)
  }
}

// authHeaders returns Authorization header when an API key is stored.
// Merge into the headers of any mutating fetch() call.
export function authHeaders(extra?: Record<string, string>): Record<string, string> {
  const key = getApiKey()
  const base: Record<string, string> = key ? { Authorization: `Bearer ${key}` } : {}
  return extra ? { ...base, ...extra } : base
}
