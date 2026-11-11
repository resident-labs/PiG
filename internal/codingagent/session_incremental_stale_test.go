package codingagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/sessionentry"
)

func TestIncrementalStaleCustomSuffixAndSkippedPrefix(t *testing.T) {
	withoutSnapshots(t)
	path := incrementalFixture(t)
	open := func() *Session {
		s, e := NewSessionManagerWithDir(t.TempDir(), filepath.Dir(path)).Open(path)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := open()
	data, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	// Change the final custom record without moving any other record.
	changed := strings.Replace(string(data), `"id":"last"`, `"id":"lost"`, 1)
	if e = os.WriteFile(path, []byte(changed), 0600); e != nil {
		t.Fatal(e)
	}
	entries, e := (HandleReads{s}).GetEntriesSince("dup")
	if e != nil {
		t.Fatal(e)
	}
	raws, e := sessionentry.ReadRaws(entries)
	if e != nil {
		t.Fatal(e)
	}
	if s.staleReads.Load() != 1 {
		t.Fatalf("stale reads %d", s.staleReads.Load())
	}
	if strings.Contains(string(raws[2]), `"content"`) || entries[2].Base().ID != "last" {
		t.Fatalf("expected base-only stale custom: %s", raws[2])
	}
	// This same bad custom record is now discarded prefix, and is not diagnosed.
	s.staleReads.Store(0)
	entries, e = (HandleReads{s}).GetEntriesSince("last")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = sessionentry.ReadRaws(entries); e != nil {
		t.Fatal(e)
	}
	if s.staleReads.Load() != 0 {
		t.Fatal("read discarded prefix")
	}
}

func TestIncrementalEmptyAfterAppendDoesNotGrowHandles(t *testing.T) {
	path := incrementalFixture(t)
	s, e := NewSessionManagerWithDir(t.TempDir(), filepath.Dir(path)).Open(path)
	if e != nil {
		t.Fatal(e)
	}
	(HandleReads{s}).GetEntries()
	n := len(s.handles)
	id, e := s.AppendMessage(mkUserMsg("appended"))
	if e != nil {
		t.Fatal(e)
	}
	entries, e := (HandleReads{s}).GetEntriesSince(id)
	if e != nil {
		t.Fatal(e)
	}
	if len(entries) != 0 || len(s.handles) != n {
		t.Fatal("empty suffix grew handles after append")
	}
}
