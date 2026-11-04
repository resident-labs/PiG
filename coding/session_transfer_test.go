package coding

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
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

func TestTransferCompactsOnTheOldModelFirst(t *testing.T) {
	var summarizedBy string
	h := newRecoveryHarness(t, harnessOptions{}, func(messages []ai.Message) *ai.AssistantMessage {
		return fauxReply("the summary", ai.StopReasonStop, 0)(messages)
	})
	h.session.Subscribe(func(e agent.AgentEvent) {
		if _, ok := e.(agent.CompactionStartEvent); ok {
			summarizedBy = h.session.Model().ID
		}
	})
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferCompacted || result.TokensAfter >= result.TokensBefore || h.session.Model().ID != "faux-2" {
		t.Fatalf("%+v %v on %s", result, err, h.session.Model().ID)
	}
	if summarizedBy != "faux-1" {
		t.Fatalf("summarized on %q", summarizedBy)
	}
}

// A summary that fails leaves the new model a boundary that names the session file, which keeps every entry.
func TestTransferLeavesABoundaryWhenNoSummaryFits(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{settings: `{"retry":{"enabled":false}}`}, fauxError("overloaded"))
	h.provider.wrap = true
	longConversation(t, h)
	result, err := h.session.TransferModel(t.Context(), transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferBoundary || !strings.Contains(result.Reason, "overloaded") || h.session.Model().ID != "faux-2" {
		t.Fatalf("%+v %v", result, err)
	}
	data, err := os.ReadFile(h.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "This session moved to a new model without its earlier conversation") || !strings.Contains(string(data), h.session.Path()) || !strings.Contains(string(data), "first word") {
		t.Fatal("the boundary or the earlier conversation is missing from the session file")
	}
}

func TestAStoppedTransferKeepsTheModel(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("the summary", ai.StopReasonStop, 0))
	longConversation(t, h)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := h.session.TransferModel(ctx, transferTarget(h, 32_000), ModelTransferOptions{ThinkingLevel: ai.ThinkingOff})
	if err != nil || result.Path != TransferAborted || h.session.Model().ID != "faux-1" {
		t.Fatalf("%+v %v on %s", result, err, h.session.Model().ID)
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
