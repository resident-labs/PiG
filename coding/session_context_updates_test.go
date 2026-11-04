package coding

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
)

// dyna patch P5 (PATCHES.md).
func hostSections(t *testing.T, h *recoveryHarness, pairs ...string) {
	t.Helper()
	var sections ai.OrderedSections
	for i := 0; i < len(pairs); i += 2 {
		sections = append(sections, ai.PromptSection{Name: pairs[i], Value: new(pairs[i+1])})
	}
	if err := h.session.SetHostPromptSections(sections); err != nil {
		t.Fatal(err)
	}
}

// contextUpdates are the texts of the context updates the model read in request i.
func contextUpdates(t *testing.T, h *recoveryHarness, i int) []string {
	t.Helper()
	var messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(h.provider.requests[i]), &messages); err != nil {
		t.Fatal(err)
	}
	var updates []string
	for _, message := range messages {
		var blocks []ai.TextContent
		if message.Role == "user" && json.Unmarshal(message.Content, &blocks) == nil && len(blocks) == 1 && strings.HasPrefix(blocks[0].Text, "[Context update]") {
			updates = append(updates, blocks[0].Text)
		}
	}
	return updates
}

// head is the sections of the system message request i starts with.
func head(t *testing.T, h *recoveryHarness, i int) map[string]string {
	t.Helper()
	var messages []struct {
		Sections map[string]string `json:"sections"`
	}
	if err := json.Unmarshal([]byte(h.provider.requests[i]), &messages); err != nil {
		t.Fatal(err)
	}
	return messages[0].Sections
}

func TestHostSectionsChangeThroughContextUpdates(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := newRecoveryHarness(t, harnessOptions{contextUpdates: true}, done, done, done, done)
	prompt := func(text string) {
		t.Helper()
		if err := h.session.Prompt(t.Context(), text); err != nil {
			t.Fatal(err)
		}
	}
	hostSections(t, h, "goal", "one\ntwo\nthree")
	prompt("first")
	if head(t, h, 0)["goal"] != "<goal>\none\ntwo\nthree\n</goal>" || len(contextUpdates(t, h, 0)) > 0 {
		t.Fatalf("first request: %s", h.provider.requests[0])
	}
	hostSections(t, h, "goal", "one\nTWO\nthree", "usage", "low")
	prompt("second")
	want := "[Context update]\nChanged section goal:\n@@ -1,5 +1,5 @@\n <goal>\n one\n-two\n+TWO\n three\n </goal>\n\nNew section usage:\n<usage>\nlow\n</usage>\n[/Context update]"
	if got := contextUpdates(t, h, 1); len(got) != 1 || got[0] != want {
		t.Fatalf("second request's updates %q", got)
	}
	if sections := head(t, h, 1); sections["goal"] != "<goal>\none\ntwo\nthree\n</goal>" || sections["usage"] != "" {
		t.Fatal("the head changed")
	}
	hostSections(t, h, "usage", "low")
	prompt("third")
	if got := contextUpdates(t, h, 2); len(got) != 2 || got[1] != "[Context update]\nRemoved section goal.\n[/Context update]" {
		t.Fatalf("third request's updates %q", got)
	}
	prompt("fourth")
	if got := contextUpdates(t, h, 3); len(got) != 2 {
		t.Fatalf("an unchanged prompt sent %q", got)
	}
}

// A change during a run reaches the model before the run's next request.
func TestAContextUpdateArrivesBeforeTheNextRequestOfARun(t *testing.T) {
	var h *recoveryHarness
	tool := runPromptTool(t, "work", func() { hostSections(t, h, "goal", "after") })
	h = newRecoveryHarness(t, harnessOptions{contextUpdates: true, tools: []agent.AgentTool{tool}}, fauxToolCall("work"), fauxReply("done", ai.StopReasonStop, 0))
	hostSections(t, h, "goal", "before")
	if err := h.session.Prompt(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}
	if got := contextUpdates(t, h, 1); len(got) != 1 || !strings.Contains(got[0], "-before\n+after") {
		t.Fatalf("the run's second request read %q", got)
	}
	recorded := 0
	for _, message := range h.session.Messages() {
		if _, ok := contextUpdateSections(message); ok {
			recorded++
		}
	}
	if recorded != 1 {
		t.Fatalf("%d context updates recorded", recorded)
	}
}

// A resumed session reads what the model holds from the session's messages, so it sends only what changed since: here the
// docs section, whose path differs in the second harness, and then the goal.
func TestAResumedSessionSendsOnlyTheChangeSinceItsLastUpdate(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	first := newRecoveryHarness(t, harnessOptions{contextUpdates: true, emptySessionManager: true}, done, done)
	hostSections(t, first, "goal", "a")
	if err := first.session.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	hostSections(t, first, "goal", "b")
	if err := first.session.Prompt(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	second := newRecoveryHarness(t, harnessOptions{contextUpdates: true, emptySessionManager: true, sessionManager: first.session.inner}, done, done)
	hostSections(t, second, "goal", "b")
	if err := second.session.Prompt(t.Context(), "three"); err != nil {
		t.Fatal(err)
	}
	hostSections(t, second, "goal", "c")
	if err := second.session.Prompt(t.Context(), "four"); err != nil {
		t.Fatal(err)
	}
	if got := contextUpdates(t, second, 0); len(got) != 2 || strings.Contains(got[1], "section goal") {
		t.Fatalf("resumed with the same sections, the model read %q", got)
	}
	if got := contextUpdates(t, second, 1); len(got) != 3 || got[2] != "[Context update]\nChanged section goal:\n@@ -1,3 +1,3 @@\n <goal>\n-b\n+c\n </goal>\n[/Context update]" {
		t.Fatalf("after the change the model read %q", got)
	}
}

func TestAHostSectionWithABadNameIsRefused(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{contextUpdates: true})
	if err := h.session.SetHostPromptSections(ai.OrderedSections{{Name: "Bad Name", Value: new("x")}}); err == nil {
		t.Fatal("accepted")
	}
}

// A system message with sections after an update, as a session that delivered sections natively before records, holds
// the replayed sections.
func TestTheLatestOfAnUpdateAndASystemMessageHoldsTheSections(t *testing.T) {
	system := func(value string) agent.AgentMessage {
		return agent.AgentMessage{System: &ai.SystemMessage{Content: ai.SystemText(""), Sections: ai.OrderedSections{{Name: "goal", Value: new(value)}}}}
	}
	update := *contextUpdate(map[string]string{"goal": "a"}, ai.OrderedSections{{Name: "goal", Value: new("b")}})
	if held := heldSections([]agent.AgentMessage{system("a"), update}, *system("a").System); held["goal"] != "b" {
		t.Fatalf("after an update: %v", held)
	}
	if held := heldSections([]agent.AgentMessage{system("a"), update, system("c")}, *system("c").System); held["goal"] != "c" {
		t.Fatalf("after a system message: %v", held)
	}
}

// An AGENTS.md edit between two requests of a run reaches the model before the second as a context update.
func TestAnAgentsFileEditReachesTheNextRequest(t *testing.T) {
	var h *recoveryHarness
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(h.session.CWD(), "AGENTS.md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tool := runPromptTool(t, "work", func() { write("rule two") })
	h = newRecoveryHarness(t, harnessOptions{contextUpdates: true, tools: []agent.AgentTool{tool}}, fauxToolCall("work"), fauxReply("done", ai.StopReasonStop, 0))
	write("rule one")
	if err := h.session.Prompt(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(head(t, h, 0)["project_context"], "rule one") {
		t.Fatalf("the head lacks the file: %v", head(t, h, 0))
	}
	if got := contextUpdates(t, h, 1); len(got) != 1 || !strings.Contains(got[0], "Changed section project_context:") || !strings.Contains(got[0], "-rule one\n+rule two") {
		t.Fatalf("the run's second request read %q", got)
	}
}

// The prompt lists tools by name, so a tool that becomes active again shows no change.
func TestAToolActiveAgainChangesNoSection(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := newRecoveryHarness(t, harnessOptions{contextUpdates: true, tools: []agent.AgentTool{runPromptTool(t, "alpha", nil), runPromptTool(t, "beta", nil)}}, done, done)
	if err := h.session.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	h.session.SetActiveToolsByName([]string{"beta", "alpha"})
	if err := h.session.Prompt(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(head(t, h, 0)["tools"], "- alpha: alpha prompt snippet\n- beta: beta prompt snippet") {
		t.Fatalf("tools section: %s", head(t, h, 0)["tools"])
	}
	if got := contextUpdates(t, h, 1); len(got) > 0 {
		t.Fatalf("the model read %q", got)
	}
}
