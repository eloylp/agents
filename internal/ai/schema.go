package ai

import (
	_ "embed"
	"fmt"
	"os"
	"sync"
)

// responseSchema is the structured output JSON schema shared by all backends.
// It is embedded in the binary so the daemon is fully self-contained — no
// external file mounts or inline JSON in config needed.
//
// - Claude backends receive it via --output-format stream-json --json-schema <string>
// - Codex backends receive it via --output-schema <temp-file-path>
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

var (
	schemaOnce sync.Once
	schemaPath string
	schemaErr  error
)

// ResponseSchemaPath materializes the embedded schema to a temp file (once)
// and returns its path. Used by codex backends for --output-schema.
// Safe for concurrent use.
func ResponseSchemaPath() (string, error) {
	schemaOnce.Do(func() {
		f, err := os.CreateTemp("", "agents-response-schema-*.json")
		if err != nil {
			schemaErr = fmt.Errorf("create response schema temp file: %w", err)
			return
		}
		if _, err := f.Write(responseSchema); err != nil {
			f.Close()
			os.Remove(f.Name())
			schemaErr = fmt.Errorf("write response schema: %w", err)
			return
		}
		if err := f.Close(); err != nil {
			os.Remove(f.Name())
			schemaErr = fmt.Errorf("close response schema: %w", err)
			return
		}
		schemaPath = f.Name()
	})
	return schemaPath, schemaErr
}
