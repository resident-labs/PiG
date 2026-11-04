package coding

import (
	"os"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// dyna patch P1 (PATCHES.md): a prompt's and a steer's members are recorded on their user messages in the session file,
// whether the steer comes through Steer or through Prompt while the session streams.
func TestPromptAndSteerMembersAreRecorded(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	err := h.session.Prompt(t.Context(), "first", &PromptOptions{
		Members: map[string]json.RawMessage{"deliveryId": json.RawMessage(`"1"`)},
		PreflightResult: func(PromptDisposition) {
			if _, err := h.session.Steer(t.Context(), "second", nil, &QueueInputOptions{Members: map[string]json.RawMessage{"deliveryId": json.RawMessage(`"2"`)}}); err != nil {
				t.Error(err)
			}
			if err := h.session.Prompt(t.Context(), "third", &PromptOptions{StreamingBehavior: extension.DeliverAsSteer, Members: map[string]json.RawMessage{"deliveryId": json.RawMessage(`"3"`)}}); err != nil {
				t.Error(err)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(h.session.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"content":[{"type":"text","text":"first"}],"timestamp":`, `"deliveryId":"1"`, `"deliveryId":"2"`, `"text":"third"}],"timestamp":`, `"deliveryId":"3"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("session file lacks %s:\n%s", want, data)
		}
	}
}

// An input with a bad member is refused before input handlers see it, so nothing is queued or started.
func TestAnInputWithAMemberOfTheMessageIsRefused(t *testing.T) {
	var seen []string
	h := newRecoveryHarness(t, harnessOptions{extension: extension.Extension{Handlers: map[string][]extension.HandlerFn{"input": {func(args ...any) (any, error) {
		seen = append(seen, args[0].(extension.InputEvent).Text)
		return nil, nil
	}}}}}, fauxReply("done", ai.StopReasonStop, 0))
	bad := map[string]json.RawMessage{"role": json.RawMessage(`"x"`)}
	if err := h.session.Prompt(t.Context(), "prompt", &PromptOptions{Members: bad}); err == nil {
		t.Fatal("prompt accepted")
	}
	if _, err := h.session.Steer(t.Context(), "steer", nil, &QueueInputOptions{Members: bad}); err == nil {
		t.Fatal("steer accepted")
	}
	if h.session.IsStreaming() || len(seen) > 0 {
		t.Fatalf("the refused input reached handlers %v or left the session busy", seen)
	}
}
