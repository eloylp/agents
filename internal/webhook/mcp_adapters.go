package webhook

import (
	"github.com/eloylp/agents/internal/config"
	"github.com/eloylp/agents/internal/mcp"
)

// UpdateAgentPatch adapts an mcp.AgentPatch to the webhook-internal
// storeAgentPatchJSON and delegates to UpdateAgent. Implements the
// mcp.AgentWriter contract so the MCP server and REST PATCH /agents/{name}
// share the same store-mutation path.
func (s *Server) UpdateAgentPatch(name string, patch mcp.AgentPatch) (config.AgentDef, error) {
	return s.UpdateAgent(name, storeAgentPatchJSON{
		Backend:       patch.Backend,
		Model:         patch.Model,
		Skills:        patch.Skills,
		Prompt:        patch.Prompt,
		AllowPRs:      patch.AllowPRs,
		AllowDispatch: patch.AllowDispatch,
		CanDispatch:   patch.CanDispatch,
		Description:   patch.Description,
		AllowMemory:   patch.AllowMemory,
	})
}

// UpdateSkillPatch adapts an mcp.SkillPatch to storeSkillPatchJSON and
// delegates to UpdateSkill. Implements mcp.SkillWriter.
func (s *Server) UpdateSkillPatch(name string, patch mcp.SkillPatch) (string, config.SkillDef, error) {
	return s.UpdateSkill(name, storeSkillPatchJSON{Prompt: patch.Prompt})
}

// UpdateBackendPatch adapts an mcp.BackendPatch to storeBackendPatchJSON and
// delegates to UpdateBackend. Implements mcp.BackendWriter.
func (s *Server) UpdateBackendPatch(name string, patch mcp.BackendPatch) (string, config.AIBackendConfig, error) {
	return s.UpdateBackend(name, storeBackendPatchJSON{
		Command:          patch.Command,
		Version:          patch.Version,
		Models:           patch.Models,
		Healthy:          patch.Healthy,
		HealthDetail:     patch.HealthDetail,
		LocalModelURL:    patch.LocalModelURL,
		TimeoutSeconds:   patch.TimeoutSeconds,
		MaxPromptChars:   patch.MaxPromptChars,
		RedactionSaltEnv: patch.RedactionSaltEnv,
	})
}
