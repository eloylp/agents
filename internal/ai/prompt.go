package ai

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type IssuePromptData struct {
	Repo   string
	Number int
}

type PRReviewPromptData struct {
	Repo            string
	Number          int
	Backend         string
	Agent           string
	AgentHeading    string
	WorkflowPartKey string
}

type AutonomousPromptData struct {
	Repo        string
	AgentName   string
	Description string
	Task        string
	Memory      string
	MemoryPath  string
}

// PromptSource holds either a file path or an inline prompt string.
type PromptSource struct {
	PromptFile string // absolute path, mutually exclusive with Prompt
	Prompt     string // inline text, mutually exclusive with PromptFile
}

// AgentGuidance holds the resolved guidance for an agent, either from
// a file path or an inline prompt string.
type AgentGuidance struct {
	Name       string
	PromptFile string // absolute path, mutually exclusive with Prompt
	Prompt     string // inline text, mutually exclusive with PromptFile
}

type PromptStore struct {
	agents        map[string]AgentGuidance
	issueTpl      *template.Template
	prTemplates   map[string]*template.Template
	autoTemplates map[string]*template.Template
}

func NewPromptStore(issueBase PromptSource, prBase PromptSource, autoBase PromptSource, agents []AgentGuidance, autonomousAgentNames []string) (*PromptStore, error) {
	agentMap := make(map[string]AgentGuidance, len(agents))
	for _, a := range agents {
		agentMap[NormalizeToken(a.Name)] = a
	}
	store := &PromptStore{
		agents:        agentMap,
		prTemplates:   make(map[string]*template.Template),
		autoTemplates: make(map[string]*template.Template),
	}
	if err := store.loadTemplates(issueBase, prBase, autoBase, agents, autonomousAgentNames); err != nil {
		return nil, err
	}
	return store, nil
}

func (p *PromptStore) IssueRefinePrompt(repo string, number int) (string, error) {
	if p.issueTpl == nil {
		return "", fmt.Errorf("issue prompt not loaded")
	}
	data := IssuePromptData{Repo: repo, Number: number}
	return executeTemplate(p.issueTpl, data, "issue refine")
}

func (p *PromptStore) PRReviewPrompt(agent string, backend string, repo string, number int) (string, error) {
	normalizedAgent := NormalizeToken(agent)
	pl, ok := p.prTemplates[normalizedAgent]
	if !ok {
		return "", fmt.Errorf("pr review prompt for agent %s not loaded", normalizedAgent)
	}
	data := PRReviewPromptData{
		Repo:            repo,
		Number:          number,
		Backend:         backend,
		Agent:           normalizedAgent,
		AgentHeading:    fmt.Sprintf("## %s specialist: %s", backend, normalizedAgent),
		WorkflowPartKey: fmt.Sprintf("%s/%s", backend, normalizedAgent),
	}
	return executeTemplate(pl, data, "pr review")
}

func (p *PromptStore) AutonomousPrompt(agent string, data AutonomousPromptData) (string, error) {
	normalizedAgent := NormalizeToken(agent)
	pl, ok := p.autoTemplates[normalizedAgent]
	if !ok {
		return "", fmt.Errorf("autonomous prompt for agent %s not loaded", normalizedAgent)
	}
	if strings.TrimSpace(data.AgentName) == "" {
		data.AgentName = normalizedAgent
	}
	return executeTemplate(pl, data, "autonomous agent")
}

func (p *PromptStore) loadTemplates(issueBase PromptSource, prBase PromptSource, autoBase PromptSource, agents []AgentGuidance, autonomousAgentNames []string) error {
	issueTpl, err := loadPromptSource(issueBase)
	if err != nil {
		return err
	}
	p.issueTpl = issueTpl

	prBaseTpl, err := loadPromptSource(prBase)
	if err != nil {
		return err
	}

	// All defined agents get a PR review template.
	p.prTemplates = make(map[string]*template.Template)
	for _, agent := range agents {
		normalized := NormalizeToken(agent.Name)
		tpl, err := p.composeAgentTemplate(prBaseTpl, normalized)
		if err != nil {
			return err
		}
		p.prTemplates[normalized] = tpl
	}

	autoBaseTpl, err := loadPromptSource(autoBase)
	if err != nil {
		return err
	}

	// Only autonomous agents get an autonomous template.
	p.autoTemplates = make(map[string]*template.Template)
	for _, name := range dedupeTokens(autonomousAgentNames) {
		normalized := NormalizeToken(name)
		tpl, err := p.composeAgentTemplate(autoBaseTpl, normalized)
		if err != nil {
			return err
		}
		p.autoTemplates[normalized] = tpl
	}

	return nil
}

// composeAgentTemplate clones a base template and appends the agent's guidance,
// either from a file or from an inline prompt string.
func (p *PromptStore) composeAgentTemplate(baseTpl *template.Template, agent string) (*template.Template, error) {
	ag, ok := p.agents[agent]
	if !ok {
		return nil, fmt.Errorf("no guidance defined for agent %q", agent)
	}
	clone, err := baseTpl.Clone()
	if err != nil {
		return nil, fmt.Errorf("clone base template for agent %q: %w", agent, err)
	}
	if ag.PromptFile != "" {
		content, err := os.ReadFile(ag.PromptFile)
		if err != nil {
			return nil, fmt.Errorf("load guidance %s: %w", ag.PromptFile, err)
		}
		clone, err = clone.Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("parse guidance %s: %w", ag.PromptFile, err)
		}
		return clone, nil
	}
	inlineBlock := fmt.Sprintf("{{define \"agent_guidance\"}}%s{{end}}", ag.Prompt)
	clone, err = clone.Parse(inlineBlock)
	if err != nil {
		return nil, fmt.Errorf("parse inline guidance for agent %q: %w", agent, err)
	}
	return clone, nil
}

// loadPromptSource parses a template from either a file path or inline string.
func loadPromptSource(src PromptSource) (*template.Template, error) {
	if src.PromptFile != "" {
		content, err := os.ReadFile(src.PromptFile)
		if err != nil {
			return nil, fmt.Errorf("load prompt %s: %w", src.PromptFile, err)
		}
		tpl, err := template.New(filepath.Base(src.PromptFile)).Option("missingkey=error").Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("parse prompt %s: %w", src.PromptFile, err)
		}
		return tpl, nil
	}
	if src.Prompt != "" {
		tpl, err := template.New("inline").Option("missingkey=error").Parse(src.Prompt)
		if err != nil {
			return nil, fmt.Errorf("parse inline prompt: %w", err)
		}
		return tpl, nil
	}
	return nil, fmt.Errorf("prompt source has neither file nor inline content")
}

func executeTemplate(tpl *template.Template, data any, name string) (string, error) {
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s prompt: %w", name, err)
	}
	return buf.String(), nil
}

// NormalizeToken canonicalises user-provided agent or backend identifiers so
// they can be safely used as map keys and filesystem fragments.
func NormalizeToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	token = filepath.Clean(token)
	token = strings.TrimLeft(token, string(filepath.Separator)+".")
	token = strings.ReplaceAll(token, "..", "_")
	token = strings.ReplaceAll(token, string(filepath.Separator), "_")
	token = strings.ReplaceAll(token, "\\", "_")
	token = strings.ReplaceAll(token, "\x00", "_")
	return token
}

func dedupeTokens(tokens []string) []string {
	seen := make(map[string]struct{})
	unique := make([]string, 0, len(tokens))
	for _, token := range tokens {
		normalized := NormalizeToken(token)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		unique = append(unique, normalized)
	}
	return unique
}
