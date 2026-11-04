package coding

import (
	"os"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// dyna patch P1 (PATCHES.md): a prompt's and a steer's members are recorded on their user messages in the session file.
func TestPromptAndSteerMembersAreRecorded(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	err := h.session.Prompt(t.Context(), "first", &PromptOptions{
		Members: map[string]json.RawMessage{"deliveryId": json.RawMessage(`"1"`)},
		PreflightResult: func(PromptDisposition) {
			if _, err := h.session.Steer(t.Context(), "second", nil, &QueueInputOptions{Members: map[string]json.RawMessage{"deliveryId": json.RawMessage(`"2"`)}}); err != nil {
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
	for _, want := range []string{`"content":[{"type":"text","text":"first"}],"timestamp":`, `"deliveryId":"1"`, `"deliveryId":"2"`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("session file lacks %s:\n%s", want, data)
		}
	}
}

func TestAPromptWithAMemberOfTheMessageIsRefused(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{}, fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "first", &PromptOptions{Members: map[string]json.RawMessage{"role": json.RawMessage(`"x"`)}}); err == nil {
		t.Fatal("accepted")
	}
	if h.session.IsStreaming() {
		t.Fatal("the refused prompt left the session busy")
	}
}
