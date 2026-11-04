package coding

import (
	"context"
	"errors"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

// ContinueOptions are the choices of ContinueRecovered.
type ContinueOptions struct {
	// Interrupted is the text recorded as the error result of a tool call that has none: it started, and the process
	// that ran it ended before its result was written.
	Interrupted string
	// Started is called once the run is admitted, before its first request.
	Started func()
}

// ErrNothingToContinue is ContinueRecovered's answer when the session ends with an answer: it has no input or tool
// result for a model to read.
var ErrNothingToContinue = errors.New("coding: the recovered session has no pending input or tool result")

// ContinueRecovered resumes a session whose process ended during a run, from what the session file holds. A final
// attempt that failed is left out of the model's context, as a retry leaves it out. Each tool call of the last assistant
// message that has no result gets an error result with the given text, written to the session file, and is not run
// again: whether it finished is unknown. The run then continues from the last message.
//
// dyna patch P2 (PATCHES.md): Pi resumes a session only with a new prompt. A host that restarts mid-run, and must not
// send its inputs twice, needs to finish the run they started instead.
func (s *Session) ContinueRecovered(ctx context.Context, options ContinueOptions) error {
	if s.IsStreaming() {
		return errAgentAlreadyProcessing
	}
	if failed := lastAssistantMessage(s.agent.Messages()); failed != nil && failed.StopReason == ai.StopReasonError {
		if err := s.omitRecoveryAttempt(failed); err != nil {
			return err
		}
	}
	messages := s.agent.Messages()
	for i := len(messages) - 1; i >= 0; i-- {
		assistant := messages[i].Assistant
		if assistant == nil {
			continue
		}
		answered := map[string]bool{}
		for _, m := range messages[i+1:] {
			if m.ToolResult != nil {
				answered[m.ToolResult.ToolCallID] = true
			}
		}
		for _, block := range assistant.Content {
			call, ok := block.(ai.ToolCall)
			if !ok || answered[call.ID] {
				continue
			}
			result := agent.AgentMessage{ToolResult: &agent.ToolResultMessage{
				Role:       agent.RoleToolResult,
				ToolCallID: call.ID,
				ToolName:   call.Name,
				Content:    []ai.ToolResultMessageContent{ai.TextContent{Text: options.Interrupted}},
				IsError:    true,
				Timestamp:  time.Now().UnixMilli(),
			}}
			if err := s.persistMessage(result); err != nil {
				return err
			}
			s.agent.SetMessages(append(s.agent.Messages(), result))
		}
		break
	}
	last := s.agent.Messages()
	if len(last) == 0 || last[len(last)-1].Assistant != nil || last[len(last)-1].System != nil {
		return ErrNothingToContinue
	}
	_, err := s.RunAgentPrompt(ctx, func(ctx context.Context) ([]agent.AgentMessage, error) {
		if options.Started != nil {
			options.Started()
		}
		return s.agent.ContinueMessages(ctx)
	})
	return err
}
