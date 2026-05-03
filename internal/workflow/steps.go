package workflow

// TraceStep records one event in an agent's tool loop. Two kinds are
// emitted today:
//
//   - "tool":     a paired tool_use + tool_result. ToolName is the tool
//                 name; InputSummary is the call args; OutputSummary is
//                 the tool's reply; DurationMs is the round-trip time.
//   - "thinking": a text block the assistant emitted between tool calls.
//                 ToolName is empty; InputSummary carries the text;
//                 OutputSummary and DurationMs are empty.
//
// The persisted row preserves the full content of each field; the UI
// (StreamCard) decides how much to show by default and exposes an
// expand affordance to recover the rest.
type TraceStep struct {
	Kind          string `json:"kind"`
	ToolName      string `json:"tool_name,omitempty"`
	InputSummary  string `json:"input_summary,omitempty"`
	OutputSummary string `json:"output_summary,omitempty"`
	DurationMs    int64  `json:"duration_ms,omitempty"`
}

// Step kind constants.
const (
	StepKindTool     = "tool"
	StepKindThinking = "thinking"
)
