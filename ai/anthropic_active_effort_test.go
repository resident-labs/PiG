package ai

import (
	"encoding/json"
	"errors"
	"testing"
)

// dyna patch P8: with ActiveEffortAtTopLevel a request that carries mid-conversation effort markers also sends the active
// effort at the top level, where Pi sends "high"; without it nothing changes.
func TestActiveEffortAtTopLevelReplacesTheHighDefault(t *testing.T) {
	config := AnthropicConfig{Model: "claude-fable-5-1", ProviderID: "anthropic", APIKey: "test-key", BaseURL: "http://127.0.0.1:9", Compat: &AnthropicMessagesCompat{ForceAdaptiveThinking: new(true), SupportsMidConvoEffort: new(true)}, ModelMetadata: &Model{ID: "claude-fable-5-1", Input: []string{"text"}, Capabilities: ModelCapabilities{MaxThinking: ThinkingLevelMax, ContextWindow: 200000, MaxOutputTokens: 32000}, ProviderMeta: ProviderMetadata{ProviderID: "anthropic", API: APIAnthropicMessages, BaseURL: "http://127.0.0.1:9", Reasoning: true}, ThinkingLevelMap: ThinkingLevelMap{ThinkingOff: nil, ThinkingLow: new("low"), ThinkingMedium: new("medium"), ThinkingHigh: new("high"), ThinkingMax: new("max")}}}
	for _, c := range []struct {
		top  bool
		want string
	}{{false, `{"effort":"high"}`}, {true, `{"effort":"low"}`}} {
		provider := NewAnthropicProvider(config)
		var payload map[string]json.RawMessage
		stream, err := provider.Stream(t.Context(), NormalizeContext(managedAnthropicContext("medium", false)), StreamOptions{ThinkingEnabled: new(true), Effort: "low", ActiveEffortAtTopLevel: c.top, CacheRetention: CacheRetentionNone, OnPayload: func(value any, _ *Model) (any, error) {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			if err := json.Unmarshal(encoded, &payload); err != nil {
				return nil, err
			}
			return nil, errors.New("payload captured")
		}})
		if err != nil {
			t.Fatal(err)
		}
		stream.Result()
		_ = provider.Close()
		var messages []map[string]json.RawMessage
		if err := json.Unmarshal(payload["messages"], &messages); err != nil {
			t.Fatal(err)
		}
		if marker := messages[len(messages)-1]; string(marker["role"]) != `"system"` {
			t.Fatalf("top %v: no effort marker: %s", c.top, payload["messages"])
		}
		assertShapeJSON(t, payload["output_config"], c.want)
	}
}
