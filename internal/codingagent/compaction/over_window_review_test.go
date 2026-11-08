package compaction

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/agent"
	"github.com/MichaelKinsy/PiG/ai"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
)

// labelledMessages is overWindowMessages with its own label, so two message runs stay distinguishable in a request.
func labelledMessages(label string, count, chars int) []agent.AgentMessage {
	messages := make([]agent.AgentMessage, count)
	for index := range messages {
		text := fmt.Sprintf("%s%03d ", label, index) + strings.Repeat("x", chars)
		messages[index] = agent.AgentMessage{User: &agent.UserMessage{Role: "user", Content: ai.UserContentBlocks{ai.TextContent{Text: text}}}}
	}
	return messages
}

// assertSummarizedInOrder checks that every accepted request stays within the limit, that each accepted request after the first carries the summary of the accepted request before it, and that every label appears in order across the accepted requests.
func assertSummarizedInOrder(t *testing.T, completer *overWindowCompleter, labels []string) {
	t.Helper()
	seen, previous := 0, -1
	for index, request := range completer.requests {
		if len(request)/4 > completer.limitTokens {
			continue
		}
		if previous >= 0 && !strings.Contains(request, fmt.Sprintf("<previous-summary>\nsummary-%d\n</previous-summary>", previous+1)) {
			t.Fatalf("accepted request %d does not carry the summary of accepted request %d", index+1, previous+1)
		}
		previous = index
		for position, label := range labels {
			if strings.Contains(request, label) {
				if position != seen {
					t.Fatalf("accepted request %d holds %q, want the messages in order (next %q)", index+1, label, labels[seen])
				}
				seen++
			}
		}
	}
	if seen != len(labels) {
		t.Fatalf("%d of %d messages were summarized", seen, len(labels))
	}
}

func messageLabels(label string, count int) []string {
	labels := make([]string, count)
	for index := range labels {
		labels[index] = fmt.Sprintf("%s%03d ", label, index)
	}
	return labels
}

// The halves of a refused chunk form one chain: the second half updates the summary of the first, so nothing summarized before the refusal is lost.
func TestGenerateBranchSummaryHalvingKeepsTheNewestMessage(t *testing.T) {
	entries := overWindowBranchEntries(10, 300)
	parent := "e009"
	entries = append(entries, makeMessageEntry("big", &parent, 3100))
	completer := &overWindowCompleter{limitTokens: 4000}
	result := GenerateBranchSummary(t.Context(), entries, GenerateBranchSummaryOptions{Model: &ai.Model{Capabilities: ai.ModelCapabilities{ContextWindow: 100000}}, Completer: completer})
	if result.Error != "" || !strings.Contains(result.Summary, fmt.Sprintf("summary-%d", len(completer.requests))) || len(completer.requests) != 2 {
		t.Fatalf("result %+v after %d requests, want the summary of the newest message from the second request", result, len(completer.requests))
	}
}

// A smaller budget that selects the same messages again (the older ones estimate at zero tokens) ends with the provider's error instead of repeating the refused request.
func TestGenerateBranchSummaryHalvingWithoutProgressKeepsTheProvidersError(t *testing.T) {
	first := "empty"
	entries := []codingagent.SessionEntry{makeMessageEntry(first, nil, 0), makeMessageEntry("big", &first, 1000)}
	completer := &overWindowCompleter{limitTokens: 500}
	result := GenerateBranchSummary(t.Context(), entries, GenerateBranchSummaryOptions{Model: &ai.Model{Capabilities: ai.ModelCapabilities{ContextWindow: 100000}}, Completer: completer})
	if !strings.Contains(result.Error, "prompt is too long") || len(completer.requests) != 1 {
		t.Fatalf("result %+v after %d requests, want the provider's error from one request", result, len(completer.requests))
	}
}
