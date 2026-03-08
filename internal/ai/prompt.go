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

type PromptStore struct {
	baseDir       string
	issueTpl      *template.Template
	prTemplates   map[string]*template.Template
	autoTemplates map[string]*template.Template
}

func NewPromptStore(baseDir string) (*PromptStore, error) {
	if strings.TrimSpace(baseDir) == "" {
		return nil, fmt.Errorf("prompt store base directory is required")
	}
	return &PromptStore{
		baseDir:       baseDir,
		prTemplates:   make(map[string]*template.Template),
		autoTemplates: make(map[string]*template.Template),
	}, nil
}

func (p *PromptStore) IssueRefinePrompt(repo string, number int) (string, error) {
	if p.issueTpl == nil {
		return "", fmt.Errorf("issue prompt not loaded; call Validate before use")
	}
	data := IssuePromptData{Repo: repo, Number: number}
	return executeTemplate(p.issueTpl, data, "issue refine")
}

func (p *PromptStore) PRReviewPrompt(agent string, backend string, repo string, number int) (string, error) {
	normalizedAgent := normalizeToken(agent)
	pl, ok := p.prTemplates[normalizedAgent]
	if !ok {
		return "", fmt.Errorf("pr review prompt for agent %s not loaded; call Validate", normalizedAgent)
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
	normalizedAgent := normalizeToken(agent)
	pl, ok := p.autoTemplates[normalizedAgent]
	if !ok {
		return "", fmt.Errorf("autonomous prompt for agent %s not loaded; call Validate", normalizedAgent)
	}
	if strings.TrimSpace(data.AgentName) == "" {
		data.AgentName = normalizedAgent
	}
	return executeTemplate(pl, data, "autonomous agent")
}

func (p *PromptStore) Validate(prAgents []string, autonomousAgents []string) error {
	issueTpl, err := p.loadTemplate(filepath.Join(p.baseDir, "issue_refinement_prompts", "PROMPT.md"))
	if err != nil {
		return err
	}
	p.issueTpl = issueTpl

	p.prTemplates = make(map[string]*template.Template)
	seenPR := make(map[string]struct{})
	for _, agent := range prAgents {
		normalized := normalizeToken(agent)
		if _, ok := seenPR[normalized]; ok {
			continue
		}
		tpl, err := p.loadCompositeTemplates(
			filepath.Join(p.baseDir, "pr_review_prompts", "base", "PROMPT.md"),
			filepath.Join(p.baseDir, "guidance", normalized+".md"),
		)
		if err != nil {
			return err
		}
		p.prTemplates[normalized] = tpl
		seenPR[normalized] = struct{}{}
	}

	p.autoTemplates = make(map[string]*template.Template)
	seenAuto := make(map[string]struct{})
	for _, agent := range autonomousAgents {
		normalized := normalizeToken(agent)
		if _, ok := seenAuto[normalized]; ok {
			continue
		}
		tpl, err := p.loadCompositeTemplates(
			filepath.Join(p.baseDir, "autonomous", "base", "PROMPT.md"),
			filepath.Join(p.baseDir, "guidance", normalized+".md"),
		)
		if err != nil {
			return err
		}
		p.autoTemplates[normalized] = tpl
		seenAuto[normalized] = struct{}{}
	}

	return nil
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

func (p *PromptStore) loadCompositeTemplates(paths ...string) (*template.Template, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("no template paths provided")
	}
	content, err := os.ReadFile(paths[0])
	if err != nil {
		return nil, fmt.Errorf("load prompt %s: %w", paths[0], err)
	}
	tpl, err := template.New(filepath.Base(paths[0])).Option("missingkey=error").Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("parse prompt %s: %w", paths[0], err)
	}
	for _, path := range paths[1:] {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("load prompt %s: %w", path, err)
		}
		tpl, err = tpl.Parse(string(content))
		if err != nil {
			return nil, fmt.Errorf("parse prompt %s: %w", path, err)
		}
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

func normalizeToken(token string) string {
	token = strings.ToLower(strings.TrimSpace(token))
	token = filepath.Clean(token)
	token = strings.TrimLeft(token, string(filepath.Separator)+".")
	token = strings.ReplaceAll(token, "..", "_")
	token = strings.ReplaceAll(token, string(filepath.Separator), "_")
	return token
}
