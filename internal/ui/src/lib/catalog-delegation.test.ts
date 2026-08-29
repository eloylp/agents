import { describe, expect, it } from 'vitest'
import { catalogDelegationFileURL } from './catalog-delegation'

describe('catalogDelegationFileURL', () => {
  it('builds a GitHub file link from delegation metadata', () => {
    expect(catalogDelegationFileURL({
      enabled: true,
      repo: 'acme/catalog',
      branch: 'feature/delegated catalog',
      catalog_path: 'config/catalog.yml',
    })).toBe('https://github.com/acme/catalog/blob/feature/delegated%20catalog/config/catalog.yml')
  })

  it('returns null when the repository is missing', () => {
    expect(catalogDelegationFileURL({ enabled: true })).toBeNull()
  })
})
