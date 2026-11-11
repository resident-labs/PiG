package codingagent

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/MichaelKinsy/PiG/internal/sessionentry"
)

func incrementalFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{`{"type":"session","version":3,"id":"s","timestamp":"2026-01-01T00:00:00.000Z","cwd":"/w"}`}
	for i, id := range []string{"a", "dup", "b", "dup", "last"} {
		lines = append(lines, fmt.Sprintf(`{"type":"custom_message","id":%q,"parentId":null,"timestamp":"2026-01-01T00:00:00.000Z","customType":"test","content":"hello","display":true,"details":{"n":%d}}`, id, i))
	}
	lines = append(lines, `{"type":"message","id":"message","parentId":null,"timestamp":"2026-01-01T00:00:01.000Z","message":{"role":"user","content":"hello","timestamp":1}}`)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIncrementalHandles(t *testing.T) {
	withoutSnapshots(t)
	for _, stale := range []bool{false, true} {
		for _, since := range []string{"", "a", "dup", "last", "message", "absent"} {
			t.Run(fmt.Sprintf("stale=%v/since=%s", stale, since), func(t *testing.T) {
				path := incrementalFixture(t)
				open := func() *Session {
					s, e := NewSessionManagerWithDir(t.TempDir(), filepath.Dir(path)).Open(path)
					if e != nil {
						t.Fatal(e)
					}
					return s
				}
				old, fresh := open(), open()
				if stale {
					if e := os.Truncate(path, 0); e != nil {
						t.Fatal(e)
					}
				}
				all := (HandleReads{old}).GetEntries()
				want := all
				found := since == ""
				if since != "" {
					for i, e := range all {
						if e.Base().ID == since {
							want = all[i+1:]
							found = true
							break
						}
					}
				}
				got, err := (HandleReads{fresh}).GetEntriesSince(since)
				if !found {
					if err == nil || err.Error() != "Entry not found: "+since {
						t.Fatalf("missing: %v", err)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(got) != len(want) {
					t.Fatalf("length %d != %d", len(got), len(want))
				}
				for i := range got {
					if !reflect.DeepEqual(got[i].Base(), want[i].Base()) {
						t.Fatalf("base %d", i)
					}
				}
				a, ae := sessionentry.ReadRaws(got)
				b, be := sessionentry.ReadRaws(want)
				if (ae == nil) != (be == nil) {
					t.Fatalf("errors %v / %v", ae, be)
				}
				if ae == nil && !reflect.DeepEqual(a, b) {
					t.Fatalf("raw mismatch: %s / %s", a, b)
				}
				if since == "message" && len(fresh.handles) != 0 {
					t.Fatal("empty suffix allocated handles")
				}
			})
		}
	}
}

var incrementalSink []SessionEntry

// Open is deliberately outside timers. Cold means no handles, not a cold OS page cache.
func BenchmarkIncrementalRealFile(b *testing.B) {
	for _, path := range []string{"/tmp/pig-half.jsonl", "/box/shared/codemode-bench/dyna-root-session.jsonl"} {
		for _, mode := range []string{"old-empty", "new-empty-warm", "new-empty-cold", "new-nonempty"} {
			b.Run(filepath.Base(path)+"/"+mode, func(b *testing.B) {
				s, e := NewSessionManagerWithDir("/tmp", "/tmp").Open(path)
				if e != nil {
					b.Fatal(e)
				}
				id := s.slots[len(s.slots)-1].id
				if mode == "new-nonempty" {
					id = s.slots[len(s.slots)-2].id
				}
				if mode != "new-empty-cold" {
					(HandleReads{s}).GetEntries()
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if mode == "old-empty" {
						a := (HandleReads{s}).GetEntries()
						for i, e := range a {
							if e.Base().ID == id {
								incrementalSink = a[i+1:]
								break
							}
						}
					} else {
						incrementalSink, e = (HandleReads{s}).GetEntriesSince(id)
						if e != nil {
							b.Fatal(e)
						}
					}
				}
				b.StopTimer()
				if mode == "new-empty-cold" && len(s.handles) != 0 {
					b.Fatal("grew handles")
				}
			})
		}
	}
}
