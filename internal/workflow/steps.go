package workflow

import "github.com/eloylp/agents/internal/ai"

// TraceStep aliases ai.TraceStep so the workflow and observe packages share
// one definition without a conversion step in the engine's step-recording path.
type TraceStep = ai.TraceStep

// Step kind constants.
const (
	StepKindTool     = "tool"
	StepKindThinking = "thinking"
)
