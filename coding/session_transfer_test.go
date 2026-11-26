package coding

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

// transferTarget is a second model of the harness's provider, with the given context window.
func transferTarget(h *recoveryHarness, window int) *ai.Model {
	source := h.session.Model()
	return &ai.Model{ID: "faux-2", DisplayName: "faux-2", Provider: source.Provider, Capabilities: ai.ModelCapabilities{ContextWindow: window}}
}

// longConversation is three exchanges, the first two of about 15,000 tokens each.
func longConversation(t *testing.T, h *recoveryHarness) {
	t.Helper()
	big := strings.Repeat("word ", 12_000)
	recovered(t, h,
		recoveredUser("first "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "a"}),
		recoveredUser("second "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "b"}),
		recoveredUser("third"), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "c"}))
}

// dyna patch P4.
func TestTransferOfASmallConversation(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{})
	recovered(t, h, recoveredUser("hi"), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "hello"}))
	result, err := h.session.TransferModel(t.Context(), h.session.Model(), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff, CompactionTriggerTokens: 50_000})
	if err != nil || result.Path != TransferUnchanged || result.TokensBefore == 0 || result.TokensAfter != result.TokensBefore {
		t.Fatalf("same model: %+v %v", result, err)
	}
	if settings, _ := h.session.compactionSettings(); settings.TriggerTokens != 50_000 {
		t.Fatalf("trigger %d", settings.TriggerTokens)
	}
	target := transferTarget(h, 128_000)
	result, err = h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferSwitched || h.session.Model().ID != "faux-2" {
		t.Fatalf("other model: %+v %v on %s", result, err, h.session.Model().ID)
	}
}

func TestSameModelTransferIgnoresAnEmptyRecordedModelID(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{})
	missing := recoveredAssistant(ai.StopReasonError)
	missing.Assistant.ModelID = ""
	recovered(t, h, recoveredUser("hi"), missing)
	result, err := h.session.TransferModel(t.Context(), h.session.Model(), ModelTransferOptions{})
	if err != nil || result.Path != TransferUnchanged {
		t.Fatalf("transfer = %+v, %v", result, err)
	}
}

func TestTransferCompactsOnTheOldModelFirst(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("the summary", ai.StopReasonStop, 0))
	target := transferTarget(h, 32_000)
	fallback := &scriptedProvider{}
	target.Provider = fallback
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferCompacted || result.Reason != "summarized by faux/faux-1" || result.TokensAfter >= result.TokensBefore || h.session.Model().ID != "faux-2" {
		t.Fatalf("%+v %v on %s", result, err, h.session.Model().ID)
	}
	if h.provider.callCount() != 1 || fallback.callCount() != 0 {
		t.Fatalf("summary calls: old %d, new %d", h.provider.callCount(), fallback.callCount())
	}
}

func TestTransferFallsBackToTheNewModelAfterAnError(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("old model unavailable"))
	target := transferTarget(h, 32_000)
	fallback := &scriptedProvider{responses: []scriptedResponse{func(messages []ai.Message) *ai.AssistantMessage {
		if h.provider.callCount() != 1 {
			t.Error("the new model summarized before the old model was tried")
		}
		return fauxReply("the fallback summary", ai.StopReasonStop, 0)(messages)
	}}}
	target.Provider = fallback
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferCompacted || result.Reason != "summarized by faux/faux-2" || h.session.Model().ID != "faux-2" || fallback.callCount() != 1 {
		t.Fatalf("%+v %v on %s, new model calls %d", result, err, h.session.Model().ID, fallback.callCount())
	}
}

func TestFailedFallbackDoesNotReplaceTheLiveCompactionPrefix(t *testing.T) {
	for _, path := range []string{TransferAborted, TransferSwitched} {
		t.Run(path, func(t *testing.T) {
			reply := fauxReply("reply", ai.StopReasonStop, 0)
			h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, reply, reply, reply, fauxError("old model unavailable"))
			for range 3 {
				if _, err := h.session.Send(t.Context(), "a question long enough to keep apart from the next one"); err != nil {
					t.Fatal(err)
				}
			}
			old := h.session.Model()
			if _, ok := h.session.liveCompactionRequest(old); !ok {
				t.Fatal("the old model must start with a live prefix")
			}
			target := transferTarget(h, 128_000)
			fallback := &scriptedProvider{responses: []scriptedResponse{func(messages []ai.Message) *ai.AssistantMessage {
				if path == TransferAborted {
					h.session.RequestAbort()
				}
				return fauxError("new model failed")(messages)
			}}}
			target.Provider = fallback
			result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{CompactionTriggerTokens: 1})
			if err != nil || result.Path != path || fallback.callCount() != 1 {
				t.Fatalf("transfer = %+v, %v; fallback calls = %d", result, err, fallback.callCount())
			}
			if _, ok := h.session.liveCompactionRequest(old); !ok {
				t.Fatal("the fallback erased the old model's live prefix")
			}
			if _, ok := h.session.liveCompactionRequest(target); ok {
				t.Fatal("the fallback request is not the conversation's live prefix")
			}
		})
	}
}

// A summary that fails leaves the new model a boundary that names the session file, which keeps every entry.
func TestTransferLeavesABoundaryWhenNoSummaryFits(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("overloaded"))
	target := transferTarget(h, 32_000)
	fallback := &scriptedProvider{responses: []scriptedResponse{fauxError("context too long")}}
	target.Provider = fallback
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferBoundary || !strings.Contains(result.Reason, "overloaded") || !strings.Contains(result.Reason, "context too long") || h.session.Model().ID != "faux-2" || h.provider.callCount() != 1 || fallback.callCount() != 1 {
		t.Fatalf("%+v %v; summary calls: old %d, new %d", result, err, h.provider.callCount(), fallback.callCount())
	}
	path := h.session.Path()
	entries, err := icodingagent.LoadEntriesFromFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var boundary string
	var keptEarlier bool
	for _, entry := range entries {
		switch entry := entry.(type) {
		case icodingagent.CompactionEntry:
			boundary = entry.Summary
		case icodingagent.MessageEntry:
			if user := entry.Message.User; user != nil {
				if text, ok := user.Content.(ai.UserText); ok && strings.HasPrefix(string(text), "first word") {
					keptEarlier = true
				}
			}
		}
	}
	if !strings.HasPrefix(boundary, "This session moved to a new model without its earlier conversation:") || !strings.Contains(boundary, "previous session's file, "+path+".") {
		t.Fatalf("boundary %q does not name session file %q", boundary, path)
	}
	if !keptEarlier {
		t.Fatal("the earlier conversation is missing from the session file")
	}
}

func TestAStoppedTransferKeepsTheModel(t *testing.T) {
	var h *recoveryHarness
	h = newRecoveryHarness(t, harnessOptions{}, func([]ai.Message) *ai.AssistantMessage {
		h.session.RequestAbort()
		return fauxError("interrupted")(nil)
	})
	target := transferTarget(h, 32_000)
	fallback := &scriptedProvider{}
	target.Provider = fallback
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferAborted || h.session.Model().ID != "faux-1" || h.provider.callCount() != 1 || fallback.callCount() != 0 {
		t.Fatalf("%+v %v on %s; summary calls: old %d, new %d", result, err, h.session.Model().ID, h.provider.callCount(), fallback.callCount())
	}
}

// The new model would read a large conversation with a cold cache, so it is summarized on the old one even though it fits.
func TestTransferOfALargeConversationToAnotherModelCompacts(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("the summary", ai.StopReasonStop, 0))
	big := strings.Repeat("word ", 50_000)
	recovered(t, h,
		recoveredUser("first "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "a"}),
		recoveredUser("second "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "b"}),
		recoveredUser("third"), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "c"}))
	result, err := h.session.TransferModel(t.Context(), transferTarget(h, 1_000_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferCompacted || result.TokensBefore <= transferCompactAbove {
		t.Fatalf("%+v %v", result, err)
	}
}

// contextHolds reports whether the session's context, as the next request would read it, holds text.
func contextHolds(h *recoveryHarness, text string) bool {
	for _, message := range h.session.Messages() {
		raw, _ := message.MarshalJSON()
		if strings.Contains(string(raw), text) {
			return true
		}
	}
	return false
}

// Over the trigger alone the conversation still works, so a failed summary moves it whole.
func TestATransferOverTheTriggerOnlyKeepsTheConversation(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("overloaded"))
	h.provider.wrap = true
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), h.session.Model(), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff, CompactionTriggerTokens: 20_000})
	if err != nil || result.Path != TransferUnchanged || !strings.Contains(result.Reason, "overloaded") || !contextHolds(h, "first word") {
		t.Fatalf("%+v %v", result, err)
	}
}

// The trigger bounds what a boundary keeps: a tail over it is left in the file.
func TestABoundaryKeepsATailOnlyWithinTheTrigger(t *testing.T) {
	for _, trigger := range []int{0, 4_000} {
		h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("overloaded"))
		h.provider.wrap = true
		big, middle := strings.Repeat("word ", 12_000), strings.Repeat("tail ", 6_400)
		recovered(t, h,
			recoveredUser("first "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "a"}),
			recoveredUser("second "+big), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "b"}),
			recoveredUser("last "+middle), recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "c"}))
		result, err := h.session.TransferModel(t.Context(), transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff, CompactionTriggerTokens: trigger})
		if err != nil || result.Path != TransferBoundary {
			t.Fatalf("trigger %d: %+v %v", trigger, result, err)
		}
		if kept := contextHolds(h, "last tail"); kept != (trigger == 0) {
			t.Fatalf("trigger %d: the tail kept is %v", trigger, kept)
		}
	}
}

// One tool result over the limit cannot be cut, so the boundary keeps no tail.
func TestABoundaryKeepsNoTailOverTheLimit(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("overloaded"))
	h.provider.wrap = true
	recovered(t, h,
		recoveredUser("go"), recoveredAssistant(ai.StopReasonToolUse, ai.ToolCall{ID: "call-1", Name: "bash", Arguments: ai.JsonObject{}}),
		agent.AgentMessage{ToolResult: &agent.ToolResultMessage{Role: agent.RoleToolResult, ToolCallID: "call-1", ToolName: "bash", Content: []ai.ToolResultMessageContent{ai.TextContent{Text: "huge " + strings.Repeat("output ", 20_000)}}, Timestamp: time.Now().UnixMilli()}},
		recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "c"}))
	result, err := h.session.TransferModel(t.Context(), transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferBoundary || contextHolds(h, "huge output") {
		t.Fatalf("%+v %v, tail kept %v", result, err, contextHolds(h, "huge output"))
	}
}

// A process that restarted on the target still holds the conversation of the model the branch records, so the
// transfer counts as a change of model and summarizes on the recorded one. Here both providers fail, so the
// conversation moves whole because it fits.
func TestATransferSummarizesOnTheModelTheBranchRecords(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`})
	big := strings.Repeat("word ", 60_000)
	recorded := recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "a"})
	recorded.Assistant.ModelID = "faux-auth"
	recovered(t, h, recoveredUser("first "+big), recorded, recoveredUser("second "+big), recorded)
	target := transferTarget(h, 1_000_000)
	fallback := &scriptedProvider{responses: []scriptedResponse{fauxError("target unavailable")}}
	target.Provider = fallback
	h.session.agent.SetModel(target)
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferSwitched || !strings.Contains(result.Reason, "old model:") || !strings.Contains(result.Reason, "target unavailable") || fallback.callCount() != 1 {
		t.Fatalf("%+v %v, new model calls %d", result, err, fallback.callCount())
	}
}

func TestTransferFallsBackWhenTheRecordedModelIsNoLongerRegistered(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{})
	recorded := recoveredAssistant(ai.StopReasonStop, ai.TextContent{Text: "a"})
	recorded.Assistant.ModelID = "removed-model"
	big := strings.Repeat("word ", 12_000)
	recovered(t, h, recoveredUser("first "+big), recorded, recoveredUser("second "+big), recorded, recoveredUser("third"), recorded)
	target := transferTarget(h, 32_000)
	fallback := &scriptedProvider{responses: []scriptedResponse{fauxReply("the fallback summary", ai.StopReasonStop, 0)}}
	target.Provider = fallback
	h.session.agent.SetModel(target) // a restarted process was opened on the target
	result, err := h.session.TransferModel(t.Context(), target, ModelTransferOptions{})
	if err != nil || result.Path != TransferCompacted || result.Reason != "summarized by faux/faux-2" || fallback.callCount() != 1 {
		t.Fatalf("transfer = %+v, %v; fallback calls = %d", result, err, fallback.callCount())
	}
}

func TestTransferFallsBackAfterATimeoutOnlyWithMostOfTheBudgetLeft(t *testing.T) {
	for _, budget := range []time.Duration{8 * time.Minute, 4 * time.Minute} {
		t.Run(budget.String(), func(t *testing.T) {
			h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("provider request timed out"))
			target := transferTarget(h, 32_000)
			fallback := &scriptedProvider{responses: []scriptedResponse{fauxReply("the fallback summary", ai.StopReasonStop, 0)}}
			target.Provider = fallback
			longConversation(t, h)
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			result, err := h.session.TransferModel(ctx, target, ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
			path, calls := TransferCompacted, 1
			if budget < transferSummaryTimeout/2 {
				path, calls = TransferBoundary, 0
			}
			if err != nil || result.Path != path || fallback.callCount() != calls {
				t.Fatalf("%+v %v, new model calls %d", result, err, fallback.callCount())
			}
		})
	}
}

// A summary that takes too long gives way to the move.
func TestATransferWhoseSummaryTimesOutMovesAnyway(t *testing.T) {
	defer func(timeout time.Duration) { transferSummaryTimeout = timeout }(transferSummaryTimeout)
	transferSummaryTimeout = 50 * time.Millisecond
	h := newRecoveryHarness(t, harnessOptions{}, func(messages []ai.Message) *ai.AssistantMessage {
		time.Sleep(200 * time.Millisecond)
		return fauxReply("late", ai.StopReasonStop, 0)(messages)
	})
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferBoundary || result.Reason != "the summary timed out" || h.session.Model().ID != "faux-2" {
		t.Fatalf("%+v %v", result, err)
	}
}
