package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The streaming request owns its budget even when a gateway returns JSON rather than SSE headers.
func TestStreamStallNonSSEResponseBodyStillAborts(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		for _, custom := range []bool{false, true} {
			name := http.StatusText(status) + "/native"
			if custom {
				name = http.StatusText(status) + "/custom-fetch"
			}
			t.Run(name, func(t *testing.T) {
				server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(status)
					w.(http.Flusher).Flush()
					<-r.Context().Done()
				})
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				opts := StreamOptions{StallTimeoutMs: new(40), MaxRetries: new(0)}
				reader, writer := io.Pipe()
				defer func() { _ = reader.Close(); _ = writer.Close() }()
				if custom {
					opts.Fetch = &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: reader, Request: r}, nil
					})}
				}
				provider := fetchOptionProvider(APIOpenAIResponses, server.URL)
				defer func() { _ = provider.Close() }()
				stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), opts)
				if err != nil {
					t.Fatal(err)
				}
				result := make(chan *AssistantMessage, 1)
				go func() { result <- stream.Result() }()
				select {
				case message := <-result:
					if message.StopReason != StopReasonError || !IsRetryableAssistantError(*message) {
						t.Fatalf("stop=%s error=%q", message.StopReason, message.ErrorMessage)
					}
					stalledDiagnostic(t, *message)
				case <-time.After(500 * time.Millisecond):
					cancel()
					_ = reader.Close()
					<-result
					t.Fatal("typed streaming request lost its watchdog at non-SSE response headers")
				}
			})
		}
	}
}

func TestStreamStallCompletedNonSSEResponseStopsTimersAndDetachesAbort(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			raw := &stallResponseCloseCounter{Reader: strings.NewReader(`{"error":"complete"}`)}
			transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: raw, Request: r}, nil
			})}
			request, err := http.NewRequestWithContext(withStreamStall(ctx, StreamOptions{StallTimeoutMs: new(1000)}), http.MethodPost, "https://example.invalid", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if data, err := io.ReadAll(response.Body); err != nil || string(data) != `{"error":"complete"}` {
				t.Fatalf("body=%q error=%v", data, err)
			}
			body, ok := response.Body.(*sseIdleBody)
			if !ok {
				t.Fatalf("body=%T, want watchdog-owned non-SSE body", response.Body)
			}
			if context.Cause(response.Request.Context()) != nil {
				t.Fatal("completed body was canceled")
			}
			for _, timer := range body.timers {
				if timer.timer.Stop() {
					t.Fatal("EOF left a request timer armed")
				}
			}
			cancel()
			body.stopAbort()
			if raw.closed.Load() != 0 {
				t.Fatal("EOF left the cancellation callback attached")
			}
			if err := body.Close(); err != nil {
				t.Fatal(err)
			}
			if raw.closed.Load() != 1 {
				t.Fatalf("underlying close count=%d", raw.closed.Load())
			}
		})
	}
}

func TestStreamStallUnmarkedNonSSEResponsesRemainUntouched(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx := context.WithValue(t.Context(), providerRequestOptionsKey{}, StreamOptions{StallTimeoutMs: new(40)})
			raw := io.NopCloser(strings.NewReader(`{"image":"complete"}`))
			transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
				if r.Context() != ctx {
					t.Fatal("unmarked request context changed")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: raw, Request: r}, nil
			})}
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/images", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if response.Body != raw {
				t.Fatal("unmarked response body changed")
			}
		})
	}
}

func TestStreamStallDisabledNonSSEResponseStillClosesOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close(); _ = writer.Close() }()
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: reader, Request: r}, nil
	})}
	request, err := http.NewRequestWithContext(withStreamStall(ctx, StreamOptions{StallTimeoutMs: new(0)}), http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	result := make(chan error, 1)
	go func() { _, err := response.Body.Read(make([]byte, 1)); result <- err }()
	cancel()
	select {
	case err := <-result:
		if _, stalled := errors.AsType[*sseIdleError](err); err == nil || stalled {
			t.Fatalf("caller abort became a stall: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		_ = reader.Close()
		<-result
		t.Fatal("non-SSE acquired body ignored caller cancellation")
	}
}

func TestStreamStallMistralBeforeHeadersProducesARetryableMessage(t *testing.T) {
	server, _ := stallServer(t, func(_ int, _ http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	provider := fetchOptionProvider(APIMistralConversations, server.URL)
	defer func() { _ = provider.Close() }()
	stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: new(40), MaxRetries: new(0)})
	if err != nil {
		t.Fatalf("watchdog expiry rejected outside the message stream: %v", err)
	}
	message := stream.Result()
	if ctx.Err() != nil || message.StopReason != StopReasonError || !IsRetryableAssistantError(*message) {
		t.Fatalf("caller=%v stop=%s error=%q", ctx.Err(), message.StopReason, message.ErrorMessage)
	}
	stalledDiagnostic(t, *message)
}

type stallResponseCloseCounter struct {
	io.Reader
	closed atomic.Int32
}

func (r *stallResponseCloseCounter) Close() error { r.closed.Add(1); return nil }

func TestStreamStallNonSSEErrorBodyKeepsTypedCauseAcrossProviders(t *testing.T) {
	for _, api := range []API{APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIAnthropicMessages, APIGoogleGenerativeAI, APIMistralConversations, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			provider := fetchOptionProvider(api, server.URL)
			defer func() { _ = provider.Close() }()
			stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: new(40), MaxRetries: new(0), Transport: TransportSSE})
			if err != nil {
				t.Fatalf("typed body stall rejected outside the message stream: %v", err)
			}
			message := stream.Result()
			if ctx.Err() != nil || message.StopReason != StopReasonError || !IsRetryableAssistantError(*message) {
				t.Fatalf("caller=%v stop=%s error=%q", ctx.Err(), message.StopReason, message.ErrorMessage)
			}
			stalledDiagnostic(t, *message)
		})
	}
}

func TestStreamStallNonSSEBytesCannotResetTheRequestBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	done := make(chan struct{})
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		go func() {
			defer close(done)
			defer func() { _ = writer.Close() }()
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-r.Context().Done():
					return
				case <-ticker.C:
					if _, err := io.WriteString(writer, "data: {\"not\":\"an SSE response\"}\n\n"); err != nil {
						return
					}
				}
			}
		}()
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Content-Type": {"application/json"}}, Body: reader, Request: r}, nil
	})}
	request, err := http.NewRequestWithContext(withStreamStall(ctx, StreamOptions{StallTimeoutMs: new(150)}), http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close(); <-done }()
	type readResult struct {
		n   int64
		err error
	}
	result := make(chan readResult, 1)
	go func() { n, err := io.Copy(io.Discard, response.Body); result <- readResult{n, err} }()
	select {
	case result := <-result:
		if _, stalled := errors.AsType[*sseIdleError](result.err); result.n == 0 || !stalled || ctx.Err() != nil {
			t.Fatalf("bytes=%d error=%v caller=%v", result.n, result.err, ctx.Err())
		}
	case <-time.After(500 * time.Millisecond):
		_ = response.Body.Close()
		<-result
		t.Fatal("bytes in a non-SSE body extended the request budget")
	}
}

func TestStreamStallCompletedHTTPErrorTextDoesNotBecomeATypedStall(t *testing.T) {
	const serverMessage = "server-reported SSE Event Timeout Error: no event for 40ms"
	for _, api := range []API{APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIAnthropicMessages, APIGoogleGenerativeAI, APIMistralConversations, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `{"error":{"code":503,"status":"UNAVAILABLE","message":"`+serverMessage+`"}}`)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			provider := fetchOptionProvider(api, server.URL)
			defer func() { _ = provider.Close() }()
			stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: new(1000), MaxRetries: new(0), Transport: TransportSSE})
			if err != nil {
				if _, stalled := errors.AsType[*sseIdleError](err); stalled || ctx.Err() != nil || !strings.Contains(err.Error(), serverMessage) {
					t.Fatalf("ordinary HTTP error became a stall: %v, caller=%v", err, ctx.Err())
				}
				return
			}
			message := stream.Result()
			if ctx.Err() != nil || message.StopReason != StopReasonError || !strings.Contains(message.ErrorMessage, serverMessage) {
				t.Fatalf("caller=%v stop=%s error=%q", ctx.Err(), message.StopReason, message.ErrorMessage)
			}
			for _, diagnostic := range message.Diagnostics {
				if diagnostic.Type == "provider_transport_failure" && diagnostic.Error != nil && diagnostic.Error.Name == "StreamStallError" {
					t.Fatalf("HTTP error text was classified as a typed stall: %+v", diagnostic)
				}
			}
		})
	}
}
