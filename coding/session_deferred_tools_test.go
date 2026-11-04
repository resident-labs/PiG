package coding

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/extensions/sdk/json"
)

// dyna patch P7 (PATCHES.md).

// reachableTool is a tool of the namespace that a search or a script reaches, with the given exposure and schema.
func reachableTool(t *testing.T, name, namespace string, exposure extension.ToolExposure, parameters string) agent.AgentTool {
	t.Helper()
	definition := extension.ToolDefinition{Name: name, Label: name, Description: name + " description", Parameters: json.RawMessage(parameters), Exposure: exposure,
		Execute: func(context.Context, string, json.RawMessage, extension.AgentToolUpdateCallback) (extension.AgentToolResult, error) {
			return agent.AgentToolResult{Content: []ai.ToolResultMessageContent{ai.TextContent{Text: name}}}, nil
		}}
	if namespace != "" {
		definition.Namespace = &extension.ToolNamespace{Name: namespace}
	}
	tool, err := newBridgeTool(extension.RegisteredTool{Definition: definition})
	if err != nil {
		t.Fatal(err)
	}
	return tool
}

const noParameters = `{"type":"object","properties":{}}`

// deferredHarness has tool_search, a server's tools that a search loads, one with actions, and a script-only tool.
func deferredHarness(t *testing.T, responses ...scriptedResponse) *recoveryHarness {
	t.Helper()
	h := newRecoveryHarness(t, harnessOptions{contextUpdates: true, tools: []agent.AgentTool{
		runPromptTool(t, "tool_search", nil),
		reachableTool(t, "mcp__srv__write", "mcp__srv", extension.ToolExposureDeferred, noParameters),
		reachableTool(t, "mcp__srv__goal", "mcp__srv", extension.ToolExposureDeferred, `{"type":"object","properties":{"action":{"type":"string","enum":["set","done",3]}}}`),
		reachableTool(t, "lookup", "", extension.ToolExposureCodemode, noParameters),
	}}, responses...)
	// A server's tools start inactive, as the MCP extension registers them; tools given to a Session start active.
	h.session.SetActiveToolsByName([]string{"tool_search"})
	return h
}

func TestTheFirstHeadNamesTheDeferredTools(t *testing.T) {
	h := deferredHarness(t, fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	want := "<deferred_tools>\nTools not declared to you yet, by name prefix, with a tool's actions after it. tool_search loads one.\nlookup\nmcp__srv__: write\n- goal: set, done\n</deferred_tools>"
	if got := head(t, h, 0)["deferred_tools"]; got != want {
		t.Fatalf("deferred_tools:\n%s", got)
	}
}

// A loaded tool is declared by its own system message; the section stays as the head has it, so the prefix is cached.
func TestALoadChangesNoSection(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := deferredHarness(t, done, done)
	if err := h.session.Prompt(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	h.session.SetActiveToolsByName(append(h.session.ActiveToolNames(), "mcp__srv__write"))
	if err := h.session.Prompt(t.Context(), "two"); err != nil {
		t.Fatal(err)
	}
	if got := contextUpdates(t, h, 1); len(got) > 0 {
		t.Fatalf("the model read %q", got)
	}
	if first, second := head(t, h, 0)["deferred_tools"], head(t, h, 1)["deferred_tools"]; first != second {
		t.Fatalf("the head changed:\n%s\n%s", first, second)
	}
}

// A compaction's head holds the sections as they are, and names the deferred tools again, leaving out a loaded one.
func TestACompactionHeadHoldsTheCurrentSections(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := deferredHarness(t, done, done, done, fauxReply("the summary", ai.StopReasonStop, 0), done)
	hostSections(t, h, "goal", "g1")
	if err := h.session.Prompt(t.Context(), "one "+strings.Repeat("word ", 12_000)); err != nil {
		t.Fatal(err)
	}
	hostSections(t, h, "goal", "g2")
	h.session.SetActiveToolsByName(append(h.session.ActiveToolNames(), "mcp__srv__write"))
	if err := h.session.Prompt(t.Context(), "two "+strings.Repeat("word ", 12_000)); err != nil {
		t.Fatal(err)
	}
	if err := h.session.Prompt(t.Context(), "three "+strings.Repeat("word ", 12_000)); err != nil {
		t.Fatal(err)
	}
	if _, err := h.session.Compact(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if err := h.session.Prompt(t.Context(), "four"); err != nil {
		t.Fatal(err)
	}
	sections := head(t, h, 4)
	if sections["goal"] != "<goal>\ng2\n</goal>" || strings.Contains(sections["deferred_tools"], "write") || !strings.Contains(sections["deferred_tools"], "- goal: set, done") {
		t.Fatalf("the compaction head: %v", sections)
	}
	if got := contextUpdates(t, h, 4); len(got) > 0 {
		t.Fatalf("after the compaction the model read %q", got)
	}
}

func TestTheDeferredToolsSectionCountsWhatItLeavesOut(t *testing.T) {
	var tools []deferredTool
	var actions []string
	for i := range 81 {
		tools = append(tools, deferredTool{name: fmt.Sprintf("mcp__big__t%02d", i), namespace: "mcp__big"})
		actions = append(actions, fmt.Sprintf("a%02d", i))
	}
	tools = append(tools, deferredTool{name: "act", actions: actions})
	text, ok := renderDeferredTools(tools, false, true)
	if !ok || !strings.HasPrefix(text, "Tools not declared to you yet, by name prefix, with a tool's actions after it. A codemode script calls one as tools.<prefix><name>.\n") {
		t.Fatalf("%v %s", ok, text)
	}
	if !strings.Contains(text, "\n- act: a00, ") || !strings.Contains(text, "a79, +1 more; tool_search finds them\nmcp__big__: t00, ") || !strings.HasSuffix(text, "t79, +1 more; tool_search finds them") {
		t.Fatalf("%s", text)
	}
	if text, _ := renderDeferredTools(tools[:1], true, true); !strings.Contains(text, "tool_search loads one, and a codemode script calls one as tools.<prefix><name>.") {
		t.Fatalf("%s", text)
	}
	if _, ok := renderDeferredTools(tools, false, false); ok {
		t.Fatal("a section with neither tool_search nor codemode")
	}
	if _, ok := renderDeferredTools(nil, true, true); ok {
		t.Fatal("a section with no tools")
	}
}

func TestTheDeferredToolsSectionIsReserved(t *testing.T) {
	h := newRecoveryHarness(t, harnessOptions{contextUpdates: true})
	if err := h.session.SetHostPromptSections(ai.OrderedSections{{Name: "deferred_tools", Value: new("x")}}); err == nil {
		t.Fatal("accepted")
	}
}
