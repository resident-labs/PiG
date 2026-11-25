package ai

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestEnumErrorNamesTheAllowedValues pins that enum validation errors include the allowed values.
// Upstream pi-schema (schema/explain.go) now formats enum failures as `expected one of [...]`, superseding Dyna patch P17.
func TestEnumErrorNamesTheAllowedValues(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"method":{"type":"string","enum":["get","list"]}},"required":["method"]}`)
	_, err := ValidateToolArgumentsJSON("pick", schema, json.RawMessage(`{"method":"put"}`))
	if err == nil || !strings.Contains(err.Error(), `- method: expected one of ["get","list"]`) {
		t.Fatalf("err = %v", err)
	}
}
