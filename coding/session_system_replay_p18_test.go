package coding

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
)

func TestP18ReplayMatchesWholeBranchIncludingPreCompactionToolErrors(t *testing.T) {
	for _, raw := range []string{
		`{"role":"system","content":"","toolsAdded":[{"name":"read","description":"read","parameters":{"type":"object"}}],"timestamp":1}`,
		`{"role":"system","content":"","toolsAdded":5,"timestamp":1}`,
		`{"role":"system","content":"","toolsRemoved":["read"],"timestamp":1}`,
	} {
		s := icodingagent.NewSession("replay", t.TempDir())
		var msg agent.AgentMessage
		if err := msg.UnmarshalJSON([]byte(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendMessage(msg); err != nil {
			t.Fatal(err)
		}
		if err := msg.UnmarshalJSON([]byte(`{"role":"user","content":"kept","timestamp":2}`)); err != nil {
			t.Fatal(err)
		}
		kept, err := s.AppendMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.AppendCompaction("summary", kept, 100, nil, false, nil); err != nil {
			t.Fatal(err)
		}
		var want []agent.AgentMessage
		for _, entry := range s.GetBranch() {
			if m, ok := icodingagent.AsMessage(entry); ok && m.Message.System != nil {
				want = append(want, m.Message)
			}
		}
		got, err := replayedTranscript(s)
		if !reflect.DeepEqual(got, want) || fmt.Sprint(err) != fmt.Sprint(agent.ToolReplayError(want)) {
			t.Fatalf("replay changed for %s: %v", raw, err)
		}
	}
}
