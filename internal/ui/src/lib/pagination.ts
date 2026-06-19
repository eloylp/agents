export interface PaginatedResponse<T> {
  items: T[]
  total: number
  limit: number
  offset: number
}

export const selectorLimit = 500

export function selectorURL(path: string): string {
  const separator = path.includes('?') ? '&' : '?'
  return `${path}${separator}limit=${selectorLimit}&offset=0`
}

export function itemsFromResponse<T>(value: unknown): T[] {
	if (!value) return []
	if (Array.isArray(value)) return value as T[]
	const page = value as Partial<PaginatedResponse<T>>
	return page.items ?? []
}

export function pageFromResponse<T>(value: unknown, fallbackLimit: number, fallbackOffset: number): PaginatedResponse<T> {
  if (Array.isArray(value)) {
    return { items: value as T[], total: value.length, limit: fallbackLimit, offset: fallbackOffset }
  }
  const page = value as Partial<PaginatedResponse<T>>
  const items = page?.items ?? []
  return {
    items,
    total: page?.total ?? items.length,
    limit: page?.limit ?? fallbackLimit,
    offset: page?.offset ?? fallbackOffset,
  }
}
