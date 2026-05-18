package config

import (
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"
)

const InlineAgentPromptUnsupported = "agent inline prompt bodies are unsupported; create a prompt catalog entry and use prompt_ref or prompt_id"

func rejectInlineAgentPrompts(data []byte) error {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse config: %w", err)
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return nil
	}
	doc := root.Content[0]
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "agents" || doc.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, agent := range doc.Content[i+1].Content {
			if agent.Kind != yaml.MappingNode {
				continue
			}
			for j := 0; j+1 < len(agent.Content); j += 2 {
				if agent.Content[j].Value == "prompt" {
					return errors.New(InlineAgentPromptUnsupported)
				}
			}
		}
	}
	return nil
}
