package workflow

// TraceStep records one tool call in an agent's tool loop.
// It is used by StepRecorder implementations to persist and serve the
// per-span tool-loop transcript.
type TraceStep struct {
	ToolName      string `json:"tool_name"`
	InputSummary  string `json:"input_summary"`
	OutputSummary string `json:"output_summary"`
	DurationMs    int64  `json:"duration_ms"`
}
