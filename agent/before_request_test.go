package agent

import (
	"context"
	"reflect"
	"testing"
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
	a.Steer(userMessage("redirect"))
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if provider.calls() != 1 {
		t.Fatalf("requests = %d, want 1 including steer", provider.calls())
	}
	found := false
	for _, m := range a.Messages() {
		if m.User != nil && reflect.DeepEqual(m.User, userMessage("redirect").User) {
			found = true
		}
	}
	if !found {
		t.Fatal("queued steer not read")
	}
}
