package codemode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/coding/extension"
	"github.com/MichaelKinsy/PiG/internal/codingagent"
)

type measuredBranch struct {
	extension.ReadonlySessionManager
	s     *codingagent.Session
	reads int
}

func (m *measuredBranch) GetSessionId() string { return m.s.GetSessionId() }
func (m *measuredBranch) GetLeafID() *string   { return m.s.GetLeafID() }
func (m *measuredBranch) GetBranch(ids ...string) []codingagent.SessionEntry {
	return m.s.GetBranch(ids...)
}
func (m *measuredBranch) GetEntry(id string) (codingagent.SessionEntry, bool) {
	m.reads++
	return m.s.GetEntry(id)
}

func (m *measuredBranch) GetEntryHandle(id string) (codingagent.SessionEntry, bool) {
	m.reads++
	return m.s.GetEntryHandle(id)
}

func TestBranchStoreCacheReadsOnlyNewEntries(t *testing.T) {
	m := &measuredBranch{s: codingagent.NewSession("test", t.TempDir())}
	c := &branchStoreCache{}
	appendStore := func(data StoreEntryData) {
		t.Helper()
		if _, err := m.s.AppendCustomEntry(StoreEntryType, data); err != nil {
			t.Fatal(err)
		}
	}
	appendStore(StoreEntryData{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}})
	if got := c.read(m); string(got["x"]) != "1" {
		t.Fatal(got)
	}
	first := *m.GetLeafID()
	m.reads = 0
	for range 10 {
		c.read(m)
	}
	if m.reads != 0 {
		t.Fatalf("unchanged branch read %d entries", m.reads)
	}
	appendStore(StoreEntryData{Delete: []string{"x"}})
	if got := c.read(m); len(got) != 0 {
		t.Fatal(got)
	}
	if m.reads != 1 {
		t.Fatalf("append read %d entries", m.reads)
	}
	// Switching to an ancestor restores the value deleted on the abandoned branch.
	if err := m.s.SetLeafID(&first); err != nil {
		t.Fatal(err)
	}
	if got := c.read(m); string(got["x"]) != "1" {
		t.Fatal(got)
	}
	// Replacing a session must discard the previous checkpoint.
	m.s = codingagent.NewSession("replacement", t.TempDir())
	if got := c.read(m); len(got) != 0 {
		t.Fatal(got)
	}
}

// Metadata reads must never fall back to payload decoding on a capable manager.
type metadataOnlyBranch struct{ *measuredBranch }

func (m metadataOnlyBranch) GetEntry(string) (codingagent.SessionEntry, bool) {
	panic("codemode decoded a message-capable entry read")
}

func TestBranchStoreCacheColdUsesMetadata(t *testing.T) {
	m := metadataOnlyBranch{&measuredBranch{s: codingagent.NewSession("cold", t.TempDir())}}
	if _, err := m.s.AppendCustomEntry(StoreEntryType, StoreEntryData{Set: map[string]json.RawMessage{"x": json.RawMessage(`1`)}}); err != nil {
		t.Fatal(err)
	}
	if got := (&branchStoreCache{}).read(m); string(got["x"]) != "1" {
		t.Fatal(got)
	}
}

func BenchmarkExecuteHistory(b *testing.B) {
	for _, n := range []int{100, 10000, 50000} {
		for _, cached := range []bool{false, true} {
			b.Run(fmt.Sprintf("entries=%d/cached=%t", n, cached), func(b *testing.B) {
				m := &measuredBranch{s: codingagent.NewSession("bench", b.TempDir())}
				payload := strings.Repeat("x", 8192)
				for range n {
					if _, err := m.s.AppendCustomEntry("other", payload); err != nil {
						b.Fatal(err)
					}
				}
				base := extension.NewContext(b.TempDir(), nil, func() error { return nil }, extension.ContextActions{SessionManager: m})
				ctx := extension.WithToolContext(context.Background(), extension.NewToolContext(base, "bench", context.Background(), extension.ToolActions{}))
				options := Options{CacheDir: b.TempDir()}
				if cached {
					options.storeCache = &branchStoreCache{}
				}
				run := func() {
					if _, err := Execute(ctx, "bench", json.RawMessage(`{"code":"return 1"}`), nil, options); err != nil {
						b.Fatal(err)
					}
				}
				run()
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					run()
				}
			})
		}
	}
}
