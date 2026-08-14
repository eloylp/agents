export interface CatalogDelegationConfig {
  enabled?: boolean
  repo?: string
  branch?: string
  catalog_path?: string
  last_synced_commit?: string
  last_successful_sync_at?: string
  last_sync_status?: string
  last_sync_error?: string
  disabled_at?: string
  credential_status?: string
}

export function configCatalogDelegation(config: Record<string, unknown> | null): CatalogDelegationConfig | null {
  const catalog = config?.catalog as { delegation?: CatalogDelegationConfig } | undefined
  return catalog?.delegation ?? null
}

export function isCatalogDelegated(config: Record<string, unknown> | null): boolean {
  return configCatalogDelegation(config)?.enabled === true
}

export function isGlobalCatalogAsset(item: { workspace_id?: string; repo?: string }): boolean {
  return !item.workspace_id && !item.repo
}
