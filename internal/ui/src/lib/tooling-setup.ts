export type ToolStatus = {
  name: string
  detected?: boolean
  authenticated?: boolean
  healthy?: boolean
  detail?: string
  version?: string
}

export type BackendStatus = {
  name: string
  detected?: boolean
  healthy?: boolean
  models?: string[]
  health_detail?: string
  version?: string
}

export type BackendsDiagnostics = {
  backends?: BackendStatus[]
  tools?: ToolStatus[]
  github_cli?: ToolStatus
}

export type SetupCheck = {
  key: string
  label: string
  ok: boolean
  detail: string
  command?: string
}

export function selectedBackends(value: string | null | undefined): string[] {
  if (value === 'claude') return ['claude']
  if (value === 'codex') return ['codex']
  return ['claude', 'codex']
}

export function inferredBackends(diag: BackendsDiagnostics | null | undefined, value: string | null | undefined): string[] {
  if (value === 'claude' || value === 'codex' || value === 'both') return selectedBackends(value)
  const configured = (diag?.backends ?? [])
    .filter(backend => (backend.name === 'claude' || backend.name === 'codex') && (backend.detected || (backend.models ?? []).length > 0))
    .map(backend => backend.name)
  return configured.length > 0 ? configured : selectedBackends(value)
}

export function githubMCPConnected(backend: BackendStatus | undefined): boolean {
  return Boolean(backend?.health_detail?.toLowerCase().includes('github mcp: connected'))
}

export function setupComplete(diag: BackendsDiagnostics | null | undefined, selected: string[]): boolean {
  return buildSetupChecks(diag, selected).every(check => check.ok)
}

export function buildSetupChecks(diag: BackendsDiagnostics | null | undefined, selected: string[]): SetupCheck[] {
  // Older diagnostics exposed github_cli as a top-level singleton; newer
  // responses include it in tools so additional tool checks can share a shape.
  const tools = diag?.tools ?? (diag?.github_cli ? [diag.github_cli] : [])
  const toolByName = new Map(tools.map(tool => [tool.name, tool]))
  const backendByName = new Map((diag?.backends ?? []).map(backend => [backend.name, backend]))
  const gh = toolByName.get('github_cli') ?? diag?.github_cli

  const checks: SetupCheck[] = [
    {
      key: 'daemon',
      label: 'Daemon reachable',
      ok: Boolean(diag),
      detail: diag ? 'Diagnostics loaded from the daemon.' : 'The dashboard could not load backend diagnostics.',
    },
    {
      key: 'github_token',
      label: 'GitHub MCP credential',
      ok: selected.some(name => githubMCPConnected(backendByName.get(name))),
      detail: selected.some(name => githubMCPConnected(backendByName.get(name)))
        ? 'At least one selected backend reports GitHub MCP connected.'
        : 'Run agents-setup with GITHUB_TOKEN set so the selected backend can register GitHub MCP.',
      command: 'docker compose exec -it agents agents-setup',
    },
    {
      key: 'github_cli',
      label: 'GitHub CLI fallback',
      ok: Boolean(gh?.detected && gh.authenticated && gh.healthy),
      detail: gh?.detail || (gh?.detected ? 'gh is installed but is not authenticated.' : 'gh is not available in the daemon container.'),
      command: 'docker compose exec -it agents agents-setup',
    },
  ]

  for (const name of selected) {
    const backend = backendByName.get(name)
    const display = name === 'claude' ? 'Claude' : 'Codex'
    checks.push(
      {
        key: `${name}_installed`,
        label: `${display} installed`,
        ok: Boolean(backend?.detected),
        detail: backend?.version || backend?.health_detail || `${display} CLI was not detected in the daemon container.`,
        command: 'docker compose exec -it agents agents-setup',
      },
      {
        key: `${name}_auth`,
        label: `${display} authenticated`,
        ok: Boolean(backend?.healthy),
        detail: backend?.health_detail || `${display} CLI is not authenticated or could not run a version check.`,
        command: 'docker compose exec -it agents agents-setup',
      },
      {
        key: `${name}_mcp`,
        label: `${display} GitHub MCP`,
        ok: githubMCPConnected(backend),
        detail: backend?.health_detail || `${display} GitHub MCP is not registered or connected.`,
        command: 'docker compose exec -it agents agents-setup',
      },
      {
        key: `${name}_models`,
        label: `${display} model catalog`,
        ok: Boolean((backend?.models ?? []).length > 0),
        detail: (backend?.models ?? []).length > 0
          ? `${backend!.models!.length} model${backend!.models!.length === 1 ? '' : 's'} discovered.`
          : 'Refresh discovery after authenticating the backend.',
        command: 'docker compose exec -it agents agents-setup',
      },
    )
  }

  return checks
}
