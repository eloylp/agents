// Package catalog parses and validates delegated intelligence catalog files.
package catalog

import (
	"bytes"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/eloylp/agents/internal/fleet"
	"github.com/eloylp/agents/internal/store"
)

const Version = 1

type File struct {
	Version int     `yaml:"version"`
	Assets  []Asset `yaml:"assets"`
}

type Asset struct {
	ID          string `yaml:"id"`
	Kind        string `yaml:"kind"`
	WorkspaceID string `yaml:"workspace_id,omitempty"`
	Repo        string `yaml:"repo,omitempty"`
	Name        string `yaml:"name"`
	Description string `yaml:"description,omitempty"`
	Body        string `yaml:"body"`
	Enabled     *bool  `yaml:"enabled,omitempty"`
	Position    *int   `yaml:"position,omitempty"`
}

var forbiddenAssetFields = map[string]struct{}{
	"workspace":        {},
	"repo_binding":     {},
	"agent":            {},
	"agents":           {},
	"prompt_id":        {},
	"skills":           {},
	"backend":          {},
	"model":            {},
	"labels":           {},
	"events":           {},
	"cron":             {},
	"schedule":         {},
	"dispatch":         {},
	"can_dispatch":     {},
	"allow_dispatch":   {},
	"token_budget":     {},
	"token_budgets":    {},
	"graph":            {},
	"graph_layout":     {},
	"version_id":       {},
	"catalog_version":  {},
	"catalog_versions": {},
	"internal_id":      {},
}

func Parse(data []byte) (File, error) {
	var root yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(false)
	if err := dec.Decode(&root); err != nil {
		return File{}, &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: malformed YAML: %v", err)}
	}
	if len(root.Content) == 0 {
		return File{}, &store.ErrValidation{Msg: "catalog.yml: document is required"}
	}
	if err := validateTopLevelFields(root.Content[0]); err != nil {
		return File{}, err
	}
	assetsNode := mappingValue(root.Content[0], "assets")
	if assetsNode == nil {
		return File{}, &store.ErrValidation{Msg: "catalog.yml: assets is required"}
	}
	if assetsNode.Kind != yaml.SequenceNode {
		return File{}, &store.ErrValidation{Msg: "catalog.yml: assets must be a list"}
	}
	for i, node := range assetsNode.Content {
		if err := validateAssetFields(i, node); err != nil {
			return File{}, err
		}
	}
	var file File
	if err := root.Decode(&file); err != nil {
		return File{}, &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: decode: %v", err)}
	}
	if err := Validate(file); err != nil {
		return File{}, err
	}
	return file, nil
}

func Validate(file File) error {
	if file.Version != Version {
		return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: unsupported version %d", file.Version)}
	}
	if file.Assets == nil {
		return &store.ErrValidation{Msg: "catalog.yml: assets is required"}
	}
	seen := map[string]struct{}{}
	for i, asset := range file.Assets {
		asset.ID = strings.TrimSpace(asset.ID)
		asset.Kind = strings.TrimSpace(asset.Kind)
		asset.WorkspaceID = strings.TrimSpace(asset.WorkspaceID)
		if asset.WorkspaceID != "" {
			asset.WorkspaceID = fleet.NormalizeWorkspaceID(asset.WorkspaceID)
		}
		asset.Repo = fleet.NormalizeRepoName(asset.Repo)
		asset.Name = strings.TrimSpace(asset.Name)
		asset.Body = strings.TrimSpace(asset.Body)
		prefix := fmt.Sprintf("catalog.yml: assets[%d]", i)
		if asset.ID == "" {
			return &store.ErrValidation{Msg: prefix + ": id is required"}
		}
		if err := validatePublicRef(asset.ID); err != nil {
			return &store.ErrValidation{Msg: fmt.Sprintf("%s: id %q: %v", prefix, asset.ID, err)}
		}
		if _, ok := seen[asset.ID]; ok {
			return &store.ErrValidation{Msg: fmt.Sprintf("%s: duplicate id %q", prefix, asset.ID)}
		}
		seen[asset.ID] = struct{}{}
		if asset.Kind == "" {
			return &store.ErrValidation{Msg: prefix + ": kind is required"}
		}
		if asset.Name == "" {
			return &store.ErrValidation{Msg: prefix + ": name is required"}
		}
		if asset.Body == "" {
			return &store.ErrValidation{Msg: prefix + ": body is required"}
		}
		if asset.WorkspaceID == "" && asset.Repo != "" {
			return &store.ErrValidation{Msg: prefix + ": repo scope requires workspace_id"}
		}
		switch asset.Kind {
		case "prompt", "skill":
			if asset.Enabled != nil || asset.Position != nil {
				return &store.ErrValidation{Msg: fmt.Sprintf("%s: enabled and position are only valid for guardrails", prefix)}
			}
		case "guardrail":
			if asset.Repo != "" {
				return &store.ErrValidation{Msg: prefix + ": repo scope is only valid for prompts and skills"}
			}
		default:
			return &store.ErrValidation{Msg: fmt.Sprintf("%s: unsupported kind %q", prefix, asset.Kind)}
		}
	}
	return nil
}

func ToFile(prompts []fleet.Prompt, skills map[string]fleet.Skill, guardrails []fleet.Guardrail) File {
	file := File{Version: Version}
	for _, p := range prompts {
		file.Assets = append(file.Assets, Asset{
			ID:          p.ID,
			Kind:        "prompt",
			WorkspaceID: p.WorkspaceID,
			Repo:        p.Repo,
			Name:        p.Name,
			Description: p.Description,
			Body:        p.Content,
		})
	}
	for id, sk := range skills {
		name := sk.Name
		if name == "" {
			name = id
		}
		file.Assets = append(file.Assets, Asset{
			ID:          id,
			Kind:        "skill",
			WorkspaceID: sk.WorkspaceID,
			Repo:        sk.Repo,
			Name:        name,
			Body:        sk.Prompt,
		})
	}
	for _, g := range guardrails {
		if g.IsBuiltin {
			continue
		}
		enabled := g.Enabled
		position := g.Position
		file.Assets = append(file.Assets, Asset{
			ID:          g.ID,
			Kind:        "guardrail",
			WorkspaceID: g.WorkspaceID,
			Name:        g.Name,
			Description: g.Description,
			Body:        g.Content,
			Enabled:     &enabled,
			Position:    &position,
		})
	}
	return file
}

func Marshal(file File) ([]byte, error) {
	if err := Validate(file); err != nil {
		return nil, err
	}
	out, err := yaml.Marshal(file)
	if err != nil {
		return nil, fmt.Errorf("catalog.yml: marshal: %w", err)
	}
	return out, nil
}

func validateTopLevelFields(node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return &store.ErrValidation{Msg: "catalog.yml: top-level document must be a map"}
	}
	seen := map[string]struct{}{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := seen[key]; ok {
			return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: duplicate top-level field %q", key)}
		}
		seen[key] = struct{}{}
		switch key {
		case "version", "assets":
		default:
			return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: unsupported top-level field %q", key)}
		}
	}
	if _, ok := seen["version"]; !ok {
		return &store.ErrValidation{Msg: "catalog.yml: version is required"}
	}
	if _, ok := seen["assets"]; !ok {
		return &store.ErrValidation{Msg: "catalog.yml: assets is required"}
	}
	return nil
}

func validateAssetFields(index int, node *yaml.Node) error {
	if node.Kind != yaml.MappingNode {
		return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: assets[%d] must be a map", index)}
	}
	seen := map[string]struct{}{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if _, ok := seen[key]; ok {
			return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: assets[%d]: duplicate field %q", index, key)}
		}
		seen[key] = struct{}{}
		if _, forbidden := forbiddenAssetFields[key]; forbidden {
			return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: assets[%d]: forbidden field %q", index, key)}
		}
		switch key {
		case "id", "kind", "workspace_id", "repo", "name", "description", "body", "enabled", "position":
		default:
			return &store.ErrValidation{Msg: fmt.Sprintf("catalog.yml: assets[%d]: unsupported field %q", index, key)}
		}
	}
	return nil
}

func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func validatePublicRef(ref string) error {
	if strings.Contains(ref, "@") {
		return fmt.Errorf("must not include a version suffix")
	}
	for _, r := range ref {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return fmt.Errorf("must contain only lowercase letters, digits, hyphen, or underscore")
	}
	return nil
}
