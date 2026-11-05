package coding

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/coding/extension"
)

// dyna patch P12 (PATCHES.md): a call to a deferred tool by its exact name loads the tool and runs the call.

const autoloadNumberSchema = `{"type":"object","required":["n"],"properties":{"n":{"type":"number"}}}`

// callsTool is a model turn that calls the named tool with arguments.
func callsTool(name string, arguments ai.JsonObject) scriptedResponse {
	return func([]ai.Message) *ai.AssistantMessage {
		return &ai.AssistantMessage{
			Content:  []ai.AssistantContentBlock{ai.ToolCall{ID: "call-" + name, Name: name, Arguments: arguments}},
			Provider: "faux", Model: "faux-1", StopReason: ai.StopReasonToolUse, Timestamp: time.Now().UnixMilli(),
		}
	}
}

// autoloadHarness has tool_search and alpha active, and three deferred tools: write, count (needs a number) and a script-only one.
// unload names the tools its session_before_compact hook deactivates, as a host's compaction hook does.
func autoloadHarness(t *testing.T, contextUpdates bool, unload []string, responses ...scriptedResponse) *recoveryHarness {
	t.Helper()
	var h *recoveryHarness
	hook := extension.Extension{Path: "unloader", Handlers: map[string][]extension.HandlerFn{"session_before_compact": {func(...any) (any, error) {
		var kept []string
		for _, name := range h.session.ActiveToolNames() {
			if !slices.Contains(unload, name) {
				kept = append(kept, name)
			}
		}
		h.session.SetActiveToolsByName(kept)
		return nil, nil
	}}}}
	h = newRecoveryHarness(t, harnessOptions{contextUpdates: contextUpdates, extension: hook, tools: []agent.AgentTool{
		runPromptTool(t, "tool_search", nil),
		runPromptTool(t, "alpha", nil),
		reachableTool(t, "mcp__srv__write", "mcp__srv", extension.ToolExposureDeferred, noParameters),
		reachableTool(t, "mcp__srv__count", "mcp__srv", extension.ToolExposureDeferred, autoloadNumberSchema),
		reachableTool(t, "lookup", "", extension.ToolExposureCodemode, noParameters),
		reachableTool(t, "inactive_direct", "", extension.ToolExposureDirect, noParameters),
	}}, responses...)
	h.session.SetActiveToolsByName([]string{"tool_search", "alpha"})
	return h
}

// toolResult is the text and the error flag of the tool result the transcript holds for the call of tool.
func toolResult(t *testing.T, h *recoveryHarness, tool string) (string, bool) {
	t.Helper()
	for _, message := range h.session.Messages() {
		if result := message.ToolResult; result != nil && result.ToolName == tool {
			var text strings.Builder
			for _, block := range result.Content {
				if content, ok := block.(ai.TextContent); ok {
					text.WriteString(content.Text)
				}
			}
			return text.String(), result.IsError
		}
	}
	t.Fatalf("no tool result for %s", tool)
	return "", false
}

func TestADirectCallToADeferredToolLoadsItAndRunsTheCall(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := autoloadHarness(t, true, nil, callsTool("mcp__srv__write", ai.JsonObject{}), done)
	if err := h.session.Prompt(t.Context(), "write"); err != nil {
		t.Fatal(err)
	}
	if text, isError := toolResult(t, h, "mcp__srv__write"); text != "mcp__srv__write" || isError {
		t.Fatalf("result %q, error %v", text, isError)
	}
	if got, want := h.session.ActiveToolNames(), []string{"tool_search", "alpha", "mcp__srv__write"}; !slices.Equal(got, want) {
		t.Fatalf("active tools %v, want %v", got, want)
	}
	added, _ := systemTools(t, h.provider.requests[len(h.provider.requests)-1])
	if len(added) < 2 || !slices.Contains(added[len(added)-1], "mcp__srv__write") {
		t.Fatalf("the request after the call declares %v", added)
	}
}

// A script-only (codemode) tool is listed in deferred_tools too.
func TestADirectCallToACodemodeListedToolLoadsIt(t *testing.T) {
	h := autoloadHarness(t, true, nil, callsTool("lookup", ai.JsonObject{}), fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "lookup"); err != nil {
		t.Fatal(err)
	}
	if text, isError := toolResult(t, h, "lookup"); text != "lookup" || isError {
		t.Fatalf("result %q, error %v", text, isError)
	}
}

// The compaction hook unloads a tool that was loaded before; the model, remembering it, calls it directly after the compaction.
func TestADirectCallAfterACompactionUnloadedTheToolRunsAndKeepsItLoaded(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := autoloadHarness(t, true, []string{"mcp__srv__write"}, done, done, done, fauxReply("the summary", ai.StopReasonStop, 0), callsTool("mcp__srv__write", ai.JsonObject{}), done)
	h.session.SetActiveToolsByName([]string{"tool_search", "alpha", "mcp__srv__write"})
	for _, word := range []string{"one", "two", "three"} {
		if err := h.session.Prompt(t.Context(), word+" "+strings.Repeat("word ", 12_000)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.session.Compact(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(h.session.ActiveToolNames(), "mcp__srv__write") {
		t.Fatalf("the compaction left it loaded: %v", h.session.ActiveToolNames())
	}
	if err := h.session.Prompt(t.Context(), "four"); err != nil {
		t.Fatal(err)
	}
	if text, isError := toolResult(t, h, "mcp__srv__write"); text != "mcp__srv__write" || isError {
		t.Fatalf("result %q, error %v", text, isError)
	}
	if !slices.Contains(h.session.ActiveToolNames(), "mcp__srv__write") {
		t.Fatalf("not loaded after the call: %v", h.session.ActiveToolNames())
	}
}

// A name that is neither loaded nor deferred keeps the typed error, and nothing is loaded.
func TestADirectCallToAnUnknownToolStaysNotFound(t *testing.T) {
	h := autoloadHarness(t, true, nil, callsTool("mcp__srv__nope", ai.JsonObject{}), fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "nope"); err != nil {
		t.Fatal(err)
	}
	if text, isError := toolResult(t, h, "mcp__srv__nope"); text != "Tool mcp__srv__nope not found" || !isError {
		t.Fatalf("result %q, error %v", text, isError)
	}
	if got, want := h.session.ActiveToolNames(), []string{"tool_search", "alpha"}; !slices.Equal(got, want) {
		t.Fatalf("active tools %v, want %v", got, want)
	}
}

// Only the exact name of a deferred tool loads (inactive_direct is registered but not deferred): another case, a prefix, a suffix and the unprefixed action name stay not found.
func TestOnlyTheExactNameOfADeferredToolLoads(t *testing.T) {
	for _, name := range []string{"MCP__SRV__WRITE", "mcp__srv__writ", "mcp__srv__write2", "mcp__srv__", "write", "srv__write", "mcp__srv__write ", "inactive_direct"} {
		t.Run(name, func(t *testing.T) {
			h := autoloadHarness(t, true, nil, callsTool(name, ai.JsonObject{}), fauxReply("done", ai.StopReasonStop, 0))
			if err := h.session.Prompt(t.Context(), "call"); err != nil {
				t.Fatal(err)
			}
			if text, isError := toolResult(t, h, name); text != "Tool "+name+" not found" || !isError {
				t.Fatalf("result %q, error %v", text, isError)
			}
			if got, want := h.session.ActiveToolNames(), []string{"tool_search", "alpha"}; !slices.Equal(got, want) {
				t.Fatalf("active tools %v, want %v", got, want)
			}
		})
	}
}

// A load by call adds that one tool to the active ones the way a tool_search load does: the tools already loaded stay loaded, and the
// compaction hook, which is the only unloading, treats both alike.
func TestAnAutoLoadAddsOneToolAndEvictsNothing(t *testing.T) {
	done := fauxReply("done", ai.StopReasonStop, 0)
	h := autoloadHarness(t, true, nil, callsTool("mcp__srv__write", ai.JsonObject{}), callsTool("lookup", ai.JsonObject{}), done)
	h.session.SetActiveToolsByName([]string{"tool_search", "alpha", "mcp__srv__count"})
	if err := h.session.Prompt(t.Context(), "go"); err != nil {
		t.Fatal(err)
	}
	if got, want := h.session.ActiveToolNames(), []string{"tool_search", "alpha", "mcp__srv__count", "mcp__srv__write", "lookup"}; !slices.Equal(got, want) {
		t.Fatalf("active tools %v, want %v", got, want)
	}
}

// The tool's own schema validates the arguments of the call that loaded it.
func TestAnAutoLoadedToolValidatesItsArguments(t *testing.T) {
	h := autoloadHarness(t, true, nil, callsTool("mcp__srv__count", ai.JsonObject{}), fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "count"); err != nil {
		t.Fatal(err)
	}
	text, isError := toolResult(t, h, "mcp__srv__count")
	if !isError || strings.Contains(text, "not found") || !strings.Contains(text, "Validation failed for tool \"mcp__srv__count\"") {
		t.Fatalf("result %q, error %v", text, isError)
	}
}

// Without ContextUpdates the model is never told the deferred tools, and PiG's behaviour is unchanged.
func TestADirectCallWithoutContextUpdatesStaysNotFound(t *testing.T) {
	h := autoloadHarness(t, false, nil, callsTool("mcp__srv__write", ai.JsonObject{}), fauxReply("done", ai.StopReasonStop, 0))
	if err := h.session.Prompt(t.Context(), "write"); err != nil {
		t.Fatal(err)
	}
	if text, isError := toolResult(t, h, "mcp__srv__write"); text != "Tool mcp__srv__write not found" || !isError {
		t.Fatalf("result %q, error %v", text, isError)
	}
}
