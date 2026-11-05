package ai

import (
	"net/http"
	"testing"
)

// D192: only native OpenAI Responses defaults to detailed; custom Fetch and other routes keep the fork base default.
func TestResponsesRequestAsksForADetailedReasoningSummaryByDefault(t *testing.T) {
	responses := func(providerID string, metadata *Model) Provider {
		return NewOpenAIResponsesProvider(OpenAIResponsesConfig{APIKey: "k", Model: "m", ProviderID: providerID, BaseURL: "https://example.invalid/v1", ModelMetadata: metadata})
	}
	reasoningModel := &Model{ID: "m", Capabilities: ModelCapabilities{MaxThinking: ThinkingLevelHigh}, Input: []string{"text"}, ProviderMeta: ProviderMetadata{Reasoning: true, API: APIOpenAIResponses}}
	plainModel := &Model{ID: "m", Input: []string{"text"}, ProviderMeta: ProviderMetadata{API: APIOpenAIResponses}}
	summary := func(payload map[string]any) any {
		reasoning, _ := payload["reasoning"].(map[string]any)
		return reasoning["summary"]
	}
	t.Run("default is detailed on openai", func(t *testing.T) {
		if got := summary(captureCatalogPayload(t, responses("openai", reasoningModel), StreamOptions{ReasoningEffort: "medium", IsReasoning: true})); got != "detailed" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("codex retains base auto", func(t *testing.T) {
		m := mustGeneratedModel(t, "openai-codex", "gpt-6-sol")
		p := NewOpenAICodexResponsesProvider(OpenAICodexResponsesConfig{APIKey: codexTestToken(t, "acc_test"), Model: m.ID, ProviderID: m.Provider, BaseURL: m.BaseURL, Compat: m.Compat})
		if got := summary(captureCatalogPayload(t, p, StreamOptions{ReasoningEffort: "medium", IsReasoning: m.Reasoning, Transport: TransportSSE})); got != "auto" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("custom Fetch retains base auto", func(t *testing.T) {
		if got := summary(captureCatalogPayload(t, responses("openai", reasoningModel), StreamOptions{ReasoningEffort: "medium", IsReasoning: true, Fetch: &http.Client{}})); got != "auto" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("Azure API retains base auto", func(t *testing.T) {
		p := responses("openai", reasoningModel).(*openAIResponsesProvider)
		p.cfg.api = APIAzureOpenAIResponses
		if got := summary(captureCatalogPayload(t, p, StreamOptions{ReasoningEffort: "medium", IsReasoning: true})); got != "auto" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("an explicit summary wins", func(t *testing.T) {
		if got := summary(captureCatalogPayload(t, responses("openai", reasoningModel), StreamOptions{ReasoningEffort: "medium", ReasoningSummary: "concise", IsReasoning: true})); got != "concise" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("other Responses providers keep auto", func(t *testing.T) {
		if got := summary(captureCatalogPayload(t, responses("xai", reasoningModel), StreamOptions{ReasoningEffort: "medium", IsReasoning: true})); got != "auto" {
			t.Fatalf("summary = %v", got)
		}
	})
	t.Run("a model without reasoning sends no summary", func(t *testing.T) {
		if got := summary(captureCatalogPayload(t, responses("openai", plainModel), StreamOptions{ReasoningEffort: "medium"})); got != nil {
			t.Fatalf("summary = %v", got)
		}
	})
}
