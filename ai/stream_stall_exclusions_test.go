package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// This file compiles unchanged against fork base 30282328: the base ignores the new option in JSON.
func TestStreamStallExcludedRoutesKeepBaseErrors(t *testing.T) {
	for _, api := range []API{APIOpenAICompletions, APIAnthropicMessages, APIAzureOpenAIResponses, APIOpenAICodexResponses, APIGoogleGenerativeAI, APIGoogleVertex, APIMistralConversations, APIPiMessages, APIOpenAIResponses} {
		t.Run(string(api), func(t *testing.T) {
			server, _ := stallServer(t, func(_ int, w http.ResponseWriter, _ *http.Request) {
				time.Sleep(80 * time.Millisecond)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"code":400,"status":"INVALID_ARGUMENT","message":"baseline rejection"}}`)
			})
			provider := fetchOptionProvider(api, server.URL)
			defer func() { _ = provider.Close() }()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			opts := StreamOptions{Transport: TransportSSE, MaxRetries: new(0)}
			if err := json.Unmarshal([]byte(`{"stallTimeoutMs":20}`), &opts); err != nil {
				t.Fatal(err)
			}
			stream, err := provider.Stream(ctx, NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), opts)
			message := ""
			if err != nil {
				message = err.Error()
			} else {
				result := stream.Result()
				message = result.ErrorMessage
				for _, diagnostic := range result.Diagnostics {
					if diagnostic.Error != nil && diagnostic.Error.Name == "StreamStallError" {
						t.Fatal("excluded route received new diagnostic", diagnostic)
					}
				}
			}
			if ctx.Err() != nil || !strings.Contains(message, "baseline rejection") {
				t.Fatalf("caller=%v error=%q", ctx.Err(), message)
			}
			t.Log(message)
		})
	}
}

func TestStreamStallCustomFetchKeepsBaseTransport(t *testing.T) {
	custom := &http.Client{Transport: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		time.Sleep(80 * time.Millisecond)
		return &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"baseline rejection"}}`)), Request: r}, nil
	})}
	client := providerHTTPClient(&http.Client{Transport: http.DefaultTransport}, custom)
	transport, ok := client.Transport.(*nodeFetchTransport)
	if !ok {
		t.Fatalf("transport=%T", client.Transport)
	}
	if _, ok := transport.base.(fetchTransport); !ok {
		t.Fatalf("custom Fetch acquired a new wrapper: %T", transport.base)
	}
	provider := NewOpenAIResponsesProvider(OpenAIResponsesConfig{Model: "m", APIKey: "k", ProviderID: "openai", BaseURL: "https://unused.invalid"})
	defer func() { _ = provider.Close() }()
	opts := StreamOptions{Fetch: custom, MaxRetries: new(0)}
	if err := json.Unmarshal([]byte(`{"stallTimeoutMs":20}`), &opts); err != nil {
		t.Fatal(err)
	}
	stream, err := provider.Stream(t.Context(), NormalizeContext(Context{Messages: []Message{UserMessage{Content: UserText("hi")}}}), opts)
	if err != nil {
		t.Fatal(err)
	}
	message := stream.Result()
	if !strings.Contains(message.ErrorMessage, "baseline rejection") {
		t.Fatal(message.ErrorMessage)
	}
	for _, diagnostic := range message.Diagnostics {
		if diagnostic.Error != nil && diagnostic.Error.Name == "StreamStallError" {
			t.Fatal(diagnostic)
		}
	}
}

func TestStreamStallUnmarkedRequestsKeepLegacyD131(t *testing.T) {
	previous := ConfiguredHTTPIdleTimeoutMs()
	t.Cleanup(func() { _ = ConfigureHTTPDispatcher(previous) })
	if err := ConfigureHTTPDispatcher(1000); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(t.Context(), providerRequestOptionsKey{}, StreamOptions{})
	raw := io.NopCloser(strings.NewReader("data: {}\n\n"))
	transport := &sseIdleTransport{base: fetchOptionTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context() != ctx {
			t.Fatal("unmarked request context changed")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: raw, Request: r}, nil
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
	if _, ok := response.Body.(*sseIdleBody); !ok {
		t.Fatalf("base legacy body wrapper changed: %T", response.Body)
	}
	cause := classifyTransportCause(&nodeTransportError{cause: &sseIdleError{timeout: time.Second}})
	if cause.name != "Error" || cause.code != "" {
		t.Fatalf("legacy diagnostic changed: %+v", cause)
	}
}
