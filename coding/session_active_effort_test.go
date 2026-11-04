package coding

import (
	"context"
	"sync"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

// effortProbeProvider records whether each request it is sent asks for the active effort at the top level.
type effortProbeProvider struct {
	mu   sync.Mutex
	seen []bool
}

func (p *effortProbeProvider) ID() string   { return "faux" }
func (p *effortProbeProvider) Close() error { return nil }
func (p *effortProbeProvider) Stream(_ context.Context, _ ai.TranscriptContext, options ai.StreamOptions) (*ai.AssistantMessageEventStream, error) {
	p.mu.Lock()
	p.seen = append(p.seen, options.ActiveEffortAtTopLevel)
	p.mu.Unlock()
	message := fauxReply("summary text", ai.StopReasonStop, 0)(nil)
	return newSessionTestStream(ai.StartEvent{Partial: message}, ai.DoneEvent{Reason: ai.StopReasonStop, Message: message}), nil
}

// dyna patch P8: SessionOptions.ActiveEffortAtTopLevel reaches every request of the Session, a compaction's summary included.
func TestActiveEffortAtTopLevelReachesEveryRequest(t *testing.T) {
	for _, on := range []bool{false, true} {
		probe := &effortProbeProvider{}
		model := &ai.Model{ID: "faux-1", DisplayName: "faux-1", Provider: newRequestOptionsProvider(probe, "opencode", nil), Capabilities: ai.ModelCapabilities{ContextWindow: 128_000}}
		sess, err := NewSession(newTestServicesSmallKeep(t), SessionOptions{Model: model, SkipBuiltinTools: true, ActiveEffortAtTopLevel: on})
		if err != nil {
			t.Fatal(err)
		}
		drainSessionEvents(t, sess)
		seedProviderHookSession(t, sess)
		if _, err := sess.Compact(context.Background(), ""); err != nil {
			t.Fatal(err)
		}
		_ = sess.Close()
		if len(probe.seen) < 4 {
			t.Fatalf("%d requests", len(probe.seen))
		}
		for i, seen := range probe.seen {
			if seen != on {
				t.Fatalf("option %v: request %d asked %v", on, i, seen)
			}
		}
	}
}
