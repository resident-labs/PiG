package codingagent

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

func TestSystemMessagesMatchWholeBranchAcrossCompactionAndForks(t *testing.T) {
	manager := tempSessionMgr(t)
	s, err := manager.Create("system-selection", "")
	if err != nil {
		t.Fatal(err)
	}
	system := func(text string) string {
		id, err := s.AppendMessage(agent.AgentMessage{System: &ai.SystemMessage{Content: ai.SystemText(text), Timestamp: 1}})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	root := system("before compaction")
	var kept string
	for i := range 300 {
		kept, err = s.AppendMessage(mkUserMsg(fmt.Sprint(i)))
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AppendCompaction("summary", kept, 150000, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	main := system("after compaction")
	if err := s.SetLeafID(&root); err != nil {
		t.Fatal(err)
	}
	fork := system("other branch")
	flushSession(t, s)
	loaded, err := manager.Load(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, log := range []*Session{s, loaded} {
		for _, leaf := range []*string{&main, &fork, &root, nil} {
			if err := log.SetLeafID(leaf); err != nil {
				t.Fatal(err)
			}
			got := log.GetSystemMessages()
			var want []agent.AgentMessage
			for _, entry := range log.GetBranch() {
				if msg, ok := AsMessage(entry); ok && msg.Message.System != nil {
					want = append(want, msg.Message)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("system selection differs from whole branch: got %+v want %+v", got, want)
			}
		}
	}
}
