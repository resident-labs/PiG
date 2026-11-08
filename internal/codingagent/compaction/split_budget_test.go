package compaction

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

// A history that fills most of a 272,000-token window is summarized in at most two requests, not one per 11,000 tokens,
// even when the model's output limit is 128,000.
func TestSplitForContextWindowDoesNotReserveTheWholeOutputLimit(t *testing.T) {
	model := &ai.Model{Capabilities: ai.ModelCapabilities{ContextWindow: 272000, MaxOutputTokens: 128000}}
	messages := make([]agent.AgentMessage, 500)
	for i := range messages {
		messages[i] = agent.AgentMessage{User: &agent.UserMessage{Role: "user", Content: ai.UserContentBlocks{ai.TextContent{Text: strings.Repeat("x", 2000)}}}}
	}
	if chunks := splitForContextWindow(messages, model, 16384, "", ""); len(chunks) > 2 {
		t.Fatalf("%d chunks", len(chunks))
	}
}
