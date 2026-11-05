package ai

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// D131: each streaming API owns the typed StreamOptions boundary; sharing an HTTP client does not make an image, classifier or OAuth request a model stream.
func TestStreamStallOptionsReachEverySSEBoundary(t *testing.T) {
	for _, api := range []API{APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIAnthropicMessages, APIGoogleGenerativeAI, APIMistralConversations, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			for _, tc := range []struct {
				name string
				ms   *int
				want time.Duration
			}{
				{"default", nil, 180 * time.Second},
				{"override", new(40), 40 * time.Millisecond},
				{"zero", new(0), 0},
				{"negative", new(-1), 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					provider := fetchOptionProvider(api, "https://example.invalid/v1")
					defer func() { _ = provider.Close() }()
					var client *http.Client
					switch p := provider.(type) {
					case *openAIProvider:
						client = p.client
					case *openAIResponsesProvider:
						client = p.client
					case *anthropicProvider:
						client = p.client
					case *googleProvider:
						client = p.client
					case *mistralProvider:
						client = p.client
					case *piMessagesProvider:
						client = p.client
					default:
						t.Fatalf("unexpected provider %T", provider)
					}
					observed := make(chan bool, 1)
					client.Transport = fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
						bound, streaming := r.Context().Value(streamStallOptionsKey{}).(time.Duration)
						observed <- streaming && bound == tc.want
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
					})
					stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: tc.ms, Transport: TransportSSE})
					if err != nil {
						t.Fatal(err)
					}
					_ = stream.Result()
					if !<-observed {
						t.Fatalf("%s did not forward stall budget %v", api, tc.want)
					}
				})
			}
		})
	}
}

func TestStreamStallAbortsEverySSEProvider(t *testing.T) {
	for _, api := range []API{APIOpenAICompletions, APIOpenAIResponses, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIAnthropicMessages, APIGoogleGenerativeAI, APIMistralConversations, APIPiMessages} {
		t.Run(string(api), func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				holdUntilGone(w, r)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			provider := fetchOptionProvider(api, server.URL)
			defer func() { _ = provider.Close() }()
			stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: new(40), Transport: TransportSSE})
			if err != nil {
				t.Fatal(err)
			}
			message := stream.Result()
			if message.StopReason != StopReasonError || !IsRetryableAssistantError(*message) {
				t.Fatalf("stop=%s error=%q", message.StopReason, message.ErrorMessage)
			}
			stalledDiagnostic(t, *message)
		})
	}
}

func TestStreamStallCustomFetchPreservesCauseBeforeHeaders(t *testing.T) {
	fetch := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	provider := fetchOptionProvider(APIOpenAIResponses, "https://example.invalid/v1")
	defer func() { _ = provider.Close() }()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{Fetch: fetch, StallTimeoutMs: new(40)})
	if err != nil {
		t.Fatal(err)
	}
	message := stream.Result()
	if message.StopReason != StopReasonError || !IsRetryableAssistantError(*message) {
		t.Fatalf("stop=%s error=%q", message.StopReason, message.ErrorMessage)
	}
	stalledDiagnostic(t, *message)
}

func TestStreamStallCustomFetchClosesUncooperativeBody(t *testing.T) {
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	fetch := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: r}, nil
	})}
	provider := fetchOptionProvider(APIOpenAIResponses, "https://example.invalid/v1")
	defer func() { _ = provider.Close() }()
	stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{Fetch: fetch, StallTimeoutMs: new(40)})
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan *AssistantMessage, 1)
	go func() { result <- stream.Result() }()
	select {
	case message := <-result:
		stalledDiagnostic(t, *message)
	case <-time.After(time.Second):
		_ = reader.Close()
		<-result
		t.Fatal("watchdog did not close a custom response body that ignores context cancellation")
	}
}

// Before headers, only the request-start budget applies. The legacy HTTP idle value cannot become a TLS/CONNECT budget.
func TestStreamStallRequestBudgetIsIndependentOfLegacyIdleBeforeHeaders(t *testing.T) {
	previous := ConfiguredHTTPIdleTimeoutMs()
	if err := ConfigureHTTPDispatcher(20); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureHTTPDispatcher(previous) })
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		timer := time.NewTimer(80 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-r.Context().Done():
			return nil, context.Cause(r.Context())
		case <-timer.C:
			return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: http.NoBody}, nil
		}
	})}
	request, err := http.NewRequestWithContext(withStreamStall(t.Context(), StreamOptions{}), http.MethodGet, "https://example.invalid/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamStallDisabledBudgetKeepsLegacySSEIdleTimeout(t *testing.T) {
	previous := ConfiguredHTTPIdleTimeoutMs()
	if err := ConfigureHTTPDispatcher(80); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureHTTPDispatcher(previous) })
	for _, ms := range []int{0, -1} {
		server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			for {
				_, _ = io.WriteString(w, ": keepalive\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(10 * time.Millisecond):
				}
			}
		})
		// Isolate the legacy semantic SSE timer: the native byte-idle timer has the same budget and may win if the server is not scheduled to send a keepalive.
		provider := NewOpenAIProvider(OpenAIConfig{Model: "m", APIKey: "k", BaseURL: server.URL})
		defer func() { _ = provider.Close() }()
		stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: &ms, Fetch: &http.Client{Transport: http.DefaultTransport}})
		if err != nil {
			t.Fatal(err)
		}
		message := *stream.Result()
		if message.StopReason != StopReasonError {
			t.Fatalf("stop=%s error=%q", message.StopReason, message.ErrorMessage)
		}
		stalledDiagnostic(t, message)
	}
}

func TestStreamStallLeavesNonStreamRequestsUnchanged(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	body := io.NopCloser(strings.NewReader("image response"))
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context() != ctx {
			t.Fatal("a request without the typed streaming marker received a different context")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: r}, nil
	})}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.invalid/images", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Body != body {
		t.Fatalf("non-stream body was wrapped as %T", response.Body)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStreamStallPiMessagesDiagnosticTracksTheStreamPhase(t *testing.T) {
	for _, started := range []bool{false, true} {
		server, _ := stallServer(t, func(_ int, w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			if started {
				_, _ = io.WriteString(w, "data: {\"type\":\"start\"}\n\n")
			}
			holdUntilGone(w, r)
		})
		provider := fetchOptionProvider(APIPiMessages, server.URL)
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), StreamOptions{StallTimeoutMs: new(40)})
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		message := stream.Result()
		cancel()
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
		stalledDiagnostic(t, *message)
		wantPhase := "before_message_stream_start"
		if started {
			wantPhase = "after_message_stream_start"
		}
		for _, diagnostic := range message.Diagnostics {
			if diagnostic.Type == "provider_transport_failure" && (diagnostic.Details["eventsEmitted"] != started || diagnostic.Details["phase"] != wantPhase) {
				t.Fatalf("started=%v: details=%+v", started, diagnostic.Details)
			}
		}
	}
}

func TestStreamStallHeadersDoNotExtendTheRequestStartBudget(t *testing.T) {
	previous := ConfiguredHTTPIdleTimeoutMs()
	if err := ConfigureHTTPDispatcher(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ConfigureHTTPDispatcher(previous) })
	reader, writer := io.Pipe()
	defer func() { _ = writer.Close() }()
	defer func() { _ = reader.Close() }()
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		select {
		case <-time.After(600 * time.Millisecond):
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader, Request: r}, nil
	})}
	request, err := http.NewRequestWithContext(withStreamStall(t.Context(), StreamOptions{StallTimeoutMs: new(1000)}), http.MethodPost, "https://example.invalid", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	if err := response.Request.Context().Err(); err != nil {
		t.Fatalf("headers arrived after the request budget expired: %v", err)
	}
	result := make(chan error, 1)
	go func() { _, err := response.Body.Read(make([]byte, 1)); result <- err }()
	select {
	case err := <-result:
		if _, ok := errors.AsType[*sseIdleError](err); !ok {
			t.Fatalf("read error=%v, want typed stall", err)
		}
	case <-time.After(700 * time.Millisecond):
		_ = response.Body.Close()
		<-result
		t.Fatal("headers extended the request-start budget")
	}
}
