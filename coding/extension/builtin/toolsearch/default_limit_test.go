package toolsearch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/coding/extension/builtin/toolsearch"
)

// dyna patch P11 (PATCHES.md): Options.DefaultLimit.

// loadWith registers the extension with options, runs the tool with params over twelve matching tools, and returns the registered schema and the number of tools loaded.
func loadWith(t *testing.T, options toolsearch.Options, params string) (schema string, loaded int) {
	t.Helper()
	var infos []extension.ToolInfo
	for i := range 12 {
		infos = append(infos, extension.ToolInfo{Name: fmt.Sprintf("read_%02d", i), Description: "Read a file.", Exposure: extension.ToolExposureDeferred})
	}
	var active []string
	actions := extension.ContextActions{
		GetAllTools:    func() []extension.ToolInfo { return infos },
		GetActiveTools: func() []string { return active },
		SetActiveTools: func(names []string) { active = names },
	}
	var api recordingAPI
	if err := toolsearch.CreateToolSearchExtensionWith(options)(&api); err != nil {
		t.Fatal(err)
	}
	ctx := extension.WithContext(context.Background(), extension.NewContext("", nil, func() error { return nil }, actions))
	if _, err := api.registered.Execute(ctx, "call", json.RawMessage(params), nil); err != nil {
		t.Fatal(err)
	}
	return string(api.registered.Parameters), len(active)
}

func TestToolSearchDefaultLimitStaysEightWithoutTheOption(t *testing.T) {
	schema, loaded := loadWith(t, toolsearch.Options{}, `{"query":"read"}`)
	if loaded != 8 || !strings.Contains(schema, "Defaults to 8.") {
		t.Fatalf("loaded %d, schema %s", loaded, schema)
	}
}

func TestToolSearchDefaultLimitOptionSetsTheLimitAndTheSchemaText(t *testing.T) {
	schema, loaded := loadWith(t, toolsearch.Options{DefaultLimit: 3}, `{"query":"read"}`)
	if loaded != 3 || !strings.Contains(schema, "Defaults to 3.") || strings.Contains(schema, "Defaults to 8.") {
		t.Fatalf("loaded %d, schema %s", loaded, schema)
	}
}

func TestToolSearchExplicitLimitWinsOverTheDefaultLimitOption(t *testing.T) {
	if _, loaded := loadWith(t, toolsearch.Options{DefaultLimit: 3}, `{"query":"read","limit":5}`); loaded != 5 {
		t.Fatalf("loaded %d, want 5", loaded)
	}
}
