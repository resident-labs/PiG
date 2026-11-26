package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

func TestBeforeRequestHoldsFirstRequestAndReadsQueuedSteer(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	provider := &scriptedProvider{respond: replyText("done")}
	a := mustNewAgent(AgentOptions{Model: scriptedModel(provider), BeforeRequest: func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}})
	done := make(chan error, 1)
	go func() { _, err := a.Send(t.Context(), "go"); done <- err }()
	<-entered
	if provider.calls() != 0 {
		t.Fatal("request escaped gate")
	}
	redirect := userMessage("redirect")
	a.Steer(redirect)
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if provider.calls() != 1 {
		t.Fatalf("requests = %d, want 1 including steer", provider.calls())
	}
	found := false
	for _, m := range a.Messages() {
		if m.User != nil && reflect.DeepEqual(m.User, redirect.User) {
			found = true
		}
	}
	if !found {
		t.Fatal("queued steer not read")
	}
}

func TestNoBeforeRequestDoesNotPollSteeringBeforeNextTurnPreparation(t *testing.T) {
	provider := &scriptedProvider{respond: toolCallsThenText(toolCall("t1", "echo", ai.JsonObject{"value": "x"}))}
	config := standaloneConfig(provider)
	polls := 0
	config.GetSteeringMessages = func() []AgentMessage {
		polls++
		return nil
	}
	config.PrepareNextTurn = func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
		if polls != 2 {
			t.Errorf("steering polled %d times before preparation, want the initial and post-turn polls only", polls)
		}
		return nil, nil
	}
	if _, err := RunAgentLoop(t.Context(), []AgentMessage{userMessage("start")}, AgentContext{Tools: []AgentTool{valueEchoTool(ToolModeParallel, nil)}}, config, func(AgentEvent) error { return nil }, providerStream); err != nil {
		t.Fatal(err)
	}
	if provider.calls() != 2 || polls != 4 {
		t.Fatalf("requests=%d, polls=%d; want 2 requests and 4 polls", provider.calls(), polls)
	}
}

func TestBeforeRequestFailureEndsRun(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			provider := &scriptedProvider{respond: replyText("done")}
			entered := make(chan struct{})
			failure := errors.New("gate failed")
			a := mustNewAgent(AgentOptions{Model: scriptedModel(provider), BeforeRequest: func(ctx context.Context) error {
				close(entered)
				if cancelled {
					<-ctx.Done()
					return ctx.Err()
				}
				return failure
			}})
			var ends atomic.Int32
			a.Subscribe(func(_ context.Context, event AgentEvent) error {
				if _, ok := event.(AgentEndEvent); ok {
					ends.Add(1)
				}
				return nil
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() { _, _ = a.Send(ctx, "go"); close(done) }()
			<-entered
			if cancelled {
				cancel()
			}
			<-done
			if ends.Load() != 1 || provider.calls() != 0 || a.IsStreaming() {
				t.Fatalf("ends=%d requests=%d streaming=%v", ends.Load(), provider.calls(), a.IsStreaming())
			}
		})
	}
}
