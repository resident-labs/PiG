package ai

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

// allowedValues is the list Dyna patch P17 adds to an enum error. Pi's message stops before it, so oracle tests drop it.
var allowedValues = regexp.MustCompile(`(must be equal to one of the allowed values): [^\n]*`)

// piError is err's message as Pi would write it, without P17's list of allowed values.
func piError(err error) string { return allowedValues.ReplaceAllString(err.Error(), "$1") }

func TestEnumErrorNamesTheAllowedValues(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"method":{"type":"string","enum":["get","list"]}},"required":["method"]}`)
	_, err := ValidateToolArgumentsJSON("pick", schema, json.RawMessage(`{"method":"put"}`))
	if err == nil || !strings.Contains(err.Error(), `- method: must be equal to one of the allowed values: ["get","list"]`) {
		t.Fatalf("err = %v", err)
	}
}
