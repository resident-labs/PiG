package coding

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
)

// dyna patch P11 (PATCHES.md): a compaction's head declares the session's executable tools.

// headToolsHarness has three active tools; its session_before_compact hook unloads the tools unload names, as a host's compaction hook does with ctx.SetActiveTools.
func headToolsHarness(t *testing.T, unload []string, responses ...scriptedResponse) *recoveryHarness {
	t.Helper()
	var h *recoveryHarness
	hook := extension.Extension{Path: "unloader", Handlers: map[string][]extension.HandlerFn{"session_before_compact": {func(...any) (any, error) {
		if len(unload) > 0 {
			var kept []string
			for _, name := range h.session.ActiveToolNames() {
				if !slices.Contains(unload, name) {
					kept = append(kept, name)
				}
			}
			h.session.SetActiveToolsByName(kept)
		}
		return nil, nil
	}}}}
	h = newRecoveryHarness(t, harnessOptions{contextUpdates: true, extension: hook, tools: []agent.AgentTool{
		runPromptTool(t, "alpha", nil), runPromptTool(t, "beta", nil), runPromptTool(t, "gamma", nil),
	}}, responses...)
	return h
}

// compactedRequest runs three turns, a compaction and a fourth turn, and returns the fourth request.
func compactedRequest(t *testing.T, h *recoveryHarness) string {
	t.Helper()
	for _, word := range []string{"one", "two", "three"} {
		if err := h.session.Prompt(t.Context(), word+" "+strings.Repeat("word ", 12_000)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.session.Compact(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := h.session.Prompt(t.Context(), "four"); err != nil {
		t.Fatal(err)
	}
	return h.provider.requests[len(h.provider.requests)-1]
}

// systemTools is the tool names each system message of a request adds and removes, in order.
func systemTools(t *testing.T, request string) (added, removed [][]string) {
	t.Helper()
	var messages []struct {
		Role         string `json:"role"`
		ToolsAdded   []struct{ Name string }
		ToolsRemoved []struct{ Name string }
	}
	if err := json.Unmarshal([]byte(request), &messages); err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if message.Role != "system" {
			continue
		}
		var add, remove []string
		for _, tool := range message.ToolsAdded {
			add = append(add, tool.Name)
		}
		for _, tool := range message.ToolsRemoved {
			remove = append(remove, tool.Name)
		}
		added, removed = append(added, add), append(removed, remove)
	}
	return added, removed
}

func newHeadToolsHarness(t *testing.T, unload []string) *recoveryHarness {
	done := fauxReply("done", ai.StopReasonStop, 0)
	return headToolsHarness(t, unload, done, done, done, fauxReply("the summary", ai.StopReasonStop, 0), done)
}

// The hook unloads beta before the compaction is recorded: the head declares exactly alpha and gamma, and the next request removes nothing.
func TestACompactionHeadDeclaresTheToolsAHookLeftActive(t *testing.T) {
	h := newHeadToolsHarness(t, []string{"beta"})
	added, removed := systemTools(t, compactedRequest(t, h))
	if len(added) == 0 || !slices.Equal(added[0], []string{"alpha", "gamma"}) {
		t.Fatalf("the head adds %v, want [alpha gamma]", added)
	}
	for _, names := range removed {
		if len(names) > 0 {
			t.Fatalf("a system message removes %v after the compaction: %v", names, removed)
		}
	}
}

// Without a hook the head declares the tools the transcript declared, and nothing is removed.
func TestACompactionHeadWithoutAHookDeclaresEveryTool(t *testing.T) {
	h := newHeadToolsHarness(t, nil)
	added, removed := systemTools(t, compactedRequest(t, h))
	if len(added) != 1 || !slices.Equal(added[0], []string{"alpha", "beta", "gamma"}) {
		t.Fatalf("system messages add %v, want one head adding [alpha beta gamma]", added)
	}
	for _, names := range removed {
		if len(names) > 0 {
			t.Fatalf("a system message removes %v: %v", names, removed)
		}
	}
}

// A compaction draft at a boundary declares the executable tools the same way.
func TestABoundaryCompactionHeadDeclaresTheExecutableTools(t *testing.T) {
	h := newHeadToolsHarness(t, nil)
	if err := h.session.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	h.session.SetActiveToolsByName([]string{"alpha", "gamma"})
	if err := h.session.commitBoundaryDrafts([]extension.SessionBoundaryDraft{{Type: "compaction", Summary: "s"}}); err != nil {
		t.Fatal(err)
	}
	var head struct {
		SystemMessage struct {
			ToolsAdded []struct{ Name string } `json:"toolsAdded"`
		} `json:"systemMessage"`
	}
	entries := h.session.inner.GetEntries()
	last := entries[len(entries)-1]
	if last.Base().Type != "compaction" {
		t.Fatalf("last entry %s", last.Base().Type)
	}
	if err := json.Unmarshal(last.Raw(), &head); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range head.SystemMessage.ToolsAdded {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"alpha", "gamma"}) {
		t.Fatalf("the boundary head adds %v, want [alpha gamma]", names)
	}
}
