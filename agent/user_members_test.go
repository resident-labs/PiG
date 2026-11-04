package agent

import (
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
	json "github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

func TestUserMessageMembersAreRecordedAndNotSent(t *testing.T) {
	message, err := NewUserMessage(ai.UserText("hi"), 7, map[string]json.RawMessage{"deliveryId": json.RawMessage(`"12"`), "a": json.RawMessage("1")})
	if err != nil {
		t.Fatal(err)
	}
	recorded, err := message.MarshalJSON()
	if err != nil || string(recorded) != `{"role":"user","content":"hi","timestamp":7,"a":1,"deliveryId":"12"}` {
		t.Fatalf("recorded %s, %v", recorded, err)
	}
	var reread AgentMessage
	if err := reread.UnmarshalJSON(recorded); err != nil {
		t.Fatal(err)
	}
	if again, _ := reread.MarshalJSON(); string(again) != string(recorded) {
		t.Fatalf("reread %s", again)
	}
	sent, err := json.Marshal(message.User.LLMMessage())
	if err != nil || strings.Contains(string(sent), "deliveryId") {
		t.Fatalf("sent %s, %v", sent, err)
	}
}

func TestUserMessageMembersCannotReplaceTheMessage(t *testing.T) {
	for _, members := range []map[string]json.RawMessage{{"role": json.RawMessage(`"x"`)}, {"timestamp": json.RawMessage("1")}, {"x": json.RawMessage("{")}} {
		if _, err := NewUserMessage(ai.UserText("hi"), 7, members); err == nil {
			t.Errorf("%v accepted", members)
		}
	}
}

func TestAUserMessageWithoutMembersIsPlain(t *testing.T) {
	message, err := NewUserMessage(ai.UserText("hi"), 7, nil)
	if err != nil || message.User.source != nil {
		t.Fatalf("%+v, %v", message, err)
	}
}
