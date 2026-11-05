package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const stallTextChunk = "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"output\":[]}}\n\n"
const stallFinishChunk = "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[]}}\n\n"

func stallCall(ctx context.Context, server *httptest.Server, timeout int) func() (AssistantMessage, error) {
	return func() (AssistantMessage, error) {
		provider := NewOpenAIResponsesProvider(OpenAIResponsesConfig{Model: "m", APIKey: "k", ProviderID: "openai", BaseURL: server.URL})
		defer func() { _ = provider.Close() }()
		stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: &timeout, MaxRetries: new(0)})
		if err != nil {
			return AssistantMessage{}, err
		}
		return *stream.Result(), nil
	}
}

func stalledDiagnostic(t *testing.T, message AssistantMessage) {
	t.Helper()
	for _, diagnostic := range message.Diagnostics {
		if diagnostic.Type == "provider_transport_failure" && diagnostic.Error != nil && diagnostic.Error.Name == "StreamStallError" && diagnostic.Error.Code == "PIG_STREAM_STALL" {
			return
		}
	}
	t.Fatalf("no typed native stall diagnostic: %+v, error=%q", message.Diagnostics, message.ErrorMessage)
}

func TestStreamStallNativeRequestPhases(t *testing.T) {
	for _, phase := range []string{"before headers", "before first event", "after event", "503 JSON body"} {
		t.Run(phase, func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
				if phase != "before headers" {
					w.Header().Set("Content-Type", "text/event-stream")
					if phase == "503 JSON body" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					if phase == "after event" {
						_, _ = io.WriteString(w, stallTextChunk)
					}
					w.(http.Flusher).Flush()
				}
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			message, err := stallCall(ctx, server, 40)()
			if err != nil || ctx.Err() != nil || message.StopReason != StopReasonError || !IsRetryableAssistantError(message) {
				t.Fatalf("caller=%v stop=%s err=%v message=%q", ctx.Err(), message.StopReason, err, message.ErrorMessage)
			}
			stalledDiagnostic(t, message)
		})
	}
}

func TestStreamStallNativeRetryBackoffAndLimit(t *testing.T) {
	server, attempts := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	var delays []int
	message, err := RetryAssistantCall(t.Context(), stallCall(t.Context(), server, 40), &RetryPolicy{Enabled: true, MaxRetries: 2, BaseDelayMs: 3}, RetryCallbacks{OnRetryScheduled: func(_ int, _ int, delay int, _ string) error { delays = append(delays, delay); return nil }})
	if err != nil || attempts.Load() != 3 || len(delays) != 2 || delays[0] != 3 || delays[1] != 6 {
		t.Fatalf("attempts=%d delays=%v err=%v", attempts.Load(), delays, err)
	}
	stalledDiagnostic(t, message)
}

func TestStreamStallNativeRetryCanRecover(t *testing.T) {
	server, attempts := stallServer(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stallTextChunk)
		if attempt == 1 {
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		_, _ = io.WriteString(w, stallFinishChunk)
	})
	message, err := RetryAssistantCall(t.Context(), stallCall(t.Context(), server, 40), &RetryPolicy{Enabled: true, MaxRetries: 1, BaseDelayMs: 1}, RetryCallbacks{})
	if err != nil || attempts.Load() != 2 || message.StopReason != StopReasonStop {
		t.Fatalf("attempts=%d stop=%s err=%v message=%q", attempts.Load(), message.StopReason, err, message.ErrorMessage)
	}
}

// D131's progress scanner is tested with stopped timers so scheduler delay cannot stand in for semantic idleness.
func TestStreamStallNativeSemanticProgress(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, bytes string
		sse, resets         bool
	}{
		{"data event", "", stallTextChunk, true, true},
		{"ordinary comment", "", ": keepalive\n\n", true, false},
		{"ordinary ping", "", "event: ping\ndata: {}\n\n", true, false},
		{"reasoning comment", "event: response.reasoning_summary_text.delta\ndata: {}\n\n", ": keepalive\n\n", true, true},
		{"non-SSE bytes", "", stallTextChunk, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancelCause(t.Context())
			defer cancel(nil)
			body := &openAIResponsesStallBody{ReadCloser: io.NopCloser(strings.NewReader(tc.bytes)), ctx: ctx, cancel: cancel, sse: tc.sse, timeout: time.Hour, timer: time.AfterFunc(time.Hour, func() {}), stopAbort: func() {}}
			defer func() { _ = body.Close() }()
			body.timer.Stop()
			if tc.prefix != "" {
				body.progress.scan([]byte(tc.prefix))
			}
			if _, err := body.Read(make([]byte, len(tc.bytes))); err != nil {
				t.Fatal(err)
			}
			if reset := body.timer.Stop(); reset != tc.resets {
				t.Fatalf("reset=%v, want %v", reset, tc.resets)
			}
		})
	}
}

func TestStreamStallNativeKeepaliveBytesDoNotPreventExpiry(t *testing.T) {
	server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": keepalive\n\n")
		w.(http.Flusher).Flush()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ": keepalive\n\n")
				w.(http.Flusher).Flush()
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	message, err := stallCall(ctx, server, 40)()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("caller=%v err=%v", ctx.Err(), err)
	}
	stalledDiagnostic(t, message)
}

func TestStreamStallNativeDefaultAndDisable(t *testing.T) {
	if got := openAIResponsesStallTimeout(StreamOptions{}); got != 180*time.Second {
		t.Fatal(got)
	}
	for _, value := range []int{0, -1} {
		server, _ := stallServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
			time.Sleep(80 * time.Millisecond)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, stallFinishChunk)
		})
		message, err := stallCall(t.Context(), server, value)()
		if err != nil || message.StopReason != StopReasonStop {
			t.Fatalf("disabled=%d stop=%s err=%v", value, message.StopReason, err)
		}
	}
}

func TestStreamStallNativeCleanup(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	body := &openAIResponsesStallBody{ReadCloser: io.NopCloser(strings.NewReader("")), ctx: ctx, cancel: cancel, timeout: time.Minute, timer: time.AfterFunc(time.Minute, func() { cancel(&openAIResponsesStallError{}) }), stopAbort: func() {}}
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	if body.timer.Stop() {
		t.Fatal("EOF left the request timer armed")
	}
	if context.Cause(ctx) != nil {
		t.Fatal(context.Cause(ctx))
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(context.Cause(ctx), context.Canceled) {
		t.Fatal(context.Cause(ctx))
	}
}

func TestStreamStallNativeCloseCannotRearmBufferedRead(t *testing.T) {
	ctx, cancel := context.WithCancelCause(t.Context())
	defer cancel(nil)
	body := &openAIResponsesStallBody{ctx: ctx, cancel: cancel, sse: true, timeout: time.Minute, timer: time.AfterFunc(time.Minute, func() { cancel(&openAIResponsesStallError{}) }), stopAbort: func() {}}
	body.ReadCloser = &stallBufferedRead{close: func() { _ = body.Close() }}
	if _, err := body.Read(make([]byte, len(stallTextChunk))); err != nil {
		t.Fatal(err)
	}
	if body.timer.Stop() {
		t.Fatal("buffered Read rearmed a stopped native timer")
	}
}

type stallBufferedRead struct{ close func() }

func (r *stallBufferedRead) Read(p []byte) (int, error) {
	r.close()
	return copy(p, stallTextChunk), nil
}
func (*stallBufferedRead) Close() error { return nil }
