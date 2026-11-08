package compaction

import (
	"context"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/ai"
)

func liveReply(t *testing.T, message *ai.AssistantMessage) *ai.AssistantMessageEventStream {
	t.Helper()
	stream := ai.NewAssistantMessageEventStream()
	var err error
	if message.StopReason == ai.StopReasonError {
		err = stream.Push(ai.ErrorEvent{Reason: ai.StopReasonError, Error: message})
	} else {
		err = stream.Push(ai.DoneEvent{Reason: message.StopReason, Message: message})
	}
	if err != nil {
		t.Fatal(err)
	}
	return stream
}

func liveConversation(turns int) []ai.Message {
	messages := []ai.Message{ai.SystemMessage{Content: ai.SystemText("system")}}
	for range turns {
		messages = append(messages,
			ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: "do it"}}},
			ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.TextContent{Text: "done"}}, StopReason: ai.StopReasonStop},
		)
	}
	return messages
}

func livePrep() CompactionPreparation {
	return CompactionPreparation{FirstKeptEntryID: "kept", TokensBefore: 1000, FileOps: NewFileOps()}
}

// The summary request is the live request unchanged, with the prompt appended as one user message, and keeps the
// session's options, so the provider can read the conversation from its prompt cache.
func TestCompactLiveExtendsTheLiveRequest(t *testing.T) {
	conversation := liveConversation(3)
	var requests []ai.Context
	var options []ai.StreamOptions
	request := LiveRequest{
		Model:    &ai.Model{ID: "m"},
		Messages: conversation,
		Options:  ai.StreamOptions{APIKey: "key", SessionID: "session"},
		Stream: func(_ context.Context, _ *ai.Model, context ai.Context, option ai.StreamOptions) *ai.AssistantMessageEventStream {
			requests, options = append(requests, context), append(options, option)
			return liveReply(t, &ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.TextContent{Text: "## Goal\nsummary"}}, StopReason: ai.StopReasonStop})
		},
	}
	result, err := CompactLive(t.Context(), livePrep(), request, "focus", nil, ai.RetryCallbacks{})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	sent := requests[0].Messages
	if len(sent) != len(conversation)+1 {
		t.Fatalf("sent %d messages, want the %d live ones and the prompt", len(sent), len(conversation))
	}
	for index := range conversation {
		if _, same := sent[index].(ai.UserMessage); same != isUser(conversation[index]) {
			t.Fatalf("message %d changed", index)
		}
	}
	prompt, ok := sent[len(sent)-1].(ai.UserMessage)
	if !ok || !strings.Contains(prompt.Content.(ai.UserContentBlocks)[0].(ai.TextContent).Text, "Additional focus: focus") {
		t.Fatalf("last message = %#v, want the summary prompt", sent[len(sent)-1])
	}
	if options[0].SessionID != "session" || options[0].CacheRetention != "" {
		t.Fatalf("options = %+v, want the live request's", options[0])
	}
	if !strings.HasPrefix(result.Summary, "## Goal\nsummary") || result.FirstKeptEntryID != "kept" || result.TokensBefore != 1000 {
		t.Fatalf("result = %+v", result)
	}
}

func isUser(message ai.Message) bool {
	_, ok := message.(ai.UserMessage)
	return ok
}

// A request the provider rejects as too long goes again without its oldest messages, keeping the system message and
// starting at a user message.
func TestCompactLiveDropsTheOldestMessagesOnOverflow(t *testing.T) {
	var sizes []int
	var firsts []ai.Message
	request := LiveRequest{
		Model:    &ai.Model{ID: "m"},
		Messages: liveConversation(10),
		Stream: func(_ context.Context, _ *ai.Model, context ai.Context, _ ai.StreamOptions) *ai.AssistantMessageEventStream {
			sizes = append(sizes, len(context.Messages))
			firsts = append(firsts, context.Messages[1])
			if _, ok := context.Messages[0].(ai.SystemMessage); !ok {
				t.Fatal("the system message was dropped")
			}
			if len(sizes) < 3 {
				return liveReply(t, &ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: "prompt is too long: 300000 tokens > 272000 maximum"})
			}
			return liveReply(t, &ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.TextContent{Text: "summary"}}, StopReason: ai.StopReasonStop})
		},
	}
	if _, err := CompactLive(t.Context(), livePrep(), request, "", nil, ai.RetryCallbacks{}); err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || !(sizes[0] > sizes[1] && sizes[1] > sizes[2]) {
		t.Fatalf("request sizes = %v, want three shrinking requests", sizes)
	}
	for _, first := range firsts {
		if !isUser(first) {
			t.Fatalf("a request starts with %T, want a user message", first)
		}
	}
}

// One long turn is cut at an assistant message, after a note that earlier messages were dropped.
func TestDropOldestCutsOneLongTurnAtAnAssistantMessage(t *testing.T) {
	messages := []ai.Message{ai.SystemMessage{Content: ai.SystemText("system")}, ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: "go"}}}}
	for range 10 {
		messages = append(messages,
			ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "c", Name: "bash"}}, StopReason: ai.StopReasonToolUse},
			ai.ToolResultMessage{ToolCallID: "c", ToolName: "bash"},
		)
	}
	kept := dropOldest(messages)
	if len(kept) >= len(messages) {
		t.Fatal("nothing was dropped")
	}
	if !isUser(kept[1]) {
		t.Fatalf("kept[1] = %T, want the dropped-messages note", kept[1])
	}
	if _, ok := kept[2].(ai.AssistantMessage); !ok {
		t.Fatalf("kept[2] = %T, want an assistant message", kept[2])
	}
}

// A reply that calls a tool is asked once more for text; a second tool call fails the compaction.
func TestCompactLiveNudgesOnceAfterAToolCall(t *testing.T) {
	calls := 0
	request := LiveRequest{
		Model:    &ai.Model{ID: "m"},
		Messages: liveConversation(1),
		Stream: func(_ context.Context, _ *ai.Model, context ai.Context, _ ai.StreamOptions) *ai.AssistantMessageEventStream {
			calls++
			return liveReply(t, &ai.AssistantMessage{Content: []ai.AssistantContentBlock{ai.ToolCall{ID: "c", Name: "bash"}}, StopReason: ai.StopReasonToolUse})
		},
	}
	_, err := CompactLive(t.Context(), livePrep(), request, "", nil, ai.RetryCallbacks{})
	if err == nil || calls != 2 {
		t.Fatalf("calls = %d, err = %v; want two calls and an error", calls, err)
	}
}
