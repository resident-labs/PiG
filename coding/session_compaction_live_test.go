package coding

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/internal/codingagent/compaction"
)

// liveProbeProvider records each request's messages and options and answers every request with the same text.
type liveProbeProvider struct {
	mu       sync.Mutex
	requests [][]ai.Message
	options  []ai.StreamOptions
}

func (p *liveProbeProvider) ID() string   { return "faux" }
func (p *liveProbeProvider) Close() error { return nil }
func (p *liveProbeProvider) Stream(_ context.Context, transcript ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.mu.Lock()
	p.requests = append(p.requests, transcript.Messages())
	p.options = append(p.options, options)
	p.mu.Unlock()
	message := fauxReply("## Goal\nsummary text", ai.StopReasonStop, 0)(nil)
	return newSessionTestStream(ai.StartEvent{Partial: message}, ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}), nil
}

// A compaction's summary request is the agent's latest request with the messages since and the summary prompt
// appended, on the same session ID and with prompt caching left as the agent's (dyna patch P16).
func TestCompactionExtendsTheAgentsLatestRequest(t *testing.T) {
	probe := &liveProbeProvider{}
	model := &ai.Model{ID: "faux-1", DisplayName: "faux-1", Provider: newRequestOptionsProvider(probe, "opencode", nil), Capabilities: ai.ModelCapabilities{ContextWindow: 128_000}}
	sess, err := NewSession(newTestServicesSmallKeep(t), SessionOptions{Model: model, SkipBuiltinTools: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	drainSessionEvents(t, sess)
	for range 3 {
		if _, err := sess.Send(context.Background(), "a question long enough to be kept apart from the next one"); err != nil {
			t.Fatal(err)
		}
	}
	drainSessionEvents(t, sess)
	agentRequests := len(probe.requests)
	if _, err := sess.Compact(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := len(probe.requests) - agentRequests; got != 1 {
		t.Fatalf("summary requests = %d, want 1", got)
	}
	last, summary := probe.requests[agentRequests-1], probe.requests[agentRequests]
	if len(summary) != len(last)+2 {
		t.Fatalf("summary request has %d messages, want the %d of the last agent request, its reply and the prompt", len(summary), len(last))
	}
	if !reflect.DeepEqual(summary[:len(last)], last) {
		t.Fatal("the summary request does not start with the last agent request")
	}
	if _, ok := summary[len(last)].(ai.AssistantMessage); !ok {
		t.Fatalf("message after the prefix = %T, want the agent's reply", summary[len(last)])
	}
	prompt, ok := summary[len(summary)-1].(ai.UserMessage)
	if !ok || !strings.HasPrefix(prompt.Content.(ai.UserContentBlocks)[0].(ai.TextContent).Text, compaction.LIVE_SUMMARIZATION_PROMPT[:40]) {
		t.Fatalf("last message = %#v, want the summary prompt", summary[len(summary)-1])
	}
	agentOptions, summaryOptions := probe.options[agentRequests-1], probe.options[agentRequests]
	if summaryOptions.SessionID != agentOptions.SessionID || summaryOptions.CacheRetention != agentOptions.CacheRetention {
		t.Fatalf("summary options session %q cache %q, want the agent's %q and %q", summaryOptions.SessionID, summaryOptions.CacheRetention, agentOptions.SessionID, agentOptions.CacheRetention)
	}
}
