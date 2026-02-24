package ai

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	AgentGuidance   string
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

type PromptStore struct {
	baseDir      string
	issueTpl     *template.Template
	prTemplates  map[string]*template.Template
	autoTemplate map[string]*template.Template
	mu           sync.Mutex
}

func NewPromptStore(baseDir string) (*PromptStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("prompt store base directory is required")
	}
	return &PromptStore{
		baseDir:      baseDir,
		prTemplates:  make(map[string]*template.Template),
		autoTemplate: make(map[string]*template.Template),
	}, nil
}

func (p *PromptStore) IssueRefinePrompt(repo string, number int) (string, error) {
	p.mu.Lock()
	tpl := p.issueTpl
	p.mu.Unlock()
	if tpl == nil {
		loaded, err := p.loadTemplate(filepath.Join(p.baseDir, "issue_refinement_prompts", "PROMPT.md"))
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		p.issueTpl = loaded
		tpl = loaded
		p.mu.Unlock()
	}
	data := IssuePromptData{Repo: repo, Number: number}
	return executeTemplate(tpl, data, "issue refine")
}

func (p *PromptStore) PRReviewPrompt(agent string, backend string, repo string, number int) (string, error) {
	normalizedAgent := normalizeToken(agent)
	p.mu.Lock()
	tpl := p.prTemplates[normalizedAgent]
	p.mu.Unlock()
	if tpl == nil {
		loaded, err := p.loadTemplate(filepath.Join(p.baseDir, "pr_review_prompts", normalizedAgent, "PROMPT.md"))
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		p.prTemplates[normalizedAgent] = loaded
		tpl = loaded
		p.mu.Unlock()
	}
	guidance := agentGuidance(normalizedAgent)
	data := PRReviewPromptData{
		Repo:            repo,
		Number:          number,
		Backend:         backend,
		Agent:           normalizedAgent,
		AgentHeading:    fmt.Sprintf("## %s specialist: %s", backend, normalizedAgent),
		AgentGuidance:   guidance,
		WorkflowPartKey: fmt.Sprintf("%s/%s", backend, normalizedAgent),
	}
	return executeTemplate(tpl, data, "pr review")
}

func (p *PromptStore) AutonomousPrompt(agent string, data AutonomousPromptData) (string, error) {
	normalizedAgent := normalizeToken(agent)
	p.mu.Lock()
	tpl := p.autoTemplate[normalizedAgent]
	p.mu.Unlock()
	if tpl == nil {
		loaded, err := p.loadTemplate(filepath.Join(p.baseDir, "autonomous", normalizedAgent, "PROMPT.md"))
		if err != nil {
			return "", err
		}
		p.mu.Lock()
		p.autoTemplate[normalizedAgent] = loaded
		tpl = loaded
		p.mu.Unlock()
	}
	if strings.TrimSpace(data.AgentName) == "" {
		data.AgentName = normalizedAgent
	}
	return executeTemplate(tpl, data, "autonomous agent")
}

func (p *PromptStore) loadTemplate(path string) (*template.Template, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load prompt %s: %w", path, err)
	}
	tpl, err := template.New(filepath.Base(path)).Option("missingkey=error").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse prompt %s: %w", path, err)
	}
	return tpl, nil
}

func executeTemplate(tpl *template.Template, data any, name string) (string, error) {
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("render %s prompt: %w", name, err)
	}
	return buf.String(), nil
}

func agentGuidance(agent string) string {
	switch agent {
	case "architect":
		return "Focus on architecture, boundaries, coupling, and long-term maintainability."
	case "security":
		return "Focus on security vulnerabilities, trust boundaries, secrets handling, and unsafe defaults."
	case "testing":
		return "Focus on test coverage gaps, fragile tests, and missing validation scenarios."
	case "devops":
		return "Focus on CI/CD, deployment safety, observability, and runtime operability."
	case "ux":
		return "Focus on developer/user experience, clarity, ergonomics, and error messaging."
	default:
		return "Focus on the requested specialist agent."
	}
}

func normalizeToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	return strings.ReplaceAll(token, string(filepath.Separator), "_")
}
