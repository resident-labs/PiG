// SPDX-License-Identifier: MIT

package coding

import (
	"context"
	"errors"
	"maps"
	"slices"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	icodingagent "github.com/MichaelKinsy/PiG/internal/codingagent"
	"github.com/MichaelKinsy/PiG/internal/codingagent/compaction"
)

// Ports packages/coding-agent/src/core/agent-session.ts (_getSummarizationRequestAuth).
type summarizationRequest struct {
	model *ai.Model
	// apiKey, headers and env are the request auth of the summary model; headers omit the ones the auth deletes.
	apiKey    string
	headers   map[string]string
	env       map[string]string
	completer compaction.SimpleCompleter
	streamFn  compaction.StreamFn
	// thinkingLevel is the thinking level of the summary request: the session's, or the router's for a virtual selection.
	thinkingLevel ai.ModelThinkingLevel
}

func (s *Session) runDefaultCompaction(ctx context.Context, prep compaction.CompactionPreparation, model *ai.Model, customInstructions, reason string) (compaction.CompactionResult, error) {
	if live, ok := s.liveCompactionRequest(model); ok {
		retry, retryCallbacks := s.summarizationRetryOptions("compaction", reason)
		return compaction.CompactLive(ctx, prep, live, customInstructions, retry, retryCallbacks)
	}
	request, err := s.prepareSummarizationRequest(ctx, model)
	if err != nil {
		return compaction.CompactionResult{}, err
	}
	retry, retryCallbacks := s.summarizationRetryOptions("compaction", reason)
	// upstream compact(preparation, model, apiKey, headers, customInstructions, signal, thinkingLevel, this.agent.streamFunction, env, retry, callbacks, sessionId): request.completer holds the Agent's stream function.
	streamFn := request.streamFn
	if streamFn == nil {
		streamFn = compaction.CompleterStreamFn(request.completer)
	}
	return compaction.Compact(ctx, prep, request.model, request.apiKey, request.headers, customInstructions, request.thinkingLevel, streamFn, request.env, retry, retryCallbacks, "")
}

// sessionRequest is one request the agent sent: its model, transcript and options, and the agent messages it was built from.
type sessionRequest struct {
	model      *ai.Model
	transcript ai.TranscriptContext
	options    ai.StreamOptions
	messages   []agent.AgentMessage
}

// liveCompactionRequest is the request a compaction on model extends (dyna patch P16, PATCHES.md): the agent's latest
// request, byte for byte, followed by the messages added since, so the summary reads the conversation from the prompt
// cache. There is none when that request was on another model, when the conversation was rewritten since, before the
// first request, or when a caller supplied its own completer or stream function; Pi's standalone summary request is
// sent then.
func (s *Session) liveCompactionRequest(model *ai.Model) (compaction.LiveRequest, bool) {
	last := s.lastRequest.Load()
	if s.completer != nil || s.streamFn != nil || last == nil || model == nil || providerID(last.model) != providerID(model) || last.model.ID != model.ID {
		return compaction.LiveRequest{}, false
	}
	current := s.agent.MessagesSnapshot()
	if len(last.messages) > len(current) || !slices.EqualFunc(last.messages, current[:len(last.messages)], sameAgentMessage) {
		return compaction.LiveRequest{}, false
	}
	// A summary request reaches extensions only through the header hook, as Pi's does: the agent's payload, response and
	// stream-event observers would take it for an agent reply.
	options := last.options
	options.OnPayload, options.OnResponse, options.OnProviderStreamEvent, options.ProviderStreamEventWanted = nil, nil, nil, nil
	return compaction.LiveRequest{
		Model:    last.model,
		Messages: append(last.transcript.Messages(), agent.ConvertToLLM(current[len(last.messages):])...),
		Options:  options,
		Stream: func(ctx context.Context, model *ai.Model, request ai.Context, options ai.StreamOptions) *ai.AssistantMessageEventStream {
			return s.modelRuntime.StreamSimple(ctx, model, request, options)
		},
	}, true
}

// withoutDeletedHeaders drops the headers an auth result deletes; summary requests take only header values.
// upstream: packages/coding-agent/src/core/agent-session.ts:withoutDeletedHeaders
func withoutDeletedHeaders(headers ai.ProviderHeaders) map[string]string {
	if headers == nil {
		return nil
	}
	values := make(map[string]string, len(headers))
	for name, value := range headers {
		if value != nil {
			values[name] = *value
		}
	}
	return values
}

// prepareSummarizationRequest resolves the summary model and its request auth (agent-session.ts:560 _getSummarizationRequestAuth).
func (s *Session) prepareSummarizationRequest(ctx context.Context, model *ai.Model) (summarizationRequest, error) {
	thinking := s.ThinkingLevel()
	// Pi's getAuth rejects with the signal's reason when the signal is already aborted (resolve.ts:33-44), before any request exists. A caller-owned Go provider has no getAuth: it is not called once the signal has aborted, with the same reason (pinned by TestCompactCancelsSynchronouslyFromStartEvent).
	if ctx.Err() != nil {
		return summarizationRequest{model: model, thinkingLevel: thinking}, abortSignalReason(ctx)
	}
	// Route a virtual model first: summaries size their input and output from the model they get. upstream: agent-session.ts:541-548
	if IsVirtualModel(model) {
		route, err := s.modelRuntime.ResolveModel(ctx, model, agent.ConvertToLLM(agent.NormalizeMessages(s.agent.Messages(), model)), ResolveModelOptions{Reason: ModelRouteReasonDirect, ThinkingLevel: thinking})
		if err != nil {
			return summarizationRequest{model: model, thinkingLevel: thinking}, err
		}
		model, thinking = route.Model, route.ThinkingLevel
	}
	request := summarizationRequest{model: model, completer: s.resolveCompleter(), streamFn: s.streamFn, thinkingLevel: thinking}
	// Registry-built providers use the stock request path. A directly supplied Go provider is a caller-owned stream, even when it carries provider identity metadata.
	_, registryBuilt := model.Provider.(*requestOptionsProvider)
	_, nativeAuth := model.Provider.(interface{ Auth() ai.ProviderAuth })
	prepared, provider, options := model, model.Provider, ai.StreamOptions{Headers: ai.ProviderHeadersFromStrings(model.ProviderMeta.Headers)}
	var err error
	if registryBuilt || nativeAuth {
		prepared, provider, options, err = s.modelRuntime.prepare(ctx, model, ai.StreamOptions{})
		if ctx.Err() != nil {
			// Pi's getAuth races its signal and rejects with the signal's reason (resolve.ts:33-44), which _getSummarizationRequestAuth rethrows.
			return request, abortSignalReason(ctx)
		}
	}
	if err != nil {
		if (s.streamFn != nil || s.agent.StreamFunction() != nil || !registryBuilt) && ctx.Err() == nil {
			return request, nil
		}
		if missing, ok := errors.AsType[*modelRuntimeAuthMissingError](err); ok {
			return request, errors.New(icodingagent.FormatNoAPIKeyFoundMessage(missing.provider))
		}
		return request, err
	}
	clone := *prepared
	clone.Provider = provider
	request.model = &clone
	request.apiKey, request.headers, request.env = options.APIKey, withoutDeletedHeaders(options.Headers), maps.Clone(options.Env)
	return request, nil
}

// errOperationAborted is DOMException AbortError's message: the reason a plainly aborted AbortSignal rejects with.
var errOperationAborted = errors.New("This operation was aborted")

// abortSignalReason is the reason an aborted signal rejects with: AbortError for a plain abort, TimeoutError for AbortSignal.timeout(), or the caller's cause.
func abortSignalReason(ctx context.Context) error {
	cause := context.Cause(ctx)
	switch {
	case cause == nil || errors.Is(cause, context.Canceled):
		return errOperationAborted
	case errors.Is(cause, context.DeadlineExceeded):
		return errors.New("The operation was aborted due to timeout")
	}
	return cause
}
