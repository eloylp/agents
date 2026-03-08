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
	issueOnce    sync.Once
	prTemplates  map[string]*template.Template
	prOnce       map[string]*sync.Once
	autoTemplate map[string]*template.Template
	autoOnce     map[string]*sync.Once
	mu           sync.Mutex
}

func NewPromptStore(baseDir string) (*PromptStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("prompt store base directory is required")
	}
	return &PromptStore{
		baseDir:      baseDir,
		prTemplates:  make(map[string]*template.Template),
		prOnce:       make(map[string]*sync.Once),
		autoTemplate: make(map[string]*template.Template),
		autoOnce:     make(map[string]*sync.Once),
	}, nil
}

func (p *PromptStore) IssueRefinePrompt(repo string, number int) (string, error) {
	var loadErr error
	p.issueOnce.Do(func() {
		tpl, err := p.loadTemplate(filepath.Join(p.baseDir, "issue_refinement_prompts", "PROMPT.md"))
		if err != nil {
			loadErr = err
			return
		}
		p.issueTpl = tpl
	})
	if loadErr != nil {
		return "", loadErr
	}
	data := IssuePromptData{Repo: repo, Number: number}
	return executeTemplate(p.issueTpl, data, "issue refine")
}

func (p *PromptStore) PRReviewPrompt(agent string, backend string, repo string, number int) (string, error) {
	normalizedAgent := normalizeToken(agent)
	once := p.onceFor(normalizedAgent, p.prOnce)
	var loadErr error
	once.Do(func() {
		tpl, err := p.loadTemplate(filepath.Join(p.baseDir, "pr_review_prompts", normalizedAgent, "PROMPT.md"))
		if err != nil {
			loadErr = err
			return
		}
		p.prTemplates[normalizedAgent] = tpl
	})
	if loadErr != nil {
		return "", loadErr
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
	return executeTemplate(p.prTemplates[normalizedAgent], data, "pr review")
}

func (p *PromptStore) AutonomousPrompt(agent string, data AutonomousPromptData) (string, error) {
	normalizedAgent := normalizeToken(agent)
	once := p.onceFor(normalizedAgent, p.autoOnce)
	var loadErr error
	once.Do(func() {
		tpl, err := p.loadTemplate(filepath.Join(p.baseDir, "autonomous", normalizedAgent, "PROMPT.md"))
		if err != nil {
			loadErr = err
			return
		}
		p.autoTemplate[normalizedAgent] = tpl
	})
	if loadErr != nil {
		return "", loadErr
	}
	if strings.TrimSpace(data.AgentName) == "" {
		data.AgentName = normalizedAgent
	}
	return executeTemplate(p.autoTemplate[normalizedAgent], data, "autonomous agent")
}

func (p *PromptStore) Validate(prAgents []string, autonomousAgents []string) error {
	if _, err := p.IssueRefinePrompt("validate/repo", 0); err != nil {
		return err
	}
	for _, agent := range prAgents {
		if _, err := p.PRReviewPrompt(agent, "validate", "validate/repo", 0); err != nil {
			return err
		}
	}
	for _, agent := range autonomousAgents {
		if _, err := p.AutonomousPrompt(agent, AutonomousPromptData{Repo: "validate/repo", AgentName: agent}); err != nil {
			return err
		}
	}
	return nil
}

func (p *PromptStore) onceFor(key string, store map[string]*sync.Once) *sync.Once {
	p.mu.Lock()
	defer p.mu.Unlock()
	once := store[key]
	if once == nil {
		once = &sync.Once{}
		store[key] = once
	}
	return once
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
	token = filepath.Clean(token)
	token = strings.TrimLeft(token, string(filepath.Separator)+".")
	token = strings.ReplaceAll(token, "..", "_")
	token = strings.ReplaceAll(token, string(filepath.Separator), "_")
	return token
}
