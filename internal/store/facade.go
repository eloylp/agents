package store

import (
	"database/sql"
	"time"

	"github.com/rs/zerolog"

	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/fleet"
)

// Store is the central data-access facade used throughout the daemon.
// Every component that needs to read or write fleet entities holds a
// *Store; the bare *sql.DB only appears inside this package and at
// daemon.LoadConfig where the connection is opened.
//
// The methods below are thin delegates over the package-level functions
// (ReadAgents, UpsertAgent, …). The package-level functions remain so
// internal helpers and test fixtures can keep using them without going
// through the facade.

// Store wraps a SQLite handle behind a typed surface.
type Store struct {
	db *sql.DB
}

// New wraps the supplied *sql.DB. The caller owns and closes the
// connection; Store only borrows it.
func New(db *sql.DB) *Store {
	return &Store{db: db}
}

// DB returns the underlying handle. Reserved for migrations and the
// occasional administrative script; production callers use the typed
// methods.
func (s *Store) DB() *sql.DB { return s.db }

// ── Agents ──────────────────────────────────────────────────────────────

func (s *Store) ReadAgents() ([]fleet.Agent, error)   { return ReadAgents(s.db) }
func (s *Store) UpsertAgent(a fleet.Agent) error      { return UpsertAgent(s.db, a) }
func (s *Store) DeleteAgent(name string) error        { return DeleteAgent(s.db, name) }
func (s *Store) DeleteAgentCascade(name string) error { return DeleteAgentCascade(s.db, name) }
func (s *Store) DeleteWorkspaceAgent(workspace, name string) error {
	return DeleteWorkspaceAgent(s.db, workspace, name)
}
func (s *Store) DeleteWorkspaceAgentCascade(workspace, name string) error {
	return DeleteWorkspaceAgentCascade(s.db, workspace, name)
}
func (s *Store) ReadGraphLayout() ([]GraphNodePosition, error) {
	return ReadGraphLayout(s.db)
}
func (s *Store) ReadWorkspaceGraphLayout(workspace string) ([]GraphNodePosition, error) {
	return ReadWorkspaceGraphLayout(s.db, workspace)
}
func (s *Store) UpsertGraphLayout(positions []GraphNodePosition) error {
	return UpsertGraphLayout(s.db, positions)
}
func (s *Store) UpsertWorkspaceGraphLayout(workspace string, positions []GraphNodePosition) error {
	return UpsertWorkspaceGraphLayout(s.db, workspace, positions)
}
func (s *Store) ClearGraphLayout() error { return ClearGraphLayout(s.db) }
func (s *Store) ClearWorkspaceGraphLayout(workspace string) error {
	return ClearWorkspaceGraphLayout(s.db, workspace)
}

// ── Workspaces and prompts ──────────────────────────────────────────────

func (s *Store) ReadWorkspaces() ([]fleet.Workspace, error) { return ReadWorkspaces(s.db) }
func (s *Store) ReadPrompts() ([]fleet.Prompt, error)       { return ReadPrompts(s.db) }
func (s *Store) ReadRuntimeSettings() (fleet.RuntimeSettings, error) {
	return ReadRuntimeSettings(s.db)
}
func (s *Store) WriteRuntimeSettings(settings fleet.RuntimeSettings) (fleet.RuntimeSettings, error) {
	return WriteRuntimeSettings(s.db, settings)
}
func (s *Store) ReadWorkspace(workspace string) (fleet.Workspace, error) {
	return ReadWorkspace(s.db, workspace)
}
func (s *Store) UpsertWorkspace(w fleet.Workspace) (fleet.Workspace, error) {
	return UpsertWorkspace(s.db, w)
}
func (s *Store) DeleteWorkspace(workspace string) error { return DeleteWorkspace(s.db, workspace) }
func (s *Store) SetWorkspaceRunnerImage(workspace, image string) (fleet.Workspace, error) {
	return SetWorkspaceRunnerImage(s.db, workspace, image)
}
func (s *Store) ReadWorkspaceGuardrails(workspace string) ([]fleet.WorkspaceGuardrailRef, error) {
	return ReadWorkspaceGuardrails(s.db, workspace)
}
func (s *Store) ReplaceWorkspaceGuardrails(workspace string, refs []fleet.WorkspaceGuardrailRef) ([]fleet.WorkspaceGuardrailRef, error) {
	return ReplaceWorkspaceGuardrails(s.db, workspace, refs)
}
func (s *Store) UpsertPrompt(p fleet.Prompt) (fleet.Prompt, error) {
	return UpsertPrompt(s.db, p)
}
func (s *Store) ReadPrompt(ref string) (fleet.Prompt, error) { return ReadPrompt(s.db, ref) }
func (s *Store) DeletePrompt(ref string) error               { return DeletePrompt(s.db, ref) }

// ── Skills ──────────────────────────────────────────────────────────────

func (s *Store) ReadSkills() (map[string]fleet.Skill, error)   { return ReadSkills(s.db) }
func (s *Store) UpsertSkill(name string, sk fleet.Skill) error { return UpsertSkill(s.db, name, sk) }
func (s *Store) DeleteSkill(name string) error                 { return DeleteSkill(s.db, name) }

// ── Backends ────────────────────────────────────────────────────────────

func (s *Store) ReadBackends() (map[string]fleet.Backend, error) { return ReadBackends(s.db) }
func (s *Store) UpsertBackend(name string, b fleet.Backend) error {
	return UpsertBackend(s.db, name, b)
}
func (s *Store) DeleteBackend(name string) error { return DeleteBackend(s.db, name) }

// ── Repos and bindings ──────────────────────────────────────────────────

func (s *Store) ReadRepos() ([]fleet.Repo, error) { return ReadRepos(s.db) }
func (s *Store) UpsertRepo(r fleet.Repo) error    { return UpsertRepo(s.db, r) }
func (s *Store) DeleteRepo(name string) error     { return DeleteRepo(s.db, name) }
func (s *Store) DeleteWorkspaceRepo(workspace, name string) error {
	return DeleteWorkspaceRepo(s.db, workspace, name)
}

func (s *Store) CreateBinding(repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	return CreateBinding(s.db, repoName, b)
}
func (s *Store) CreateWorkspaceBinding(workspace, repoName string, b fleet.Binding) (int64, fleet.Binding, error) {
	return CreateWorkspaceBinding(s.db, workspace, repoName, b)
}
func (s *Store) UpdateBinding(id int64, b fleet.Binding) (fleet.Binding, error) {
	return UpdateBinding(s.db, id, b)
}
func (s *Store) DeleteBinding(id int64) error { return DeleteBinding(s.db, id) }
func (s *Store) ReadBinding(id int64) (string, fleet.Binding, bool, error) {
	return ReadBinding(s.db, id)
}
func (s *Store) ReadWorkspaceBinding(id int64) (string, string, fleet.Binding, bool, error) {
	return ReadWorkspaceBinding(s.db, id)
}

// EnableRepo flips a repo's enabled flag without rewriting bindings ,
// the dedicated path that PATCH /repos/{owner}/{repo} relies on.
func (s *Store) EnableRepo(name string, enabled bool) error {
	_, err := s.db.Exec("UPDATE repos SET enabled=? WHERE workspace_id=? AND name=?", boolToInt(enabled), fleet.DefaultWorkspaceID, name)
	return err
}
func (s *Store) EnableWorkspaceRepo(workspace, name string, enabled bool) error {
	_, err := s.db.Exec("UPDATE repos SET enabled=? WHERE workspace_id=? AND name=?", boolToInt(enabled), fleet.NormalizeWorkspaceID(workspace), name)
	return err
}

// ── Guardrails ──────────────────────────────────────────────────────────

func (s *Store) ReadEnabledGuardrails() ([]fleet.Guardrail, error) {
	return ReadEnabledGuardrails(s.db)
}
func (s *Store) ReadWorkspacePromptGuardrails(workspace string) ([]fleet.Guardrail, error) {
	return ReadWorkspacePromptGuardrails(s.db, workspace)
}
func (s *Store) ReadAllGuardrails() ([]fleet.Guardrail, error)     { return ReadAllGuardrails(s.db) }
func (s *Store) GetGuardrail(name string) (fleet.Guardrail, error) { return GetGuardrail(s.db, name) }
func (s *Store) UpsertGuardrail(g fleet.Guardrail) error           { return UpsertGuardrail(s.db, g) }
func (s *Store) DeleteGuardrail(name string) error                 { return DeleteGuardrail(s.db, name) }
func (s *Store) ResetGuardrail(name string) error                  { return ResetGuardrail(s.db, name) }

// ── Memory ──────────────────────────────────────────────────────────────

// ReadMemoryRaw exposes the four-value result for the Default workspace.
// New callers should prefer ReadWorkspaceMemoryRaw.
func (s *Store) ReadMemoryRaw(agent, repo string) (string, bool, time.Time, error) {
	return ReadMemory(s.db, fleet.DefaultWorkspaceID, agent, repo)
}

// ReadWorkspaceMemoryRaw exposes the workspace-scoped four-value memory read.
func (s *Store) ReadWorkspaceMemoryRaw(workspace, agent, repo string) (string, bool, time.Time, error) {
	return ReadMemory(s.db, fleet.NormalizeWorkspaceID(workspace), agent, repo)
}

// WriteMemoryRaw exposes the raw write for the Default workspace; production
// callers use NewMemoryBackend (which also fires the SSE notifier).
func (s *Store) WriteMemoryRaw(agent, repo, content string) error {
	return WriteMemory(s.db, fleet.DefaultWorkspaceID, agent, repo, content)
}

// WriteWorkspaceMemoryRaw exposes the workspace-scoped raw write.
func (s *Store) WriteWorkspaceMemoryRaw(workspace, agent, repo, content string) error {
	return WriteMemory(s.db, fleet.NormalizeWorkspaceID(workspace), agent, repo, content)
}

// NewMemoryBackend constructs the engine-side MemoryBackend rooted in
// this store.
func (s *Store) NewMemoryBackend() *MemoryBackend { return NewMemoryBackend(s.db) }

// NewMemoryReader constructs the HTTP-side MemoryReader rooted in this
// store.
func (s *Store) NewMemoryReader() *MemoryReader { return NewMemoryReader(s.db) }

// ── Snapshots, import / export, validation ──────────────────────────────

func (s *Store) ReadSnapshot() ([]fleet.Agent, []fleet.Repo, map[string]fleet.Skill, map[string]fleet.Backend, error) {
	return ReadSnapshot(s.db)
}

func (s *Store) ImportAll(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend, guardrails []fleet.Guardrail, budgets []TokenBudget) error {
	return ImportAll(s.db, agents, repos, skills, backends, guardrails, budgets)
}

func (s *Store) ReplaceAll(agents []fleet.Agent, repos []fleet.Repo, skills map[string]fleet.Skill, backends map[string]fleet.Backend, guardrails []fleet.Guardrail, budgets []TokenBudget) error {
	return ReplaceAll(s.db, agents, repos, skills, backends, guardrails, budgets)
}

func (s *Store) ImportConfig(cfg *config.Config, budgets []TokenBudget) error {
	return ImportConfig(s.db, cfg, budgets)
}

func (s *Store) ReplaceConfig(cfg *config.Config, budgets []TokenBudget) error {
	return ReplaceConfig(s.db, cfg, budgets)
}

func (s *Store) Import(cfg *config.Config) error          { return Import(s.db, cfg) }
func (s *Store) Load() (*config.Config, error)            { return Load(s.db) }
func (s *Store) LoadAndValidate() (*config.Config, error) { return LoadAndValidate(s.db) }
func (s *Store) CountFrom() (ImportCount, error)          { return CountFrom(s.db) }

// ── Token budgets and leaderboard ────────────────────────────────────────

func (s *Store) ListTokenBudgets() ([]TokenBudget, error)     { return ListTokenBudgets(s.db) }
func (s *Store) GetTokenBudget(id int64) (TokenBudget, error) { return GetTokenBudget(s.db, id) }
func (s *Store) CreateTokenBudget(b TokenBudget) (TokenBudget, error) {
	return CreateTokenBudget(s.db, b)
}
func (s *Store) UpdateTokenBudget(id int64, b TokenBudget) (TokenBudget, error) {
	return UpdateTokenBudget(s.db, id, b)
}
func (s *Store) PatchTokenBudget(id int64, p TokenBudgetPatch) (TokenBudget, error) {
	return PatchTokenBudget(s.db, id, p)
}
func (s *Store) DeleteTokenBudget(id int64) error     { return DeleteTokenBudget(s.db, id) }
func (s *Store) BudgetAlerts() ([]BudgetAlert, error) { return BudgetAlerts(s.db) }
func (s *Store) TokenLeaderboard(workspaceID, repo, period string) ([]LeaderboardEntry, error) {
	return TokenLeaderboard(s.db, workspaceID, repo, period)
}
func (s *Store) CheckBudgets(workspaceID, repo, backend, agentName string) error {
	return CheckBudgets(s.db, workspaceID, repo, backend, agentName)
}
func (s *Store) CheckBudgetsWithLogger(workspaceID, repo, backend, agentName string, logger zerolog.Logger) error {
	return CheckBudgetsWithLogger(s.db, workspaceID, repo, backend, agentName, logger)
}
func (s *Store) ImportTokenBudgets(budgets []TokenBudget, replace bool) error {
	return ImportTokenBudgets(s.db, budgets, replace)
}

// Close closes the underlying handle. Provided so the daemon's lifecycle
// only juggles one handle.
func (s *Store) Close() error { return s.db.Close() }
