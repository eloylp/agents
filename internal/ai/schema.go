package ai

import (
	_ "embed"
)

// responseSchema is the structured output JSON schema shared by all backends.
// It is embedded in the binary so the daemon is fully self-contained, no
// external file mounts or inline JSON in config needed.
//
// - Claude backends receive it via --output-format stream-json --json-schema <string>
// - Codex backends receive it via --output-schema <runner-local-schema-path>
//
// Both are injected automatically by buildDelivery in cmdrunner.go.
//
//go:embed response-schema.json
var responseSchema []byte

// ResponseSchemaString returns the embedded schema as a string, used by
// claude backends for the --json-schema inline argument.
func ResponseSchemaString() string {
	return string(responseSchema)
}
