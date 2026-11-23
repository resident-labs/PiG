package codemode

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
)

func TestConfiguredOutputBudget(t *testing.T) {
	def := Definition(Options{DefaultMaxOutputTokens: 100})
	for _, description := range []string{def.Description, def.PrepareLoadout(extension.ToolLoadout{}).Descriptions[ToolName]} {
		for _, part := range []string{`"max_output_tokens": 100`, "no timeout is set unless requested", "{ ...args }", "truncation notices", "reject with an Error"} {
			if !strings.Contains(description, part) {
				t.Errorf("description lacks %q: %s", part, description)
			}
		}
	}
	for _, tc := range []struct {
		name, opts, value string
		wantFile          bool
	}{
		{"default", "", strings.Repeat("x", 800), true},
		{"raise", "// @options: {\"max_output_tokens\": 300}\n", strings.Repeat("x", 800), false},
		{"lower", "// @options: {\"max_output_tokens\": 10}\n", strings.Repeat("x", 80), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			params, _ := json.Marshal(map[string]string{"code": tc.opts + `return "` + tc.value + `"`})
			result, err := def.Execute(t.Context(), "cap-test", params, nil)
			if err != nil || result.IsError {
				t.Fatalf("script failed: %v %v", result.Text(), err)
			}
			path := result.Details.(ToolDetails).FullOutputPath
			if path != "" {
				t.Cleanup(func() { _ = os.Remove(path) })
			}
			if (path != "") != tc.wantFile {
				t.Fatalf("truncation path = %q, want truncation = %v", path, tc.wantFile)
			}
			if path == "" {
				if !strings.Contains(result.Text(), tc.value) {
					t.Fatal("output was lost without truncation")
				}
				return
			}
			full, err := os.ReadFile(path)
			if err != nil || string(full) != tc.value || !strings.Contains(result.Text(), path) {
				t.Fatalf("cannot recover full output at %q: %v", path, err)
			}
		})
	}
}
