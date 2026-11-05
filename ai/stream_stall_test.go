package ai

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// D131: a model call with no semantic SSE event for StreamOptions.StallTimeoutMs, connect and first byte included, aborts with StreamStallError and retries through the transport-failure path.

const stallTextChunk = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"x"}}]}` + "\n\n"
const stallFinishChunk = `data: {"id":"c","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"!"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"

// stallServer runs behavior for each request, numbered from 1; it returns when the client goes away.
func stallServer(t *testing.T, behavior func(attempt int, w http.ResponseWriter, r *http.Request)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The server notices a closed connection only once the request body is read.
		_, _ = io.Copy(io.Discard, r.Body)
		behavior(int(attempts.Add(1)), w, r)
	}))
	t.Cleanup(server.Close)
	return server, &attempts
}

func stallCall(server *httptest.Server, stallMs int) func() (AssistantMessage, error) {
	return func() (AssistantMessage, error) {
		provider := NewOpenAIProvider(OpenAIConfig{Model: "m", APIKey: "k", BaseURL: server.URL})
		stream, err := provider.Stream(context.Background(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: &stallMs})
		if err != nil {
			return AssistantMessage{}, err
		}
		return *stream.Result(), nil
	}
}

func holdUntilGone(w http.ResponseWriter, r *http.Request) {
	w.(http.Flusher).Flush()
	<-r.Context().Done()
}

func stalledDiagnostic(t *testing.T, message AssistantMessage) {
	t.Helper()
	for _, diagnostic := range message.Diagnostics {
		if diagnostic.Type == "provider_transport_failure" && diagnostic.Error != nil && diagnostic.Error.Name == "StreamStallError" {
			return
		}
	}
	t.Fatalf("no provider_transport_failure diagnostic named StreamStallError: %+v (error %q)", message.Diagnostics, message.ErrorMessage)
}

func TestStreamStallAfterEventsAbortsAndRetriesOnce(t *testing.T) {
	server, attempts := stallServer(t, func(attempt int, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stallTextChunk)
		if attempt == 1 {
			holdUntilGone(w, r)
			return
		}
		_, _ = io.WriteString(w, stallFinishChunk)
	})
	var failures []AssistantMessage
	var retries atomic.Int32
	call := stallCall(server, 200)
	final, err := RetryAssistantCall(t.Context(), func() (AssistantMessage, error) {
		message, err := call()
		if message.StopReason == StopReasonError {
			failures = append(failures, message)
		}
		return message, err
	}, &RetryPolicy{Enabled: true, MaxRetries: 3, BaseDelayMs: 1}, RetryCallbacks{OnRetryScheduled: func(int, int, int, string) error { retries.Add(1); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	if final.StopReason != StopReasonStop || attempts.Load() != 2 || retries.Load() != 1 || len(failures) != 1 {
		t.Fatalf("stop=%s attempts=%d retries=%d failures=%d error=%q", final.StopReason, attempts.Load(), retries.Load(), len(failures), final.ErrorMessage)
	}
	stalledDiagnostic(t, failures[0])
}

func TestStreamStallBeforeTheFirstByteAborts(t *testing.T) {
	for name, silence := range map[string]func(http.ResponseWriter, *http.Request){
		"before response headers": func(_ http.ResponseWriter, r *http.Request) { <-r.Context().Done() },
		"after headers": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			holdUntilGone(w, r)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) { silence(w, r) })
			start := time.Now()
			message, err := stallCall(server, 200)()
			if err != nil {
				t.Fatal(err)
			}
			if message.StopReason != StopReasonError || time.Since(start) > 5*time.Second {
				t.Fatalf("stop=%s after %s error=%q", message.StopReason, time.Since(start), message.ErrorMessage)
			}
			if !IsRetryableAssistantError(message) {
				t.Fatalf("the stall error is not retryable: %q", message.ErrorMessage)
			}
			stalledDiagnostic(t, message)
		})
	}
}

func TestStreamWithSteadyEventsIsNeverAborted(t *testing.T) {
	server, attempts := stallServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for range 12 {
			_, _ = io.WriteString(w, stallTextChunk)
			w.(http.Flusher).Flush()
			time.Sleep(60 * time.Millisecond)
		}
		_, _ = io.WriteString(w, stallFinishChunk)
	})
	message, err := stallCall(server, 250)()
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != StopReasonStop || attempts.Load() != 1 {
		t.Fatalf("stop=%s attempts=%d error=%q", message.StopReason, attempts.Load(), message.ErrorMessage)
	}
}

// A stream that completed leaves no armed timer: the stall cancel never fires after the body reached its end.
func TestStreamStallTimerIsStoppedWhenTheStreamCompletes(t *testing.T) {
	var mu sync.Mutex
	var causes []error
	ctx, cancel := context.WithCancelCause(t.Context())
	record := func(cause error) {
		mu.Lock()
		causes = append(causes, cause)
		mu.Unlock()
		cancel(cause)
	}
	const bound = 80 * time.Millisecond
	body := &sseIdleBody{ReadCloser: io.NopCloser(&emptyReader{}), ctx: ctx, cancel: record, timeout: bound}
	body.timer = time.AfterFunc(bound, func() { record(&sseIdleError{timeout: bound}) })
	if _, err := io.ReadAll(body); err != nil {
		t.Fatal(err)
	}
	time.Sleep(4 * bound)
	mu.Lock()
	defer mu.Unlock()
	if len(causes) != 0 {
		t.Fatalf("the stall timer fired after the stream completed: %v", causes)
	}
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }

func TestStreamStallOptionDisablesTheWatchdog(t *testing.T) {
	server, _ := stallServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, stallTextChunk)
		w.(http.Flusher).Flush()
		time.Sleep(400 * time.Millisecond)
		_, _ = io.WriteString(w, stallFinishChunk)
	})
	message, err := stallCall(server, 0)()
	if err != nil {
		t.Fatal(err)
	}
	if message.StopReason != StopReasonStop {
		t.Fatalf("stop=%s error=%q", message.StopReason, message.ErrorMessage)
	}
}

func TestStreamStallDefaultsToOneHundredEightySeconds(t *testing.T) {
	previous := ConfiguredHTTPIdleTimeoutMs()
	t.Cleanup(func() { _ = ConfigureHTTPDispatcher(previous) })
	if err := ConfigureHTTPDispatcher(DefaultHTTPIdleTimeoutMs); err != nil {
		t.Fatal(err)
	}
	if got := streamStall(StreamOptions{}); got != 180*time.Second {
		t.Fatalf("default stall bound %s", got)
	}
	override := 5000
	if got := streamStall(StreamOptions{StallTimeoutMs: &override}); got != 5*time.Second {
		t.Fatalf("overridden stall bound %s", got)
	}
}
