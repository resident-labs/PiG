package coding

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

// recovered records messages as a process that ended mid-run left them, in the session file and the agent's context.
func recovered(t *testing.T, h *recoveryHarness, messages ...agent.AgentMessage) {
	t.Helper()
	for _, m := range messages {
		if err := h.session.persistMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	h.session.agent.SetMessages(append(h.session.agent.Messages(), messages...))
}

func recoveredUser(text string) agent.AgentMessage {
	return agent.AgentMessage{User: &agent.UserMessage{Role: agent.RoleUser, Content: ai.UserText(text), Timestamp: time.Now().UnixMilli()}}
}

func recoveredAssistant(stop ai.StopReason, blocks ...ai.AssistantContentBlock) agent.AgentMessage {
	return agent.AgentMessage{Assistant: &agent.AssistantMessage{Role: agent.RoleAssistant, Content: blocks, Provider: "faux", ModelID: "faux-1", StopReason: stop, Timestamp: time.Now().UnixMilli()}}
}

// dyna patch P2: a tool call the ended process left without a result gets the interrupted result, and is not run again.
func TestContinueRecordsInterruptedToolCalls(t *testing.T) {
	var sent []ai.Message
	h := newRecoveryHarness(t, harnessOptions{}, func(messages []ai.Message) *ai.AssistantMessage {
		sent = messages
		return fauxReply("done", ai.StopReasonStop, 0)(messages)
	})
	recovered(t, h, recoveredUser("go"), recoveredAssistant(ai.StopReasonToolUse, ai.ToolCall{ID: "call-1", Name: "bash", Arguments: ai.JsonObject{"command": "sleep 100"}}))
	started := false
	if err := h.session.ContinueRecovered(t.Context(), ContinueOptions{Interrupted: "interrupted: unknown", Started: func() { started = true }}); err != nil {
		t.Fatal(err)
	}
	if !started {
		t.Fatal("Started was not called")
	}
	last, ok := sent[len(sent)-1].(ai.ToolResultMessage)
	if !ok || last.ToolCallID != "call-1" || !last.IsError {
		t.Fatalf("the model read %#v", sent[len(sent)-1])
	}
	data, err := os.ReadFile(h.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"toolCallId":"call-1","toolName":"bash","content":[{"type":"text","text":"interrupted: unknown"}]`) || !strings.Contains(string(data), `"text":"done"`) {
		t.Fatalf("session file:\n%s", data)
	}
}

func TestContinueReadsAPendingInput(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	recovered(t, h, recoveredUser("go"))
	if err := h.session.ContinueRecovered(t.Context(), ContinueOptions{Interrupted: "x"}); err != nil {
		t.Fatal(err)
	}
	if text := h.session.LastAssistantText(); text == nil || *text != "done" {
		t.Fatalf("answer %v", text)
	}
}

// A failed final attempt never answered the run, so the model reads the input again without it.
func TestContinueLeavesOutAFailedAttempt(t *testing.T) {
	var sent []ai.Message
	h := newRecoveryHarness(t, harnessOptions{}, func(messages []ai.Message) *ai.AssistantMessage {
		sent = messages
		return fauxReply("done", ai.StopReasonStop, 0)(messages)
	})
	failed := recoveredAssistant(ai.StopReasonError)
	failed.Assistant.ErrorMessage = "overloaded"
	recovered(t, h, recoveredUser("go"), failed)
	if err := h.session.ContinueRecovered(t.Context(), ContinueOptions{Interrupted: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, user := sent[0].(ai.UserMessage); len(sent) != 1 || !user {
		t.Fatalf("the model read %+v", sent)
	}
}

func TestContinueAfterAnAnswerIsRefused(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	recovered(t, h, recoveredUser("go"), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "answer"}))
	if err := h.session.ContinueRecovered(t.Context(), ContinueOptions{Interrupted: "x"}); !errors.Is(err, ErrNothingToContinue) {
		t.Fatalf("err %v", err)
	}
}

// Of one message's tool calls, only those without a result get the interrupted one; a recorded result stays the only one.
func TestContinueAnswersOnlyTheCallsWithoutResults(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	recovered(t, h, recoveredUser("go"),
		recoveredAssistant(ai.StopReasonToolUse,
			ai.ToolCall{ID: "call-1", Name: "bash", Arguments: ai.JsonObject{"command": "true"}},
			ai.ToolCall{ID: "call-2", Name: "bash", Arguments: ai.JsonObject{"command": "sleep 100"}}),
		agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Role: agent.RoleToolResult, ToolCallID: "call-1", ToolName: "bash", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "ok"}}, Timestamp: time.Now().UnixMilli()}})
	if err := h.session.ContinueRecovered(t.Context(), ContinueOptions{Interrupted: "interrupted: unknown"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"toolCallId":"call-1"`); got != 1 {
		t.Fatalf("call-1 has %d results in the session file", got)
	}
	if !strings.Contains(string(data), `"toolCallId":"call-2","toolName":"bash","content":[{"type":"text","text":"interrupted: unknown"}]`) {
		t.Fatal("call-2 has no interrupted result")
	}
}
