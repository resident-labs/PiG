package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadConfiguredOutputCaps(t *testing.T) {
	dir := t.TempDir()
	tool := CreateReadTool(dir, &ReadToolOptions{MaxLines: 2, MaxBytes: 8})
	if got := tool.Schema().Description; !strings.Contains(got, "2 lines or 8B") {
		t.Fatalf("unexpected description: %q", got)
	}
	next := 3.0
	for _, tc := range []struct {
		name, file, want string
		offset           *float64
	}{
		{"lines", "one\ntwo\nthree", "one\ntwo\n\n[Showing lines 1-2 of 3. Use offset=3 to continue.]", nil},
		{"next page", "one\ntwo\nthree", "three", &next},
		{"bytes", "one\ntwo-three", "one\n\n[Showing lines 1-1 of 2 (8B limit). Use offset=2 to continue.]", nil},
		{"long first line", "one-two-three", "[Line 1 is 13B, exceeds 8B limit. Use bash: sed -n '1p' note.txt | head -c 8]", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tool.readTextResult(readParams{Path: "note.txt", Offset: tc.offset}, []byte(tc.file))
			if err != nil || got.Text() != tc.want {
				t.Fatalf("whole file got %q, %v; want %q", got.Text(), err, tc.want)
			}
			if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte(tc.file), 0o600); err != nil {
				t.Fatal(err)
			}
			offset := 0.0
			if tc.offset != nil {
				offset = *tc.offset
			}
			got, err = tool.Execute(t.Context(), "", []byte(fmt.Sprintf(`{"path":"note.txt","offset":%g}`, offset)), nil)
			if err != nil || got.IsError || got.Text() != tc.want {
				t.Fatalf("streamed file got %q, %v; want %q", got.Text(), err, tc.want)
			}
		})
	}
}
