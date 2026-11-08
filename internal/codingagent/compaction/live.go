package compaction

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/MichaelKinsy/PiG/ai"
)

// pig divergence (dyna patch P16, PATCHES.md): Pi summarizes a conversation by serializing it into one new user message
// under its own system prompt, with prompt caching off, so a summary of a long session reads every token at full price
// and, on a slow model, takes as long as the model needs to read it. PiG sends the session's live request unchanged,
// with the summary prompt appended as one more user message, as opencode does (packages/core/src/session/compaction.ts
// summarize). The prefix is byte-identical to the request the session just sent, so the provider reads it from its
// prompt cache, and the summary is one request whatever the length of the conversation.

// LIVE_SUMMARIZATION_PROMPT is the user message appended to the live conversation to ask for its summary.
const LIVE_SUMMARIZATION_PROMPT = `Stop working on the task now. Do not call tools. Your context window is full, so the conversation above is about to be replaced by a summary that you write now. Another instance of you will continue the work from that summary and the most recent messages alone.

Summarize the whole conversation above. If it begins with the summary of an earlier part, carry that summary's information forward: preserve what still matters, move finished items from "In Progress" to "Done", and update "Next Steps". Drop what is no longer relevant. Leave out what the next instance can recover from the code or the files listed below.

Use this EXACT format:

## Goal
[What is the user trying to accomplish? Can be multiple items if the session covers different tasks.]

## Constraints & Preferences
- [Any constraints, preferences, or requirements mentioned by user]
- [Or "(none)" if none were mentioned]

## Progress
### Done
- [x] [Completed tasks/changes]

### In Progress
- [ ] [Current work]

### Blocked
- [Issues preventing progress, if any]

## Key Decisions
- **[Decision]**: [Brief rationale]

## Next Steps
1. [Ordered list of what should happen next]

## Critical Context
- [Any data, examples, or references needed to continue]
- [Or "(none)" if not applicable]

Keep each section concise. Preserve exact file paths, function names, and error messages. Reply with the summary only.`

// liveToolCallNudge follows a reply that called a tool instead of writing the summary.
const liveToolCallNudge = "\n\nYour previous reply called a tool. Do not call tools: reply with the summary as text."

// LiveStreamFn sends one request of the session's own conversation, as the session's model runtime does.
type LiveStreamFn func(ctx context.Context, model *ai.Model, request ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream

// LiveRequest is the session's live request: the transcript and options of the request a summary extends. Messages
// hold the system message with the tools, as the session sends them.
type LiveRequest struct {
	Model    *ai.Model
	Messages []ai.Message
	Options  ai.StreamOptions
	Stream   LiveStreamFn
}

// CompactLive summarizes the conversation in one request that extends the session's live request, and returns the
// compaction result for prep. A request the provider rejects as too long is sent again without its oldest messages,
// as Codex trims its history, until it fits.
func CompactLive(ctx context.Context, prep CompactionPreparation, request LiveRequest, customInstructions string, retry *ai.RetryPolicy, callbacks ai.RetryCallbacks) (CompactionResult, error) {
	if prep.FirstKeptEntryID == "" {
		return CompactionResult{}, errors.New("First kept entry has no UUID - session may need migration")
	}
	prompt := LIVE_SUMMARIZATION_PROMPT
	if customInstructions != "" {
		prompt += "\n\nAdditional focus: " + customInstructions
	}
	summary, usage, err := summarizeLive(ctx, request, prompt, retry, callbacks)
	if err != nil {
		return CompactionResult{}, summarizationFailure("Summarization", err)
	}
	readFiles, modifiedFiles := ComputeFileLists(prep.FileOps)
	return CompactionResult{
		Summary:          summary + FormatFileOperations(readFiles, modifiedFiles),
		FirstKeptEntryID: prep.FirstKeptEntryID,
		TokensBefore:     prep.TokensBefore,
		Usage:            usage,
		Details:          CompactionDetails{ReadFiles: readFiles, ModifiedFiles: modifiedFiles},
	}, nil
}

func summarizeLive(ctx context.Context, request LiveRequest, prompt string, retry *ai.RetryPolicy, callbacks ai.RetryCallbacks) (string, *ai.Usage, error) {
	messages := request.Messages
	nudged := false
	for {
		text, usage, err := completeSimpleWithRetries(ctx, retry, callbacks, func() (string, *ai.Usage, error) {
			return sendLive(ctx, request, messages, prompt)
		})
		switch {
		case errors.Is(err, ErrSummarizationToolCall) && !nudged:
			nudged = true
			prompt += liveToolCallNudge
		case err != nil && isOverflow(err):
			trimmed := dropOldest(messages)
			if len(trimmed) == len(messages) {
				return "", nil, err
			}
			messages = trimmed
		default:
			return text, usage, err
		}
	}
}

func isOverflow(err error) bool {
	return ai.IsContextOverflow(ai.AssistantMessage{StopReason: ai.StopReasonError, ErrorMessage: err.Error()}, 0)
}

func sendLive(ctx context.Context, request LiveRequest, messages []ai.Message, prompt string) (string, *ai.Usage, error) {
	ask := ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: prompt}}, Timestamp: time.Now().UnixMilli()}
	full := append(messages[:len(messages):len(messages)], ask)
	message := request.Stream(ctx, request.Model, ai.Context{Messages: full}, request.Options).Result()
	if ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	switch message.StopReason {
	case ai.StopReasonError:
		return "", nil, errors.New(cmpOr(message.ErrorMessage, "Unknown error"))
	case ai.StopReasonAborted:
		return "", nil, errors.New(cmpOr(message.ErrorMessage, "This operation was aborted"))
	case ai.StopReasonLength:
		return "", nil, errors.New("generation hit the token cap and the summary is incomplete")
	}
	var text strings.Builder
	for _, block := range message.Content {
		switch block := block.(type) {
		case ai.ToolCall:
			return "", nil, ErrSummarizationToolCall
		case ai.TextContent:
			text.WriteString(block.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", nil, errors.New("the model returned an empty summary")
	}
	usage := message.Usage
	return text.String(), &usage, nil
}

func cmpOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// dropOldest removes about a fifth of the conversation from its start, keeping every system message, and starts what
// is left at a user or assistant message so no tool result loses its call; one long turn is cut at an assistant
// message, after a user message saying earlier messages were dropped. It returns messages unchanged when nothing can go.
func dropOldest(messages []ai.Message) []ai.Message {
	var conversation []int
	for index, message := range messages {
		if _, ok := message.(ai.SystemMessage); !ok {
			conversation = append(conversation, index)
		}
	}
	if len(conversation) < 2 {
		return messages
	}
	cut := max(1, len(conversation)/5)
	for cut < len(conversation) && !startsTurnPart(messages[conversation[cut]]) {
		cut++
	}
	if cut >= len(conversation) {
		return messages
	}
	first := conversation[cut]
	kept := make([]ai.Message, 0, len(messages)-cut+1)
	for _, message := range messages[:first] {
		if _, ok := message.(ai.SystemMessage); ok {
			kept = append(kept, message)
		}
	}
	if _, ok := messages[first].(ai.AssistantMessage); ok {
		kept = append(kept, ai.UserMessage{Content: ai.UserContentBlocks{ai.TextContent{Text: "[Earlier messages of this conversation were dropped to fit the context window.]"}}, Timestamp: time.Now().UnixMilli()})
	}
	return append(kept, messages[first:]...)
}

func startsTurnPart(message ai.Message) bool {
	switch message.(type) {
	case ai.UserMessage, ai.AssistantMessage:
		return true
	}
	return false
}
